package configuration

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestServiceSaveDefinitionUsesCurrentStateRevision(t *testing.T) {
	definition, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	repository := &serviceRepository{version: Version{ID: 31, AccountID: 7, Number: 4, Definition: definition}, state: State{AccountID: 7, ConfigVersionID: 31, Revision: 9, Runtime: runtime}}
	service := NewService(repository, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() })

	version, state, err := service.SaveDefinition(context.Background(), 7, SaveDefinitionCommand{ExpectedVersion: 4, Definition: definition})
	if err != nil {
		t.Fatalf("SaveDefinition() error = %v", err)
	}
	if version.AccountID != 7 || state.AccountID != 7 || repository.activation.AccountID != 7 || repository.activation.ExpectedVersion != 4 || repository.activation.ExpectedRevision != 9 {
		t.Fatalf("activation = %#v, result = (%#v, %#v)", repository.activation, version, state)
	}
}

func TestServiceRejectsStaleDefinitionBeforeWrite(t *testing.T) {
	definition, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	repository := &serviceRepository{version: Version{ID: 31, AccountID: 7, Number: 4, Definition: definition}, state: State{AccountID: 7, ConfigVersionID: 31, Revision: 9, Runtime: runtime}}
	service := NewService(repository, time.Now)

	_, _, err = service.SaveDefinition(context.Background(), 7, SaveDefinitionCommand{ExpectedVersion: 3, Definition: definition})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("SaveDefinition() error = %v, want ErrRevisionConflict", err)
	}
	if repository.activateCalls != 0 {
		t.Fatalf("Activate() calls = %d, want no write", repository.activateCalls)
	}
}

func TestServiceSaveStateRejectsUnrelatedAccountInjection(t *testing.T) {
	definition, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	repository := &serviceRepository{version: Version{ID: 31, AccountID: 7, Number: 4, Definition: definition}, state: State{AccountID: 7, ConfigVersionID: 31, Revision: 9, Runtime: runtime}, swapResult: State{AccountID: 99, ConfigVersionID: 31, Revision: 10, Runtime: runtime}}
	service := NewService(repository, time.Now)

	_, err = service.SaveState(context.Background(), 7, SaveStateCommand{ExpectedRevision: 9, Runtime: runtime})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("SaveState() error = %v, want ErrUnavailable", err)
	}
	if repository.swapCalls != 1 || repository.swap.AccountID != 7 {
		t.Fatalf("CAS command = %#v, calls = %d", repository.swap, repository.swapCalls)
	}
}

func TestServiceSaveStateCanonicalizesRuntimeBeforeCASWithoutAliasingInput(t *testing.T) {
	snapshot := fixtureSnapshot()
	secondActivity := snapshot.Activities[0]
	secondActivity.ID, secondActivity.Name = "round-2", "Round two"
	secondActivity.Milestones[0].ID = "health-cap-2"
	snapshot.Activities = append(snapshot.Activities, secondActivity)
	snapshot.Gifts = append(snapshot.Gifts, snapshot.Gifts[0])
	snapshot.Gifts[1].ID, snapshot.Gifts[1].Name = 2, "Tulip"
	snapshot.GiftTargetPanels[0].Items = append(snapshot.GiftTargetPanels[0].Items, snapshot.GiftTargetPanels[0].Items[0])
	snapshot.GiftTargetPanels[0].Items[1].GiftID, snapshot.GiftTargetPanels[0].Items[1].Name = 2, "Tulip"
	definition, runtime, err := Split(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	untrusted := cloneRuntimeForTest(t, runtime)
	untrusted.Activities[0], untrusted.Activities[1] = untrusted.Activities[1], untrusted.Activities[0]
	untrusted.GiftTargetReceived[0], untrusted.GiftTargetReceived[1] = untrusted.GiftTargetReceived[1], untrusted.GiftTargetReceived[0]
	repository := &serviceRepository{
		version:    Version{ID: 31, AccountID: 7, Number: 4, Definition: definition},
		state:      State{AccountID: 7, ConfigVersionID: 31, Revision: 9, Runtime: runtime},
		swapResult: State{AccountID: 7, ConfigVersionID: 31, Revision: 10, Runtime: runtime},
	}

	if _, err := NewService(repository, time.Now).SaveState(context.Background(), 7, SaveStateCommand{ExpectedRevision: 9, Runtime: untrusted}); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}
	if !reflect.DeepEqual(repository.swap.Runtime, runtime) {
		t.Fatalf("CAS runtime = %#v, want canonical %#v", repository.swap.Runtime, runtime)
	}
	untrusted.AttributeValues["health"] = -99
	untrusted.Activities[0].Status = "mutated"
	if repository.swap.Runtime.AttributeValues["health"] == -99 || repository.swap.Runtime.Activities[0].Status == "mutated" {
		t.Fatalf("CAS runtime aliases caller input: %#v", repository.swap.Runtime)
	}
}

func cloneRuntimeForTest(t *testing.T, runtime RuntimeState) RuntimeState {
	t.Helper()
	encoded, err := json.Marshal(runtime)
	if err != nil {
		t.Fatal(err)
	}
	var clone RuntimeState
	if err := json.Unmarshal(encoded, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func TestServiceSuggestRoomOnlyUpsertsSuggestion(t *testing.T) {
	repository := &serviceRepository{}
	service := NewService(repository, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() })

	if err := service.SuggestRoom(context.Background(), 7, RoomSuggestionCommand{RoomID: "12345"}); err != nil {
		t.Fatalf("SuggestRoom() error = %v", err)
	}
	if repository.suggestion.AccountID != 7 || repository.suggestion.RoomID != "12345" || repository.activateCalls != 0 || repository.swapCalls != 0 {
		t.Fatalf("suggestion/write side effects = %#v activate=%d cas=%d", repository.suggestion, repository.activateCalls, repository.swapCalls)
	}
}

func TestServiceLoadRejectsMissingOrForeignRecord(t *testing.T) {
	definition, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	for name, repository := range map[string]*serviceRepository{
		"missing": {loadErr: ErrNotFound},
		"foreign": {version: Version{ID: 31, AccountID: 99, Number: 1, Definition: definition}, state: State{AccountID: 99, ConfigVersionID: 31, Revision: 1, Runtime: runtime}},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := NewService(repository, time.Now).Load(context.Background(), 7)
			if err == nil || errors.Is(err, ErrNotFound) && name == "foreign" {
				t.Fatalf("Load() error = %v", err)
			}
		})
	}
}

type serviceRepository struct {
	version       Version
	state         State
	loadErr       error
	activation    ActivationCommand
	activateCalls int
	swap          UpdateStateCommand
	swapCalls     int
	swapResult    State
	suggestion    RoomSuggestion
}

func (repository *serviceRepository) LoadActive(context.Context, int64) (Version, State, error) {
	return repository.version, repository.state, repository.loadErr
}

func (repository *serviceRepository) Activate(_ context.Context, command ActivationCommand) (Version, State, error) {
	repository.activation = command
	repository.activateCalls++
	return Version{ID: 32, AccountID: command.AccountID, Number: command.ExpectedVersion + 1, Definition: command.Definition, Source: command.Source, CreatedAt: command.At}, State{AccountID: command.AccountID, ConfigVersionID: 32, Revision: command.ExpectedRevision + 1, Runtime: command.Runtime, UpdatedAt: command.At}, nil
}

func (repository *serviceRepository) CompareAndSwapState(_ context.Context, command UpdateStateCommand) (State, error) {
	repository.swap = command
	repository.swapCalls++
	return repository.swapResult, nil
}

func (repository *serviceRepository) UpsertRoomSuggestion(_ context.Context, suggestion RoomSuggestion) error {
	repository.suggestion = suggestion
	return nil
}
