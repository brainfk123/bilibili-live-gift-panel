package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
	initial.Rules = []giftRule{{ID: "random-rule", GiftID: 1, AttributeName: "积分", Formula: "RANDBETWEEN(10, 60)"}}
	if err := store.replaceState(initial); err != nil {
		t.Fatal(err)
	}

	originalRandomIntn := formulaRandomIntn
	t.Cleanup(func() { formulaRandomIntn = originalRandomIntn })
	evaluations := 0
	formulaRandomIntn = func(limit int) int {
		evaluations++
		if limit != 51 {
			t.Fatalf("random limit = %d, want 51", limit)
		}
		return 27
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
	if _, err := store.readState(); err == nil {
		t.Fatal("expected injected transaction read failure")
	}
	if !store.TransactionPending() {
		t.Fatal("unreadable transaction evidence was incorrectly reported as cleared")
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

			if _, err := store.readState(); err == nil {
				t.Fatal("expected embedded payload validation error")
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

				if _, err := store.readState(); err == nil {
					t.Fatalf("accepted %s %s payload", targetName, shapeName)
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

			if _, err := store.readState(); err == nil {
				t.Fatal("accepted noncanonical embedded payload")
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
			if _, err := store.readState(); err == nil {
				t.Fatal("expected transaction recovery error")
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != contents {
				t.Fatalf("transaction evidence changed: data=%q err=%v", data, err)
			}
		})
	}
}
