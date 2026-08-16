package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"bilibili-live-gift-panel/internal/gameplay"
)

type giftEventSource interface {
	Run(context.Context, string, runtimeCallbacks) error
}

type runtimeCallbacks struct {
	onGift             func(giftEvent)
	onFrame            func()
	onState            func(string)
	onGiftCatalog      func([]roomGiftInfo)
	onGiftCatalogError func(error)
}

type runtimeStatus struct {
	State              string           `json:"state"`
	RoomID             string           `json:"roomId"`
	LastError          string           `json:"-"`
	IngestionError     string           `json:"-"`
	IngestionErrorKind string           `json:"ingestionErrorKind,omitempty"`
	LastGiftAt         int64            `json:"lastGiftAt,omitempty"`
	LastFrameAt        int64            `json:"lastFrameAt,omitempty"`
	ConnectionGaps     []connectionGap  `json:"-"`
	Gaps               []connectionGap  `json:"gaps,omitempty"`
	ReconnectAttempts  int              `json:"reconnectAttempts"`
	Inbox              *giftInboxHealth `json:"inbox,omitempty"`
	TransactionPending bool             `json:"transactionPending"`
}

type runtimeGiftInbox interface {
	Accept(roomID, command string, gift giftEvent) (giftInboxRecord, error)
	Next() (giftInboxRecord, bool, error)
	Acknowledge(ingestionID string) error
	Release(ingestionID string) error
	Close() error
	Health() giftInboxHealth
}

type runtimeGiftInboxSnapshot interface {
	SnapshotHealth() giftInboxHealth
}

type runtimeInboxEpoch uint64

type runtimeInboxInstallation struct {
	inbox runtimeGiftInbox
	epoch runtimeInboxEpoch
}

type ingestionFailureError struct {
	source string
	err    error
}

func (failure *ingestionFailureError) Error() string { return failure.err.Error() }
func (failure *ingestionFailureError) Unwrap() error { return failure.err }

func ingestionFailureSource(err error) string {
	var failure *ingestionFailureError
	if errors.As(err, &failure) {
		return failure.source
	}
	return "consumer"
}

type backgroundRuntime struct {
	store                    *configStore
	sourceFactory            func() giftEventSource
	reload                   chan struct{}
	startupResetReady        chan struct{}
	mu                       sync.RWMutex
	status                   runtimeStatus
	connectionGapRoomID      string
	ingestionGeneration      uint64
	ingestionErrorSource     string
	resetGate                sync.RWMutex
	resetGeneration          uint64
	animationMu              sync.Mutex
	animationWriteAtomically func(string, []byte) error
	retireResetArtifact      func(string) error
	timerMu                  sync.Mutex
	timerSchedules           map[string]timerSchedule
	timerTicks               <-chan time.Time
	attributeFreezes         attributeFreezeChecker
	notifications            *notificationCenter
	inbox                    runtimeGiftInbox
	inboxEpoch               runtimeInboxEpoch
	inboxRevision            uint64
	inboxWake                chan struct{}
	inboxRetryDelay          time.Duration
	profileTimeout           time.Duration
	profileResolver          userProfileResolver
	diagnostics              *diagnosticLogger
	onWorkerStart            func(string)
}

type timerSchedule struct {
	interval time.Duration
	next     time.Time
}

func newBackgroundRuntime(store *configStore, sourceFactory func() giftEventSource, notifications ...*notificationCenter) *backgroundRuntime {
	if sourceFactory == nil {
		sourceFactory = func() giftEventSource { return &bilibiliGiftSource{} }
	}
	var center *notificationCenter
	if len(notifications) > 0 {
		center = notifications[0]
	}
	return &backgroundRuntime{
		store:             store,
		sourceFactory:     sourceFactory,
		reload:            make(chan struct{}, 1),
		startupResetReady: make(chan struct{}, 1),
		status:            runtimeStatus{State: "idle"},
		timerSchedules:    map[string]timerSchedule{},
		notifications:     center,
		inboxWake:         make(chan struct{}, 1),
		inboxRetryDelay:   250 * time.Millisecond,
		profileTimeout:    2 * time.Second,
		profileResolver:   newBilibiliUserProfileResolver(nil, ""),
	}
}

func (runtime *backgroundRuntime) setDiagnosticLogger(logger *diagnosticLogger) {
	runtime.diagnostics = logger
	previousFactory := runtime.sourceFactory
	runtime.sourceFactory = func() giftEventSource {
		source := previousFactory()
		if bilibiliSource, ok := source.(*bilibiliGiftSource); ok {
			bilibiliSource.diagnostics = logger
		}
		return source
	}
}

func (runtime *backgroundRuntime) setAttributeFreezeChecker(freezes attributeFreezeChecker) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.attributeFreezes = freezes
}

func (runtime *backgroundRuntime) currentAttributeFreezeChecker() attributeFreezeChecker {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.attributeFreezes
}

func (runtime *backgroundRuntime) Run(ctx context.Context) {
	for runtime.store != nil && runtime.store.ValidResetIntentPending() {
		if outcome, err := runtime.reset(true); err == nil {
			runtime.store.notifyResetOutcome(outcome)
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-runtime.startupResetReady:
		}
	}
	installation, err := func() (runtimeInboxInstallation, error) {
		runtime.resetGate.RLock()
		defer runtime.resetGate.RUnlock()
		installation := runtime.currentInbox()
		if installation.inbox == nil {
			if runtime.store == nil {
				return runtimeInboxInstallation{}, fmt.Errorf("gift inbox requires a config store")
			}
			opened, openErr := openGiftInbox(filepath.Dir(runtime.store.path))
			if openErr != nil {
				return runtimeInboxInstallation{}, openErr
			}
			return runtime.installInbox(opened, opened.Health()), nil
		}
		if installation.epoch == 0 {
			return runtime.installInbox(installation.inbox, runtime.snapshotInboxHealth(installation.inbox)), nil
		}
		// A recovered inbox may be supplied before Run. Publish its startup
		// snapshot before HTTP readers can observe the runtime.
		runtime.publishInbox(installation, runtime.snapshotInboxHealth(installation.inbox))
		return installation, nil
	}()
	if err != nil {
		runtime.recordIngestionFailureFrom("open", err)
		return
	}
	inbox := installation.inbox
	defer inbox.Close()
	if kind := runtime.store.MutationBlockKind(); kind != "" {
		runtime.diagnostics.Error("gift_ingestion_failed", "reason", "consumer", "error_kind", kind)
	}

	var workers sync.WaitGroup
	select {
	case <-ctx.Done():
		return
	default:
	}
	workers.Add(2)
	go func() {
		defer workers.Done()
		runtime.workerStarted("gift")
		runtime.runGiftLoop(ctx)
	}()
	go func() {
		defer workers.Done()
		runtime.workerStarted("timer")
		runtime.runTimerLoop(ctx)
	}()
	runtime.workerStarted("connection")
	runtime.runConnectionLoop(ctx)
	workers.Wait()
}

func (runtime *backgroundRuntime) workerStarted(name string) {
	if runtime.onWorkerStart != nil {
		runtime.onWorkerStart(name)
	}
}

func (runtime *backgroundRuntime) runConnectionLoop(ctx context.Context) {
	for {
		producerGeneration := runtime.currentResetGeneration()
		if runtime.store.MutationBlockKind() != "" {
			runtime.setStatus("error", "", &stateMutationsBlockedError{})
			if !runtime.wait(ctx, 2*time.Second) {
				return
			}
			continue
		}
		state, err := runtime.store.readState()
		if err != nil {
			runtime.setStatus("error", "", err)
			if !runtime.wait(ctx, 2*time.Second) {
				return
			}
			continue
		}
		if strings.TrimSpace(state.RoomID) == "" {
			runtime.setStatus("idle", "", nil)
			select {
			case <-ctx.Done():
				return
			case <-runtime.reload:
				continue
			}
		}

		roomID := state.RoomID
		runtime.resetConnectionGapsForRoom(roomID)
		if err := runtime.prepareRoomConnectionForGeneration(producerGeneration, roomID); err != nil {
			if errors.Is(err, errResetGenerationChanged) {
				continue
			}
			runtime.setStatus("error", roomID, err)
			if !runtime.wait(ctx, 2*time.Second) {
				return
			}
			continue
		}
		runtime.setConnectionGapRoom(roomID)
		connectionContext, cancel := context.WithCancel(ctx)
		finished := make(chan error, 1)
		supervisor := newConnectionSupervisor(runtime.sourceFactory)
		supervisor.gaps = append([]connectionGap(nil), runtime.Status().ConnectionGaps...)
		supervisor.onGap = func(gaps []connectionGap) {
			runtime.withProducerGeneration(producerGeneration, func() { runtime.setConnectionGaps(gaps) })
		}
		supervisor.onFailure = func(err error) {
			runtime.withProducerGeneration(producerGeneration, func() {
				runtime.setStatus("reconnecting", roomID, err)
			})
		}
		runtime.withProducerGeneration(producerGeneration, func() { runtime.setStatus("connecting", roomID, nil) })
		go func() {
			finished <- supervisor.Run(connectionContext, roomID, runtimeCallbacks{
				onGift: func(gift giftEvent) {
					runtime.acceptGiftForGeneration(connectionContext, producerGeneration, roomID, giftCommandCategory(gift), gift)
				},
				onFrame: func() {
					runtime.withProducerGeneration(producerGeneration, func() { runtime.recordLastFrame(roomID) })
				},
				onGiftCatalog: func(gifts []roomGiftInfo) {
					runtime.withProducerGeneration(producerGeneration, func() { runtime.mergeBlindBoxGiftCatalog(gifts) })
				},
				onGiftCatalogError: func(err error) {
					runtime.withProducerGeneration(producerGeneration, func() {
						runtime.diagnostics.Error("blind_box_catalog_failed", "room_id", roomID, "reason", "catalog_fetch_failed", "error", err)
					})
				},
				onState: func(status string) {
					runtime.withProducerGeneration(producerGeneration, func() { runtime.setStatus(status, roomID, nil) })
				},
			})
		}()

		select {
		case <-ctx.Done():
			cancel()
			<-finished
			return
		case <-runtime.reload:
			cancel()
			<-finished
			continue
		case err := <-finished:
			cancel()
			if ctx.Err() != nil {
				return
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				runtime.withProducerGeneration(producerGeneration, func() { runtime.setStatus("error", roomID, err) })
			}
			if !runtime.wait(ctx, time.Second) {
				return
			}
		}
	}
}

var errResetGenerationChanged = errors.New("runtime reset generation changed")

func (runtime *backgroundRuntime) prepareRoomConnection(roomID string) error {
	return runtime.prepareRoomConnectionForGeneration(runtime.currentResetGeneration(), roomID)
}

func (runtime *backgroundRuntime) prepareRoomConnectionForGeneration(generation uint64, roomID string) error {
	runtime.resetGate.RLock()
	defer runtime.resetGate.RUnlock()
	if generation != runtime.currentResetGeneration() {
		return errResetGenerationChanged
	}
	roomID = strings.TrimSpace(roomID)
	runtime.animationMu.Lock()
	defer runtime.animationMu.Unlock()
	metadata, err := runtime.loadPendingGiftAnimationFileLocked()
	if err != nil {
		return err
	}
	preparedRoomID := strings.TrimSpace(metadata.PreparedRoomID)
	if preparedRoomID == roomID {
		return nil
	}
	if _, err := runtime.store.updateState(func(state *appState) error {
		state.RecentSourceGiftKeys = map[string]int64{}
		return nil
	}); err != nil {
		return err
	}
	filtered := []pendingGiftAnimation{}
	if preparedRoomID != "" {
		for _, record := range metadata.Records {
			if strings.TrimSpace(record.RoomID) != preparedRoomID {
				filtered = append(filtered, record)
			}
		}
	}
	metadata.PreparedRoomID = roomID
	metadata.Records = filtered
	if err := runtime.savePendingGiftAnimationFileLocked(metadata); err != nil {
		return err
	}
	select {
	case runtime.inboxWake <- struct{}{}:
	default:
	}
	return nil
}

func giftCommandCategory(gift giftEvent) string {
	if gift.GiftID == specialGiftSuperChat {
		return "SUPER_CHAT_MESSAGE"
	}
	if gift.GiftID == specialGiftGuardCaptain || gift.GiftID == specialGiftGuardAdmiral || gift.GiftID == specialGiftGuardGovernor {
		return "GUARD_BUY"
	}
	return "SEND_GIFT"
}

func (runtime *backgroundRuntime) acceptGift(ctx context.Context, roomID, command string, gift giftEvent) {
	runtime.acceptGiftForGeneration(ctx, runtime.currentResetGeneration(), roomID, command, gift)
}

func (runtime *backgroundRuntime) acceptGiftForGeneration(ctx context.Context, generation uint64, roomID, command string, gift giftEvent) {
	runtime.resetGate.RLock()
	defer runtime.resetGate.RUnlock()
	if ctx.Err() != nil || generation != runtime.currentResetGeneration() {
		return
	}
	if runtime.store != nil && runtime.store.MutationBlockKind() != "" {
		return
	}
	installation := runtime.currentInbox()
	if installation.inbox == nil {
		runtime.recordIngestionFailure(fmt.Errorf("gift inbox is not available"))
		return
	}
	inbox := installation.inbox
	ingestionGeneration := runtime.currentIngestionGeneration()
	acceptedAt := time.Now()
	_, err := inbox.Accept(roomID, command, gift)
	acceptWriteLatency := time.Since(acceptedAt)
	committedWithWarning := giftInboxRecordCommitted(err)
	if err != nil && !committedWithWarning {
		runtime.publishInbox(installation, runtime.snapshotInboxHealth(inbox))
		runtime.recordIngestionFailureFrom("accept", err)
		return
	}
	if committedWithWarning {
		runtime.recordIngestionFailureFrom("accept", err)
	} else {
		runtime.clearIngestionFailure(ingestionGeneration, "accept")
	}
	health := runtime.snapshotInboxHealth(inbox)
	runtime.publishInbox(installation, health)
	oldestPendingAge := int64(0)
	if health.OldestPendingAt > 0 {
		oldestPendingAge = maxInt64(0, acceptedAt.UnixMilli()-health.OldestPendingAt*1000)
	}
	runtime.diagnostics.Info(
		"gift_accepted",
		"gift_id", gift.GiftID,
		"blind_parent_id", gift.BlindGiftID,
		"count", maxInt(1, gift.Num),
		"timestamp", gift.Timestamp,
		"rnd_hash", diagnosticHash(gift.Rnd),
		"accept_write_ms", acceptWriteLatency.Milliseconds(),
		"inbox_depth", health.PendingCount,
		"oldest_pending_age_ms", oldestPendingAge,
	)
	select {
	case runtime.inboxWake <- struct{}{}:
	default:
	}
}

func (runtime *backgroundRuntime) mergeBlindBoxGiftCatalog(gifts []roomGiftInfo) {
	mappedChildren := 0
	_, err := runtime.store.updateState(func(state *appState) error {
		for _, gift := range gifts {
			if !gift.BlindBoxParent && gift.BlindBoxParentID <= 0 {
				continue
			}
			if gift.BlindBoxParentID > 0 {
				mappedChildren++
			}
			mapped := giftInfo{
				ID: gift.ID, Name: gift.Name, Price: gift.Price, CoinType: gift.CoinType, ImgBasic: gift.ImgBasic,
				AnimationGIF: gift.AnimationGIF, AnimationWebP: gift.AnimationWebP, AnimationDurationMS: gift.AnimationDurationMS,
				EffectID: gift.EffectID, EffectMP4: gift.EffectMP4, EffectMP4JSON: gift.EffectMP4JSON,
				BlindBoxParentID: gift.BlindBoxParentID, BlindBoxParentName: gift.BlindBoxParentName, BlindBoxParentPrice: gift.BlindBoxParentPrice,
			}
			if index := findGiftIndex(state.GiftCatalog, gift.ID); index >= 0 {
				state.GiftCatalog[index] = mapped
			} else {
				state.GiftCatalog = append(state.GiftCatalog, mapped)
			}
		}
		return nil
	})
	if err != nil {
		runtime.diagnostics.Error("blind_box_catalog_save_failed", "reason", "state_save_failed", "error", err)
		return
	}
	runtime.diagnostics.Info("blind_box_catalog_ready", "mapped_children", mappedChildren)
}

func findGiftIndex(gifts []giftInfo, giftID int) int {
	for index := range gifts {
		if gifts[index].ID == giftID {
			return index
		}
	}
	return -1
}

func (runtime *backgroundRuntime) runGiftLoop(ctx context.Context) {
	retryDelay := runtime.inboxRetryDelay
	if retryDelay <= 0 {
		retryDelay = 250 * time.Millisecond
	}
	retry := time.NewTicker(retryDelay)
	defer retry.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if runtime.store != nil && runtime.store.MutationBlockKind() != "" {
			select {
			case <-ctx.Done():
				return
			case <-runtime.inboxWake:
			case <-retry.C:
			}
			continue
		}
		processed, err := runtime.consumeAvailableInboxRecord(ctx)
		if err != nil {
			runtime.recordIngestionFailureFrom(ingestionFailureSource(err), err)
			select {
			case <-ctx.Done():
				return
			case <-retry.C:
			}
			continue
		}
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-runtime.inboxWake:
		case <-retry.C:
		}
	}
}

func (runtime *backgroundRuntime) consumeAvailableInboxRecord(ctx context.Context) (bool, error) {
	runtime.resetGate.RLock()
	defer runtime.resetGate.RUnlock()
	ingestionGeneration := runtime.currentIngestionGeneration()
	installation := runtime.currentInbox()
	if installation.inbox == nil {
		return false, fmt.Errorf("gift inbox is not available")
	}
	inbox := installation.inbox
	record, ok, err := inbox.Next()
	if err != nil {
		return false, &ingestionFailureError{source: "next", err: err}
	}
	runtime.clearIngestionFailure(ingestionGeneration, "next")
	if !ok {
		return false, nil
	}
	if err := runtime.consumeClaimedInboxRecord(ctx, installation, record); err != nil {
		if err == errRoomPreparationPending {
			return false, nil
		}
		return false, err
	}
	runtime.clearIngestionFailure(ingestionGeneration, "consumer")
	return true, nil
}

func (runtime *backgroundRuntime) consumeClaimedInboxRecord(ctx context.Context, installation runtimeInboxInstallation, record giftInboxRecord) (err error) {
	ingestionGeneration := runtime.currentIngestionGeneration()
	inbox := installation.inbox
	acknowledged := false
	defer func() {
		if acknowledged {
			return
		}
		if releaseErr := inbox.Release(record.IngestionID); releaseErr != nil && !errors.Is(releaseErr, errGiftInboxClosed) {
			err = errors.Join(err, &ingestionFailureError{source: "release", err: releaseErr})
		} else if releaseErr == nil {
			runtime.clearIngestionFailure(ingestionGeneration, "release")
		}
		runtime.publishInbox(installation, runtime.snapshotInboxHealth(inbox))
	}()
	if err := runtime.processPreparedInboxRecord(ctx, record); err != nil {
		return err
	}
	if err := inbox.Acknowledge(record.IngestionID); err != nil {
		return &ingestionFailureError{source: "ack", err: err}
	}
	health := runtime.snapshotInboxHealth(inbox)
	runtime.publishInbox(installation, health)
	runtime.clearIngestionFailure(ingestionGeneration, "ack")
	runtime.clearCapacityFailureIfDrained(installation, health, ingestionGeneration)
	acknowledged = true
	return nil
}

var errRoomPreparationPending = errors.New("room preparation pending")

func (runtime *backgroundRuntime) processInboxRecord(ctx context.Context, record giftInboxRecord) error {
	runtime.animationMu.Lock()
	defer runtime.animationMu.Unlock()
	return runtime.processInboxRecordLocked(ctx, record, false)
}

func (runtime *backgroundRuntime) processPreparedInboxRecord(ctx context.Context, record giftInboxRecord) error {
	runtime.animationMu.Lock()
	defer runtime.animationMu.Unlock()
	return runtime.processInboxRecordLocked(ctx, record, true)
}

func (runtime *backgroundRuntime) processInboxRecordLocked(ctx context.Context, record giftInboxRecord, requirePreparation bool) error {
	gift := record.Gift
	current, err := runtime.store.readState()
	if err != nil {
		return err
	}
	recordRoomID := strings.TrimSpace(record.RoomID)
	currentRoomID := strings.TrimSpace(current.RoomID)
	preparedRoomID := ""
	if requirePreparation && currentRoomID != "" {
		metadata, loadErr := runtime.loadPendingGiftAnimationFileLocked()
		if loadErr != nil {
			return loadErr
		}
		preparedRoomID = strings.TrimSpace(metadata.PreparedRoomID)
		if preparedRoomID != currentRoomID {
			return errRoomPreparationPending
		}
	}
	roomMatches := recordRoomID != "" && currentRoomID != "" && recordRoomID == currentRoomID
	pendingAnimations := []pendingGiftAnimation{}
	matchedPendingIndex := -1
	if roomMatches && !gift.AnimationOnly {
		pendingAnimations, err = runtime.loadPendingGiftAnimationsLocked()
		if err != nil {
			return err
		}
		matchedPendingIndex = findPendingGiftAnimation(pendingAnimations, record.RoomID, gift)
		if matchedPendingIndex >= 0 {
			gift = mergePendingAnimationIntoGift(gift, pendingAnimations[matchedPendingIndex].Gift)
		}
	}
	if roomMatches && !gift.AnimationOnly && needsUserProfile(gift) && runtime.profileResolver != nil {
		profileTimeout := runtime.profileTimeout
		if profileTimeout <= 0 {
			profileTimeout = 2 * time.Second
		}
		profileContext, cancel := context.WithTimeout(ctx, profileTimeout)
		profile, resolveErr := runtime.profileResolver.Resolve(profileContext, gift.UID)
		cancel()
		if resolveErr == nil {
			if isMaskedUsername(gift.Uname) && profile.Name != "" {
				gift.Uname = profile.Name
			}
			if strings.TrimSpace(gift.Avatar) == "" && profile.Avatar != "" {
				gift.Avatar = profile.Avatar
			}
		}
	}

	now := time.Now()
	runtime.diagnostics.Info(
		"gift_transaction_prepare",
		"gift_id", gift.GiftID,
		"count", maxInt(1, gift.Num),
		"timestamp", gift.Timestamp,
		"rnd_hash", diagnosticHash(gift.Rnd),
	)
	settlement := giftSettlement{}
	_, applied, err := runtime.store.updateStateForIngestion(record.IngestionID, func(state *appState) error {
		if requirePreparation && strings.TrimSpace(state.RoomID) != preparedRoomID {
			return errRoomPreparationPending
		}
		settlement = settleGiftInStateWithFreeze(state, record.RoomID, gift, now, true, runtime.currentAttributeFreezeChecker())
		return nil
	})
	if err != nil {
		return err
	}
	if !applied {
		runtime.diagnostics.Info(
			"gift_transaction_recovery",
			"gift_id", gift.GiftID,
			"rnd_hash", diagnosticHash(gift.Rnd),
		)
	} else {
		runtime.diagnostics.Info(
			"gift_transaction_complete",
			"gift_id", gift.GiftID,
			"count", maxInt(1, gift.Num),
			"rnd_hash", diagnosticHash(gift.Rnd),
		)
	}
	if roomMatches && gift.AnimationOnly {
		attached := settlement.animationAttached
		if !applied {
			latest, readErr := runtime.store.readState()
			if readErr != nil {
				return readErr
			}
			attached = giftAnimationAttachedToReceipt(latest, gift)
		}
		if !attached {
			if err := runtime.addPendingGiftAnimationLocked(record.RoomID, gift); err != nil {
				return err
			}
		}
	}
	if roomMatches && !gift.AnimationOnly && matchedPendingIndex >= 0 {
		pendingAnimations = append(pendingAnimations[:matchedPendingIndex], pendingAnimations[matchedPendingIndex+1:]...)
		if err := runtime.savePendingGiftAnimationsLocked(pendingAnimations); err != nil {
			return err
		}
	}
	if !applied {
		return nil
	}
	if settlement.roomMismatch {
		runtime.diagnostics.Info("gift_ignored", "reason", "room_mismatch", "room_id", record.RoomID, "gift_id", gift.GiftID, "rnd_hash", diagnosticHash(gift.Rnd))
		return nil
	}
	if settlement.animationOnly {
		return nil
	}
	if settlement.sourceDuplicate {
		runtime.diagnostics.Info(
			"gift_ignored",
			"reason", "duplicate",
			"gift_id", gift.GiftID,
			"count", maxInt(1, gift.Num),
			"timestamp", gift.Timestamp,
			"rnd_hash", diagnosticHash(gift.Rnd),
			"source_duplicate", true,
		)
		return nil
	}
	runtime.mu.Lock()
	runtime.status.LastGiftAt = time.Now().UnixMilli()
	runtime.mu.Unlock()
	runtime.diagnostics.Info(
		"gift_received",
		"gift_id", settlement.gift.GiftID,
		"count", maxInt(1, settlement.gift.Num),
		"timestamp", settlement.gift.Timestamp,
		"rnd_hash", diagnosticHash(settlement.gift.Rnd),
		"source_duplicate", false,
		"blind_parent_id", settlement.gift.BlindGiftID,
		"blind_source", settlement.blindSource,
		"blind_cost", settlement.blindCost,
		"blind_value", settlement.blindValue,
		"blind_priced", settlement.blindPriced,
	)
	return nil
}

type giftSettlement struct {
	gift              giftEvent
	roomMismatch      bool
	animationOnly     bool
	sourceDuplicate   bool
	blindSource       string
	blindCost         float64
	blindValue        float64
	blindPriced       bool
	animationAttached bool
}

func settleGiftInState(state *appState, roomID string, gift giftEvent, now time.Time, durableDedupe bool) giftSettlement {
	return settleGiftInStateWithFreeze(state, roomID, gift, now, durableDedupe, nil)
}

func settleGiftInStateWithFreeze(state *appState, roomID string, gift giftEvent, now time.Time, durableDedupe bool, freezes attributeFreezeChecker) giftSettlement {
	settlement := giftSettlement{gift: gift, animationOnly: gift.AnimationOnly, blindSource: "none"}
	recordRoomID := strings.TrimSpace(roomID)
	currentRoomID := strings.TrimSpace(state.RoomID)
	if recordRoomID == "" || currentRoomID == "" || recordRoomID != currentRoomID {
		settlement.roomMismatch = true
		return settlement
	}
	if durableDedupe {
		normalizeInternalIngestionLedgers(state, now)
	}
	if gift.AnimationOnly {
		settlement.animationAttached = attachGiftAnimationToReceipt(state, gift)
		return settlement
	}
	if durableDedupe {
		if gift.Rnd != "" {
			last, exists := state.RecentSourceGiftKeys[gift.Rnd]
			settlement.sourceDuplicate = exists && last > now.Add(-time.Minute).UnixMilli()
			state.RecentSourceGiftKeys[gift.Rnd] = now.UnixMilli()
		}
		if settlement.sourceDuplicate {
			return settlement
		}
	}
	originalBlindGiftID := gift.BlindGiftID
	settlement.gift = gift
	settlement.gift = enrichBlindBoxGiftFromCatalog(*state, settlement.gift)
	if settlement.gift.BlindGiftID > 0 {
		settlement.blindSource = "catalog"
		if originalBlindGiftID > 0 {
			settlement.blindSource = "event"
		}
		count := maxInt(1, settlement.gift.Num)
		settlement.blindCost, settlement.blindPriced = blindBoxCost(*state, settlement.gift, count)
		settlement.blindValue = blindBoxOutputValue(*state, settlement.gift, count)
	}
	applyGiftEventWithFreeze(state, settlement.gift, freezes)
	return settlement
}

func mergePendingAnimationIntoGift(gift, pending giftEvent) giftEvent {
	if gift.Membership == "" {
		gift.Membership = pending.Membership
	}
	gift.EffectID = pending.EffectID
	gift.EffectMP4 = pending.EffectMP4
	gift.EffectMP4JSON = pending.EffectMP4JSON
	gift.AnimationGIF = pending.AnimationGIF
	gift.AnimationWebP = pending.AnimationWebP
	gift.AnimationDurationMS = pending.AnimationDurationMS
	return gift
}

const pendingGiftAnimationsSchemaVersion = 1
const maxPendingGiftAnimations = 500

type pendingGiftAnimation struct {
	RoomID string    `json:"roomId"`
	Gift   giftEvent `json:"gift"`
}

type pendingGiftAnimationFile struct {
	SchemaVersion  int                    `json:"schemaVersion"`
	PreparedRoomID string                 `json:"preparedRoomId,omitempty"`
	Records        []pendingGiftAnimation `json:"records"`
}

func (runtime *backgroundRuntime) pendingGiftAnimationsPath() string {
	return filepath.Join(filepath.Dir(runtime.store.path), "pending-gift-animations.json")
}

func (runtime *backgroundRuntime) loadPendingGiftAnimations() ([]pendingGiftAnimation, error) {
	file, err := runtime.loadPendingGiftAnimationFile()
	return file.Records, err
}

func (runtime *backgroundRuntime) loadPendingGiftAnimationFile() (pendingGiftAnimationFile, error) {
	runtime.animationMu.Lock()
	defer runtime.animationMu.Unlock()
	return runtime.loadPendingGiftAnimationFileLocked()
}

func (runtime *backgroundRuntime) loadPendingGiftAnimationsLocked() ([]pendingGiftAnimation, error) {
	file, err := runtime.loadPendingGiftAnimationFileLocked()
	return file.Records, err
}

func (runtime *backgroundRuntime) loadPendingGiftAnimationFileLocked() (pendingGiftAnimationFile, error) {
	data, err := os.ReadFile(runtime.pendingGiftAnimationsPath())
	if errors.Is(err, os.ErrNotExist) {
		return pendingGiftAnimationFile{SchemaVersion: pendingGiftAnimationsSchemaVersion, Records: []pendingGiftAnimation{}}, nil
	}
	if err != nil {
		return pendingGiftAnimationFile{}, fmt.Errorf("read pending gift animations: %w", err)
	}
	var file pendingGiftAnimationFile
	if err := json.Unmarshal(data, &file); err != nil {
		return pendingGiftAnimationFile{}, fmt.Errorf("parse pending gift animations: %w", err)
	}
	if file.SchemaVersion != pendingGiftAnimationsSchemaVersion || file.Records == nil {
		return pendingGiftAnimationFile{}, fmt.Errorf("unsupported pending gift animations schema")
	}
	return file, nil
}

func (runtime *backgroundRuntime) savePendingGiftAnimations(records []pendingGiftAnimation) error {
	runtime.animationMu.Lock()
	defer runtime.animationMu.Unlock()
	file, err := runtime.loadPendingGiftAnimationFileLocked()
	if err != nil {
		return err
	}
	file.Records = records
	return runtime.savePendingGiftAnimationFileLocked(file)
}

func (runtime *backgroundRuntime) savePendingGiftAnimationsLocked(records []pendingGiftAnimation) error {
	file, err := runtime.loadPendingGiftAnimationFileLocked()
	if err != nil {
		return err
	}
	file.Records = records
	return runtime.savePendingGiftAnimationFileLocked(file)
}

func (runtime *backgroundRuntime) savePendingGiftAnimationFile(file pendingGiftAnimationFile) error {
	runtime.animationMu.Lock()
	defer runtime.animationMu.Unlock()
	return runtime.savePendingGiftAnimationFileLocked(file)
}

func (runtime *backgroundRuntime) savePendingGiftAnimationFileLocked(file pendingGiftAnimationFile) error {
	records := file.Records
	if len(records) > maxPendingGiftAnimations {
		records = records[len(records)-maxPendingGiftAnimations:]
	}
	file.SchemaVersion = pendingGiftAnimationsSchemaVersion
	file.Records = records
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("serialize pending gift animations: %w", err)
	}
	write := runtime.animationWriteAtomically
	if write == nil {
		write = writeFileAtomically
	}
	if err := write(runtime.pendingGiftAnimationsPath(), append(data, '\n')); err != nil {
		return fmt.Errorf("persist pending gift animations: %w", err)
	}
	return nil
}

func (runtime *backgroundRuntime) addPendingGiftAnimation(roomID string, gift giftEvent) error {
	if giftReceiptAnimationFromEvent(gift) == nil {
		return nil
	}
	runtime.animationMu.Lock()
	defer runtime.animationMu.Unlock()
	return runtime.addPendingGiftAnimationLocked(roomID, gift)
}

func (runtime *backgroundRuntime) addPendingGiftAnimationLocked(roomID string, gift giftEvent) error {
	records, err := runtime.loadPendingGiftAnimationsLocked()
	if err != nil {
		return err
	}
	for index := range records {
		if samePendingGiftAnimation(records[index], roomID, gift) {
			records[index] = pendingGiftAnimation{RoomID: strings.TrimSpace(roomID), Gift: gift}
			return runtime.savePendingGiftAnimationsLocked(records)
		}
	}
	records = append(records, pendingGiftAnimation{RoomID: strings.TrimSpace(roomID), Gift: gift})
	return runtime.savePendingGiftAnimationsLocked(records)
}

func findPendingGiftAnimation(records []pendingGiftAnimation, roomID string, gift giftEvent) int {
	for index := range records {
		if samePendingGiftAnimation(records[index], roomID, gift) && nearbyGiftTimestamps(records[index].Gift.Timestamp, gift.Timestamp) {
			return index
		}
	}
	return -1
}

func samePendingGiftAnimation(record pendingGiftAnimation, roomID string, gift giftEvent) bool {
	return strings.TrimSpace(record.RoomID) == strings.TrimSpace(roomID) && record.Gift.GiftID == gift.GiftID && record.Gift.UID == gift.UID
}

func giftAnimationAttachedToReceipt(state appState, gift giftEvent) bool {
	want := giftReceiptAnimationFromEvent(gift)
	if want == nil {
		return true
	}
	for _, receipt := range state.GiftReceipts {
		if receipt.GiftID == gift.GiftID && receipt.SenderUID == gift.UID && nearbyGiftTimestamps(receipt.Time, gift.Timestamp) && receipt.Animation != nil && receipt.Animation.EffectID == want.EffectID {
			return true
		}
	}
	return false
}

func (runtime *backgroundRuntime) recordIngestionFailure(err error) {
	runtime.recordIngestionFailureFrom("consumer", err)
}

func (runtime *backgroundRuntime) recordIngestionFailureFrom(source string, err error) {
	if err == nil {
		return
	}
	runtime.mu.Lock()
	runtime.ingestionGeneration++
	runtime.ingestionErrorSource = source
	runtime.status.IngestionError = err.Error()
	runtime.status.IngestionErrorKind = safeIngestionFailureKind(source, err)
	runtime.mu.Unlock()
	runtime.diagnostics.Error("gift_ingestion_failed", "reason", safeIngestionFailureSource(source), "error_kind", safeIngestionFailureKind(source, err))
}

func safeIngestionFailureKind(source string, err error) string {
	if giftInboxRecordCommitted(err) {
		return "inbox_durability"
	}
	if errors.Is(err, errGiftInboxCapacity) {
		return "inbox_capacity"
	}
	switch source {
	case "open":
		return "inbox_open"
	case "accept":
		return "inbox_persist"
	case "next", "ack", "release":
		return "inbox_recovery"
	default:
		return "transaction"
	}
}

func safeIngestionFailureSource(source string) string {
	switch source {
	case "accept", "consumer":
		return source
	default:
		return "consumer"
	}
}

func (runtime *backgroundRuntime) currentIngestionGeneration() uint64 {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.ingestionGeneration
}

func (runtime *backgroundRuntime) clearIngestionFailure(generation uint64, source string) {
	runtime.mu.Lock()
	if runtime.ingestionGeneration == generation && runtime.ingestionErrorSource == source {
		runtime.status.IngestionError = ""
		runtime.status.IngestionErrorKind = ""
		runtime.ingestionErrorSource = ""
	}
	runtime.mu.Unlock()
}

func (runtime *backgroundRuntime) runTimerLoop(ctx context.Context) {
	if runtime.timerTicks != nil {
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-runtime.timerTicks:
				runtime.handleTimerTick(now)
			}
		}
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			runtime.handleTimerTick(now)
		}
	}
}

func (runtime *backgroundRuntime) handleTimerTick(now time.Time) {
	runtime.resetGate.RLock()
	defer runtime.resetGate.RUnlock()
	if runtime.store == nil || runtime.store.MutationBlockKind() != "" {
		return
	}
	state, err := runtime.store.readState()
	if err != nil {
		status := runtime.Status()
		runtime.setStatus("error", status.RoomID, err)
		return
	}
	dueRuleIDs := runtime.dueTimerRuleIDs(state, now)
	dueActivityIDs := dueActivityGiftTimeoutIDs(state, now)
	if len(dueRuleIDs) == 0 && len(dueActivityIDs) == 0 {
		return
	}
	_, err = runtime.store.updateState(func(current *appState) error {
		appliedRules := applyTimerRulesWithFreeze(current, dueRuleIDs, now, runtime.currentAttributeFreezeChecker())
		appliedTimeouts := applyActivityGiftTimeouts(current, dueActivityIDs, now)
		if appliedRules == 0 && appliedTimeouts == 0 {
			return errNoTimerChanges
		}
		return nil
	})
	if err != nil && !errors.Is(err, errNoTimerChanges) {
		status := runtime.Status()
		runtime.setStatus("error", status.RoomID, err)
	}
}

func (runtime *backgroundRuntime) dueTimerRuleIDs(state appState, now time.Time) []string {
	runtime.timerMu.Lock()
	defer runtime.timerMu.Unlock()
	valid := make(map[string]struct{}, len(state.TimerRules))
	due := []string{}
	for _, rule := range state.TimerRules {
		if !rule.Enabled || rule.IntervalSeconds < 1 || !activityAllowsRulesForAttribute(state, rule.AttributeName) {
			continue
		}
		valid[rule.ID] = struct{}{}
		interval := time.Duration(rule.IntervalSeconds) * time.Second
		schedule, exists := runtime.timerSchedules[rule.ID]
		if !exists || schedule.interval != interval {
			runtime.timerSchedules[rule.ID] = timerSchedule{interval: interval, next: now.Add(interval)}
			continue
		}
		if !now.Before(schedule.next) {
			due = append(due, rule.ID)
			schedule.next = now.Add(interval)
			runtime.timerSchedules[rule.ID] = schedule
		}
	}
	for ruleID := range runtime.timerSchedules {
		if _, exists := valid[ruleID]; !exists {
			delete(runtime.timerSchedules, ruleID)
		}
	}
	return due
}

func (runtime *backgroundRuntime) NotifyConfigChanged() {
	select {
	case runtime.reload <- struct{}{}:
	default:
	}
}

type runtimeGiftInboxResetter interface {
	Reset() error
}

func (runtime *backgroundRuntime) currentResetGeneration() uint64 {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.resetGeneration
}

func (runtime *backgroundRuntime) withProducerGeneration(generation uint64, callback func()) {
	runtime.resetGate.RLock()
	defer runtime.resetGate.RUnlock()
	if generation != runtime.currentResetGeneration() {
		return
	}
	callback()
}

func (runtime *backgroundRuntime) resetFailure(err error) error {
	if runtime.store != nil {
		runtime.store.recordResetFailure(err)
	}
	runtime.diagnostics.Error("gift_ingestion_failed", "reason", "consumer", "error_kind", "reset_failure", "error", err)
	return err
}

// Reset is the runtime-wide barrier behind the configuration API's 恢复默认
// action. Producers and the claim/settle/ack consumer hold a read lock, so the
// write lock waits for in-flight work and prevents new work until every owned
// durable artifact has been cleared or the store has been failed closed.
func (runtime *backgroundRuntime) Reset() error {
	_, err := runtime.ResetWithOutcome()
	return err
}

func (runtime *backgroundRuntime) ResetWithOutcome() (resetOutcome, error) {
	pendingOnly := runtime.store != nil && runtime.store.ValidResetIntentPending()
	return runtime.reset(pendingOnly)
}

func (runtime *backgroundRuntime) reset(pendingOnly bool) (resetOutcome, error) {
	runtime.resetGate.Lock()
	defer runtime.resetGate.Unlock()
	if runtime.store == nil {
		return resetOutcome{}, runtime.resetFailure(fmt.Errorf("runtime reset requires a config store"))
	}
	if pendingOnly && !runtime.store.ValidResetIntentPending() {
		runtime.signalStartupResetReady()
		return resetOutcome{}, nil
	}
	runtime.mu.Lock()
	if runtime.resetGeneration == ^uint64(0) {
		runtime.mu.Unlock()
		return resetOutcome{}, runtime.resetFailure(fmt.Errorf("runtime reset generation exhausted"))
	}
	runtime.resetGeneration++
	installation := runtimeInboxInstallation{inbox: runtime.inbox, epoch: runtime.inboxEpoch}
	runtime.mu.Unlock()
	if err := runtime.store.beginResetIntent(); err != nil {
		return resetOutcome{}, runtime.resetFailure(err)
	}

	resetInbox := installation.inbox
	closeResetInbox := false
	if resetInbox == nil {
		opened, err := openGiftInbox(filepath.Dir(runtime.store.path))
		if err != nil {
			return resetOutcome{}, runtime.resetFailure(err)
		}
		resetInbox = opened
		closeResetInbox = true
	}
	if closeResetInbox {
		defer resetInbox.Close()
	}
	resetter, ok := resetInbox.(runtimeGiftInboxResetter)
	if !ok {
		return resetOutcome{}, runtime.resetFailure(fmt.Errorf("installed gift inbox does not support reset"))
	}
	if err := resetter.Reset(); err != nil {
		return resetOutcome{}, runtime.resetFailure(err)
	}
	if err := runtime.resetPendingGiftAnimations(); err != nil {
		return resetOutcome{}, runtime.resetFailure(err)
	}
	outcome, err := runtime.store.resetStateArtifactsWithOutcome()
	if err != nil {
		return resetOutcome{}, runtime.resetFailure(err)
	}

	runtime.mu.Lock()
	runtime.status.State = "idle"
	runtime.status.RoomID = ""
	runtime.status.LastError = ""
	runtime.status.IngestionError = ""
	runtime.status.IngestionErrorKind = ""
	runtime.status.LastGiftAt = 0
	runtime.status.LastFrameAt = 0
	runtime.status.ConnectionGaps = nil
	runtime.status.Gaps = nil
	runtime.status.ReconnectAttempts = 0
	runtime.connectionGapRoomID = ""
	runtime.ingestionErrorSource = ""
	runtime.ingestionGeneration++
	runtime.mu.Unlock()
	runtime.timerMu.Lock()
	clear(runtime.timerSchedules)
	runtime.timerMu.Unlock()
	if installation.inbox != nil {
		runtime.publishInbox(installation, runtime.snapshotInboxHealth(installation.inbox))
	}
	select {
	case runtime.reload <- struct{}{}:
	default:
	}
	select {
	case runtime.inboxWake <- struct{}{}:
	default:
	}
	runtime.signalStartupResetReady()
	return outcome, nil
}

func (runtime *backgroundRuntime) signalStartupResetReady() {
	select {
	case runtime.startupResetReady <- struct{}{}:
	default:
	}
}

func (runtime *backgroundRuntime) resetPendingGiftAnimations() error {
	runtime.animationMu.Lock()
	defer runtime.animationMu.Unlock()
	path := runtime.pendingGiftAnimationsPath()
	dir := filepath.Dir(path)
	if err := validateResetScanDirectory(dir, dir); err != nil {
		return fmt.Errorf("validate pending gift animation reset directory: %w", err)
	}
	retire := runtime.retireResetArtifact
	if retire == nil {
		retire = retireFileDurably
	}
	if err := retire(path); err != nil {
		return fmt.Errorf("remove pending gift animations during reset: %w", err)
	}
	return nil
}

func (runtime *backgroundRuntime) NotifyTimerConfigChanged() {
	runtime.timerMu.Lock()
	clear(runtime.timerSchedules)
	runtime.timerMu.Unlock()
}

func (runtime *backgroundRuntime) Status() runtimeStatus {
	runtime.mu.RLock()
	connectionGaps := append([]connectionGap(nil), runtime.status.ConnectionGaps...)
	status := runtimeStatus{
		State:              runtime.status.State,
		RoomID:             runtime.status.RoomID,
		LastError:          runtime.status.LastError,
		IngestionError:     runtime.status.IngestionError,
		IngestionErrorKind: runtime.status.IngestionErrorKind,
		LastGiftAt:         runtime.status.LastGiftAt,
		LastFrameAt:        runtime.status.LastFrameAt,
		ConnectionGaps:     connectionGaps,
		Gaps:               append([]connectionGap(nil), connectionGaps...),
		ReconnectAttempts:  runtime.status.ReconnectAttempts,
		Inbox:              runtime.status.Inbox,
	}
	if len(status.Gaps) > 0 {
		status.ReconnectAttempts = status.Gaps[len(status.Gaps)-1].Attempts
	}
	store := runtime.store
	runtime.mu.RUnlock()
	if store != nil {
		status.TransactionPending = store.TransactionPending()
		if kind := store.MutationBlockKind(); kind != "" {
			status.IngestionErrorKind = kind
		}
	}
	return status
}

func (runtime *backgroundRuntime) clearCapacityFailureIfDrained(installation runtimeInboxInstallation, health giftInboxHealth, generation uint64) {
	if health.CapacityError {
		return
	}
	runtime.mu.Lock()
	if runtime.status.IngestionErrorKind == "inbox_capacity" && runtime.ingestionErrorSource == "accept" && runtime.ingestionGeneration == generation && runtime.inboxEpoch == installation.epoch && runtime.inboxRevision == health.Revision {
		runtime.status.IngestionError = ""
		runtime.status.IngestionErrorKind = ""
		runtime.ingestionErrorSource = ""
	}
	runtime.mu.Unlock()
}

func (runtime *backgroundRuntime) recordLastFrame(roomID string) {
	runtime.mu.Lock()
	if runtime.status.RoomID == roomID {
		runtime.status.LastFrameAt = time.Now().UnixMilli()
	}
	runtime.mu.Unlock()
}

func (runtime *backgroundRuntime) currentInbox() runtimeInboxInstallation {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtimeInboxInstallation{inbox: runtime.inbox, epoch: runtime.inboxEpoch}
}

func (runtime *backgroundRuntime) snapshotInboxHealth(inbox runtimeGiftInbox) giftInboxHealth {
	if snapshot, ok := inbox.(runtimeGiftInboxSnapshot); ok {
		return snapshot.SnapshotHealth()
	}
	return inbox.Health()
}

func (runtime *backgroundRuntime) installInbox(inbox runtimeGiftInbox, health giftInboxHealth) runtimeInboxInstallation {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if inbox == nil {
		panic("cannot install a nil gift inbox")
	}
	if runtime.inboxEpoch == ^runtimeInboxEpoch(0) {
		panic("gift inbox installation epoch exhausted")
	}
	runtime.inboxEpoch++
	runtime.inbox = inbox
	runtime.inboxRevision = health.Revision
	runtime.status.Inbox = &health
	return runtimeInboxInstallation{inbox: inbox, epoch: runtime.inboxEpoch}
}

func (runtime *backgroundRuntime) publishInbox(installation runtimeInboxInstallation, health giftInboxHealth) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if installation.epoch == 0 || installation.epoch != runtime.inboxEpoch || health.Revision < runtime.inboxRevision {
		return
	}
	runtime.inboxRevision = health.Revision
	runtime.status.Inbox = &health
}

func (runtime *backgroundRuntime) setConnectionGaps(gaps []connectionGap) {
	runtime.mu.Lock()
	runtime.status.ConnectionGaps = append([]connectionGap(nil), gaps...)
	runtime.mu.Unlock()
	if len(gaps) == 0 {
		return
	}
	latest := gaps[len(gaps)-1]
	runtime.diagnostics.Info(
		"connection_gap",
		"attempts", latest.Attempts,
		"error_kind", latest.ErrorKind,
		"duration_ms", latest.DurationMS,
	)
}

func (runtime *backgroundRuntime) resetConnectionGapsForRoom(roomID string) {
	runtime.mu.Lock()
	if runtime.connectionGapRoomID != "" && runtime.connectionGapRoomID != roomID {
		runtime.status.ConnectionGaps = nil
	}
	runtime.mu.Unlock()
}

func (runtime *backgroundRuntime) setConnectionGapRoom(roomID string) {
	runtime.mu.Lock()
	runtime.connectionGapRoomID = roomID
	runtime.mu.Unlock()
}

func (runtime *backgroundRuntime) connectionGapRoom() string {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.connectionGapRoomID
}

func (runtime *backgroundRuntime) setStatus(state, roomID string, err error) {
	runtime.mu.Lock()
	previous := runtime.status
	nextLastError := ""
	runtime.status.State = state
	if runtime.status.RoomID != roomID {
		runtime.status.LastFrameAt = 0
	}
	runtime.status.RoomID = roomID
	if err == nil {
		runtime.status.LastError = ""
	} else {
		nextLastError = err.Error()
		runtime.status.LastError = nextLastError
	}
	runtime.mu.Unlock()
	if previous.State != state || previous.RoomID != roomID || previous.LastError != nextLastError {
		if err != nil {
			runtime.diagnostics.Error("connection_state", "state", state, "room_id", roomID, "reason", connectionFailureKind(err), "error", err)
		} else {
			runtime.diagnostics.Info("connection_state", "state", state, "room_id", roomID)
		}
	}

	if previous.State != "connected" && state == "connected" {
		runtime.notifications.Publish(notificationRoomConnected, roomID)
	}
	if previous.State == "connected" && state != "connected" {
		disconnectedRoomID := previous.RoomID
		if disconnectedRoomID == "" {
			disconnectedRoomID = roomID
		}
		runtime.notifications.Publish(notificationRoomDisconnected, disconnectedRoomID)
	}
}

func (runtime *backgroundRuntime) wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-runtime.reload:
		return true
	case <-timer.C:
		return true
	}
}

func applyGiftEvent(state *appState, gift giftEvent) {
	applyGiftEventWithFreeze(state, gift, nil)
}

func applyGiftEventWithFreeze(state *appState, gift giftEvent, freezes attributeFreezeChecker) {
	normalizeAppState(state)
	gift = enrichBlindBoxGiftFromCatalog(*state, gift)
	upsertRecentGiftState(state, gift)
	stats := state.todayStats()
	stats.GiftTotals[giftKey(gift.GiftID)] += maxInt(1, gift.Num)
	state.Stats[stats.Date] = stats
	snapshot := gameplaySnapshotForGift(*state, gift, freezes)
	transition, err := (gameplay.Engine{}).ApplyGiftWithRandom(snapshot, gameplayGift(gift), time.Now(), formulaRandomIntn)
	repetitions := maxInt(1, gift.Num)
	changes := []logEntry{}
	appliedRuleTriggers := 0
	if err == nil {
		applyGameplayTransition(state, transition)
		appliedRuleTriggers = gameplayAppliedRuleDelta(snapshot.RuleLimits, transition.Next.RuleLimits, state.Rules)
		for _, effect := range transition.Effects {
			if effect.AttributeName == "" {
				continue
			}
			changes = append(changes, logEntry{
				Time:          gift.Timestamp,
				GiftID:        gift.GiftID,
				GiftName:      gift.GiftName,
				Num:           repetitions,
				Uname:         gift.Uname,
				Avatar:        gift.Avatar,
				SenderUID:     gift.UID,
				AttributeName: effect.AttributeName,
				Delta:         effect.Delta,
				ValueAfter:    effect.ValueAfter,
				RuleID:        effect.RuleID,
				Source:        "gift",
				TriggerName:   effect.TriggerName,
				EventID:       fmt.Sprintf("%s:%s", gift.Rnd, effect.AttributeName),
			})
		}
	}
	if len(changes) > 0 {
		state.Log = append(changes, state.Log...)
		if len(state.Log) > maxLogEntries {
			state.Log = state.Log[:maxLogEntries]
		}
	}
	appendGiftReceipt(state, gift, changes)
	recordGiftContribution(state, gift, giftContributionOutcome{
		RuleTriggers: appliedRuleTriggers,
		Changes:      changes,
	})
}

var errNoTimerChanges = errors.New("no timer changes")

func applyTimerRules(state *appState, dueRuleIDs []string, now time.Time) int {
	return applyTimerRulesWithFreeze(state, dueRuleIDs, now, nil)
}

func applyTimerRulesWithFreeze(state *appState, dueRuleIDs []string, now time.Time, freezes attributeFreezeChecker) int {
	normalizeAppState(state)
	stats := state.todayStats()
	state.Stats[stats.Date] = stats
	snapshot := gameplaySnapshotForTimers(*state, dueRuleIDs, freezes)
	activityDate := now.In(time.Local).Format("2006-01-02")
	snapshot.RuleLimits.LocalDate = activityDate
	transition, err := (gameplay.Engine{}).ApplyTimersWithRandom(snapshot, dueRuleIDs, now, formulaRandomIntn)
	if err != nil {
		return 0
	}
	applied := gameplayAppliedTimerRuleDelta(snapshot.RuleLimits, transition.Next.RuleLimits, state.TimerRules)
	transition.Next.RuleLimits.LocalDate = stats.Date
	applyGameplayTransition(state, transition)
	for _, effect := range transition.Effects {
		if effect.AttributeName == "" {
			continue
		}
		state.Log = append([]logEntry{{
			Time: now.Unix(), AttributeName: effect.AttributeName, Delta: effect.Delta,
			ValueAfter: effect.ValueAfter, RuleID: effect.RuleID, Source: "timer", TriggerName: effect.TriggerName,
		}}, state.Log...)
		if len(state.Log) > maxLogEntries {
			state.Log = state.Log[:maxLogEntries]
		}
	}
	return applied
}

func gameplayAppliedRuleDelta(before, after gameplay.RuleLimitState, rules []giftRule) int {
	ruleIDs := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		ruleIDs[rule.ID] = struct{}{}
	}
	return gameplayAppliedCountDelta(before, after, ruleIDs)
}

func gameplayAppliedTimerRuleDelta(before, after gameplay.RuleLimitState, rules []timerRule) int {
	ruleIDs := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		ruleIDs[rule.ID] = struct{}{}
	}
	return gameplayAppliedCountDelta(before, after, ruleIDs)
}

func gameplayAppliedCountDelta(before, after gameplay.RuleLimitState, ruleIDs map[string]struct{}) int {
	count := 0
	for ruleID := range ruleIDs {
		baseline := 0
		if before.LocalDate == after.LocalDate {
			baseline = before.AppliedCounts[ruleID]
		}
		if delta := after.AppliedCounts[ruleID] - baseline; delta > 0 {
			count += delta
		}
	}
	return count
}

func upsertRecentGiftState(state *appState, gift giftEvent) {
	for index := range state.RecentGifts {
		if state.RecentGifts[index].ID == gift.GiftID {
			state.RecentGifts[index].Count += maxInt(1, gift.Num)
			state.RecentGifts[index].LastReceived = gift.Timestamp
			state.RecentGifts[index].Name = gift.GiftName
			state.RecentGifts[index].Price = gift.Price
			state.RecentGifts[index].CoinType = gift.CoinType
			if gift.ImgBasic != "" {
				state.RecentGifts[index].ImgBasic = gift.ImgBasic
			}
			return
		}
	}
	state.RecentGifts = append([]recentGift{{
		giftInfo:     giftInfo{ID: gift.GiftID, Name: gift.GiftName, Price: gift.Price, CoinType: gift.CoinType, ImgBasic: gift.ImgBasic},
		LastReceived: gift.Timestamp,
		Count:        maxInt(1, gift.Num),
	}}, state.RecentGifts...)
	if len(state.RecentGifts) > 100 {
		state.RecentGifts = state.RecentGifts[:100]
	}
}

func sameGiftIdentity(configured giftInfo, gift giftEvent) bool {
	if strings.TrimSpace(configured.Name) != strings.TrimSpace(gift.GiftName) {
		return false
	}
	return configured.Price == gift.Price && configured.CoinType == gift.CoinType
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func formulaPreview(state appState, formula, attributeName string, attributeValue float64) (float64, error) {
	return formulaPreviewWithPrice(state, formula, attributeName, attributeValue, 1000)
}

func formulaPreviewWithPrice(state appState, formula, attributeName string, attributeValue, giftPrice float64) (float64, error) {
	environment := buildGiftFormulaEnvironment(state, attributeName, attributeValue, giftPrice, "")
	result, err := evaluateFormula(formula, environment)
	if err != nil {
		return 0, err
	}
	if math.IsInf(result, 0) || math.IsNaN(result) {
		return 0, fmt.Errorf("规则结果不是有效数字")
	}
	return result, nil
}

func validateGiftFormula(state appState, formula, attributeName string, attributeValue, giftPrice float64) error {
	environment := buildGiftFormulaEnvironment(state, attributeName, attributeValue, giftPrice, "")
	return validateFormula(formula, environment)
}

func validateTimerFormula(state appState, formula, attributeName string, attributeValue float64) error {
	environment := map[string]float64{}
	for _, attribute := range state.Attributes {
		environment[attribute.Name] = attribute.Value
	}
	environment[attributeName] = attributeValue
	return validateFormula(formula, environment)
}

type giftRulePreviewResult struct {
	Triggered bool    `json:"triggered"`
	Result    float64 `json:"result"`
}

func previewGiftRule(state appState, condition, formula, attributeName string, attributeValue, giftPrice float64, identityLevel int) (giftRulePreviewResult, error) {
	environment := buildGiftFormulaEnvironmentWithIdentity(state, attributeName, attributeValue, giftPrice, float64(identityLevel))
	if strings.TrimSpace(condition) != "" {
		conditionResult, err := evaluateFormula(condition, environment)
		if err != nil {
			return giftRulePreviewResult{}, err
		}
		if math.IsInf(conditionResult, 0) || math.IsNaN(conditionResult) {
			return giftRulePreviewResult{}, fmt.Errorf("规则结果不是有效数字")
		}
		if conditionResult == 0 {
			return giftRulePreviewResult{Triggered: false, Result: attributeValue}, nil
		}
	}
	result, err := evaluateFormula(formula, environment)
	if err != nil {
		return giftRulePreviewResult{}, err
	}
	if math.IsInf(result, 0) || math.IsNaN(result) {
		return giftRulePreviewResult{}, fmt.Errorf("规则结果不是有效数字")
	}
	return giftRulePreviewResult{Triggered: true, Result: result}, nil
}

func timerFormulaPreview(state appState, formula, attributeName string, attributeValue float64) (float64, error) {
	environment := map[string]float64{}
	for _, attribute := range state.Attributes {
		environment[attribute.Name] = attribute.Value
	}
	environment[attributeName] = attributeValue
	result, err := evaluateFormula(formula, environment)
	if err != nil {
		return 0, err
	}
	if math.IsInf(result, 0) || math.IsNaN(result) {
		return 0, fmt.Errorf("规则结果不是有效数字")
	}
	return result, nil
}
