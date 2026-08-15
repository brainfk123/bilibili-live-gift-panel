package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestStatePersistenceRetainsRealNonDurableJournalWithoutShardReplay(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	if err := store.replaceState(defaultAppState()); err != nil {
		t.Fatal(err)
	}
	before := snapshotStateFiles(t, store)
	desired := defaultAppState()
	desired.RoomID = "all-shards-durable"
	desired.Attributes = []attributeState{{ID: "attribute-a", Name: "积分", Value: 17}}
	injected := errors.New("injected real non-durable journal directory sync warning")
	journalWrites := 0
	shardWrites := 0
	store.writeAtomicallyOutcome = func(path string, data []byte) atomicWriteOutcome {
		if filepath.Base(path) == "state-transaction.json" {
			journalWrites++
			return writeFileAtomicallyOutcomeWith(path, data, func(string) error { return injected })
		}
		shardWrites++
		return writeFileAtomicallyOutcome(path, data)
	}

	store.mu.Lock()
	outcome := store.persistPreparedStateWithOutcomeLocked(desired, "")
	store.mu.Unlock()
	if outcome.Committed || !errors.Is(outcome.Err, injected) {
		t.Fatalf("state persistence outcome=%+v, want uncommitted with real journal warning", outcome)
	}
	if journalWrites != 1 || shardWrites != 0 {
		t.Fatalf("journal writes=%d shard writes=%d, want 1/0", journalWrites, shardWrites)
	}
	assertStateFilesEqual(t, store, before)
	if _, err := os.Stat(store.stateTransactionPath()); err != nil {
		t.Fatalf("real-warning WAL was not retained: %v", err)
	}
	store.writeAtomicallyOutcome = nil
	direct, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := (&configStore{path: store.path}).readState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(direct, desired) || !reflect.DeepEqual(restarted, desired) {
		t.Fatalf("whole committed state mismatch: direct=%#v restarted=%#v want=%#v", direct, restarted, desired)
	}
}

func TestStatePersistenceDoesNotReplayShardsBeforeJournalIsDurable(t *testing.T) {
	dir := t.TempDir()
	store := &configStore{path: filepath.Join(dir, "config.json")}
	initial := defaultAppState()
	initial.RoomID = "old-room"
	initial.Attributes = []attributeState{{ID: "attribute-a", Name: "积分", Value: 1}}
	if err := store.replaceState(initial); err != nil {
		t.Fatal(err)
	}
	before := snapshotStateFiles(t, store)

	desired := initial
	desired.RoomID = "new-room"
	desired.Attributes = []attributeState{{ID: "attribute-a", Name: "积分", Value: 9}}
	injected := errors.New("injected WAL directory sync failure")
	shardWrites := make([]string, 0, len(store.statePaths()))
	store.writeAtomicallyOutcome = func(path string, data []byte) atomicWriteOutcome {
		if filepath.Clean(path) == filepath.Clean(store.stateTransactionPath()) {
			return writeFileAtomicallyOutcomeWith(path, data, func(string) error { return injected })
		}
		shardWrites = append(shardWrites, filepath.Base(path))
		return writeFileAtomicallyOutcome(path, data)
	}

	store.mu.Lock()
	outcome := store.persistPreparedStateWithOutcomeLocked(desired, "")
	store.mu.Unlock()
	if outcome.Committed {
		t.Fatalf("nondurable WAL outcome committed=%v, want false (err=%v)", outcome.Committed, outcome.Err)
	}
	if !errors.Is(outcome.Err, injected) {
		t.Fatalf("nondurable WAL error=%v, want injected sync failure", outcome.Err)
	}
	if len(shardWrites) != 0 {
		t.Fatalf("nondurable WAL triggered shard writes: %v", shardWrites)
	}
	assertStateFilesEqual(t, store, before)
	if _, err := os.Stat(store.stateTransactionPath()); err != nil {
		t.Fatalf("nondurable WAL evidence was not retained: %v", err)
	}
}

func TestPendingStateTransactionFailsClosedUntilExactWALRepublicationIsDurable(t *testing.T) {
	dir := t.TempDir()
	seed := &configStore{path: filepath.Join(dir, "config.json")}
	initial := defaultAppState()
	initial.RoomID = "old-room"
	initial.Attributes = []attributeState{{ID: "attribute-a", Name: "积分", Value: 1}}
	if err := seed.replaceState(initial); err != nil {
		t.Fatal(err)
	}
	desired := initial
	desired.RoomID = "new-room"
	desired.Attributes = []attributeState{{ID: "attribute-a", Name: "积分", Value: 9}}
	tx := preparedTransactionForTest(t, desired)
	walBytes, err := json.MarshalIndent(tx, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	walBytes = append(walBytes, '\n')
	publicationFailure := errors.New("injected pre-restart WAL directory sync failure")
	publication := writeFileAtomicallyOutcomeWith(
		seed.stateTransactionPath(),
		walBytes,
		func(string) error { return publicationFailure },
	)
	if !publication.Committed || publication.Durable || !errors.Is(publication.Err, publicationFailure) {
		t.Fatalf("seed WAL outcome=%+v, want visible/non-durable real rename", publication)
	}

	restarted := &configStore{path: seed.path}
	endorsementFailure := errors.New("injected recovery WAL endorsement failure")
	endorsementAttempts := 0
	shardWrites := 0
	restarted.writeAtomicallyOutcome = func(path string, data []byte) atomicWriteOutcome {
		if filepath.Clean(path) == filepath.Clean(restarted.stateTransactionPath()) {
			endorsementAttempts++
			if !bytes.Equal(data, walBytes) {
				t.Fatalf("recovery republished different WAL bytes\ngot:  %q\nwant: %q", data, walBytes)
			}
			return writeFileAtomicallyOutcomeWith(path, data, func(string) error { return endorsementFailure })
		}
		shardWrites++
		return writeFileAtomicallyOutcome(path, data)
	}
	if _, err := restarted.readState(); !errors.Is(err, endorsementFailure) {
		t.Fatalf("recovery read error=%v, want endorsement failure", err)
	}
	if endorsementAttempts != 1 || shardWrites != 0 {
		t.Fatalf("failed endorsement attempts=%d shard writes=%d, want 1/0", endorsementAttempts, shardWrites)
	}
	if restarted.committedTransactionState != nil {
		t.Fatal("failed endorsement installed an authoritative candidate")
	}
	if restarted.MutationBlockKind() != "transaction_recovery" {
		t.Fatalf("failed endorsement mutation block=%q, want transaction_recovery", restarted.MutationBlockKind())
	}
	mutationRan := false
	if _, err := restarted.updateState(func(*appState) error {
		mutationRan = true
		return nil
	}); err == nil {
		t.Fatal("mutation unexpectedly succeeded while WAL endorsement still failed")
	}
	if mutationRan || shardWrites != 0 {
		t.Fatalf("blocked mutation ran=%v shard writes=%d", mutationRan, shardWrites)
	}

	restarted.writeAtomicallyOutcome = func(path string, data []byte) atomicWriteOutcome {
		if filepath.Clean(path) == filepath.Clean(restarted.stateTransactionPath()) {
			endorsementAttempts++
			if !bytes.Equal(data, walBytes) {
				t.Fatalf("successful recovery republished different WAL bytes")
			}
		}
		return writeFileAtomicallyOutcome(path, data)
	}
	recovered, err := restarted.readState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recovered, desired) {
		t.Fatalf("recovered state=%#v, want %#v", recovered, desired)
	}
	if restarted.MutationBlockKind() != "" {
		t.Fatalf("successful endorsement left mutation block=%q", restarted.MutationBlockKind())
	}
}

func TestPendingStateTransactionClearsEndorsementBlockBeforeReplayRetry(t *testing.T) {
	dir := t.TempDir()
	seed := &configStore{path: filepath.Join(dir, "config.json")}
	initial := defaultAppState()
	initial.RoomID = "old-room"
	if err := seed.replaceState(initial); err != nil {
		t.Fatal(err)
	}
	desired := initial
	desired.RoomID = "authoritative-candidate"
	tx := preparedTransactionForTest(t, desired)
	walBytes, err := json.MarshalIndent(tx, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	walBytes = append(walBytes, '\n')
	if err := os.WriteFile(seed.stateTransactionPath(), walBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	restarted := &configStore{path: seed.path}
	endorsementFailure := errors.New("injected first endorsement failure")
	restarted.writeAtomicallyOutcome = func(path string, data []byte) atomicWriteOutcome {
		if filepath.Clean(path) == filepath.Clean(restarted.stateTransactionPath()) {
			return atomicWriteOutcome{Committed: true, Durable: false, Err: endorsementFailure}
		}
		return writeFileAtomicallyOutcome(path, data)
	}
	if _, err := restarted.readState(); !errors.Is(err, endorsementFailure) {
		t.Fatalf("initial endorsement error=%v, want injected failure", err)
	}
	if restarted.MutationBlockKind() != "transaction_recovery" {
		t.Fatalf("initial recovery block=%q, want transaction_recovery", restarted.MutationBlockKind())
	}

	replayFailure := errors.New("injected first shard replay failure")
	replayFailuresRemaining := 1
	restarted.writeAtomicallyOutcome = func(path string, data []byte) atomicWriteOutcome {
		if filepath.Clean(path) == filepath.Clean(restarted.stateTransactionPath()) {
			return writeFileAtomicallyOutcome(path, data)
		}
		if replayFailuresRemaining > 0 {
			replayFailuresRemaining--
			return atomicWriteOutcome{Err: replayFailure}
		}
		return writeFileAtomicallyOutcome(path, data)
	}
	authoritative, err := restarted.readState()
	if err != nil {
		t.Fatalf("authoritative read after durable endorsement failed: %v", err)
	}
	if !reflect.DeepEqual(authoritative, desired) {
		t.Fatalf("authoritative state=%#v, want %#v", authoritative, desired)
	}
	if restarted.committedTransactionState == nil {
		t.Fatal("failed replay discarded the durably endorsed candidate")
	}
	if restarted.MutationBlockKind() != "" {
		t.Fatalf("durably endorsed candidate retained obsolete block=%q", restarted.MutationBlockKind())
	}
	if _, err := os.Stat(restarted.stateTransactionPath()); err != nil {
		t.Fatalf("failed replay lost retryable WAL: %v", err)
	}

	recovered, err := restarted.readState()
	if err != nil {
		t.Fatalf("subsequent replay retry failed: %v", err)
	}
	if !reflect.DeepEqual(recovered, desired) {
		t.Fatalf("recovered state=%#v, want %#v", recovered, desired)
	}
	if restarted.committedTransactionState != nil || restarted.MutationBlockKind() != "" {
		t.Fatalf("successful replay left candidate=%v block=%q", restarted.committedTransactionState != nil, restarted.MutationBlockKind())
	}
	if _, err := os.Stat(restarted.stateTransactionPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful replay left WAL: %v", err)
	}
}

func TestConfigStoreStartupKeepsFailedWALEndorsementRetryableAndFailClosed(t *testing.T) {
	dir := t.TempDir()
	seed := &configStore{path: filepath.Join(dir, "config.json")}
	initial := defaultAppState()
	initial.RoomID = "old-room"
	if err := seed.replaceState(initial); err != nil {
		t.Fatal(err)
	}
	desired := initial
	desired.RoomID = "new-room"
	tx := preparedTransactionForTest(t, desired)
	walBytes, err := json.MarshalIndent(tx, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	walBytes = append(walBytes, '\n')
	seedFailure := errors.New("injected pre-startup WAL directory sync failure")
	seedOutcome := writeFileAtomicallyOutcomeWith(
		seed.stateTransactionPath(),
		walBytes,
		func(string) error { return seedFailure },
	)
	if !seedOutcome.Committed || seedOutcome.Durable {
		t.Fatalf("seed WAL outcome=%+v, want visible/non-durable", seedOutcome)
	}

	restarted := &configStore{path: seed.path}
	endorsementFailure := errors.New("injected startup WAL endorsement failure")
	shardWrites := 0
	restarted.writeAtomicallyOutcome = func(path string, data []byte) atomicWriteOutcome {
		if filepath.Clean(path) == filepath.Clean(restarted.stateTransactionPath()) {
			return writeFileAtomicallyOutcomeWith(path, data, func(string) error { return endorsementFailure })
		}
		shardWrites++
		return writeFileAtomicallyOutcome(path, data)
	}
	started, err := initializeConfigStore(restarted)
	if err != nil {
		t.Fatalf("startup returned an error instead of retaining a fail-closed store: %v", err)
	}
	response := httptest.NewRecorder()
	started.handle(response, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("failed-endorsement GET status=%d body=%s, want 500", response.Code, response.Body.String())
	}
	if shardWrites != 0 || started.committedTransactionState != nil {
		t.Fatalf("failed startup endorsement shard writes=%d candidate=%v, want 0/nil", shardWrites, started.committedTransactionState != nil)
	}
	mutationRan := false
	if _, err := started.updateState(func(*appState) error {
		mutationRan = true
		return nil
	}); err == nil || mutationRan {
		t.Fatalf("failed startup endorsement mutation ran=%v err=%v", mutationRan, err)
	}

	started.writeAtomicallyOutcome = nil
	recovered := httptest.NewRecorder()
	started.handle(recovered, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if recovered.Code != http.StatusOK || !bytes.Contains(recovered.Body.Bytes(), []byte(`"roomId":"new-room"`)) {
		t.Fatalf("retry GET status=%d body=%s, want recovered new state", recovered.Code, recovered.Body.String())
	}
	if started.MutationBlockKind() != "" {
		t.Fatalf("successful retry left mutation block=%q", started.MutationBlockKind())
	}
}

func TestTransactionRecoverySeparatesDiagnosticEvidenceFromUnendorsedValidWAL(t *testing.T) {
	t.Run("unreadable evidence keeps diagnostic read", func(t *testing.T) {
		store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
		initial := defaultAppState()
		initial.RoomID = "diagnostic-room"
		if err := store.replaceState(initial); err != nil {
			t.Fatal(err)
		}
		injected := errors.New("injected unreadable WAL")
		store.readTransaction = func(string) ([]byte, error) { return nil, injected }

		state, err := store.readState()
		if err != nil {
			t.Fatalf("diagnostic read failed: %v", err)
		}
		if state.RoomID != initial.RoomID || store.MutationBlockKind() != "transaction_recovery" {
			t.Fatalf("diagnostic state room=%q block=%q", state.RoomID, store.MutationBlockKind())
		}
	})

	t.Run("valid WAL without durable endorsement fails closed", func(t *testing.T) {
		store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
		initial := defaultAppState()
		initial.RoomID = "old-room"
		if err := store.replaceState(initial); err != nil {
			t.Fatal(err)
		}
		desired := initial
		desired.RoomID = "candidate-room"
		writePendingTransactionForTest(t, store, preparedTransactionForTest(t, desired))
		injected := errors.New("injected valid-WAL endorsement failure")
		store.writeAtomicallyOutcome = func(path string, data []byte) atomicWriteOutcome {
			if filepath.Clean(path) == filepath.Clean(store.stateTransactionPath()) {
				return atomicWriteOutcome{Committed: true, Durable: false, Err: injected}
			}
			return writeFileAtomicallyOutcome(path, data)
		}

		if _, err := store.readState(); !errors.Is(err, injected) {
			t.Fatalf("valid unendorsed WAL read error=%v, want injected failure", err)
		}
		if store.committedTransactionState != nil || store.MutationBlockKind() != "transaction_recovery" {
			t.Fatalf("unendorsed candidate installed=%v block=%q", store.committedTransactionState != nil, store.MutationBlockKind())
		}
	})
}

func TestDurableWALKeepsFirstPostRenameShardSyncFailureWholeAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	store := &configStore{path: filepath.Join(dir, "config.json")}
	initial := defaultAppState()
	initial.RoomID = "old-room"
	initial.Attributes = []attributeState{{ID: "attribute-a", Name: "积分", Value: 1}}
	initial.Log = []logEntry{{EventID: "old-event", ValueAfter: 1}}
	if err := store.replaceState(initial); err != nil {
		t.Fatal(err)
	}
	oldEventBytes, err := os.ReadFile(store.eventLogPath())
	if err != nil {
		t.Fatal(err)
	}
	desired := initial
	desired.RoomID = "new-room"
	desired.Attributes = []attributeState{{ID: "attribute-a", Name: "积分", Value: 9}}
	desired.Log = []logEntry{{EventID: "new-event", ValueAfter: 9}}
	injected := errors.New("injected first-shard post-rename directory sync failure")
	firstShardWrites := 0
	installFailure := func(target *configStore) {
		target.writeAtomicallyOutcome = func(path string, data []byte) atomicWriteOutcome {
			if filepath.Clean(path) == filepath.Clean(target.eventLogPath()) {
				firstShardWrites++
				return writeFileAtomicallyOutcomeWith(path, data, func(string) error { return injected })
			}
			return writeFileAtomicallyOutcome(path, data)
		}
	}
	installFailure(store)

	store.mu.Lock()
	outcome := store.persistPreparedStateWithOutcomeLocked(desired, "")
	store.mu.Unlock()
	if !outcome.Committed || !errors.Is(outcome.Err, injected) {
		t.Fatalf("durable-WAL first-shard outcome=%+v, want committed with sync failure", outcome)
	}
	if firstShardWrites != 1 {
		t.Fatalf("first shard writes=%d, want 1", firstShardWrites)
	}
	served, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(served, desired) {
		t.Fatalf("same-process state=%#v, want complete WAL candidate %#v", served, desired)
	}

	// Model the allowed crash outcome where the rename-visible shard is lost.
	// The durable WAL must still make the complete new candidate authoritative.
	if err := os.WriteFile(store.eventLogPath(), oldEventBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	restarted := &configStore{path: store.path}
	installFailure(restarted)
	restarted, err = initializeConfigStore(restarted)
	if err != nil {
		t.Fatal(err)
	}
	servedAfterRestart, err := restarted.readState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(servedAfterRestart, desired) {
		t.Fatalf("restarted state=%#v, want complete WAL candidate %#v", servedAfterRestart, desired)
	}

	restarted.writeAtomicallyOutcome = nil
	recovered, err := restarted.readState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recovered, desired) {
		t.Fatalf("settled state=%#v, want %#v", recovered, desired)
	}
	if _, err := os.Stat(restarted.stateTransactionPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("settled WAL remains: %v", err)
	}
}

func TestPendingStateTransactionRecoversEveryShardWithoutReapplyingIngress(t *testing.T) {
	for _, failBase := range []string{"events.log", "history.json", "cache.json", "config.json"} {
		t.Run(failBase, func(t *testing.T) {
			dir := t.TempDir()
			store := &configStore{path: filepath.Join(dir, "config.json")}
			initial := defaultAppState()
			initial.Attributes = []attributeState{{Name: "积分", Value: 0}}
			initial.GiftCatalog = []giftInfo{{ID: 1, Name: "旧礼物"}}
			if err := store.replaceState(initial); err != nil {
				t.Fatal(err)
			}

			failed := false
			store.writeAtomically = func(path string, data []byte) error {
				if filepath.Base(path) == failBase && !failed {
					failed = true
					return errors.New("injected shard failure")
				}
				return writeFileAtomically(path, data)
			}
			_, _, err := store.updateStateForIngestion("ingress-1", func(state *appState) error {
				state.Attributes[0].Value = 7
				state.Log = append([]logEntry{{EventID: "ingress-1", ValueAfter: 7}}, state.Log...)
				state.GiftCatalog = append(state.GiftCatalog, giftInfo{ID: 2, Name: "新礼物"})
				return nil
			})
			if err == nil {
				t.Fatal("expected injected failure")
			}

			recovered := &configStore{path: filepath.Join(dir, "config.json")}
			state, err := recovered.readState()
			if err != nil {
				t.Fatal(err)
			}
			if state.Attributes[0].Value != 7 {
				t.Fatalf("value = %v", state.Attributes[0].Value)
			}
			if len(state.Log) != 1 || state.Log[0].EventID != "ingress-1" {
				t.Fatalf("recovered event log = %#v", state.Log)
			}
			if len(state.GiftCatalog) != 2 || state.GiftCatalog[1].ID != 2 {
				t.Fatalf("recovered cache = %#v", state.GiftCatalog)
			}
			_, applied, err := recovered.updateStateForIngestion("ingress-1", func(*appState) error {
				t.Fatal("replayed ingestion callback")
				return nil
			})
			if err != nil || applied {
				t.Fatalf("applied=%v err=%v", applied, err)
			}
		})
	}
}

func TestPendingStateTransactionRecoversGiftFormulaRandomResultWithoutReevaluation(t *testing.T) {
	dir := t.TempDir()
	store := &configStore{path: filepath.Join(dir, "config.json")}
	initial := defaultAppState()
	initial.Attributes = []attributeState{{Name: "积分", Value: 0}}
	initial.Rules = []giftRule{{ID: "random-rule", GiftID: 1, AttributeName: "积分", Formula: "RANDOMCHOICE(10,37,60)"}}
	if err := store.replaceState(initial); err != nil {
		t.Fatal(err)
	}

	originalRandomIntn := formulaRandomIntn
	t.Cleanup(func() { formulaRandomIntn = originalRandomIntn })
	evaluations := 0
	formulaRandomIntn = func(limit int) int {
		evaluations++
		if limit != 3 {
			t.Fatalf("random limit = %d, want 3", limit)
		}
		return 1
	}
	failed := false
	store.writeAtomically = func(path string, data []byte) error {
		if filepath.Base(path) == "events.log" && !failed {
			failed = true
			return errors.New("injected failure after prepare publication")
		}
		return writeFileAtomically(path, data)
	}
	_, _, err := store.updateStateForIngestion("random-ingress", func(state *appState) error {
		applyGiftEvent(state, giftEvent{GiftID: 1, GiftName: "随机礼物", Num: 1, Timestamp: 1_700_000_000, Rnd: "random-rnd"})
		return nil
	})
	if err == nil {
		t.Fatal("expected injected failure")
	}
	if evaluations != 1 {
		t.Fatalf("evaluations = %d, want 1", evaluations)
	}

	formulaRandomIntn = func(int) int {
		evaluations++
		return 89
	}
	recovered := &configStore{path: filepath.Join(dir, "config.json")}
	state, err := recovered.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Attributes[0].Value != 37 {
		t.Fatalf("value = %v, want recovered result 37", state.Attributes[0].Value)
	}
	_, applied, err := recovered.updateStateForIngestion("random-ingress", func(state *appState) error {
		applyGiftEvent(state, giftEvent{GiftID: 1, GiftName: "随机礼物", Num: 1, Timestamp: 1_700_000_000, Rnd: "random-rnd"})
		return nil
	})
	if err != nil || applied {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	if evaluations != 1 {
		t.Fatalf("recovery reevaluated formula: evaluations = %d", evaluations)
	}
}

func TestTransactionPendingSnapshotTracksPrepareAndNonIngestionRecovery(t *testing.T) {
	dir := t.TempDir()
	store := &configStore{path: filepath.Join(dir, "config.json")}
	if err := store.replaceState(defaultAppState()); err != nil {
		t.Fatal(err)
	}
	failed := false
	store.writeAtomically = func(path string, data []byte) error {
		if filepath.Base(path) == "config.json" && !failed {
			failed = true
			return errors.New("injected post-prepare failure")
		}
		return writeFileAtomically(path, data)
	}
	if _, _, err := store.updateStateForIngestion("pending-snapshot", func(state *appState) error {
		state.RoomID = "31567150"
		return nil
	}); err == nil {
		t.Fatal("expected post-prepare failure")
	}
	if !store.TransactionPending() {
		t.Fatal("prepare did not publish pending transaction snapshot")
	}
	store.writeAtomically = nil
	if _, err := store.readState(); err != nil {
		t.Fatal(err)
	}
	if store.TransactionPending() {
		t.Fatal("ordinary state read recovery did not clear transaction snapshot")
	}
}

func TestTransactionPendingRemainsTrueWhenExistingEvidenceCannotBeRead(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	if err := os.WriteFile(store.stateTransactionPath(), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.readTransaction = func(string) ([]byte, error) { return nil, errors.New("injected access failure") }
	if _, err := store.readState(); err != nil {
		t.Fatalf("diagnostic read failed: %v", err)
	}
	if !store.TransactionPending() {
		t.Fatal("unreadable transaction evidence was incorrectly reported as cleared")
	}
	if store.MutationBlockKind() != "transaction_recovery" {
		t.Fatalf("mutation block kind = %q, want transaction_recovery", store.MutationBlockKind())
	}
}

func TestPendingStateTransactionValidatesEveryPayloadBeforeWritingTargets(t *testing.T) {
	for name, mutate := range map[string]func(*pendingStateTransaction){
		"malformed events": func(tx *pendingStateTransaction) { tx.EventLog = []byte("not-json\n") },
		"empty config":     func(tx *pendingStateTransaction) { tx.Config = []byte{} },
		"malformed cache":  func(tx *pendingStateTransaction) { tx.Cache = []byte("{") },
		"newer history": func(tx *pendingStateTransaction) {
			tx.History = []byte(`{"schemaVersion":999}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			store := &configStore{path: filepath.Join(dir, "config.json")}
			initial := defaultAppState()
			initial.Attributes = []attributeState{{Name: "积分", Value: 1}}
			initial.GiftCatalog = []giftInfo{{ID: 1, Name: "旧礼物"}}
			initial.Log = []logEntry{{EventID: "old-event", ValueAfter: 1}}
			if err := store.replaceState(initial); err != nil {
				t.Fatal(err)
			}
			before := snapshotStateFiles(t, store)

			next := initial
			next.Attributes = []attributeState{{Name: "积分", Value: 9}}
			next.GiftCatalog = []giftInfo{{ID: 9, Name: "新礼物"}}
			next.Log = []logEntry{{EventID: "new-event", ValueAfter: 9}}
			tx := preparedTransactionForTest(t, next)
			mutate(&tx)
			writePendingTransactionForTest(t, store, tx)

			if _, err := store.readState(); err != nil {
				t.Fatalf("diagnostic read failed: %v", err)
			}
			if store.MutationBlockKind() != "transaction_recovery" {
				t.Fatalf("mutation block kind = %q, want transaction_recovery", store.MutationBlockKind())
			}
			assertStateFilesEqual(t, store, before)
			if _, err := os.Stat(store.stateTransactionPath()); err != nil {
				t.Fatalf("prepare evidence missing: %v", err)
			}
		})
	}
}

func TestPendingStateTransactionRejectsNonObjectEmbeddedPayloadsBeforeWritingTargets(t *testing.T) {
	targets := map[string]func(*pendingStateTransaction, []byte){
		"events":  func(tx *pendingStateTransaction, data []byte) { tx.EventLog = append(data, '\n') },
		"config":  func(tx *pendingStateTransaction, data []byte) { tx.Config = data },
		"cache":   func(tx *pendingStateTransaction, data []byte) { tx.Cache = data },
		"history": func(tx *pendingStateTransaction, data []byte) { tx.History = data },
	}
	shapes := map[string][]byte{
		"null":   []byte("null"),
		"scalar": []byte("7"),
		"array":  []byte("[]"),
	}
	for targetName, setPayload := range targets {
		for shapeName, payload := range shapes {
			t.Run(targetName+"/"+shapeName, func(t *testing.T) {
				dir := t.TempDir()
				store := &configStore{path: filepath.Join(dir, "config.json")}
				initial := defaultAppState()
				initial.Attributes = []attributeState{{Name: "积分", Value: 1}}
				initial.GiftCatalog = []giftInfo{{ID: 1, Name: "旧礼物"}}
				initial.Log = []logEntry{{EventID: "old-event", ValueAfter: 1}}
				if err := store.replaceState(initial); err != nil {
					t.Fatal(err)
				}
				before := snapshotStateFiles(t, store)

				next := initial
				next.Attributes = []attributeState{{Name: "积分", Value: 9}}
				next.GiftCatalog = []giftInfo{{ID: 9, Name: "新礼物"}}
				next.Log = []logEntry{{EventID: "new-event", ValueAfter: 9}}
				tx := preparedTransactionForTest(t, next)
				setPayload(&tx, payload)
				writePendingTransactionForTest(t, store, tx)

				if _, err := store.readState(); err != nil {
					t.Fatalf("diagnostic read failed for %s %s payload: %v", targetName, shapeName, err)
				}
				if store.MutationBlockKind() != "transaction_recovery" {
					t.Fatalf("mutation block kind = %q, want transaction_recovery", store.MutationBlockKind())
				}
				assertStateFilesEqual(t, store, before)
				if _, err := os.Stat(store.stateTransactionPath()); err != nil {
					t.Fatalf("prepare evidence missing: %v", err)
				}
			})
		}
	}
}

func TestPendingStateTransactionRejectsNonCanonicalEmbeddedPayloadsBeforeWritingTargets(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*pendingStateTransaction)
	}{
		{name: "config/empty-object", mutate: func(tx *pendingStateTransaction) { tx.Config = []byte("{}") }},
		{name: "config/unknown-only", mutate: func(tx *pendingStateTransaction) { tx.Config = []byte(`{"unknown":true}`) }},
		{name: "config/schema-only", mutate: func(tx *pendingStateTransaction) { tx.Config = []byte(`{"schemaVersion":12}`) }},
		{name: "config/unknown-field", mutate: func(tx *pendingStateTransaction) { tx.Config = addUnknownJSONFieldForTest(t, tx.Config) }},
		{name: "config/noncanonical-format", mutate: func(tx *pendingStateTransaction) { tx.Config = compactJSONForTest(t, tx.Config) }},
		{name: "config/older-schema", mutate: func(tx *pendingStateTransaction) {
			tx.Config = bytes.Replace(tx.Config, []byte(`"schemaVersion": 12`), []byte(`"schemaVersion": 11`), 1)
		}},
		{name: "cache/empty-object", mutate: func(tx *pendingStateTransaction) { tx.Cache = []byte("{}") }},
		{name: "cache/unknown-only", mutate: func(tx *pendingStateTransaction) { tx.Cache = []byte(`{"unknown":true}`) }},
		{name: "cache/schema-only", mutate: func(tx *pendingStateTransaction) { tx.Cache = []byte(`{"schemaVersion":12}`) }},
		{name: "cache/unknown-field", mutate: func(tx *pendingStateTransaction) { tx.Cache = addUnknownJSONFieldForTest(t, tx.Cache) }},
		{name: "history/empty-object", mutate: func(tx *pendingStateTransaction) { tx.History = []byte("{}") }},
		{name: "history/unknown-only", mutate: func(tx *pendingStateTransaction) { tx.History = []byte(`{"unknown":true}`) }},
		{name: "history/schema-only", mutate: func(tx *pendingStateTransaction) { tx.History = []byte(`{"schemaVersion":12}`) }},
		{name: "history/unknown-field", mutate: func(tx *pendingStateTransaction) { tx.History = addUnknownJSONFieldForTest(t, tx.History) }},
		{name: "events/empty-object", mutate: func(tx *pendingStateTransaction) { tx.EventLog = []byte("{}\n") }},
		{name: "events/unknown-only", mutate: func(tx *pendingStateTransaction) { tx.EventLog = []byte("{\"unknown\":true}\n") }},
		{name: "events/whitespace-only", mutate: func(tx *pendingStateTransaction) { tx.EventLog = []byte(" \t\r\n") }},
		{name: "events/blank-line", mutate: func(tx *pendingStateTransaction) { tx.EventLog = append(tx.EventLog, '\n') }},
		{name: "events/unknown-field", mutate: func(tx *pendingStateTransaction) { tx.EventLog = addUnknownJSONFieldForTest(t, tx.EventLog) }},
		{name: "events/missing-final-newline", mutate: func(tx *pendingStateTransaction) { tx.EventLog = bytes.TrimSuffix(tx.EventLog, []byte("\n")) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			store := &configStore{path: filepath.Join(dir, "config.json")}
			initial := defaultAppState()
			initial.Attributes = []attributeState{{Name: "积分", Value: 1}}
			initial.GiftCatalog = []giftInfo{{ID: 1, Name: "旧礼物"}}
			initial.Log = []logEntry{{EventID: "old-event", ValueAfter: 1}}
			if err := store.replaceState(initial); err != nil {
				t.Fatal(err)
			}
			before := snapshotStateFiles(t, store)

			next := initial
			next.Attributes = []attributeState{{Name: "积分", Value: 9}}
			next.GiftCatalog = []giftInfo{{ID: 9, Name: "新礼物"}}
			next.Log = []logEntry{{EventID: "new-event", ValueAfter: 9}}
			valid := preparedTransactionForTest(t, next)
			tx := valid
			test.mutate(&tx)
			writePendingTransactionForTest(t, store, tx)

			if _, err := store.readState(); err != nil {
				t.Fatalf("diagnostic read failed: %v", err)
			}
			if store.MutationBlockKind() != "transaction_recovery" {
				t.Fatalf("mutation block kind = %q, want transaction_recovery", store.MutationBlockKind())
			}
			assertStateFilesEqual(t, store, before)
			if _, err := os.Stat(store.stateTransactionPath()); err != nil {
				t.Fatalf("prepare evidence missing: %v", err)
			}
		})
	}
}

func TestPendingStateTransactionRecoversProductionGeneratedPrepare(t *testing.T) {
	dir := t.TempDir()
	store := &configStore{path: filepath.Join(dir, "config.json")}
	initial := defaultAppState()
	if err := store.replaceState(initial); err != nil {
		t.Fatal(err)
	}

	next := defaultAppState()
	next.Attributes = []attributeState{{Name: "积分", Value: 23}}
	next.GiftCatalog = []giftInfo{{ID: 23, Name: "恢复礼物"}}
	next.Log = []logEntry{
		{EventID: "newer-event", GiftName: "新记录", ValueAfter: 23},
		{EventID: "older-event", GiftName: "旧记录", ValueAfter: 11},
	}
	tx := preparedTransactionForTest(t, next)
	writePendingTransactionForTest(t, store, tx)

	recovered, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.Attributes) != 1 || recovered.Attributes[0].Value != 23 {
		t.Fatalf("recovered attributes = %#v", recovered.Attributes)
	}
	if len(recovered.GiftCatalog) != 1 || recovered.GiftCatalog[0].ID != 23 {
		t.Fatalf("recovered cache = %#v", recovered.GiftCatalog)
	}
	if len(recovered.Log) != 2 || recovered.Log[0].EventID != "newer-event" || recovered.Log[1].EventID != "older-event" {
		t.Fatalf("recovered event log = %#v", recovered.Log)
	}
	for path, want := range map[string][]byte{
		store.eventLogPath(): tx.EventLog,
		store.historyPath():  tx.History,
		store.cachePath():    tx.Cache,
		store.path:           tx.Config,
	} {
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("recovered target %s does not match prepared bytes", filepath.Base(path))
		}
	}
	if _, err := os.Stat(store.stateTransactionPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed prepare remains: %v", err)
	}
}

func TestUpdateStateForIngestionRejectsBlankIDBeforeCallback(t *testing.T) {
	for _, ingestionID := range []string{"", " \t\r\n"} {
		store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
		called := false
		if _, applied, err := store.updateStateForIngestion(ingestionID, func(*appState) error {
			called = true
			return nil
		}); err == nil || applied {
			t.Fatalf("id=%q applied=%v err=%v", ingestionID, applied, err)
		}
		if called {
			t.Fatalf("id=%q entered callback", ingestionID)
		}
	}
}

func TestNormalizeRecentSourceGiftKeysPreservesRawKeysAndStrictMinuteWindow(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	state := defaultAppState()
	state.RecentSourceGiftKeys = map[string]int64{
		" raw ": now.Add(-time.Minute + time.Millisecond).UnixMilli(),
		"raw":   now.Add(-time.Minute).UnixMilli(),
	}
	normalizeInternalIngestionLedgers(&state, now)
	if _, exists := state.RecentSourceGiftKeys[" raw "]; !exists {
		t.Fatalf("raw rnd key changed: %#v", state.RecentSourceGiftKeys)
	}
	if _, exists := state.RecentSourceGiftKeys["raw"]; exists {
		t.Fatalf("exactly one-minute-old key retained: %#v", state.RecentSourceGiftKeys)
	}
}

func preparedTransactionForTest(t *testing.T, state appState) pendingStateTransaction {
	t.Helper()
	eventLog, err := serializeEventLog(state.Log)
	if err != nil {
		t.Fatal(err)
	}
	history, err := serializeStateShard(historyShardFromState(state))
	if err != nil {
		t.Fatal(err)
	}
	cache, err := serializeStateShard(cacheShardFromState(state))
	if err != nil {
		t.Fatal(err)
	}
	config, err := serializeStateShard(configShardFromState(state))
	if err != nil {
		t.Fatal(err)
	}
	return pendingStateTransaction{SchemaVersion: stateTransactionSchemaVersion, EventLog: eventLog, History: history, Cache: cache, Config: config}
}

func compactJSONForTest(t *testing.T, data []byte) []byte {
	t.Helper()
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		t.Fatal(err)
	}
	return compact.Bytes()
}

func addUnknownJSONFieldForTest(t *testing.T, data []byte) []byte {
	t.Helper()
	closingBrace := bytes.LastIndexByte(data, '}')
	if closingBrace < 0 {
		t.Fatalf("test payload has no closing object brace: %q", data)
	}
	result := append([]byte(nil), data[:closingBrace]...)
	result = append(result, []byte(`,"unknown":true`)...)
	result = append(result, data[closingBrace:]...)
	return result
}

func writePendingTransactionForTest(t *testing.T, store *configStore, tx pendingStateTransaction) {
	t.Helper()
	data, err := json.Marshal(tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomically(store.stateTransactionPath(), data); err != nil {
		t.Fatal(err)
	}
}

func snapshotStateFiles(t *testing.T, store *configStore) map[string][]byte {
	t.Helper()
	result := make(map[string][]byte)
	for _, path := range store.statePaths() {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		result[path] = data
	}
	return result
}

func assertStateFilesEqual(t *testing.T, store *configStore, want map[string][]byte) {
	t.Helper()
	for _, path := range store.statePaths() {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want[path]) {
			t.Fatalf("target %s changed before validation completed", filepath.Base(path))
		}
	}
}

func TestPendingStateTransactionKeepsUnreadableEvidence(t *testing.T) {
	for name, contents := range map[string]string{
		"malformed": "{",
		"newer":     `{"schemaVersion":2}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "state-transaction.json")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			store := &configStore{path: filepath.Join(dir, "config.json")}
			if _, err := store.readState(); err != nil {
				t.Fatalf("diagnostic read failed: %v", err)
			}
			if store.MutationBlockKind() != "transaction_recovery" {
				t.Fatalf("mutation block kind = %q, want transaction_recovery", store.MutationBlockKind())
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != contents {
				t.Fatalf("transaction evidence changed: data=%q err=%v", data, err)
			}
		})
	}
}
