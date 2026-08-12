package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPendingStateTransactionRecoversEveryShardWithoutReapplyingIngress(t *testing.T) {
	for _, failBase := range []string{"events.log", "history.json", "cache.json", "config.json"} {
		t.Run(failBase, func(t *testing.T) {
			dir := t.TempDir()
			store := &configStore{path: filepath.Join(dir, "config.json")}
			initial := defaultAppState()
			initial.Attributes = []attributeState{{Name: "积分", Value: 0}}
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

func TestPendingStateTransactionRecoversChosenRandomResultWithoutReevaluation(t *testing.T) {
	dir := t.TempDir()
	store := &configStore{path: filepath.Join(dir, "config.json")}
	initial := defaultAppState()
	initial.Attributes = []attributeState{{Name: "积分", Value: 0}}
	if err := store.replaceState(initial); err != nil {
		t.Fatal(err)
	}

	evaluations := 0
	evaluate := func() float64 {
		evaluations++
		return 37
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
		state.Attributes[0].Value = evaluate()
		return nil
	})
	if err == nil {
		t.Fatal("expected injected failure")
	}
	if evaluations != 1 {
		t.Fatalf("evaluations = %d, want 1", evaluations)
	}

	evaluate = func() float64 {
		evaluations++
		return 99
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
		state.Attributes[0].Value = evaluate()
		return nil
	})
	if err != nil || applied {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	if evaluations != 1 {
		t.Fatalf("recovery reevaluated formula: evaluations = %d", evaluations)
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
