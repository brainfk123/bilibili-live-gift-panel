package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"bilibili-live-gift-panel/internal/hosted/configuration"
)

var (
	ErrInvalidInput      = errors.New("migration: invalid input")
	ErrUnavailable       = errors.New("migration: unavailable")
	ErrPreviewLimit      = errors.New("migration: preview limit reached")
	ErrNotFound          = errors.New("migration: not found")
	ErrConflict          = errors.New("migration: conflict")
	ErrOwnershipConflict = errors.New("migration: ownership conflict")
	ErrExpired           = errors.New("migration: expired")
)

const (
	jobPreviewed  = "previewed"
	jobPending    = "pending"
	jobApplied    = "applied"
	jobCancelled  = "cancelled"
	jobRolledBack = "rolled_back"
	jobExpired    = "expired"
)

// Job is the privacy-safe lifecycle projection returned to migration callers.
// It contains neither raw uploads nor normalized configuration/runtime data.
type Job struct {
	ID                 int64     `json:"id"`
	Status             string    `json:"status"`
	ExpiresAt          time.Time `json:"expiresAt,omitempty"`
	RollbackExpiresAt  time.Time `json:"rollbackExpiresAt,omitempty"`
	OBSLinks           []OBSLink `json:"obsLinks,omitempty"`
	OBSReissueRequired bool      `json:"obsReissueRequired,omitempty"`
}

type OBSLink struct {
	OutputID string `json:"outputId"`
	Name     string `json:"name"`
	URL      string `json:"url"`
}
type OBSOutput struct {
	Selector string
	Name     string
}

// HistoryJob is the bounded, read-only lifecycle projection. Optional times
// are pointers so absent database values never serialize as zero instants.
type HistoryJob struct {
	ID                int64      `json:"id"`
	Status            string     `json:"status"`
	CreatedAt         time.Time  `json:"createdAt"`
	AppliedAt         *time.Time `json:"appliedAt,omitempty"`
	ExpiresAt         *time.Time `json:"expiresAt,omitempty"`
	RollbackExpiresAt *time.Time `json:"rollbackExpiresAt,omitempty"`
}

// OwnerFence is the exact cross-process runtime ownership claim authorized to
// finish a pending migration after its live session has ended.
type OwnerFence struct {
	AccountID int64
	Token     [32]byte
	Epoch     uint64
}

type applyCommand struct {
	AccountID          int64
	JobID              int64
	KeepRoomSuggestion bool
	Candidate          ComposeCandidate
}

// BarrierCandidate is the frozen, complete candidate handed to the live
// runtime after the migration repository has sealed it. Definition is the
// activation form and therefore never carries MigrationHash itself.
type BarrierCandidate struct {
	JobID              int64
	Definition         configuration.Definition
	Runtime            configuration.RuntimeState
	Operation          configuration.BarrierOperation
	IntegritySeal      [sha256.Size]byte
	KeepRoomSuggestion bool
	RoomSuggestion     string
}

type storedJob struct {
	ID, AccountID     int64
	Status            string
	ExpiresAt         time.Time
	RollbackExpiresAt time.Time
}

type storedHistoryJob struct {
	ID, AccountID     int64
	Status            string
	CreatedAt         time.Time
	AppliedAt         sql.NullTime
	ExpiresAt         sql.NullTime
	RollbackExpiresAt sql.NullTime
}

// Preview deliberately contains no account ID, normalized configuration,
// runtime, raw upload, or canonical JSON.
type Preview struct {
	ID              int64               `json:"id"`
	ExpiresAt       time.Time           `json:"expiresAt"`
	Reused          bool                `json:"reused"`
	Counts          Counts              `json:"counts"`
	Warnings        []string            `json:"warnings,omitempty"`
	Ignored         []string            `json:"ignored,omitempty"`
	RoomSuggestion  string              `json:"roomSuggestion,omitempty"`
	Source          Source              `json:"source"`
	Units           []SelectionUnit     `json:"units,omitempty"`
	Groups          []GameplayGroup     `json:"groups,omitempty"`
	Conflicts       []SelectionConflict `json:"conflicts,omitempty"`
	Selection       SelectionCommand    `json:"selection"`
	GeneralSettings GeneralSettings     `json:"generalSettings"`
	CanConfirm      bool                `json:"canConfirm"`
	Hash            [sha256.Size]byte   `json:"-"`
}

type previewCommand struct {
	AccountID            int64
	Definition           configuration.Definition
	Runtime              configuration.RuntimeState
	RoomSuggestion       string
	Source               Source
	Counts               Counts
	Report               Report
	GeneralSettings      GeneralSettings
	CropPresets          []CropPreset
	Units                []GameplayUnit
	Groups               []GameplayGroup
	Hash                 [sha256.Size]byte
	CreatedAt, ExpiresAt time.Time
}
type storedPreview struct {
	ID, AccountID  int64
	Status         string
	ExpiresAt      time.Time
	Reused         bool
	RoomSuggestion string
	Source         Source
	Hash           [sha256.Size]byte
	Report         Report
}

type previewMetadata struct {
	Report
	GeneralSettings GeneralSettings `json:"generalSettings"`
	CropPresets     []CropPreset    `json:"cropPresets,omitempty"`
	Units           []GameplayUnit  `json:"units,omitempty"`
	Groups          []GameplayGroup `json:"groups,omitempty"`
}

type storedComposition struct {
	ID, AccountID      int64
	Status             string
	ExpiresAt          time.Time
	Imported           Envelope
	HostedDefinition   configuration.Definition
	HostedRuntime      configuration.RuntimeState
	KeepRoomSuggestion bool
}

type storedAppliedDefinition struct {
	ID, AccountID int64
	Status        string
	Definition    configuration.Definition
}

// Repository is intentionally narrow: the migration package owns preview
// transaction and quota semantics without exposing a raw-upload repository.
type Repository interface {
	Preview(context.Context, previewCommand) (storedPreview, error)
}

// lifecycleRepository remains deliberately internal so the preview seam stays
// narrow for callers that never need to mutate a migration job.
type lifecycleRepository interface {
	Apply(context.Context, applyCommand) (storedJob, error)
	ApplyPendingAfterSession(context.Context, OwnerFence, int64) (storedJob, error)
	PreauthorizeApply(context.Context, int64, int64) (storedJob, error)
	PreauthorizeRollback(context.Context, int64, int64) (storedJob, error)
	Cancel(context.Context, int64, int64) (storedJob, error)
	Rollback(context.Context, int64, int64) (storedJob, error)
	Get(context.Context, int64, int64) (storedJob, error)
}

type historyRepository interface {
	History(context.Context, int64, time.Time, int) ([]storedHistoryJob, error)
}

type compositionRepository interface {
	LoadComposition(context.Context, int64, int64) (storedComposition, error)
}

type obsOutputRepository interface {
	LoadAppliedDefinition(context.Context, int64, int64) (storedAppliedDefinition, error)
}

type stagedApplyRepository interface {
	StageApply(context.Context, applyCommand) (storedJob, error)
}

type rollbackCandidateRepository interface {
	LoadRollbackCandidate(context.Context, int64, int64) (configuration.Definition, configuration.RuntimeState, error)
}

// ConfigurationBarrier is implemented by the Hosted runtime manager and is
// injected only after both sides of production composition exist.
type ConfigurationBarrier interface {
	ApplyConfigurationBarrier(context.Context, int64, BarrierCandidate) (configuration.Boundary, error)
}

// pendingConfigurationBarrier is the owner-fenced no-session path used while
// runtime already holds the account operation gate during a room transition.
type pendingConfigurationBarrier interface {
	ApplyPendingConfigurationBarrier(context.Context, OwnerFence, BarrierCandidate) (configuration.Boundary, error)
}

type Service struct {
	repository   Repository
	now          func() time.Time
	capabilities CapabilitySet
	barrierMu    sync.RWMutex
	barrier      ConfigurationBarrier
}

func NewService(repository Repository, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, now: now, capabilities: hostedMigrationCapabilities()}
}

func (service *Service) SetConfigurationBarrier(barrier ConfigurationBarrier) error {
	if service == nil || barrier == nil {
		return ErrInvalidInput
	}
	service.barrierMu.Lock()
	service.barrier = barrier
	service.barrierMu.Unlock()
	return nil
}

func (service *Service) configurationBarrier() ConfigurationBarrier {
	if service == nil {
		return nil
	}
	service.barrierMu.RLock()
	defer service.barrierMu.RUnlock()
	return service.barrier
}

func (service *Service) Preview(ctx context.Context, accountID int64, envelope Envelope) (Preview, error) {
	if service == nil || service.repository == nil || service.now == nil || accountID <= 0 {
		return Preview{}, ErrInvalidInput
	}
	inputDefinition := envelope.Definition
	if inputDefinition.GeneralSettings == nil && envelope.GeneralSettings.ConfigurationMode != "" {
		settings := envelope.GeneralSettings
		inputDefinition.GeneralSettings = &settings
	}
	if len(inputDefinition.CropPresets) == 0 && len(envelope.CropPresets) != 0 {
		inputDefinition.CropPresets = append([]CropPreset(nil), envelope.CropPresets...)
	}
	definition, runtime, _, hash, err := freshCanonical(inputDefinition, envelope.Runtime)
	if err != nil {
		return Preview{}, ErrInvalidInput
	}
	command := previewCommand{AccountID: accountID, Definition: definition, Runtime: runtime, RoomSuggestion: envelope.RoomSuggestion, Source: envelope.Source, Counts: countDefinition(definition), Report: envelope.Report, GeneralSettings: envelope.GeneralSettings, CropPresets: envelope.CropPresets, Units: envelope.Units, Groups: envelope.Groups, Hash: hash}
	if command.Source.AppVersion == "" || command.Source.ConfigurationSchemaVersion < 1 || command.Source.ConfigurationSchemaVersion > configurationSchemaVersion {
		return Preview{}, ErrInvalidInput
	}
	stored, err := service.repository.Preview(ctx, command)
	if err != nil {
		return Preview{}, err
	}
	if stored.ID <= 0 || stored.AccountID != accountID || stored.ExpiresAt.IsZero() {
		return Preview{}, ErrUnavailable
	}
	preview := Preview{ID: stored.ID, ExpiresAt: stored.ExpiresAt, Reused: stored.Reused, Counts: stored.Report.Counts, Warnings: append([]string(nil), stored.Report.Warnings...), Ignored: append([]string(nil), stored.Report.Ignored...), RoomSuggestion: stored.RoomSuggestion, Source: stored.Source, Hash: stored.Hash}
	if repository, ok := service.repository.(compositionRepository); ok && stored.Status == jobPending {
		composition, loadErr := repository.LoadComposition(ctx, accountID, stored.ID)
		if loadErr != nil {
			return Preview{}, loadErr
		}
		selected, projectionErr := service.pendingProjection(accountID, composition)
		if projectionErr != nil {
			return Preview{}, projectionErr
		}
		selected.Reused, selected.Source, selected.Warnings, selected.Ignored = stored.Reused, stored.Source, append([]string(nil), stored.Report.Warnings...), append([]string(nil), stored.Report.Ignored...)
		return selected, nil
	}
	if _, ok := service.repository.(compositionRepository); ok {
		selected, selectErr := service.Select(ctx, accountID, stored.ID, SelectionCommand{})
		if selectErr != nil {
			return Preview{}, selectErr
		}
		selected.Reused, selected.Source, selected.Counts, selected.Warnings, selected.Ignored, selected.Hash = stored.Reused, stored.Source, stored.Report.Counts, append([]string(nil), stored.Report.Warnings...), append([]string(nil), stored.Report.Ignored...), stored.Hash
		return selected, nil
	}
	return preview, nil
}

func (service *Service) pendingProjection(accountID int64, composition storedComposition) (Preview, error) {
	if composition.AccountID != accountID || composition.ID <= 0 || composition.Status != jobPending || composition.ExpiresAt.IsZero() {
		return Preview{}, ErrUnavailable
	}
	if !composition.ExpiresAt.After(service.now().UTC()) {
		return Preview{}, ErrExpired
	}
	units := DeriveUnits(composition.Imported.Definition, composition.Imported.Runtime)
	selectedUnits := make([]SelectionUnit, len(units))
	selection := SelectionCommand{UnitIDs: make([]string, len(units)), IncludeGeneralSettings: composition.Imported.Definition.GeneralSettings != nil, IncludeRoomSuggestion: composition.KeepRoomSuggestion}
	for index, unit := range units {
		selection.UnitIDs[index] = unit.ID
		selectedUnits[index] = SelectionUnit{GameplayUnit: unit, Compatibility: AssessCompatibility(unit, service.capabilities), Selected: true}
	}
	settings := GeneralSettings{}
	if composition.Imported.Definition.GeneralSettings != nil {
		settings = *composition.Imported.Definition.GeneralSettings
	}
	preview := Preview{
		ID: composition.ID, ExpiresAt: composition.ExpiresAt, Counts: countDefinition(composition.Imported.Definition), RoomSuggestion: composition.Imported.RoomSuggestion,
		Source: composition.Imported.Source, Units: selectedUnits, Groups: ConnectedGroups(units), Selection: selection, GeneralSettings: settings, CanConfirm: true,
	}
	if rawHash, err := hex.DecodeString(composition.Imported.Definition.MigrationHash); err == nil && len(rawHash) == sha256.Size {
		copy(preview.Hash[:], rawHash)
	}
	return preview, nil
}

func (service *Service) Select(ctx context.Context, accountID, jobID int64, selection SelectionCommand) (Preview, error) {
	composition, candidate, err := service.loadCandidate(ctx, accountID, jobID, selection)
	if err != nil {
		return Preview{}, err
	}
	units := make([]SelectionUnit, len(composition.Imported.Units))
	selected := make(map[string]struct{}, len(selection.UnitIDs))
	for _, id := range selection.UnitIDs {
		selected[id] = struct{}{}
	}
	for index, unit := range composition.Imported.Units {
		_, chosen := selected[unit.ID]
		units[index] = SelectionUnit{GameplayUnit: unit, Compatibility: AssessCompatibility(unit, service.capabilities), Selected: chosen}
	}
	return Preview{
		ID: composition.ID, ExpiresAt: composition.ExpiresAt, RoomSuggestion: composition.Imported.RoomSuggestion, Source: composition.Imported.Source,
		Counts: composition.Imported.Report.Counts, Warnings: append([]string(nil), composition.Imported.Report.Warnings...), Ignored: append([]string(nil), composition.Imported.Report.Ignored...),
		Units: units, Groups: append([]GameplayGroup(nil), composition.Imported.Groups...), Conflicts: candidate.Conflicts,
		Selection: cloneSelection(selection), GeneralSettings: composition.Imported.GeneralSettings, CanConfirm: candidate.Ready,
	}, nil
}

func (service *Service) Apply(ctx context.Context, accountID, jobID int64, selection SelectionCommand) (Job, error) {
	if service == nil || accountID <= 0 || jobID <= 0 {
		return Job{}, ErrInvalidInput
	}
	repository, ok := service.repository.(lifecycleRepository)
	if !ok {
		return Job{}, ErrUnavailable
	}
	barrier := service.configurationBarrier()
	if barrier != nil {
		preauthorized, err := repository.PreauthorizeApply(ctx, accountID, jobID)
		if err != nil {
			return Job{}, err
		}
		if preauthorized.ID != jobID || preauthorized.AccountID != accountID {
			return Job{}, ErrUnavailable
		}
		switch preauthorized.Status {
		case jobApplied:
			return service.applyPersistedBarrier(ctx, accountID, jobID, barrier)
		case jobPending:
			return service.applyPersistedBarrier(ctx, accountID, jobID, barrier)
		case jobPreviewed:
		default:
			return Job{}, ErrConflict
		}
	}
	_, candidate, err := service.loadCandidate(ctx, accountID, jobID, selection)
	if err != nil {
		return Job{}, err
	}
	if !candidate.Ready {
		return Job{}, ErrConflict
	}
	if barrier != nil {
		stager, ok := service.repository.(stagedApplyRepository)
		if !ok {
			return Job{}, ErrUnavailable
		}
		staged, stageErr := stager.StageApply(ctx, applyCommand{AccountID: accountID, JobID: jobID, KeepRoomSuggestion: selection.IncludeRoomSuggestion, Candidate: candidate})
		if stageErr != nil {
			return Job{}, stageErr
		}
		if staged.ID != jobID || staged.AccountID != accountID {
			return Job{}, ErrUnavailable
		}
		if staged.Status == jobApplied {
			return service.applyPersistedBarrier(ctx, accountID, jobID, barrier)
		}
		if staged.Status != jobPending {
			return Job{}, ErrUnavailable
		}
		return service.applyPersistedBarrier(ctx, accountID, jobID, barrier)
	}
	stored, err := repository.Apply(ctx, applyCommand{AccountID: accountID, JobID: jobID, KeepRoomSuggestion: selection.IncludeRoomSuggestion, Candidate: candidate})
	return publicJob(stored, accountID, err)
}

func (service *Service) loadCandidate(ctx context.Context, accountID, jobID int64, selection SelectionCommand) (storedComposition, ComposeCandidate, error) {
	if service == nil || service.repository == nil || service.now == nil || accountID <= 0 || jobID <= 0 {
		return storedComposition{}, ComposeCandidate{}, ErrInvalidInput
	}
	repository, ok := service.repository.(compositionRepository)
	if !ok {
		return storedComposition{}, ComposeCandidate{}, ErrUnavailable
	}
	stored, err := repository.LoadComposition(ctx, accountID, jobID)
	if err != nil {
		return storedComposition{}, ComposeCandidate{}, err
	}
	if stored.ID != jobID || stored.AccountID != accountID || stored.Status != jobPreviewed || stored.ExpiresAt.IsZero() {
		return storedComposition{}, ComposeCandidate{}, ErrUnavailable
	}
	if !stored.ExpiresAt.After(service.now().UTC()) {
		return storedComposition{}, ComposeCandidate{}, ErrExpired
	}
	candidate, err := composeCandidate(stored.Imported, stored.HostedDefinition, stored.HostedRuntime, service.capabilities, selection)
	return stored, candidate, err
}

func cloneSelection(value SelectionCommand) SelectionCommand {
	result := value
	result.UnitIDs = append([]string(nil), value.UnitIDs...)
	if value.ConflictChoices != nil {
		result.ConflictChoices = make(map[string]ConflictChoice, len(value.ConflictChoices))
		for key, choice := range value.ConflictChoices {
			result.ConflictChoices[key] = choice
		}
	}
	return result
}

func hostedMigrationCapabilities() CapabilitySet {
	return CapabilitySet{
		UnitKinds: map[string]bool{"attribute": true, "activity": true, "gift-target": true, "simple-play": true},
		SimplePlayTemplates: map[string]int{
			"overtime": 2, "countdown": 1, "counter": 1, "goal": 1, "boss": 1, "resource": 1, "tug": 1,
			"team-duel": 1, "gift-vote": 1, "combo": 1, "milestone": 1, "random-event": 1,
		}, RulesSupported: true, TimerRulesSupported: false,
		FormulaPresetsSupported: true, DisplayScenesSupported: true, CropPresetsSupported: true,
	}
}

func (service *Service) PreauthorizeApply(ctx context.Context, accountID, jobID int64) (Job, error) {
	if service == nil || accountID <= 0 || jobID <= 0 {
		return Job{}, ErrInvalidInput
	}
	repository, ok := service.repository.(lifecycleRepository)
	if !ok {
		return Job{}, ErrUnavailable
	}
	stored, err := repository.PreauthorizeApply(ctx, accountID, jobID)
	if err != nil {
		return Job{}, err
	}
	if stored.ID != jobID || stored.AccountID != accountID {
		return Job{}, ErrUnavailable
	}
	barrier := service.configurationBarrier()
	if barrier != nil && (stored.Status == jobPending || stored.Status == jobApplied) {
		return service.applyPersistedBarrier(ctx, accountID, jobID, barrier)
	}
	return publicJob(stored, accountID, nil)
}

func (service *Service) applyPersistedBarrier(ctx context.Context, accountID, jobID int64, barrier ConfigurationBarrier) (Job, error) {
	candidate, err := service.persistedBarrierCandidate(ctx, accountID, jobID)
	if err != nil {
		return Job{}, err
	}
	boundary, err := barrier.ApplyConfigurationBarrier(ctx, accountID, candidate)
	if err != nil {
		return Job{}, err
	}
	if boundary.AccountID != accountID || boundary.MigrationJobID != jobID || boundary.Operation != configuration.BarrierMigrationApply || boundary.NewConfigVersionID <= 0 || boundary.FirstNewRevision == 0 || boundary.FirstNewRevision != boundary.LastOldRevision+1 {
		return Job{}, ErrUnavailable
	}
	repository, ok := service.repository.(lifecycleRepository)
	if !ok {
		return Job{}, ErrUnavailable
	}
	stored, err := repository.Get(ctx, accountID, jobID)
	return publicJob(stored, accountID, err)
}

func (service *Service) persistedBarrierCandidate(ctx context.Context, accountID, jobID int64) (BarrierCandidate, error) {
	repository, ok := service.repository.(compositionRepository)
	if !ok {
		return BarrierCandidate{}, ErrUnavailable
	}
	composition, err := repository.LoadComposition(ctx, accountID, jobID)
	if err != nil {
		return BarrierCandidate{}, err
	}
	if composition.ID != jobID || composition.AccountID != accountID || composition.Status != jobPending && composition.Status != jobApplied || composition.ExpiresAt.IsZero() {
		return BarrierCandidate{}, ErrUnavailable
	}
	definition, runtime, _, hash, err := freshCanonical(composition.Imported.Definition, composition.Imported.Runtime)
	if err != nil {
		return BarrierCandidate{}, ErrConflict
	}
	frozen, err := freezeCandidate(ComposeCandidate{Definition: definition, Runtime: runtime, Ready: true, Hash: hash})
	if err != nil {
		return BarrierCandidate{}, err
	}
	activeDefinition := frozen.Definition
	activeDefinition.MigrationHash = ""
	candidate := BarrierCandidate{JobID: jobID, Definition: activeDefinition, Runtime: frozen.Runtime, Operation: configuration.BarrierMigrationApply, IntegritySeal: frozen.Hash, KeepRoomSuggestion: composition.KeepRoomSuggestion}
	if composition.KeepRoomSuggestion {
		candidate.RoomSuggestion = composition.Imported.RoomSuggestion
	}
	return candidate, nil
}

func (service *Service) PreauthorizeRollback(ctx context.Context, accountID, jobID int64) (Job, error) {
	if service == nil || accountID <= 0 || jobID <= 0 {
		return Job{}, ErrInvalidInput
	}
	repository, ok := service.repository.(lifecycleRepository)
	if !ok {
		return Job{}, ErrUnavailable
	}
	stored, err := repository.PreauthorizeRollback(ctx, accountID, jobID)
	if err != nil {
		return Job{}, err
	}
	if stored.ID != jobID || stored.AccountID != accountID {
		return Job{}, ErrUnavailable
	}
	if barrier := service.configurationBarrier(); barrier != nil && stored.Status == jobRolledBack {
		return service.applyPersistedRollbackBarrier(ctx, accountID, jobID, barrier)
	}
	return publicJob(stored, accountID, err)
}

func (service *Service) ApplyPendingAfterSession(ctx context.Context, owner OwnerFence, jobID int64) (Job, error) {
	if service == nil || !validOwnerFence(owner) || jobID <= 0 {
		return Job{}, ErrInvalidInput
	}
	repository, ok := service.repository.(lifecycleRepository)
	if !ok {
		return Job{}, ErrUnavailable
	}
	if barrier := service.configurationBarrier(); barrier != nil {
		pendingBarrier, ok := barrier.(pendingConfigurationBarrier)
		if !ok {
			return Job{}, ErrUnavailable
		}
		preauthorized, err := repository.PreauthorizeApply(ctx, owner.AccountID, jobID)
		if err != nil {
			return Job{}, err
		}
		if preauthorized.ID != jobID || preauthorized.AccountID != owner.AccountID || preauthorized.Status != jobPending && preauthorized.Status != jobApplied {
			return Job{}, ErrUnavailable
		}
		candidate, err := service.persistedBarrierCandidate(ctx, owner.AccountID, jobID)
		if err != nil {
			return Job{}, err
		}
		boundary, err := pendingBarrier.ApplyPendingConfigurationBarrier(ctx, owner, candidate)
		if err != nil {
			return Job{}, err
		}
		if boundary.AccountID != owner.AccountID || boundary.MigrationJobID != jobID || boundary.Operation != configuration.BarrierMigrationApply || boundary.NewConfigVersionID <= 0 || boundary.FirstNewRevision == 0 || boundary.FirstNewRevision != boundary.LastOldRevision+1 {
			return Job{}, ErrUnavailable
		}
		stored, err := repository.Get(ctx, owner.AccountID, jobID)
		return publicJob(stored, owner.AccountID, err)
	}
	stored, err := repository.ApplyPendingAfterSession(ctx, owner, jobID)
	return publicJob(stored, owner.AccountID, err)
}

func (service *Service) Cancel(ctx context.Context, accountID, jobID int64) (Job, error) {
	if service == nil || accountID <= 0 || jobID <= 0 {
		return Job{}, ErrInvalidInput
	}
	repository, ok := service.repository.(lifecycleRepository)
	if !ok {
		return Job{}, ErrUnavailable
	}
	stored, err := repository.Cancel(ctx, accountID, jobID)
	return publicJob(stored, accountID, err)
}

func (service *Service) Rollback(ctx context.Context, accountID, jobID int64) (Job, error) {
	if service == nil || accountID <= 0 || jobID <= 0 {
		return Job{}, ErrInvalidInput
	}
	repository, ok := service.repository.(lifecycleRepository)
	if !ok {
		return Job{}, ErrUnavailable
	}
	barrier := service.configurationBarrier()
	if barrier != nil {
		preauthorized, err := repository.PreauthorizeRollback(ctx, accountID, jobID)
		if err != nil {
			return Job{}, err
		}
		if preauthorized.ID != jobID || preauthorized.AccountID != accountID {
			return Job{}, ErrUnavailable
		}
		if preauthorized.Status != jobApplied && preauthorized.Status != jobRolledBack {
			return Job{}, ErrConflict
		}
		return service.applyPersistedRollbackBarrier(ctx, accountID, jobID, barrier)
	}
	stored, err := repository.Rollback(ctx, accountID, jobID)
	return publicJob(stored, accountID, err)
}

func (service *Service) applyPersistedRollbackBarrier(ctx context.Context, accountID, jobID int64, barrier ConfigurationBarrier) (Job, error) {
	loader, ok := service.repository.(rollbackCandidateRepository)
	if !ok {
		return Job{}, ErrUnavailable
	}
	definition, runtime, err := loader.LoadRollbackCandidate(ctx, accountID, jobID)
	if err != nil {
		return Job{}, err
	}
	definition.MigrationHash = ""
	definition, runtime, err = configuration.Normalize(definition, runtime)
	if err != nil {
		return Job{}, ErrConflict
	}
	boundary, err := barrier.ApplyConfigurationBarrier(ctx, accountID, BarrierCandidate{JobID: jobID, Definition: definition, Runtime: runtime, Operation: configuration.BarrierMigrationRollback})
	if err != nil {
		return Job{}, err
	}
	if boundary.AccountID != accountID || boundary.MigrationJobID != jobID || boundary.Operation != configuration.BarrierMigrationRollback || boundary.NewConfigVersionID <= 0 || boundary.FirstNewRevision == 0 || boundary.FirstNewRevision != boundary.LastOldRevision+1 {
		return Job{}, ErrUnavailable
	}
	repository, ok := service.repository.(lifecycleRepository)
	if !ok {
		return Job{}, ErrUnavailable
	}
	stored, err := repository.Get(ctx, accountID, jobID)
	return publicJob(stored, accountID, err)
}

func (service *Service) Get(ctx context.Context, accountID, jobID int64) (Job, error) {
	if service == nil || accountID <= 0 || jobID <= 0 {
		return Job{}, ErrInvalidInput
	}
	repository, ok := service.repository.(lifecycleRepository)
	if !ok {
		return Job{}, ErrUnavailable
	}
	stored, err := repository.Get(ctx, accountID, jobID)
	return publicJob(stored, accountID, err)
}

const historyLimit = 20

func (service *Service) History(ctx context.Context, accountID int64) ([]HistoryJob, error) {
	if service == nil || accountID <= 0 {
		return nil, ErrInvalidInput
	}
	repository, ok := service.repository.(historyRepository)
	if !ok {
		return nil, ErrUnavailable
	}
	now := service.now().UTC()
	stored, err := repository.History(ctx, accountID, now, historyLimit)
	if err != nil {
		return nil, err
	}
	if len(stored) > historyLimit {
		return nil, ErrUnavailable
	}
	result := make([]HistoryJob, len(stored))
	for index, item := range stored {
		if item.ID <= 0 || item.AccountID != accountID || item.CreatedAt.IsZero() || (item.Status != jobPending && (item.Status != jobApplied || !item.RollbackExpiresAt.Valid || !item.RollbackExpiresAt.Time.After(now))) {
			return nil, ErrUnavailable
		}
		result[index] = HistoryJob{ID: item.ID, Status: item.Status, CreatedAt: item.CreatedAt.UTC(), AppliedAt: nullableTime(item.AppliedAt), ExpiresAt: nullableTime(item.ExpiresAt), RollbackExpiresAt: nullableTime(item.RollbackExpiresAt)}
	}
	return result, nil
}

func (service *Service) OBSOutputs(ctx context.Context, accountID, jobID int64) ([]OBSOutput, error) {
	if service == nil || accountID <= 0 || jobID <= 0 {
		return nil, ErrInvalidInput
	}
	repository, ok := service.repository.(obsOutputRepository)
	if !ok {
		return nil, ErrUnavailable
	}
	stored, err := repository.LoadAppliedDefinition(ctx, accountID, jobID)
	if err != nil {
		return nil, err
	}
	if stored.ID != jobID || stored.AccountID != accountID {
		return nil, ErrUnavailable
	}
	if stored.Status != jobApplied {
		return nil, ErrConflict
	}
	definition := stored.Definition
	result := make([]OBSOutput, 0, len(definition.Attributes)+len(definition.DisplayScenes)+len(definition.GiftTargetPanels))
	for _, item := range definition.Attributes {
		result = append(result, OBSOutput{Selector: "attribute:" + item.ID, Name: item.Name})
	}
	for _, item := range definition.DisplayScenes {
		if len(item.AttributeIDs) == 0 {
			continue
		}
		result = append(result, OBSOutput{Selector: "scene:" + item.ID + ":" + strings.Join(item.AttributeIDs, ","), Name: item.Name})
	}
	for _, item := range definition.GiftTargetPanels {
		result = append(result, OBSOutput{Selector: "gift-target:" + item.ID, Name: item.Name})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Selector < result[j].Selector })
	return result, nil
}

func nullableTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC()
	return &result
}

func publicJob(stored storedJob, accountID int64, err error) (Job, error) {
	if err != nil {
		return Job{}, err
	}
	if stored.ID <= 0 || stored.AccountID != accountID || !validJobStatus(stored.Status) {
		return Job{}, ErrUnavailable
	}
	return Job{ID: stored.ID, Status: stored.Status, ExpiresAt: stored.ExpiresAt, RollbackExpiresAt: stored.RollbackExpiresAt}, nil
}

func validJobStatus(status string) bool {
	switch status {
	case jobPreviewed, jobPending, jobApplied, jobCancelled, jobRolledBack, jobExpired:
		return true
	}
	return false
}

func freshCanonical(definition configuration.Definition, runtime configuration.RuntimeState) (configuration.Definition, configuration.RuntimeState, []byte, [sha256.Size]byte, error) {
	persistedHash := definition.MigrationHash
	definition.MigrationHash = ""
	definition, runtime, err := configuration.Normalize(definition, runtime)
	if err != nil {
		return configuration.Definition{}, configuration.RuntimeState{}, nil, [sha256.Size]byte{}, err
	}
	canonical, err := json.Marshal(struct {
		Definition configuration.Definition   `json:"definition"`
		Runtime    configuration.RuntimeState `json:"runtime"`
	}{definition, runtime})
	if err != nil {
		return configuration.Definition{}, configuration.RuntimeState{}, nil, [sha256.Size]byte{}, err
	}
	hash := sha256.Sum256(canonical)
	if persistedHash != "" {
		if persistedHash != hex.EncodeToString(hash[:]) {
			return configuration.Definition{}, configuration.RuntimeState{}, nil, [sha256.Size]byte{}, errors.New("migration: candidate hash mismatch")
		}
		definition.MigrationHash = persistedHash
	}
	return definition, runtime, canonical, hash, nil
}

type frozenCandidate struct {
	Definition       configuration.Definition
	Runtime          configuration.RuntimeState
	Hash             [sha256.Size]byte
	SealedDefinition []byte
	ActiveDefinition []byte
	RuntimeJSON      []byte
}

func freezeCandidate(candidate ComposeCandidate) (frozenCandidate, error) {
	if !candidate.Ready || candidate.Definition.MigrationHash == "" {
		return frozenCandidate{}, ErrConflict
	}
	definition, runtime, _, hash, err := freshCanonical(candidate.Definition, candidate.Runtime)
	if err != nil || hash != candidate.Hash {
		return frozenCandidate{}, ErrConflict
	}
	sealedDefinition, err := json.Marshal(definition)
	if err != nil {
		return frozenCandidate{}, ErrConflict
	}
	activeDefinition := definition
	activeDefinition.MigrationHash = ""
	activeDefinitionJSON, err := json.Marshal(activeDefinition)
	if err != nil {
		return frozenCandidate{}, ErrConflict
	}
	runtimeJSON, err := json.Marshal(runtime)
	if err != nil {
		return frozenCandidate{}, ErrConflict
	}
	return frozenCandidate{Definition: definition, Runtime: runtime, Hash: hash, SealedDefinition: sealedDefinition, ActiveDefinition: activeDefinitionJSON, RuntimeJSON: runtimeJSON}, nil
}

func freezePersistedCandidate(definitionJSON, runtimeJSON []byte) (frozenCandidate, error) {
	var definition configuration.Definition
	var runtime configuration.RuntimeState
	if len(definitionJSON) == 0 || len(runtimeJSON) == 0 || json.Unmarshal(definitionJSON, &definition) != nil || json.Unmarshal(runtimeJSON, &runtime) != nil || len(definition.MigrationHash) != sha256.Size*2 {
		return frozenCandidate{}, ErrConflict
	}
	rawHash, err := hex.DecodeString(definition.MigrationHash)
	if err != nil || len(rawHash) != sha256.Size || hex.EncodeToString(rawHash) != definition.MigrationHash {
		return frozenCandidate{}, ErrConflict
	}
	var hash [sha256.Size]byte
	copy(hash[:], rawHash)
	return freezeCandidate(ComposeCandidate{Definition: definition, Runtime: runtime, Ready: true, Hash: hash})
}
func countDefinition(definition configuration.Definition) Counts {
	result := Counts{Attributes: len(definition.Attributes), Rules: len(definition.Rules), Activities: len(definition.Activities), GiftTargetPanels: len(definition.GiftTargetPanels)}
	for _, panel := range definition.GiftTargetPanels {
		result.GiftTargetItems += len(panel.Items)
	}
	return result
}

type sqlRepository struct{ db *sql.DB }

func NewRepository(db *sql.DB) Repository { return &sqlRepository{db: db} }

const previewBaseQuery = "SELECT COALESCE(v.number, 0), COALESCE(s.revision, 0) FROM streamer_accounts AS a LEFT JOIN account_active_config AS active ON active.account_id = a.id LEFT JOIN account_config_versions AS v ON v.account_id = active.account_id AND v.id = active.config_version_id LEFT JOIN account_runtime_state AS s ON s.account_id = a.id AND s.config_version_id = active.config_version_id WHERE a.id = ? FOR UPDATE"

const previewDatabaseNowQuery = "SELECT UTC_TIMESTAMP(6)"
const previewExistingQuery = "SELECT id, status, expires_at, request_hash, source_app_version, source_schema_version, room_suggestion, report_json FROM migration_jobs WHERE account_id = ? AND active_request_hash = ? FOR UPDATE"
const previewQuotaQuery = "SELECT COUNT(*) FROM migration_jobs WHERE account_id = ? AND created_at >= ? AND created_at < ?"
const previewInsertQuery = "INSERT INTO migration_jobs (account_id, request_hash, status, base_config_version_number, base_state_revision, definition_json, runtime_json, room_suggestion, source_app_version, source_schema_version, report_json, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
const previewExpireQuery = "UPDATE migration_jobs SET status = 'expired' WHERE id = ? AND account_id = ? AND status IN ('previewed', 'pending') AND expires_at <= ?"

func (repository *sqlRepository) Preview(ctx context.Context, command previewCommand) (storedPreview, error) {
	if repository == nil || repository.db == nil || command.AccountID <= 0 {
		return storedPreview{}, ErrInvalidInput
	}
	definitionJSON, err := json.Marshal(command.Definition)
	if err != nil {
		return storedPreview{}, ErrInvalidInput
	}
	runtimeJSON, err := json.Marshal(command.Runtime)
	if err != nil {
		return storedPreview{}, ErrInvalidInput
	}
	report := command.Report
	report.Counts = command.Counts
	reportJSON, err := json.Marshal(previewMetadata{Report: report, GeneralSettings: command.GeneralSettings, CropPresets: command.CropPresets, Units: command.Units, Groups: command.Groups})
	if err != nil {
		return storedPreview{}, ErrInvalidInput
	}
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return storedPreview{}, ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	var baseVersion, baseRevision uint64
	if err := transaction.QueryRowContext(ctx, previewBaseQuery, command.AccountID).Scan(&baseVersion, &baseRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storedPreview{}, ErrUnavailable
		}
		return storedPreview{}, ErrUnavailable
	}
	if baseVersion == 0 && baseRevision != 0 || baseVersion != 0 && baseRevision == 0 {
		return storedPreview{}, ErrUnavailable
	}
	var now time.Time
	if err := transaction.QueryRowContext(ctx, previewDatabaseNowQuery).Scan(&now); err != nil {
		return storedPreview{}, ErrUnavailable
	}
	now = now.UTC()
	expiresAt := now.Add(24 * time.Hour)
	var existingID int64
	var existingStatus string
	var existingExpiry time.Time
	var existingHash []byte
	var app sql.NullString
	var schema sql.NullInt64
	var room sql.NullString
	var persistedReport []byte
	err = transaction.QueryRowContext(ctx, previewExistingQuery, command.AccountID, command.Hash[:]).Scan(&existingID, &existingStatus, &existingExpiry, &existingHash, &app, &schema, &room, &persistedReport)
	if err == nil && existingID > 0 && (existingStatus == "previewed" || existingStatus == "pending") && existingExpiry.After(now) {
		stored, err := decodeStoredPreview(existingID, command.AccountID, existingStatus, existingExpiry, true, existingHash, app, schema, room, persistedReport)
		if err != nil {
			return storedPreview{}, ErrUnavailable
		}
		if err := transaction.Commit(); err != nil {
			return storedPreview{}, ErrUnavailable
		}
		committed = true
		return stored, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return storedPreview{}, ErrUnavailable
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	var successful int
	if err := transaction.QueryRowContext(ctx, previewQuotaQuery, command.AccountID, start, end).Scan(&successful); err != nil {
		return storedPreview{}, ErrUnavailable
	}
	if successful >= 5 {
		return storedPreview{}, ErrPreviewLimit
	}
	if existingID > 0 {
		result, err := transaction.ExecContext(ctx, previewExpireQuery, existingID, command.AccountID, now)
		if err != nil || !exactlyOne(result) {
			return storedPreview{}, ErrUnavailable
		}
	}
	result, err := transaction.ExecContext(ctx, previewInsertQuery, command.AccountID, command.Hash[:], "previewed", baseVersion, baseRevision, definitionJSON, runtimeJSON, nullableRoom(command.RoomSuggestion), command.Source.AppVersion, command.Source.ConfigurationSchemaVersion, reportJSON, now, expiresAt)
	if err != nil {
		return storedPreview{}, ErrUnavailable
	}
	id, err := result.LastInsertId()
	if err != nil || id <= 0 || !exactlyOne(result) {
		return storedPreview{}, ErrUnavailable
	}
	existingID = id
	if err := transaction.Commit(); err != nil {
		return storedPreview{}, ErrUnavailable
	}
	committed = true
	return storedPreview{ID: existingID, AccountID: command.AccountID, Status: jobPreviewed, ExpiresAt: expiresAt, RoomSuggestion: command.RoomSuggestion, Source: command.Source, Hash: command.Hash, Report: report}, nil
}

func decodeStoredPreview(id, accountID int64, status string, expiry time.Time, reused bool, hash []byte, app sql.NullString, schema sql.NullInt64, room sql.NullString, rawReport []byte) (storedPreview, error) {
	var result storedPreview
	var metadata previewMetadata
	if id <= 0 || status != jobPreviewed && status != jobPending || len(hash) != sha256.Size || !app.Valid || !schema.Valid || schema.Int64 < 1 || schema.Int64 > configurationSchemaVersion || json.Unmarshal(rawReport, &metadata) != nil {
		return storedPreview{}, ErrUnavailable
	}
	result.Report = metadata.Report
	copy(result.Hash[:], hash)
	result.ID = id
	result.AccountID = accountID
	result.Status = status
	result.ExpiresAt = expiry
	result.Reused = reused
	result.Source = Source{AppVersion: app.String, ConfigurationSchemaVersion: int(schema.Int64)}
	if room.Valid {
		result.RoomSuggestion = room.String
	}
	return result, nil
}

const compositionQuery = "SELECT j.status, j.expires_at, j.keep_room_suggestion, j.definition_json, j.runtime_json, j.room_suggestion, j.source_app_version, j.source_schema_version, j.report_json, v.definition_json, s.runtime_json FROM migration_jobs AS j LEFT JOIN account_active_config AS active ON active.account_id = j.account_id LEFT JOIN account_config_versions AS v ON v.account_id = active.account_id AND v.id = active.config_version_id LEFT JOIN account_runtime_state AS s ON s.account_id = j.account_id AND s.config_version_id = active.config_version_id WHERE j.id = ? AND j.account_id = ?"
const appliedDefinitionQuery = "SELECT j.id, j.account_id, j.status, v.definition_json FROM migration_jobs AS j JOIN account_config_versions AS v ON v.account_id = j.account_id AND v.id = j.applied_config_version_id WHERE j.id = ? AND j.account_id = ?"
const rollbackCandidateQuery = "SELECT j.status, v.definition_json, j.rollback_runtime_json FROM migration_jobs AS j JOIN account_config_versions AS v ON v.account_id = j.account_id AND v.id = j.rollback_config_version_id WHERE j.id = ? AND j.account_id = ?"

func (repository *sqlRepository) LoadComposition(ctx context.Context, accountID, jobID int64) (storedComposition, error) {
	if repository == nil || repository.db == nil || accountID <= 0 || jobID <= 0 {
		return storedComposition{}, ErrInvalidInput
	}
	var result storedComposition
	var importedDefinitionJSON, importedRuntimeJSON, reportJSON []byte
	var hostedDefinitionJSON, hostedRuntimeJSON []byte
	var room sql.NullString
	var keepRoom uint8
	var source Source
	err := repository.db.QueryRowContext(ctx, compositionQuery, jobID, accountID).Scan(
		&result.Status, &result.ExpiresAt, &keepRoom, &importedDefinitionJSON, &importedRuntimeJSON, &room, &source.AppVersion, &source.ConfigurationSchemaVersion, &reportJSON, &hostedDefinitionJSON, &hostedRuntimeJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedComposition{}, ErrNotFound
	}
	if err != nil || keepRoom > 1 || !validJobStatus(result.Status) || result.ExpiresAt.IsZero() || source.AppVersion == "" || source.ConfigurationSchemaVersion < 1 || source.ConfigurationSchemaVersion > configurationSchemaVersion {
		return storedComposition{}, ErrUnavailable
	}
	var metadata previewMetadata
	if json.Unmarshal(importedDefinitionJSON, &result.Imported.Definition) != nil || json.Unmarshal(importedRuntimeJSON, &result.Imported.Runtime) != nil || json.Unmarshal(reportJSON, &metadata) != nil {
		return storedComposition{}, ErrUnavailable
	}
	definition, runtime, _, _, canonicalErr := freshCanonical(result.Imported.Definition, result.Imported.Runtime)
	if canonicalErr != nil {
		return storedComposition{}, ErrUnavailable
	}
	result.Imported.Definition, result.Imported.Runtime = definition, runtime
	if result.Status == jobPending {
		if definition.GeneralSettings != nil {
			result.Imported.GeneralSettings = *definition.GeneralSettings
		}
		result.Imported.CropPresets = append([]CropPreset(nil), definition.CropPresets...)
		result.Imported.Units = DeriveUnits(definition, runtime)
		result.Imported.Groups = ConnectedGroups(result.Imported.Units)
	} else {
		result.Imported.GeneralSettings, result.Imported.CropPresets = metadata.GeneralSettings, metadata.CropPresets
		result.Imported.Units, result.Imported.Groups = metadata.Units, metadata.Groups
	}
	if len(result.Imported.Units) == 0 {
		result.Imported.Units = DeriveUnits(definition, runtime)
		result.Imported.Groups = ConnectedGroups(result.Imported.Units)
	}
	result.Imported.Source, result.Imported.Report = source, metadata.Report
	if room.Valid {
		result.Imported.RoomSuggestion = room.String
	}
	if len(hostedDefinitionJSON) == 0 && len(hostedRuntimeJSON) == 0 {
		result.HostedDefinition = configuration.Definition{}
		result.HostedRuntime = blankRuntime()
	} else {
		if len(hostedDefinitionJSON) == 0 || len(hostedRuntimeJSON) == 0 || json.Unmarshal(hostedDefinitionJSON, &result.HostedDefinition) != nil || json.Unmarshal(hostedRuntimeJSON, &result.HostedRuntime) != nil {
			return storedComposition{}, ErrUnavailable
		}
		result.HostedDefinition, result.HostedRuntime, _, _, canonicalErr = freshCanonical(result.HostedDefinition, result.HostedRuntime)
		if canonicalErr != nil {
			return storedComposition{}, ErrUnavailable
		}
	}
	result.ID, result.AccountID = jobID, accountID
	result.KeepRoomSuggestion = keepRoom == 1
	return result, nil
}

func (repository *sqlRepository) LoadAppliedDefinition(ctx context.Context, accountID, jobID int64) (storedAppliedDefinition, error) {
	if repository == nil || repository.db == nil || ctx == nil || accountID <= 0 || jobID <= 0 {
		return storedAppliedDefinition{}, ErrInvalidInput
	}
	var result storedAppliedDefinition
	var definitionJSON []byte
	err := repository.db.QueryRowContext(ctx, appliedDefinitionQuery, jobID, accountID).Scan(&result.ID, &result.AccountID, &result.Status, &definitionJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return storedAppliedDefinition{}, ErrNotFound
	}
	if err != nil || result.ID != jobID || result.AccountID != accountID || result.Status != jobApplied || len(definitionJSON) == 0 || json.Unmarshal(definitionJSON, &result.Definition) != nil {
		return storedAppliedDefinition{}, ErrUnavailable
	}
	result.Definition.MigrationHash = ""
	return result, nil
}

func (repository *sqlRepository) LoadRollbackCandidate(ctx context.Context, accountID, jobID int64) (configuration.Definition, configuration.RuntimeState, error) {
	if repository == nil || repository.db == nil || ctx == nil || accountID <= 0 || jobID <= 0 {
		return configuration.Definition{}, configuration.RuntimeState{}, ErrInvalidInput
	}
	var status string
	var definitionJSON, runtimeJSON []byte
	err := repository.db.QueryRowContext(ctx, rollbackCandidateQuery, jobID, accountID).Scan(&status, &definitionJSON, &runtimeJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return configuration.Definition{}, configuration.RuntimeState{}, ErrNotFound
	}
	if err != nil || status != jobApplied && status != jobRolledBack || len(definitionJSON) == 0 || len(runtimeJSON) == 0 {
		return configuration.Definition{}, configuration.RuntimeState{}, ErrUnavailable
	}
	var definition configuration.Definition
	var runtime configuration.RuntimeState
	if json.Unmarshal(definitionJSON, &definition) != nil || json.Unmarshal(runtimeJSON, &runtime) != nil {
		return configuration.Definition{}, configuration.RuntimeState{}, ErrUnavailable
	}
	definition.MigrationHash = ""
	definition, runtime, err = configuration.Normalize(definition, runtime)
	if err != nil {
		return configuration.Definition{}, configuration.RuntimeState{}, ErrUnavailable
	}
	return definition, runtime, nil
}

func blankRuntime() configuration.RuntimeState {
	return configuration.RuntimeState{AttributeValues: map[string]float64{}, GiftTargetReceived: []configuration.GiftTargetRuntimeState{}, Activities: []configuration.ActivityRuntimeState{}}
}
func nullableRoom(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func exactlyOne(result sql.Result) bool {
	if result == nil {
		return false
	}
	rows, err := result.RowsAffected()
	return err == nil && rows == 1
}

const lifecycleJobQuery = "SELECT status, expires_at, keep_room_suggestion, rollback_config_version_id, rollback_runtime_json, rollback_expires_at, applied_config_version_id, definition_json, runtime_json, room_suggestion FROM migration_jobs WHERE id = ? AND account_id = ? FOR UPDATE"
const lifecycleAccountQuery = "SELECT active.config_version_id, COALESCE(v.number, 0), COALESCE(s.revision, 0), s.runtime_json FROM streamer_accounts AS a LEFT JOIN account_active_config AS active ON active.account_id = a.id LEFT JOIN account_config_versions AS v ON v.account_id = active.account_id AND v.id = active.config_version_id LEFT JOIN account_runtime_state AS s ON s.account_id = a.id AND s.config_version_id = active.config_version_id WHERE a.id = ? FOR UPDATE"
const lifecycleBaseQuery = "SELECT base_config_version_number, base_state_revision FROM migration_jobs WHERE id = ? AND account_id = ?"
const lifecycleOpenSessionQuery = "SELECT id FROM live_sessions WHERE account_id = ? AND ended_at IS NULL LIMIT 1 FOR UPDATE"
const lifecycleNextVersionQuery = "SELECT COALESCE(MAX(number), 0) + 1 FROM account_config_versions WHERE account_id = ?"
const lifecycleInsertVersionQuery = "INSERT INTO account_config_versions (account_id, number, definition_json, source, created_at) VALUES (?, ?, ?, ?, ?)"
const lifecycleUpsertRuntimeQuery = "INSERT INTO account_runtime_state (account_id, config_version_id, revision, runtime_json, updated_at) VALUES (?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE config_version_id = VALUES(config_version_id), revision = VALUES(revision), runtime_json = VALUES(runtime_json), updated_at = VALUES(updated_at)"
const lifecycleUpsertActiveQuery = "INSERT INTO account_active_config (account_id, config_version_id, updated_at) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE config_version_id = VALUES(config_version_id), updated_at = VALUES(updated_at)"
const stageApplyQuery = "UPDATE migration_jobs SET status = 'pending', keep_room_suggestion = ?, definition_json = ?, runtime_json = ? WHERE id = ? AND account_id = ? AND status = 'previewed'"

type lockedJob struct {
	storedJob
	keepRoom         bool
	rollbackConfigID sql.NullInt64
	appliedConfigID  sql.NullInt64
	rollbackRuntime  []byte
	definition       []byte
	runtime          []byte
	room             sql.NullString
}

const historyQuery = "SELECT id, status, created_at, applied_at, expires_at, rollback_expires_at FROM migration_jobs WHERE account_id = ? AND (status = 'pending' OR (status = 'applied' AND rollback_expires_at > ?)) ORDER BY created_at DESC, id DESC LIMIT ?"

func (repository *sqlRepository) History(ctx context.Context, accountID int64, now time.Time, limit int) ([]storedHistoryJob, error) {
	if repository == nil || repository.db == nil || accountID <= 0 || now.IsZero() || limit <= 0 || limit > historyLimit {
		return nil, ErrInvalidInput
	}
	rows, err := repository.db.QueryContext(ctx, historyQuery, accountID, now.UTC(), limit)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	result := make([]storedHistoryJob, 0, limit)
	for rows.Next() {
		var item storedHistoryJob
		item.AccountID = accountID
		if err := rows.Scan(&item.ID, &item.Status, &item.CreatedAt, &item.AppliedAt, &item.ExpiresAt, &item.RollbackExpiresAt); err != nil {
			return nil, ErrUnavailable
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrUnavailable
	}
	return result, nil
}

func (repository *sqlRepository) Get(ctx context.Context, accountID, jobID int64) (storedJob, error) {
	if repository == nil || repository.db == nil || accountID <= 0 || jobID <= 0 {
		return storedJob{}, ErrInvalidInput
	}
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return storedJob{}, ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	if _, _, _, _, err := loadLockedAccount(ctx, transaction, accountID); err != nil {
		return storedJob{}, err
	}
	job, err := loadLockedJob(ctx, transaction, accountID, jobID)
	if err != nil {
		return storedJob{}, err
	}
	now, err := databaseUTCNow(ctx, transaction)
	if err != nil {
		return storedJob{}, err
	}
	if (job.Status == jobPreviewed || job.Status == jobPending) && !job.ExpiresAt.After(now) {
		if err := setExpired(ctx, transaction, accountID, jobID, now); err != nil {
			return storedJob{}, err
		}
		if err := transaction.Commit(); err != nil {
			return storedJob{}, ErrUnavailable
		}
		committed = true
		job.Status = jobExpired
		return job.storedJob, nil
	}
	if err := transaction.Commit(); err != nil {
		return storedJob{}, ErrUnavailable
	}
	committed = true
	return job.storedJob, nil
}

func (repository *sqlRepository) PreauthorizeApply(ctx context.Context, accountID, jobID int64) (storedJob, error) {
	if repository == nil || repository.db == nil || accountID <= 0 || jobID <= 0 {
		return storedJob{}, ErrInvalidInput
	}
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return storedJob{}, ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	_, version, revision, _, err := loadLockedAccount(ctx, transaction, accountID)
	if err != nil {
		return storedJob{}, err
	}
	job, err := loadLockedJob(ctx, transaction, accountID, jobID)
	if err != nil {
		return storedJob{}, err
	}
	now, err := databaseUTCNow(ctx, transaction)
	if err != nil {
		return storedJob{}, err
	}
	if (job.Status == jobPreviewed || job.Status == jobPending) && !job.ExpiresAt.After(now) {
		if err := setExpired(ctx, transaction, accountID, jobID, now); err != nil {
			return storedJob{}, err
		}
		if err := transaction.Commit(); err != nil {
			return storedJob{}, ErrUnavailable
		}
		committed = true
		return storedJob{}, ErrExpired
	}
	if job.Status == jobApplied || job.Status == jobPending {
		if err := transaction.Commit(); err != nil {
			return storedJob{}, ErrUnavailable
		}
		committed = true
		return job.storedJob, nil
	}
	if job.Status != jobPreviewed {
		return storedJob{}, ErrConflict
	}
	baseVersion, baseRevision, err := loadLifecycleBase(ctx, transaction, accountID, jobID)
	if err != nil {
		return storedJob{}, err
	}
	if version != baseVersion || revision != baseRevision {
		return storedJob{}, ErrConflict
	}
	var sessionID int64
	if err := transaction.QueryRowContext(ctx, lifecycleOpenSessionQuery, accountID).Scan(&sessionID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return storedJob{}, ErrUnavailable
	}
	if err := transaction.Commit(); err != nil {
		return storedJob{}, ErrUnavailable
	}
	committed = true
	return job.storedJob, nil
}

func (repository *sqlRepository) PreauthorizeRollback(ctx context.Context, accountID, jobID int64) (storedJob, error) {
	if repository == nil || repository.db == nil || accountID <= 0 || jobID <= 0 {
		return storedJob{}, ErrInvalidInput
	}
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return storedJob{}, ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	activeID, _, revision, _, err := loadLockedAccount(ctx, transaction, accountID)
	if err != nil {
		return storedJob{}, err
	}
	job, err := loadLockedJob(ctx, transaction, accountID, jobID)
	if err != nil {
		return storedJob{}, err
	}
	if job.Status == jobRolledBack {
		if err := transaction.Commit(); err != nil {
			return storedJob{}, ErrUnavailable
		}
		committed = true
		return job.storedJob, nil
	}
	if job.Status != jobApplied {
		return storedJob{}, ErrConflict
	}
	now, err := databaseUTCNow(ctx, transaction)
	if err != nil {
		return storedJob{}, err
	}
	if job.RollbackExpiresAt.IsZero() || !job.RollbackExpiresAt.After(now) {
		return storedJob{}, ErrExpired
	}
	if !activeID.Valid || revision == 0 || !job.appliedConfigID.Valid || activeID.Int64 != job.appliedConfigID.Int64 {
		return storedJob{}, ErrConflict
	}
	if err := transaction.Commit(); err != nil {
		return storedJob{}, ErrUnavailable
	}
	committed = true
	return job.storedJob, nil
}

func (repository *sqlRepository) Apply(ctx context.Context, command applyCommand) (storedJob, error) {
	return repository.apply(ctx, command, false, OwnerFence{})
}

func (repository *sqlRepository) StageApply(ctx context.Context, command applyCommand) (storedJob, error) {
	if repository == nil || repository.db == nil || ctx == nil || command.AccountID <= 0 || command.JobID <= 0 {
		return storedJob{}, ErrInvalidInput
	}
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return storedJob{}, ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	_, currentVersion, currentRevision, _, err := loadLockedAccount(ctx, transaction, command.AccountID)
	if err != nil {
		return storedJob{}, err
	}
	job, err := loadLockedJob(ctx, transaction, command.AccountID, command.JobID)
	if err != nil {
		return storedJob{}, err
	}
	now, err := databaseUTCNow(ctx, transaction)
	if err != nil {
		return storedJob{}, err
	}
	if (job.Status == jobPreviewed || job.Status == jobPending) && !job.ExpiresAt.After(now) {
		if err := setExpired(ctx, transaction, command.AccountID, command.JobID, now); err != nil {
			return storedJob{}, err
		}
		if err := transaction.Commit(); err != nil {
			return storedJob{}, ErrUnavailable
		}
		committed = true
		return storedJob{}, ErrExpired
	}
	if job.Status == jobApplied || job.Status == jobPending {
		if err := transaction.Commit(); err != nil {
			return storedJob{}, ErrUnavailable
		}
		committed = true
		return job.storedJob, nil
	}
	if job.Status != jobPreviewed {
		return storedJob{}, ErrConflict
	}
	frozen, err := freezeCandidate(command.Candidate)
	if err != nil {
		return storedJob{}, err
	}
	baseVersion, baseRevision, err := loadLifecycleBase(ctx, transaction, command.AccountID, command.JobID)
	if err != nil {
		return storedJob{}, err
	}
	if currentVersion != baseVersion || currentRevision != baseRevision {
		return storedJob{}, ErrConflict
	}
	result, err := transaction.ExecContext(ctx, stageApplyQuery, command.KeepRoomSuggestion, frozen.SealedDefinition, frozen.RuntimeJSON, command.JobID, command.AccountID)
	if err != nil || !exactlyOne(result) {
		return storedJob{}, ErrUnavailable
	}
	if err := transaction.Commit(); err != nil {
		return storedJob{}, ErrUnavailable
	}
	committed = true
	job.Status = jobPending
	return job.storedJob, nil
}

func (repository *sqlRepository) ApplyPendingAfterSession(ctx context.Context, owner OwnerFence, jobID int64) (storedJob, error) {
	if !validOwnerFence(owner) {
		return storedJob{}, ErrInvalidInput
	}
	return repository.apply(ctx, applyCommand{AccountID: owner.AccountID, JobID: jobID}, true, owner)
}

func (repository *sqlRepository) apply(ctx context.Context, command applyCommand, pendingOnly bool, owner OwnerFence) (storedJob, error) {
	if repository == nil || repository.db == nil || command.AccountID <= 0 || command.JobID <= 0 {
		return storedJob{}, ErrInvalidInput
	}
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return storedJob{}, ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	activeID, currentVersion, currentRevision, currentRuntime, err := loadLockedAccount(ctx, transaction, command.AccountID)
	if err != nil {
		return storedJob{}, err
	}
	if pendingOnly {
		if err := validateOwnerFence(ctx, transaction, owner); err != nil {
			return storedJob{}, err
		}
	}
	job, err := loadLockedJob(ctx, transaction, command.AccountID, command.JobID)
	if err != nil {
		return storedJob{}, err
	}
	now, err := databaseUTCNow(ctx, transaction)
	if err != nil {
		return storedJob{}, err
	}
	if (job.Status == jobPreviewed || job.Status == jobPending) && !job.ExpiresAt.After(now) {
		if err := setExpired(ctx, transaction, command.AccountID, command.JobID, now); err != nil {
			return storedJob{}, err
		}
		if err := transaction.Commit(); err != nil {
			return storedJob{}, ErrUnavailable
		}
		committed = true
		return storedJob{}, ErrExpired
	}
	if job.Status == jobApplied || !pendingOnly && job.Status == jobPending {
		if err := transaction.Commit(); err != nil {
			return storedJob{}, ErrUnavailable
		}
		committed = true
		return job.storedJob, nil
	}
	if pendingOnly && job.Status != jobPending || !pendingOnly && job.Status != jobPreviewed {
		return storedJob{}, ErrConflict
	}
	var frozen frozenCandidate
	if pendingOnly {
		frozen, err = freezePersistedCandidate(job.definition, job.runtime)
	} else {
		frozen, err = freezeCandidate(command.Candidate)
	}
	if err != nil {
		return storedJob{}, err
	}
	baseVersion, baseRevision, err := loadLifecycleBase(ctx, transaction, command.AccountID, command.JobID)
	if err != nil {
		return storedJob{}, err
	}
	if currentVersion != baseVersion || currentRevision != baseRevision {
		return storedJob{}, ErrConflict
	}
	var sessionID int64
	err = transaction.QueryRowContext(ctx, lifecycleOpenSessionQuery, command.AccountID).Scan(&sessionID)
	if err == nil {
		if !pendingOnly && job.Status == jobPreviewed {
			result, updateErr := transaction.ExecContext(ctx, "UPDATE migration_jobs SET status = 'pending', keep_room_suggestion = ?, definition_json = ?, runtime_json = ? WHERE id = ? AND account_id = ? AND status = 'previewed'", command.KeepRoomSuggestion, frozen.SealedDefinition, frozen.RuntimeJSON, command.JobID, command.AccountID)
			if updateErr != nil || !exactlyOne(result) {
				return storedJob{}, ErrUnavailable
			}
			job.Status = jobPending
		}
		if err := transaction.Commit(); err != nil {
			return storedJob{}, ErrUnavailable
		}
		committed = true
		return job.storedJob, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return storedJob{}, ErrUnavailable
	}
	result, err := applyLockedMigration(ctx, transaction, command, job, frozen, activeID, currentRevision, currentRuntime, now)
	if err != nil {
		return storedJob{}, err
	}
	if err := transaction.Commit(); err != nil {
		return storedJob{}, ErrUnavailable
	}
	committed = true
	return result, nil
}

func validateOwnerFence(ctx context.Context, transaction *sql.Tx, owner OwnerFence) error {
	var active bool
	err := transaction.QueryRowContext(ctx, "SELECT expires_at > UTC_TIMESTAMP(6) FROM runtime_account_owners WHERE account_id = ? AND owner_token = ? AND fencing_epoch = ? FOR UPDATE", owner.AccountID, owner.Token[:], owner.Epoch).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !active {
		return ErrOwnershipConflict
	}
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func validOwnerFence(owner OwnerFence) bool {
	return owner.AccountID > 0 && owner.Token != ([32]byte{}) && owner.Epoch > 0
}

func (repository *sqlRepository) Cancel(ctx context.Context, accountID, jobID int64) (storedJob, error) {
	if repository == nil || repository.db == nil || accountID <= 0 || jobID <= 0 {
		return storedJob{}, ErrInvalidInput
	}
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return storedJob{}, ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	if _, _, _, _, err := loadLockedAccount(ctx, transaction, accountID); err != nil {
		return storedJob{}, err
	}
	job, err := loadLockedJob(ctx, transaction, accountID, jobID)
	if err != nil {
		return storedJob{}, err
	}
	now, err := databaseUTCNow(ctx, transaction)
	if err != nil {
		return storedJob{}, err
	}
	if (job.Status == jobPreviewed || job.Status == jobPending) && !job.ExpiresAt.After(now) {
		if err := setExpired(ctx, transaction, accountID, jobID, now); err != nil {
			return storedJob{}, err
		}
		if err := transaction.Commit(); err != nil {
			return storedJob{}, ErrUnavailable
		}
		committed = true
		return storedJob{}, ErrExpired
	} else if job.Status == jobPreviewed || job.Status == jobPending {
		result, updateErr := transaction.ExecContext(ctx, "UPDATE migration_jobs SET status = 'cancelled', cancelled_at = ? WHERE id = ? AND account_id = ? AND status IN ('previewed', 'pending')", now, jobID, accountID)
		if updateErr != nil || !exactlyOne(result) {
			return storedJob{}, ErrUnavailable
		}
		job.Status = jobCancelled
	} else if job.Status != jobCancelled {
		return storedJob{}, ErrConflict
	}
	if err := transaction.Commit(); err != nil {
		return storedJob{}, ErrUnavailable
	}
	committed = true
	return job.storedJob, nil
}

func (repository *sqlRepository) Rollback(ctx context.Context, accountID, jobID int64) (storedJob, error) {
	if repository == nil || repository.db == nil || accountID <= 0 || jobID <= 0 {
		return storedJob{}, ErrInvalidInput
	}
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return storedJob{}, ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	activeID, _, currentRevision, _, err := loadLockedAccount(ctx, transaction, accountID)
	if err != nil {
		return storedJob{}, err
	}
	job, err := loadLockedJob(ctx, transaction, accountID, jobID)
	if err != nil {
		return storedJob{}, err
	}
	if job.Status == jobRolledBack {
		if err := transaction.Commit(); err != nil {
			return storedJob{}, ErrUnavailable
		}
		committed = true
		return job.storedJob, nil
	}
	if job.Status != jobApplied {
		return storedJob{}, ErrConflict
	}
	now, err := databaseUTCNow(ctx, transaction)
	if err != nil {
		return storedJob{}, err
	}
	if job.RollbackExpiresAt.IsZero() || !job.RollbackExpiresAt.After(now) {
		return storedJob{}, ErrExpired
	}
	if !activeID.Valid || currentRevision == 0 || !job.appliedConfigID.Valid || activeID.Int64 != job.appliedConfigID.Int64 {
		return storedJob{}, ErrConflict
	}
	var openSession int64
	if err := transaction.QueryRowContext(ctx, lifecycleOpenSessionQuery, accountID).Scan(&openSession); err == nil {
		return storedJob{}, ErrConflict
	} else if !errors.Is(err, sql.ErrNoRows) {
		return storedJob{}, ErrUnavailable
	}
	if !job.rollbackConfigID.Valid {
		result, deleteErr := transaction.ExecContext(ctx, "DELETE FROM account_active_config WHERE account_id = ?", accountID)
		if deleteErr != nil || !exactlyOne(result) {
			return storedJob{}, ErrUnavailable
		}
		result, deleteErr = transaction.ExecContext(ctx, "DELETE FROM account_runtime_state WHERE account_id = ?", accountID)
		if deleteErr != nil || !exactlyOne(result) {
			return storedJob{}, ErrUnavailable
		}
	} else {
		var definition []byte
		if err := transaction.QueryRowContext(ctx, "SELECT definition_json FROM account_config_versions WHERE account_id = ? AND id = ?", accountID, job.rollbackConfigID.Int64).Scan(&definition); err != nil || len(definition) == 0 {
			return storedJob{}, ErrUnavailable
		}
		var number uint64
		if err := transaction.QueryRowContext(ctx, lifecycleNextVersionQuery, accountID).Scan(&number); err != nil || number == 0 {
			return storedJob{}, ErrUnavailable
		}
		result, insertErr := transaction.ExecContext(ctx, lifecycleInsertVersionQuery, accountID, number, definition, "rollback", now)
		if insertErr != nil {
			return storedJob{}, ErrUnavailable
		}
		versionID, insertErr := result.LastInsertId()
		if insertErr != nil || versionID <= 0 || !exactlyOne(result) {
			return storedJob{}, ErrUnavailable
		}
		nextRevision := currentRevision + 1
		if nextRevision == 0 {
			return storedJob{}, ErrUnavailable
		}
		result, insertErr = transaction.ExecContext(ctx, lifecycleUpsertRuntimeQuery, accountID, versionID, nextRevision, job.rollbackRuntime, now)
		if insertErr != nil || !oneOrTwoMigrationRows(result) {
			return storedJob{}, ErrUnavailable
		}
		result, insertErr = transaction.ExecContext(ctx, lifecycleUpsertActiveQuery, accountID, versionID, now)
		if insertErr != nil || !oneOrTwoMigrationRows(result) {
			return storedJob{}, ErrUnavailable
		}
	}
	result, err := transaction.ExecContext(ctx, "UPDATE migration_jobs SET status = 'rolled_back', rolled_back_at = ? WHERE id = ? AND account_id = ? AND status = 'applied'", now, jobID, accountID)
	if err != nil || !exactlyOne(result) {
		return storedJob{}, ErrUnavailable
	}
	job.Status = jobRolledBack
	if err := transaction.Commit(); err != nil {
		return storedJob{}, ErrUnavailable
	}
	committed = true
	return job.storedJob, nil
}

func loadLockedJob(ctx context.Context, transaction *sql.Tx, accountID, jobID int64) (lockedJob, error) {
	var job lockedJob
	var rollbackExpiry sql.NullTime
	var keepRoom uint8
	err := transaction.QueryRowContext(ctx, lifecycleJobQuery, jobID, accountID).Scan(&job.Status, &job.ExpiresAt, &keepRoom, &job.rollbackConfigID, &job.rollbackRuntime, &rollbackExpiry, &job.appliedConfigID, &job.definition, &job.runtime, &job.room)
	if errors.Is(err, sql.ErrNoRows) {
		return lockedJob{}, ErrNotFound
	}
	if err != nil || !validJobStatus(job.Status) || len(job.definition) == 0 || len(job.runtime) == 0 {
		return lockedJob{}, ErrUnavailable
	}
	job.ID, job.AccountID = jobID, accountID
	if keepRoom > 1 {
		return lockedJob{}, ErrUnavailable
	}
	job.keepRoom = keepRoom == 1
	if rollbackExpiry.Valid {
		job.RollbackExpiresAt = rollbackExpiry.Time.UTC()
	}
	return job, nil
}

func loadLockedAccount(ctx context.Context, transaction *sql.Tx, accountID int64) (sql.NullInt64, uint64, uint64, []byte, error) {
	var active sql.NullInt64
	var version, revision uint64
	var runtime []byte
	err := transaction.QueryRowContext(ctx, lifecycleAccountQuery, accountID).Scan(&active, &version, &revision, &runtime)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.NullInt64{}, 0, 0, nil, ErrNotFound
	}
	if err != nil || (active.Valid && (active.Int64 <= 0 || version == 0 || revision == 0 || len(runtime) == 0)) || (!active.Valid && (version != 0 || revision != 0 || len(runtime) != 0)) {
		return sql.NullInt64{}, 0, 0, nil, ErrUnavailable
	}
	return active, version, revision, runtime, nil
}

func loadLifecycleBase(ctx context.Context, transaction *sql.Tx, accountID, jobID int64) (uint64, uint64, error) {
	var version, revision uint64
	if err := transaction.QueryRowContext(ctx, lifecycleBaseQuery, jobID, accountID).Scan(&version, &revision); err != nil {
		return 0, 0, ErrUnavailable
	}
	return version, revision, nil
}

func databaseUTCNow(ctx context.Context, transaction *sql.Tx) (time.Time, error) {
	var now time.Time
	if err := transaction.QueryRowContext(ctx, previewDatabaseNowQuery).Scan(&now); err != nil {
		return time.Time{}, ErrUnavailable
	}
	return now.UTC(), nil
}

func setExpired(ctx context.Context, transaction *sql.Tx, accountID, jobID int64, now time.Time) error {
	result, err := transaction.ExecContext(ctx, previewExpireQuery, jobID, accountID, now)
	if err != nil || !exactlyOne(result) {
		return ErrUnavailable
	}
	return nil
}

func applyLockedMigration(ctx context.Context, transaction *sql.Tx, command applyCommand, job lockedJob, frozen frozenCandidate, activeID sql.NullInt64, currentRevision uint64, currentRuntime []byte, now time.Time) (storedJob, error) {
	var number uint64
	if err := transaction.QueryRowContext(ctx, lifecycleNextVersionQuery, command.AccountID).Scan(&number); err != nil || number == 0 {
		return storedJob{}, ErrUnavailable
	}
	result, err := transaction.ExecContext(ctx, lifecycleInsertVersionQuery, command.AccountID, number, frozen.ActiveDefinition, "migration", now)
	if err != nil {
		return storedJob{}, ErrUnavailable
	}
	versionID, err := result.LastInsertId()
	if err != nil || versionID <= 0 || !exactlyOne(result) {
		return storedJob{}, ErrUnavailable
	}
	nextRevision := currentRevision + 1
	if nextRevision == 0 {
		return storedJob{}, ErrUnavailable
	}
	result, err = transaction.ExecContext(ctx, lifecycleUpsertRuntimeQuery, command.AccountID, versionID, nextRevision, frozen.RuntimeJSON, now)
	if err != nil || !oneOrTwoMigrationRows(result) {
		return storedJob{}, ErrUnavailable
	}
	result, err = transaction.ExecContext(ctx, lifecycleUpsertActiveQuery, command.AccountID, versionID, now)
	if err != nil || !oneOrTwoMigrationRows(result) {
		return storedJob{}, ErrUnavailable
	}
	keepRoomSuggestion := command.KeepRoomSuggestion
	if job.Status == jobPending {
		keepRoomSuggestion = job.keepRoom
	}
	if keepRoomSuggestion && job.room.Valid {
		result, err = transaction.ExecContext(ctx, "INSERT INTO account_room_suggestions (account_id, room_id, suggested_at) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE room_id = VALUES(room_id), suggested_at = VALUES(suggested_at)", command.AccountID, job.room.String, now)
		if err != nil || !zeroOneOrTwoMigrationRows(result) {
			return storedJob{}, ErrUnavailable
		}
	}
	rollbackExpiry := now.Add(7 * 24 * time.Hour)
	var rollbackID any
	if activeID.Valid {
		rollbackID = activeID.Int64
	}
	var rollbackRuntime any
	if activeID.Valid {
		rollbackRuntime = currentRuntime
	}
	result, err = transaction.ExecContext(ctx, "UPDATE migration_jobs SET keep_room_suggestion = ?, rollback_config_version_id = ?, rollback_runtime_json = ?, rollback_expires_at = ?, applied_config_version_id = ?, status = 'applied', applied_at = ? WHERE id = ? AND account_id = ? AND status IN ('previewed', 'pending')", keepRoomSuggestion, rollbackID, rollbackRuntime, rollbackExpiry, versionID, now, command.JobID, command.AccountID)
	if err != nil || !exactlyOne(result) {
		return storedJob{}, ErrUnavailable
	}
	return storedJob{ID: command.JobID, AccountID: command.AccountID, Status: jobApplied, ExpiresAt: job.ExpiresAt, RollbackExpiresAt: rollbackExpiry}, nil
}

func oneOrTwoMigrationRows(result sql.Result) bool {
	if result == nil {
		return false
	}
	rows, err := result.RowsAffected()
	return err == nil && (rows == 1 || rows == 2)
}
func zeroOneOrTwoMigrationRows(result sql.Result) bool {
	if result == nil {
		return false
	}
	rows, err := result.RowsAffected()
	return err == nil && (rows == 0 || rows == 1 || rows == 2)
}
