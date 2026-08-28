package configuration

import (
	"context"
	"errors"
	"time"
)

// SaveDefinitionCommand replaces the immutable definition at an expected
// version. The account ID is never client input.
type SaveDefinitionCommand struct {
	ExpectedVersion uint64     `json:"expectedVersion"`
	Definition      Definition `json:"definition"`
}

// SaveStateCommand replaces the mutable runtime at an expected revision. The
// account ID is supplied separately by authenticated middleware.
type SaveStateCommand struct {
	ExpectedRevision uint64       `json:"expectedRevision"`
	Runtime          RuntimeState `json:"runtime"`
}

// RoomSuggestionCommand proposes a room for later confirmation only.
type RoomSuggestionCommand struct {
	RoomID string `json:"roomId"`
}

// Service authorizes configuration commands against a trusted account ID and
// keeps normalization and durable storage behind the repository seam.
type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, now: now}
}

func (service *Service) Load(ctx context.Context, accountID int64) (Version, State, error) {
	if !service.ready() || accountID <= 0 {
		return Version{}, State{}, ErrInvalidInput
	}
	version, state, err := service.repository.LoadActive(ctx, accountID)
	if err != nil {
		return Version{}, State{}, err
	}
	if !ownedActive(accountID, version, state) {
		return Version{}, State{}, ErrUnavailable
	}
	return version, state, nil
}

func (service *Service) SaveDefinition(ctx context.Context, accountID int64, command SaveDefinitionCommand) (Version, State, error) {
	if !service.ready() || accountID <= 0 {
		return Version{}, State{}, ErrInvalidInput
	}
	var runtime RuntimeState
	var expectedRevision uint64
	current, state, err := service.repository.LoadActive(ctx, accountID)
	switch {
	case err == nil:
		if !ownedActive(accountID, current, state) {
			return Version{}, State{}, ErrUnavailable
		}
		if current.Number != command.ExpectedVersion {
			return Version{}, State{}, ErrRevisionConflict
		}
		runtime, expectedRevision = state.Runtime, state.Revision
	case errors.Is(err, ErrNotFound):
		if command.ExpectedVersion != 0 {
			return Version{}, State{}, ErrRevisionConflict
		}
		runtime, expectedRevision = DefaultRuntime(command.Definition), 0
	default:
		return Version{}, State{}, err
	}
	// Normalize canonicalizes gameplay and the definition-only migration
	// metadata without aliasing caller-owned values.
	candidateDefinition := command.Definition
	// Manual edits create a new provenance; only the migration activation path
	// may persist a migration hash.
	candidateDefinition.MigrationHash = ""
	definition, runtime, err := Normalize(candidateDefinition, runtime)
	if err != nil {
		return Version{}, State{}, ErrInvalidInput
	}
	version, next, err := service.repository.Activate(ctx, ActivationCommand{
		AccountID: accountID, ExpectedVersion: command.ExpectedVersion, ExpectedRevision: expectedRevision,
		Definition: definition, Runtime: runtime, Source: "manual", At: service.now().UTC(),
	})
	if err != nil {
		return Version{}, State{}, err
	}
	if !ownedActive(accountID, version, next) {
		return Version{}, State{}, ErrUnavailable
	}
	return version, next, nil
}

func (service *Service) SaveState(ctx context.Context, accountID int64, command SaveStateCommand) (State, error) {
	if !service.ready() || accountID <= 0 || command.ExpectedRevision == 0 {
		return State{}, ErrInvalidInput
	}
	version, current, err := service.Load(ctx, accountID)
	if err != nil {
		return State{}, err
	}
	if current.Revision != command.ExpectedRevision {
		return State{}, ErrRevisionConflict
	}
	_, runtime, err := Normalize(version.Definition, command.Runtime)
	if err != nil {
		return State{}, ErrInvalidInput
	}
	next, err := service.repository.CompareAndSwapState(ctx, UpdateStateCommand{
		AccountID: accountID, ExpectedRevision: command.ExpectedRevision, Runtime: runtime, UpdatedAt: service.now().UTC(),
	})
	if err != nil {
		return State{}, err
	}
	if next.AccountID != accountID || next.ConfigVersionID != current.ConfigVersionID || next.Revision != command.ExpectedRevision+1 {
		return State{}, ErrUnavailable
	}
	return next, nil
}

func (service *Service) SuggestRoom(ctx context.Context, accountID int64, command RoomSuggestionCommand) error {
	if !service.ready() || accountID <= 0 || !validRoomID(command.RoomID) {
		return ErrInvalidInput
	}
	return service.repository.UpsertRoomSuggestion(ctx, RoomSuggestion{AccountID: accountID, RoomID: command.RoomID, SuggestedAt: service.now().UTC()})
}

func (service *Service) ready() bool {
	return service != nil && service.repository != nil && service.now != nil
}

func ownedActive(accountID int64, version Version, state State) bool {
	return version.ID > 0 && version.AccountID == accountID && version.Number > 0 && state.AccountID == accountID && state.ConfigVersionID == version.ID && state.Revision > 0
}
