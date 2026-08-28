package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/gameplay"
	"bilibili-live-gift-panel/internal/hosted/configuration"
	"bilibili-live-gift-panel/internal/hosted/obsselector"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestPreviewRecanonicalizesAndDoesNotExposeConfiguration(t *testing.T) {
	now := time.Date(2026, 8, 16, 9, 30, 0, 0, time.UTC)
	envelope := decodedEnvelope(t)
	envelope.Hash = [32]byte{}
	repository := &recordingPreviewRepository{result: storedPreview{ID: 8, AccountID: 7, ExpiresAt: now.Add(24 * time.Hour)}}
	service := NewService(repository, func() time.Time { return now })

	preview, err := service.Preview(context.Background(), 7, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if preview.ID != 8 || !preview.ExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	if repository.command.AccountID != 7 || repository.command.Hash == ([32]byte{}) {
		t.Fatalf("preview did not use a fresh account-owned hash: %#v", repository.command)
	}
	if repository.command.Definition.Attributes[0].ID != "health" || repository.command.Runtime.AttributeValues["health"] != 1 {
		t.Fatal("normalized configuration was not sent to persistence seam")
	}
}

func TestPreviewRejectsRepositoryOwnershipViolation(t *testing.T) {
	now := time.Date(2026, 8, 16, 9, 30, 0, 0, time.UTC)
	service := NewService(&recordingPreviewRepository{result: storedPreview{ID: 8, AccountID: 9, ExpiresAt: now.Add(24 * time.Hour)}}, func() time.Time { return now })
	if _, err := service.Preview(context.Background(), 7, decodedEnvelope(t)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v, want unavailable", err)
	}
}

func TestServiceApplyUsesOnlyTheAuthenticatedOwnerAndKeepsSuggestionExplicit(t *testing.T) {
	importedDefinition, importedRuntime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "EXE"}}, map[string]float64{"exe": 9})
	repository := &recordingLifecycleRepository{
		recordingPreviewRepository: recordingPreviewRepository{}, applyResult: storedJob{ID: 19, AccountID: 7, Status: jobApplied},
		composition: storedComposition{ID: 19, AccountID: 7, Status: jobPreviewed, ExpiresAt: time.Now().Add(time.Hour), Imported: migrationEnvelope(importedDefinition, importedRuntime), HostedDefinition: emptyDefinition(), HostedRuntime: emptyRuntime()},
	}
	service := NewService(repository, time.Now)

	selection := SelectionCommand{UnitIDs: []string{"attribute:exe"}, IncludeRoomSuggestion: true}
	result, err := service.Apply(context.Background(), 7, 19, selection)
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != 19 || result.Status != "applied" {
		t.Fatalf("Apply() = %#v", result)
	}
	if repository.apply.AccountID != 7 || repository.apply.JobID != 19 || !repository.apply.KeepRoomSuggestion || repository.apply.Candidate.Runtime.AttributeValues["exe"] != 9 {
		t.Fatalf("Apply() command = %#v", repository.apply)
	}
	if _, err := service.Apply(context.Background(), 0, 19, selection); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Apply() with invalid owner error = %v", err)
	}
}

func TestHostedMigrationCapabilitiesRejectTimerRuleUnitsUntilSchedulerExists(t *testing.T) {
	unit := GameplayUnit{ID: "attribute:clock", Kind: "attribute", TimerRuleIDs: []string{"tick"}}

	compatibility := AssessCompatibility(unit, hostedMigrationCapabilities())

	if compatibility.Status != CompatibilityIncompatible || !reflect.DeepEqual(compatibility.ReasonCodes, []string{"timer_rules_unsupported"}) {
		t.Fatalf("timer compatibility = %#v, want timer_rules_unsupported", compatibility)
	}
}

func TestServiceLiveMigrationStagesFrozenCandidateThenUsesBarrier(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	importedDefinition, importedRuntime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "EXE"}}, map[string]float64{"exe": 9})
	repository := &recordingLifecycleRepository{
		composition:             storedComposition{ID: 19, AccountID: 7, Status: jobPreviewed, ExpiresAt: now.Add(time.Hour), Imported: migrationEnvelope(importedDefinition, importedRuntime), HostedDefinition: emptyDefinition(), HostedRuntime: emptyRuntime()},
		preauthorizeApplyResult: storedJob{ID: 19, AccountID: 7, Status: jobPreviewed},
		stageResult:             storedJob{ID: 19, AccountID: 7, Status: jobPending}, getResult: storedJob{ID: 19, AccountID: 7, Status: jobApplied, RollbackExpiresAt: now.Add(7 * 24 * time.Hour)},
	}
	barrier := &recordingConfigurationBarrier{boundary: configuration.Boundary{AccountID: 7, MigrationJobID: 19, Operation: configuration.BarrierMigrationApply, OldConfigVersionID: 10, NewConfigVersionID: 11, LastOldRevision: 4, FirstNewRevision: 5, AppliedAt: now}}
	service := NewService(repository, func() time.Time { return now })
	if err := service.SetConfigurationBarrier(barrier); err != nil {
		t.Fatal(err)
	}

	job, err := service.Apply(context.Background(), 7, 19, SelectionCommand{UnitIDs: []string{"attribute:exe"}, IncludeRoomSuggestion: true})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if job.Status != jobApplied || repository.stageCalls != 1 || barrier.calls != 1 {
		t.Fatalf("job=%#v stageCalls=%d barrierCalls=%d", job, repository.stageCalls, barrier.calls)
	}
	if repository.stage.AccountID != 7 || repository.stage.JobID != 19 || !repository.stage.KeepRoomSuggestion {
		t.Fatalf("stage command = %#v", repository.stage)
	}
	if barrier.candidate.Definition.MigrationHash != "" || barrier.candidate.IntegritySeal == ([sha256.Size]byte{}) || barrier.candidate.Runtime.AttributeValues["exe"] != 9 || barrier.candidate.Operation != configuration.BarrierMigrationApply {
		t.Fatalf("barrier candidate = %#v", barrier.candidate)
	}
}

func TestServiceStageApplyAlreadyAppliedStillReconcilesThroughBarrier(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 2, 0, 0, time.UTC)
	importedDefinition, importedRuntime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "EXE"}}, map[string]float64{"exe": 9})
	repository := &recordingLifecycleRepository{
		composition:             storedComposition{ID: 19, AccountID: 7, Status: jobPreviewed, ExpiresAt: now.Add(time.Hour), Imported: migrationEnvelope(importedDefinition, importedRuntime), HostedDefinition: emptyDefinition(), HostedRuntime: emptyRuntime()},
		preauthorizeApplyResult: storedJob{ID: 19, AccountID: 7, Status: jobPreviewed},
		stageResult:             storedJob{ID: 19, AccountID: 7, Status: jobApplied},
		getResult:               storedJob{ID: 19, AccountID: 7, Status: jobApplied, RollbackExpiresAt: now.Add(7 * 24 * time.Hour)},
	}
	barrier := &recordingConfigurationBarrier{boundary: configuration.Boundary{AccountID: 7, MigrationJobID: 19, Operation: configuration.BarrierMigrationApply, OldConfigVersionID: 10, NewConfigVersionID: 11, LastOldRevision: 4, FirstNewRevision: 5, AppliedAt: now}}
	service := NewService(repository, func() time.Time { return now })
	if err := service.SetConfigurationBarrier(barrier); err != nil {
		t.Fatal(err)
	}

	job, err := service.Apply(context.Background(), 7, 19, SelectionCommand{UnitIDs: []string{"attribute:exe"}})
	if err != nil {
		t.Fatalf("Apply(already staged) error = %v", err)
	}
	if job.Status != jobApplied || barrier.calls != 1 {
		t.Fatalf("Apply(already staged) job=%#v barrierCalls=%d", job, barrier.calls)
	}
}

func TestServicePreauthorizeApplyResumesPersistedPendingBarrier(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 5, 0, 0, time.UTC)
	definition, runtime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "EXE"}}, map[string]float64{"exe": 9})
	sealed := sealedCandidateForTest(t, definition, runtime)
	repository := &recordingLifecycleRepository{
		composition:             storedComposition{ID: 19, AccountID: 7, Status: jobPending, ExpiresAt: now.Add(time.Hour), Imported: migrationEnvelope(sealed.Definition, sealed.Runtime), KeepRoomSuggestion: true},
		preauthorizeApplyResult: storedJob{ID: 19, AccountID: 7, Status: jobPending}, getResult: storedJob{ID: 19, AccountID: 7, Status: jobApplied, RollbackExpiresAt: now.Add(7 * 24 * time.Hour)},
	}
	barrier := &recordingConfigurationBarrier{boundary: configuration.Boundary{AccountID: 7, MigrationJobID: 19, Operation: configuration.BarrierMigrationApply, OldConfigVersionID: 10, NewConfigVersionID: 11, LastOldRevision: 4, FirstNewRevision: 5, AppliedAt: now}}
	service := NewService(repository, func() time.Time { return now })
	if err := service.SetConfigurationBarrier(barrier); err != nil {
		t.Fatal(err)
	}

	job, err := service.PreauthorizeApply(context.Background(), 7, 19)
	if err != nil {
		t.Fatalf("PreauthorizeApply() error = %v", err)
	}
	if job.Status != jobApplied || barrier.calls != 1 || barrier.candidate.IntegritySeal != sealed.Hash {
		t.Fatalf("resume job=%#v barrier=%#v", job, barrier)
	}
}

func TestServicePreauthorizeApplyReconcilesAnAlreadyAppliedBarrier(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 6, 0, 0, time.UTC)
	definition, runtime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "EXE"}}, map[string]float64{"exe": 9})
	sealed := sealedCandidateForTest(t, definition, runtime)
	repository := &recordingLifecycleRepository{
		composition:             storedComposition{ID: 19, AccountID: 7, Status: jobApplied, ExpiresAt: now.Add(time.Hour), Imported: migrationEnvelope(sealed.Definition, sealed.Runtime)},
		preauthorizeApplyResult: storedJob{ID: 19, AccountID: 7, Status: jobApplied},
		getResult:               storedJob{ID: 19, AccountID: 7, Status: jobApplied, RollbackExpiresAt: now.Add(7 * 24 * time.Hour)},
	}
	barrier := &recordingConfigurationBarrier{boundary: configuration.Boundary{AccountID: 7, MigrationJobID: 19, Operation: configuration.BarrierMigrationApply, OldConfigVersionID: 10, NewConfigVersionID: 11, LastOldRevision: 4, FirstNewRevision: 5, AppliedAt: now}}
	service := NewService(repository, func() time.Time { return now })
	if err := service.SetConfigurationBarrier(barrier); err != nil {
		t.Fatal(err)
	}

	job, err := service.PreauthorizeApply(context.Background(), 7, 19)
	if err != nil {
		t.Fatalf("PreauthorizeApply(applied) error = %v", err)
	}
	if job.Status != jobApplied || barrier.calls != 1 || barrier.candidate.IntegritySeal != sealed.Hash {
		t.Fatalf("applied reconciliation job=%#v barrier=%#v", job, barrier)
	}
}

func TestServiceApplyResumesPendingBeforeReinterpretingConcurrentSelection(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 7, 0, 0, time.UTC)
	definition, runtime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "EXE"}}, map[string]float64{"exe": 9})
	sealed := sealedCandidateForTest(t, definition, runtime)
	repository := &recordingLifecycleRepository{
		composition:             storedComposition{ID: 19, AccountID: 7, Status: jobPending, ExpiresAt: now.Add(time.Hour), Imported: migrationEnvelope(sealed.Definition, sealed.Runtime)},
		preauthorizeApplyResult: storedJob{ID: 19, AccountID: 7, Status: jobPending},
		getResult:               storedJob{ID: 19, AccountID: 7, Status: jobApplied, RollbackExpiresAt: now.Add(7 * 24 * time.Hour)},
	}
	barrier := &recordingConfigurationBarrier{boundary: configuration.Boundary{AccountID: 7, MigrationJobID: 19, Operation: configuration.BarrierMigrationApply, OldConfigVersionID: 10, NewConfigVersionID: 11, LastOldRevision: 4, FirstNewRevision: 5, AppliedAt: now}}
	service := NewService(repository, func() time.Time { return now })
	if err := service.SetConfigurationBarrier(barrier); err != nil {
		t.Fatal(err)
	}

	job, err := service.Apply(context.Background(), 7, 19, SelectionCommand{UnitIDs: []string{"stale-client-selection"}})
	if err != nil {
		t.Fatalf("Apply(pending) error = %v", err)
	}
	if job.Status != jobApplied || barrier.calls != 1 || repository.stageCalls != 0 {
		t.Fatalf("pending apply job=%#v barrierCalls=%d stageCalls=%d", job, barrier.calls, repository.stageCalls)
	}
}

func TestServiceRollbackUsesTheSameLiveBarrierAndCreatesANewVersion(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 10, 0, 0, time.UTC)
	rollbackDefinition, rollbackRuntime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "hosted", Name: "Hosted"}}, map[string]float64{"hosted": 3})
	repository := &recordingLifecycleRepository{
		preauthorizeRollbackResult: storedJob{ID: 19, AccountID: 7, Status: jobApplied, RollbackExpiresAt: now.Add(7 * 24 * time.Hour)},
		rollbackDefinition:         rollbackDefinition, rollbackRuntime: rollbackRuntime,
		getResult: storedJob{ID: 19, AccountID: 7, Status: jobRolledBack, RollbackExpiresAt: now.Add(7 * 24 * time.Hour)},
	}
	barrier := &recordingConfigurationBarrier{boundary: configuration.Boundary{AccountID: 7, MigrationJobID: 19, Operation: configuration.BarrierMigrationRollback, OldConfigVersionID: 11, NewConfigVersionID: 12, LastOldRevision: 5, FirstNewRevision: 6, AppliedAt: now}}
	service := NewService(repository, func() time.Time { return now })
	if err := service.SetConfigurationBarrier(barrier); err != nil {
		t.Fatal(err)
	}

	job, err := service.Rollback(context.Background(), 7, 19)
	if err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if job.Status != jobRolledBack || barrier.calls != 1 || barrier.candidate.Operation != configuration.BarrierMigrationRollback || barrier.candidate.Definition.MigrationHash != "" || barrier.candidate.Runtime.AttributeValues["hosted"] != 3 {
		t.Fatalf("rollback job=%#v candidate=%#v calls=%d", job, barrier.candidate, barrier.calls)
	}
}

func TestServiceEmptyAccountApplyCanRollbackThroughOfflineAndLiveBarrier(t *testing.T) {
	for _, test := range []struct {
		name               string
		liveSessionID      int64
		broadcastSessionID int64
	}{
		{name: "offline"},
		{name: "live", liveSessionID: 81, broadcastSessionID: 91},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 8, 29, 12, 20, 0, 0, time.UTC)
			importedDefinition, importedRuntime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "EXE"}}, map[string]float64{"exe": 9})
			repository := &recordingLifecycleRepository{
				composition:             storedComposition{ID: 19, AccountID: 7, Status: jobPreviewed, ExpiresAt: now.Add(time.Hour), Imported: migrationEnvelope(importedDefinition, importedRuntime), HostedDefinition: emptyDefinition(), HostedRuntime: emptyRuntime()},
				preauthorizeApplyResult: storedJob{ID: 19, AccountID: 7, Status: jobPreviewed},
				stageResult:             storedJob{ID: 19, AccountID: 7, Status: jobPending},
				getResult:               storedJob{ID: 19, AccountID: 7, Status: jobApplied, RollbackExpiresAt: now.Add(7 * 24 * time.Hour)},
			}
			barrier := &recordingConfigurationBarrier{boundary: configuration.Boundary{
				AccountID: 7, MigrationJobID: 19, Operation: configuration.BarrierMigrationApply,
				OldConfigVersionID: 0, NewConfigVersionID: 51, LiveSessionID: test.liveSessionID, BroadcastSessionID: test.broadcastSessionID,
				LastOldRevision: 0, FirstNewRevision: 1, AppliedAt: now,
			}}
			service := NewService(repository, func() time.Time { return now })
			if err := service.SetConfigurationBarrier(barrier); err != nil {
				t.Fatal(err)
			}

			applied, err := service.Apply(context.Background(), 7, 19, SelectionCommand{UnitIDs: []string{"attribute:exe"}})
			if err != nil || applied.Status != jobApplied || barrier.candidate.Operation != configuration.BarrierMigrationApply {
				t.Fatalf("empty apply job=%#v candidate=%#v error=%v", applied, barrier.candidate, err)
			}
			repository.preauthorizeRollbackResult = storedJob{ID: 19, AccountID: 7, Status: jobApplied, RollbackExpiresAt: now.Add(7 * 24 * time.Hour)}
			repository.rollbackDefinition = emptyDefinition()
			repository.rollbackRuntime = emptyRuntime()
			repository.getResult = storedJob{ID: 19, AccountID: 7, Status: jobRolledBack, RollbackExpiresAt: now.Add(7 * 24 * time.Hour)}
			barrier.boundary = configuration.Boundary{
				AccountID: 7, MigrationJobID: 19, Operation: configuration.BarrierMigrationRollback,
				OldConfigVersionID: 51, NewConfigVersionID: 52, LiveSessionID: test.liveSessionID, BroadcastSessionID: test.broadcastSessionID,
				LastOldRevision: 1, FirstNewRevision: 2, AppliedAt: now.Add(time.Minute),
			}

			rolledBack, err := service.Rollback(context.Background(), 7, 19)
			if err != nil || rolledBack.Status != jobRolledBack || barrier.candidate.Operation != configuration.BarrierMigrationRollback || len(barrier.candidate.Definition.Attributes) != 0 || len(barrier.candidate.Runtime.AttributeValues) != 0 {
				t.Fatalf("empty rollback job=%#v candidate=%#v error=%v", rolledBack, barrier.candidate, err)
			}
			if barrier.calls != 2 {
				t.Fatalf("barrier calls = %d, want apply + rollback", barrier.calls)
			}
		})
	}
}

func TestServicePreauthorizeRollbackReconcilesAnAlreadyRolledBackBarrier(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 11, 0, 0, time.UTC)
	rollbackDefinition, rollbackRuntime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "hosted", Name: "Hosted"}}, map[string]float64{"hosted": 3})
	repository := &recordingLifecycleRepository{
		preauthorizeRollbackResult: storedJob{ID: 19, AccountID: 7, Status: jobRolledBack, RollbackExpiresAt: now.Add(7 * 24 * time.Hour)},
		rollbackDefinition:         rollbackDefinition, rollbackRuntime: rollbackRuntime,
		getResult: storedJob{ID: 19, AccountID: 7, Status: jobRolledBack, RollbackExpiresAt: now.Add(7 * 24 * time.Hour)},
	}
	barrier := &recordingConfigurationBarrier{boundary: configuration.Boundary{AccountID: 7, MigrationJobID: 19, Operation: configuration.BarrierMigrationRollback, OldConfigVersionID: 11, NewConfigVersionID: 12, LastOldRevision: 5, FirstNewRevision: 6, AppliedAt: now}}
	service := NewService(repository, func() time.Time { return now })
	if err := service.SetConfigurationBarrier(barrier); err != nil {
		t.Fatal(err)
	}

	job, err := service.PreauthorizeRollback(context.Background(), 7, 19)
	if err != nil {
		t.Fatalf("PreauthorizeRollback(rolled_back) error = %v", err)
	}
	if job.Status != jobRolledBack || barrier.calls != 1 || barrier.candidate.Operation != configuration.BarrierMigrationRollback {
		t.Fatalf("rollback reconciliation job=%#v barrier=%#v", job, barrier)
	}
}

func TestServiceApplyPendingAfterSessionUsesTheOwnedConfigurationBarrier(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 12, 0, 0, time.UTC)
	definition, runtime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "EXE"}}, map[string]float64{"exe": 9})
	sealed := sealedCandidateForTest(t, definition, runtime)
	repository := &recordingLifecycleRepository{
		composition:             storedComposition{ID: 19, AccountID: 7, Status: jobPending, ExpiresAt: now.Add(time.Hour), Imported: migrationEnvelope(sealed.Definition, sealed.Runtime)},
		preauthorizeApplyResult: storedJob{ID: 19, AccountID: 7, Status: jobPending},
		getResult:               storedJob{ID: 19, AccountID: 7, Status: jobApplied, RollbackExpiresAt: now.Add(7 * 24 * time.Hour)},
	}
	barrier := &recordingConfigurationBarrier{boundary: configuration.Boundary{AccountID: 7, MigrationJobID: 19, Operation: configuration.BarrierMigrationApply, OldConfigVersionID: 10, NewConfigVersionID: 11, LastOldRevision: 4, FirstNewRevision: 5, AppliedAt: now}}
	service := NewService(repository, func() time.Time { return now })
	if err := service.SetConfigurationBarrier(barrier); err != nil {
		t.Fatal(err)
	}
	owner := OwnerFence{AccountID: 7, Token: [32]byte{1}, Epoch: 2}

	job, err := service.ApplyPendingAfterSession(context.Background(), owner, 19)
	if err != nil {
		t.Fatalf("ApplyPendingAfterSession() error = %v", err)
	}
	if job.Status != jobApplied || barrier.pendingCalls != 1 || barrier.pendingOwner != owner || barrier.candidate.IntegritySeal != sealed.Hash {
		t.Fatalf("pending barrier job=%#v barrier=%#v", job, barrier)
	}
}

func TestSelectionPreviewUsesServerCompositionWithoutMutatingFormalConfiguration(t *testing.T) {
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	hostedDefinition, hostedRuntime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "online", Name: "Health"}}, map[string]float64{"online": 2})
	importDefinition, importRuntime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "Health"}}, map[string]float64{"exe": 8})
	repository := &recordingLifecycleRepository{composition: storedComposition{
		ID: 21, AccountID: 7, Status: jobPreviewed, ExpiresAt: now.Add(time.Hour), Imported: migrationEnvelope(importDefinition, importRuntime), HostedDefinition: hostedDefinition, HostedRuntime: hostedRuntime,
	}}
	service := NewService(repository, func() time.Time { return now })

	preview, err := service.Select(context.Background(), 7, 21, SelectionCommand{UnitIDs: []string{"attribute:exe"}})
	if err != nil {
		t.Fatal(err)
	}
	if preview.CanConfirm || len(preview.Conflicts) != 1 || preview.Conflicts[0].SuggestedNames["attribute:exe"] != "Health（从 EXE 导入）" {
		t.Fatalf("selection preview = %#v", preview)
	}
	if repository.loadAccountID != 7 || repository.loadJobID != 21 || repository.applyCalls != 0 {
		t.Fatalf("composition seam account=%d job=%d writes=%d", repository.loadAccountID, repository.loadJobID, repository.applyCalls)
	}
	if got := repository.composition.HostedRuntime.AttributeValues["online"]; got != 2 {
		t.Fatalf("formal runtime mutated to %v", got)
	}
}

func TestSelectionRejectsRepositoryOwnershipViolationAndSelectedPartialUnit(t *testing.T) {
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	definition, runtime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "EXE"}}, map[string]float64{"exe": 8})
	imported := migrationEnvelope(definition, runtime)
	repository := &recordingLifecycleRepository{composition: storedComposition{ID: 21, AccountID: 9, Status: jobPreviewed, ExpiresAt: now.Add(time.Hour), Imported: imported, HostedDefinition: emptyDefinition(), HostedRuntime: emptyRuntime()}}
	service := NewService(repository, func() time.Time { return now })
	if _, err := service.Select(context.Background(), 7, 21, SelectionCommand{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ownership error = %v, want unavailable", err)
	}

	repository.composition.AccountID = 7
	repository.composition.Imported.Units[0].DisplaySceneIDs = []string{"scene"}
	service.capabilities.DisplayScenesSupported = false
	if _, err := service.Select(context.Background(), 7, 21, SelectionCommand{UnitIDs: []string{"attribute:exe"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("partial selection error = %v, want conflict", err)
	}
}

func TestComposeApplyRequiresEveryConflictChoiceBeforeLifecycleWrite(t *testing.T) {
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	hostedDefinition, hostedRuntime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "online", Name: "Health"}}, map[string]float64{"online": 2})
	importDefinition, importRuntime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "Health"}}, map[string]float64{"exe": 8})
	repository := &recordingLifecycleRepository{
		composition: storedComposition{ID: 21, AccountID: 7, Status: jobPreviewed, ExpiresAt: now.Add(time.Hour), Imported: migrationEnvelope(importDefinition, importRuntime), HostedDefinition: hostedDefinition, HostedRuntime: hostedRuntime},
		applyResult: storedJob{ID: 21, AccountID: 7, Status: jobApplied},
	}
	service := NewService(repository, func() time.Time { return now })
	selection := SelectionCommand{UnitIDs: []string{"attribute:exe"}}
	if _, err := service.Apply(context.Background(), 7, 21, selection); !errors.Is(err, ErrConflict) || repository.applyCalls != 0 {
		t.Fatalf("unresolved Apply() error=%v writes=%d", err, repository.applyCalls)
	}
	selection.ConflictChoices = map[string]ConflictChoice{"attribute:exe": ConflictReplace}
	job, err := service.Apply(context.Background(), 7, 21, selection)
	if err != nil || job.Status != jobApplied || repository.applyCalls != 1 {
		t.Fatalf("resolved Apply() job=%#v error=%v writes=%d", job, err, repository.applyCalls)
	}
	if got, want := repository.apply.Candidate.Runtime.AttributeValues, map[string]float64{"exe": 8}; !reflect.DeepEqual(got, want) {
		t.Fatalf("applied candidate values=%#v want=%#v", got, want)
	}
}

func TestSelectionHashIncludesNormalizedSettingsAndCropsButIgnoresClientDeclaration(t *testing.T) {
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	base := decodedEnvelope(t)
	base.GeneralSettings = GeneralSettings{ConfigurationMode: "simple"}
	base.CropPresets = []CropPreset{{ID: "gift:1", Crop: Crop{X: 0.1, Y: 0.2, Width: 0.3, Height: 0.4}}}
	firstRepository := &recordingPreviewRepository{result: storedPreview{ID: 1, AccountID: 7, ExpiresAt: now.Add(time.Hour)}}
	if _, err := NewService(firstRepository, func() time.Time { return now }).Preview(context.Background(), 7, base); err != nil {
		t.Fatal(err)
	}
	clientOnly := base
	clientOnly.ClientDeclaration = GameplayDependencyDeclaration{AlgorithmVersion: 999, Units: []GameplayUnit{{ID: "client-forged"}}}
	secondRepository := &recordingPreviewRepository{result: storedPreview{ID: 2, AccountID: 7, ExpiresAt: now.Add(time.Hour)}}
	if _, err := NewService(secondRepository, func() time.Time { return now }).Preview(context.Background(), 7, clientOnly); err != nil {
		t.Fatal(err)
	}
	if firstRepository.command.Hash != secondRepository.command.Hash || !reflect.DeepEqual(firstRepository.command.Units, base.Units) {
		t.Fatalf("client declaration affected authoritative preview: first=%x second=%x units=%#v", firstRepository.command.Hash, secondRepository.command.Hash, firstRepository.command.Units)
	}
	settingsChanged := base
	settingsChanged.GeneralSettings.ConfigurationMode = "advanced"
	thirdRepository := &recordingPreviewRepository{result: storedPreview{ID: 3, AccountID: 7, ExpiresAt: now.Add(time.Hour)}}
	if _, err := NewService(thirdRepository, func() time.Time { return now }).Preview(context.Background(), 7, settingsChanged); err != nil {
		t.Fatal(err)
	}
	if firstRepository.command.Hash == thirdRepository.command.Hash {
		t.Fatal("normalized general settings were omitted from idempotency hash")
	}
}

func TestSQLRepositorySelectionLoadIsAccountOwnedReadOnlyAndRestoresServerMetadata(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	importDefinition, importRuntime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "EXE"}}, map[string]float64{"exe": 8})
	hostedDefinition, hostedRuntime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "online", Name: "Online"}}, map[string]float64{"online": 2})
	units := DeriveUnits(importDefinition, importRuntime)
	metadata, err := json.Marshal(previewMetadata{Report: Report{Warnings: []string{"persisted"}}, GeneralSettings: GeneralSettings{ConfigurationMode: "advanced"}, Units: units})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(compositionQuery)).WithArgs(int64(21), int64(7)).WillReturnRows(sqlmock.NewRows([]string{
		"status", "expires_at", "keep_room_suggestion", "definition_json", "runtime_json", "room_suggestion", "source_app_version", "source_schema_version", "report_json", "hosted_definition_json", "hosted_runtime_json",
	}).AddRow(jobPreviewed, now.Add(time.Hour), 0, mustJSON(t, importDefinition), mustJSON(t, importRuntime), "12345", "0.5.0", 5, metadata, mustJSON(t, hostedDefinition), mustJSON(t, hostedRuntime)))

	stored, err := NewRepository(database).(compositionRepository).LoadComposition(context.Background(), 7, 21)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != 21 || stored.AccountID != 7 || stored.Imported.GeneralSettings.ConfigurationMode != "advanced" || stored.Imported.RoomSuggestion != "12345" || !reflect.DeepEqual(stored.Imported.Units, units) || stored.Imported.ClientDeclaration.AlgorithmVersion != 0 {
		t.Fatalf("stored composition = %#v", stored)
	}
	if stored.HostedRuntime.AttributeValues["online"] != 2 {
		t.Fatalf("hosted runtime = %#v", stored.HostedRuntime)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryHistoryIsAccountScopedOrderedAndBounded(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	created := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	applied := created.Add(time.Minute)
	expires := created.Add(24 * time.Hour)
	rollback := applied.Add(7 * 24 * time.Hour)
	const expectedHistoryQuery = "SELECT id, status, created_at, applied_at, expires_at, rollback_expires_at FROM migration_jobs WHERE account_id = ? AND ((status = 'pending' AND expires_at > ?) OR (status = 'applied' AND rollback_expires_at > ?)) ORDER BY created_at DESC, id DESC LIMIT ?"
	mock.ExpectQuery(regexp.QuoteMeta(expectedHistoryQuery)).WithArgs(int64(7), created, created, historyLimit).WillReturnRows(sqlmock.NewRows([]string{"id", "status", "created_at", "applied_at", "expires_at", "rollback_expires_at"}).AddRow(9, jobPending, created, nil, expires, nil).AddRow(8, jobApplied, created.Add(-time.Hour), applied, nil, rollback))
	jobs, err := NewService(NewRepository(database), func() time.Time { return created }).History(context.Background(), 7)
	if err != nil || len(jobs) != 2 || jobs[0].ID != 9 || jobs[1].ID != 8 || jobs[1].AppliedAt == nil || jobs[1].RollbackExpiresAt == nil {
		t.Fatalf("jobs=%#v error=%v", jobs, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestServiceHistoryRejectsAnExpiredPendingRowFromRepository(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	const expectedHistoryQuery = "SELECT id, status, created_at, applied_at, expires_at, rollback_expires_at FROM migration_jobs WHERE account_id = ? AND ((status = 'pending' AND expires_at > ?) OR (status = 'applied' AND rollback_expires_at > ?)) ORDER BY created_at DESC, id DESC LIMIT ?"
	mock.ExpectQuery(regexp.QuoteMeta(expectedHistoryQuery)).WithArgs(int64(7), now, now, historyLimit).WillReturnRows(sqlmock.NewRows([]string{"id", "status", "created_at", "applied_at", "expires_at", "rollback_expires_at"}).AddRow(9, jobPending, now.Add(-time.Hour), nil, now, nil))
	if _, err := NewService(NewRepository(database), func() time.Time { return now }).History(context.Background(), 7); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("History() error=%v, want unavailable", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestServiceOBSOutputsUseAppliedAccountOwnedCandidateAndSkipEmptyScenes(t *testing.T) {
	definition := emptyDefinition()
	definition.Attributes = []configuration.AttributeDefinition{{ID: "score", Name: "积分"}, {ID: "bonus", Name: "加成"}}
	definition.DisplayScenes = []gameplay.DisplayScene{{ID: "main", Name: "主场景", AttributeIDs: []string{"score", "bonus"}}, {ID: "empty", Name: "空场景"}}
	definition.GiftTargetPanels = []configuration.GiftTargetPanelDefinition{{ID: "goals", Name: "礼物目标"}}
	repository := &recordingOBSOutputRepository{stored: storedAppliedDefinition{ID: 19, AccountID: 7, Status: jobApplied, Definition: definition}}
	service := NewService(repository, time.Now)

	outputs, err := service.OBSOutputs(context.Background(), 7, 19)
	want := []OBSOutput{
		{Selector: obsselector.Selector{Kind: "attribute", ID: "bonus"}, Name: "加成"},
		{Selector: obsselector.Selector{Kind: "attribute", ID: "score"}, Name: "积分"},
		{Selector: obsselector.Selector{Kind: "gift-target", ID: "goals"}, Name: "礼物目标"},
		{Selector: obsselector.Selector{Kind: "scene", ID: "main", Attributes: []string{"score", "bonus"}}, Name: "主场景"},
	}
	if err != nil || !reflect.DeepEqual(outputs, want) {
		t.Fatalf("OBSOutputs()=%#v error=%v, want %#v", outputs, err, want)
	}
	repository.stored.AccountID = 8
	if _, err := service.OBSOutputs(context.Background(), 7, 19); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("OBSOutputs() ownership error=%v, want unavailable", err)
	}
}

func TestSQLRepositoryLoadsAppliedOutputDefinitionByAccountAndJob(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	definition := emptyDefinition()
	definition.Attributes = []configuration.AttributeDefinition{{ID: "score", Name: "积分"}}
	mock.ExpectQuery(regexp.QuoteMeta(appliedDefinitionQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id", "account_id", "status", "definition_json"}).AddRow(19, 7, jobApplied, mustJSON(t, definition)))

	stored, err := NewRepository(database).(obsOutputRepository).LoadAppliedDefinition(context.Background(), 7, 19)
	if err != nil || stored.ID != 19 || stored.AccountID != 7 || stored.Status != jobApplied || len(stored.Definition.Attributes) != 1 || stored.Definition.Attributes[0].ID != "score" {
		t.Fatalf("LoadAppliedDefinition()=%#v error=%v", stored, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositorySelectionLoadHidesAnotherAccountsPreview(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	mock.ExpectQuery(regexp.QuoteMeta(compositionQuery)).WithArgs(int64(21), int64(7)).WillReturnError(sql.ErrNoRows)
	if _, err := NewRepository(database).(compositionRepository).LoadComposition(context.Background(), 7, 21); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LoadComposition() error=%v, want not found", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryRestoresPendingCandidateMetadataAndHashFromDefinition(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	definition, runtime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "confirmed", Name: "Confirmed"}}, map[string]float64{"confirmed": 8})
	definition.GeneralSettings = &configuration.GeneralSettings{ConfigurationMode: "simple"}
	definition.CropPresets = []configuration.CropPreset{{ID: "gift:1", Crop: configuration.Crop{Width: 1, Height: 1}}}
	definition, runtime, _, candidateHash, err := freshCanonical(definition, runtime)
	if err != nil {
		t.Fatal(err)
	}
	definition.MigrationHash = hex.EncodeToString(candidateHash[:])
	metadata, err := json.Marshal(previewMetadata{Report: Report{}, Units: []GameplayUnit{{ID: "attribute:original"}}, GeneralSettings: GeneralSettings{ConfigurationMode: "advanced"}})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(compositionQuery)).WithArgs(int64(21), int64(7)).WillReturnRows(sqlmock.NewRows([]string{
		"status", "expires_at", "keep_room_suggestion", "definition_json", "runtime_json", "room_suggestion", "source_app_version", "source_schema_version", "report_json", "hosted_definition_json", "hosted_runtime_json",
	}).AddRow(jobPending, now.Add(time.Hour), 1, mustJSON(t, definition), mustJSON(t, runtime), "12345", "0.5.0", 5, metadata, nil, nil))

	stored, err := NewRepository(database).(compositionRepository).LoadComposition(context.Background(), 7, 21)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.KeepRoomSuggestion || stored.Imported.Definition.MigrationHash != hex.EncodeToString(candidateHash[:]) || stored.Imported.GeneralSettings.ConfigurationMode != "simple" || len(stored.Imported.CropPresets) != 1 || len(stored.Imported.Units) != 1 || stored.Imported.Units[0].ID != "attribute:confirmed" {
		t.Fatalf("restored pending candidate=%#v", stored)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryComposeStagesFullCandidateWithoutChangingFormalConfiguration(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	definition, runtime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "EXE"}}, map[string]float64{"exe": 8})
	definition.GeneralSettings = &configuration.GeneralSettings{ConfigurationMode: "simple"}
	definition.CropPresets = []configuration.CropPreset{{ID: "gift:1", Crop: configuration.Crop{Width: 1, Height: 1}}}
	candidate := sealedCandidateForTest(t, definition, runtime)
	definitionJSON, runtimeJSON := mustJSON(t, candidate.Definition), mustJSON(t, candidate.Runtime)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(88, 3, 6, mustJSON(t, runtime)))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(21), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobPreviewed, now.Add(time.Hour), 0, nil, nil, nil, nil, []byte(`{"attributes":[]}`), []byte(`{"attributeValues":{}}`), "12345"))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleBaseQuery)).WithArgs(int64(21), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"base_config_version_number", "base_state_revision"}).AddRow(3, 6))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleOpenSessionQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(12))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE migration_jobs SET status = 'pending', keep_room_suggestion = ?, definition_json = ?, runtime_json = ? WHERE id = ? AND account_id = ? AND status = 'previewed'")).WithArgs(true, definitionJSON, runtimeJSON, int64(21), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	job, err := NewRepository(database).(lifecycleRepository).Apply(context.Background(), applyCommand{AccountID: 7, JobID: 21, KeepRoomSuggestion: true, Candidate: candidate})
	if err != nil || job.Status != jobPending {
		t.Fatalf("Apply() job=%#v error=%v", job, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFrozenCandidateRejectsMissingTamperedAndMismatchedIntegritySeal(t *testing.T) {
	definition, runtime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "EXE"}}, map[string]float64{"exe": 8})
	valid := sealedCandidateForTest(t, definition, runtime)

	missing := valid
	missing.Definition.MigrationHash = ""
	if _, err := freezeCandidate(missing); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing hash error=%v, want conflict", err)
	}
	tampered := valid
	tampered.Definition.Attributes[0].Name = "tampered"
	if _, err := freezeCandidate(tampered); !errors.Is(err, ErrConflict) {
		t.Fatalf("tampered content error=%v, want conflict", err)
	}
	mismatched := valid
	mismatched.Hash[0] ^= 0xff
	if _, err := freezeCandidate(mismatched); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched command hash error=%v, want conflict", err)
	}
}

func TestFrozenPendingCandidateRejectsMissingOrTamperedSeal(t *testing.T) {
	definition, runtime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "EXE"}}, map[string]float64{"exe": 8})
	valid := sealedCandidateForTest(t, definition, runtime)
	missing := valid.Definition
	missing.MigrationHash = ""
	if _, err := freezePersistedCandidate(mustJSON(t, missing), mustJSON(t, valid.Runtime)); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing pending seal error=%v, want conflict", err)
	}
	tamperedRuntime := valid.Runtime
	tamperedRuntime.AttributeValues["exe"] = 99
	if _, err := freezePersistedCandidate(mustJSON(t, valid.Definition), mustJSON(t, tamperedRuntime)); !errors.Is(err, ErrConflict) {
		t.Fatalf("tampered pending runtime error=%v, want conflict", err)
	}
}

func TestFrozenCandidateActiveDefinitionAllowsRuntimeChangeAndNextSelection(t *testing.T) {
	hostedDefinition, hostedRuntime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "online", Name: "Online"}}, map[string]float64{"online": 2})
	frozen, err := freezeCandidate(sealedCandidateForTest(t, hostedDefinition, hostedRuntime))
	if err != nil {
		t.Fatal(err)
	}
	var activeDefinition configuration.Definition
	if err := json.Unmarshal(frozen.ActiveDefinition, &activeDefinition); err != nil {
		t.Fatal(err)
	}
	if activeDefinition.MigrationHash != "" {
		t.Fatalf("active definition retained migration hash %q", activeDefinition.MigrationHash)
	}
	changedRuntime := frozen.Runtime
	changedRuntime.AttributeValues["online"] = 3
	importDefinition, importRuntime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "EXE"}}, map[string]float64{"exe": 8})
	imported := migrationEnvelope(importDefinition, importRuntime)

	candidate, err := composeCandidate(imported, activeDefinition, changedRuntime, completeCapabilities(), SelectionCommand{UnitIDs: []string{"attribute:exe"}})
	if err != nil {
		t.Fatalf("next selection after runtime change error=%v", err)
	}
	if !candidate.Ready || candidate.Runtime.AttributeValues["online"] != 3 || candidate.Runtime.AttributeValues["exe"] != 8 {
		t.Fatalf("next migration candidate=%#v", candidate)
	}
}

func TestSQLRepositoryRejectsMismatchedCandidateSealBeforeStagingWrite(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	definition, runtime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "EXE"}}, map[string]float64{"exe": 8})
	candidate := sealedCandidateForTest(t, definition, runtime)
	candidate.Hash[0] ^= 0xff
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(nil, 0, 0, nil))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(21), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobPreviewed, now.Add(time.Hour), 0, nil, nil, nil, nil, []byte(`{"attributes":[]}`), []byte(`{"attributeValues":{}}`), nil))
	expectPreviewNow(mock, now)
	mock.ExpectRollback()

	_, err = NewRepository(database).(lifecycleRepository).Apply(context.Background(), applyCommand{AccountID: 7, JobID: 21, Candidate: candidate})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Apply() error=%v, want conflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryRejectsTamperedPendingSealAfterJobLock(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	fence := OwnerFence{AccountID: 7, Token: [32]byte{1}, Epoch: 2}
	definition, runtime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "EXE"}}, map[string]float64{"exe": 8})
	candidate := sealedCandidateForTest(t, definition, runtime)
	tamperedRuntime := candidate.Runtime
	tamperedRuntime.AttributeValues["exe"] = 99
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(88, 3, 6, mustJSON(t, runtime)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT expires_at > UTC_TIMESTAMP(6) FROM runtime_account_owners WHERE account_id = ? AND owner_token = ? AND fencing_epoch = ? FOR UPDATE")).WithArgs(int64(7), fence.Token[:], fence.Epoch).WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(21), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobPending, now.Add(time.Hour), 0, nil, nil, nil, nil, mustJSON(t, candidate.Definition), mustJSON(t, tamperedRuntime), nil))
	expectPreviewNow(mock, now)
	mock.ExpectRollback()

	_, err = NewRepository(database).(lifecycleRepository).ApplyPendingAfterSession(context.Background(), fence, 21)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("ApplyPendingAfterSession() error=%v, want conflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryComposeAppliesFullCandidateInsteadOfRawImport(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	definition, runtime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "EXE"}, {ID: "retained", Name: "Retained"}}, map[string]float64{"exe": 8, "retained": 2})
	definition.GeneralSettings = &configuration.GeneralSettings{ConfigurationMode: "advanced"}
	definition.CropPresets = []configuration.CropPreset{{ID: "effect:9", Crop: configuration.Crop{X: 0.2, Width: 0.8, Height: 1}}}
	candidate := sealedCandidateForTest(t, definition, runtime)
	definitionJSON, runtimeJSON := activeDefinitionJSONForTest(t, candidate), mustJSON(t, candidate.Runtime)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(nil, 0, 0, nil))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(21), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobPreviewed, now.Add(time.Hour), 0, nil, nil, nil, nil, []byte(`{"attributes":[{"id":"raw-only"}]}`), []byte(`{"attributeValues":{"raw-only":1}}`), nil))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleBaseQuery)).WithArgs(int64(21), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"base_config_version_number", "base_state_revision"}).AddRow(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleOpenSessionQuery)).WithArgs(int64(7)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleNextVersionQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleInsertVersionQuery)).WithArgs(int64(7), uint64(1), definitionJSON, "migration", now).WillReturnResult(sqlmock.NewResult(88, 1))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleUpsertRuntimeQuery)).WithArgs(int64(7), int64(88), uint64(1), runtimeJSON, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleUpsertActiveQuery)).WithArgs(int64(7), int64(88), now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE migration_jobs SET keep_room_suggestion = ?, rollback_config_version_id = ?, rollback_runtime_json = ?, rollback_expires_at = ?, applied_config_version_id = ?, status = 'applied', applied_at = ? WHERE id = ? AND account_id = ? AND status IN ('previewed', 'pending')")).WithArgs(false, nil, nil, now.Add(7*24*time.Hour), int64(88), now, int64(21), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	job, err := NewRepository(database).(lifecycleRepository).Apply(context.Background(), applyCommand{AccountID: 7, JobID: 21, Candidate: candidate})
	if err != nil || job.Status != jobApplied {
		t.Fatalf("Apply() job=%#v error=%v", job, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryReusesUnexpiredHashWithoutQuotaConsumption(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 16, 9, 30, 0, 0, time.UTC)
	command := previewCommandForTest(t, now)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(previewBaseQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number", "revision"}).AddRow(4, 9))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(previewExistingQuery)).WithArgs(int64(7), command.Hash[:]).WillReturnRows(activePreviewRows(3, "previewed", now.Add(time.Hour), command))
	mock.ExpectCommit()

	stored, err := NewRepository(database).Preview(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != 3 || !stored.Reused {
		t.Fatalf("expected reusable preview, got %#v", stored)
	}
	if stored.RoomSuggestion != "persisted-room" || stored.Source.AppVersion != "persisted-app" || stored.Source.ConfigurationSchemaVersion != 4 || stored.Report.Counts != command.Counts || stored.Hash != command.Hash {
		t.Fatalf("reuse did not return persisted preview metadata: %#v", stored)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryReusesPendingHashWithoutCreatingAnotherPreview(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	command := previewCommandForTest(t, now)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(previewBaseQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number", "revision"}).AddRow(4, 9))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(previewExistingQuery)).WithArgs(int64(7), command.Hash[:]).WillReturnRows(activePreviewRows(3, jobPending, now.Add(time.Hour), command))
	mock.ExpectCommit()

	stored, err := NewRepository(database).Preview(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != 3 || stored.Status != jobPending || !stored.Reused {
		t.Fatalf("pending reuse=%#v", stored)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewReusesPendingCandidateProjectionWithoutReinterpretingSelection(t *testing.T) {
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	definition, runtime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "confirmed", Name: "Confirmed"}}, map[string]float64{"confirmed": 8})
	definition.GeneralSettings = &configuration.GeneralSettings{ConfigurationMode: "simple"}
	definition.CropPresets = []configuration.CropPreset{{ID: "gift:1", Crop: configuration.Crop{Width: 1, Height: 1}}}
	definition, runtime, _, hash, err := freshCanonical(definition, runtime)
	if err != nil {
		t.Fatal(err)
	}
	definition.MigrationHash = hex.EncodeToString(hash[:])
	repository := &recordingLifecycleRepository{
		recordingPreviewRepository: recordingPreviewRepository{result: storedPreview{ID: 21, AccountID: 7, Status: jobPending, ExpiresAt: now.Add(time.Hour), Reused: true}},
		composition:                storedComposition{ID: 21, AccountID: 7, Status: jobPending, ExpiresAt: now.Add(time.Hour), Imported: migrationEnvelope(definition, runtime), KeepRoomSuggestion: true},
	}
	service := NewService(repository, func() time.Time { return now })

	preview, err := service.Preview(context.Background(), 7, decodedEnvelope(t))
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Reused || !preview.CanConfirm || !preview.Selection.IncludeGeneralSettings || !preview.Selection.IncludeRoomSuggestion || len(preview.Units) != 1 || !preview.Units[0].Selected || preview.Units[0].ID != "attribute:confirmed" {
		t.Fatalf("pending projection=%#v", preview)
	}
	if repository.applyCalls != 0 || repository.loadJobID != 21 {
		t.Fatalf("pending reuse writes=%d loadJob=%d", repository.applyCalls, repository.loadJobID)
	}
}

func TestSQLRepositoryUsesDatabaseUTCForNewGenerationAcrossMidnight(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	databaseNow := time.Date(2026, 8, 17, 0, 0, 1, 0, time.UTC)
	command := previewCommandForTest(t, databaseNow.Add(-48*time.Hour))
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(previewBaseQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number", "revision"}).AddRow(0, 0))
	expectPreviewNow(mock, databaseNow)
	mock.ExpectQuery(regexp.QuoteMeta(previewExistingQuery)).WithArgs(int64(7), command.Hash[:]).WillReturnError(sql.ErrNoRows)
	start := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta(previewQuotaQuery)).WithArgs(int64(7), start, start.Add(24*time.Hour)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(regexp.QuoteMeta(previewInsertQuery)).WithArgs(int64(7), command.Hash[:], "previewed", uint64(0), uint64(0), sqlmock.AnyArg(), sqlmock.AnyArg(), "12345", "0.4.4", 5, sqlmock.AnyArg(), databaseNow, databaseNow.Add(24*time.Hour)).WillReturnResult(sqlmock.NewResult(46, 1))
	mock.ExpectCommit()
	stored, err := NewRepository(database).Preview(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.ExpiresAt.Equal(databaseNow.Add(24 * time.Hour)) {
		t.Fatalf("expiry = %v, want database-time deadline", stored.ExpiresAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryCreatesAtQuotaBoundaryUnderAccountLock(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 16, 9, 30, 0, 0, time.UTC)
	command := previewCommandForTest(t, now)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(previewBaseQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number", "revision"}).AddRow(0, 0))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(previewExistingQuery)).WithArgs(int64(7), command.Hash[:]).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta(previewQuotaQuery)).WithArgs(int64(7), now.Truncate(24*time.Hour), now.Truncate(24*time.Hour).Add(24*time.Hour)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(4))
	mock.ExpectExec(regexp.QuoteMeta(previewInsertQuery)).WithArgs(int64(7), command.Hash[:], "previewed", uint64(0), uint64(0), sqlmock.AnyArg(), sqlmock.AnyArg(), "12345", "0.4.4", 5, sqlmock.AnyArg(), now, now.Add(24*time.Hour)).WillReturnResult(sqlmock.NewResult(42, 1))
	mock.ExpectCommit()

	stored, err := NewRepository(database).Preview(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != 42 || stored.Reused {
		t.Fatalf("expected a new preview, got %#v", stored)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryRejectsSixthSuccessfulPreviewAndRollsBack(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 16, 9, 30, 0, 0, time.UTC)
	command := previewCommandForTest(t, now)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(previewBaseQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number", "revision"}).AddRow(0, 0))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(previewExistingQuery)).WithArgs(int64(7), command.Hash[:]).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta(previewQuotaQuery)).WithArgs(int64(7), now.Truncate(24*time.Hour), now.Truncate(24*time.Hour).Add(24*time.Hour)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(5))
	mock.ExpectRollback()

	if _, err := NewRepository(database).Preview(context.Background(), command); !errors.Is(err, ErrPreviewLimit) {
		t.Fatalf("got %v, want preview limit", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryRefreshesExpiredPreviewWithFreshBaseSnapshot(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 16, 9, 30, 0, 0, time.UTC)
	command := previewCommandForTest(t, now)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(previewBaseQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number", "revision"}).AddRow(8, 12))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(previewExistingQuery)).WithArgs(int64(7), command.Hash[:]).WillReturnRows(activePreviewRows(3, "previewed", now.Add(-time.Second), command))
	mock.ExpectQuery(regexp.QuoteMeta(previewQuotaQuery)).WithArgs(int64(7), now.Truncate(24*time.Hour), now.Truncate(24*time.Hour).Add(24*time.Hour)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectExec(regexp.QuoteMeta(previewExpireQuery)).WithArgs(int64(3), int64(7), now).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(previewInsertQuery)).WithArgs(int64(7), command.Hash[:], "previewed", uint64(8), uint64(12), sqlmock.AnyArg(), sqlmock.AnyArg(), "12345", "0.4.4", 5, sqlmock.AnyArg(), now, now.Add(24*time.Hour)).WillReturnResult(sqlmock.NewResult(44, 1))
	mock.ExpectCommit()

	stored, err := NewRepository(database).Preview(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != 44 || stored.Reused || !stored.ExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("unexpected refreshed preview: %#v", stored)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryCreatesNewGenerationAfterTerminalHistory(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 16, 9, 30, 0, 0, time.UTC)
	command := previewCommandForTest(t, now)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(previewBaseQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number", "revision"}).AddRow(8, 12))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(previewExistingQuery)).WithArgs(int64(7), command.Hash[:]).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta(previewQuotaQuery)).WithArgs(int64(7), now.Truncate(24*time.Hour), now.Truncate(24*time.Hour).Add(24*time.Hour)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectExec(regexp.QuoteMeta(previewInsertQuery)).WithArgs(int64(7), command.Hash[:], "previewed", uint64(8), uint64(12), sqlmock.AnyArg(), sqlmock.AnyArg(), "12345", "0.4.4", 5, sqlmock.AnyArg(), now, now.Add(24*time.Hour)).WillReturnResult(sqlmock.NewResult(45, 1))
	mock.ExpectCommit()

	if _, err := NewRepository(database).Preview(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryAppliesInactivePreviewInOneTransaction(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 9, 30, 0, 0, time.UTC)
	candidate := sealedCandidateForTest(t, emptyDefinition(), emptyRuntime())
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(nil, 0, 0, nil))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobPreviewed, now.Add(time.Hour), 0, nil, nil, nil, nil, []byte(`{"attributes":[]}`), []byte(`{"attributeValues":{}}`), "12345"))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleBaseQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"base_config_version_number", "base_state_revision"}).AddRow(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleOpenSessionQuery)).WithArgs(int64(7)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleNextVersionQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleInsertVersionQuery)).WithArgs(int64(7), uint64(1), sqlmock.AnyArg(), "migration", now).WillReturnResult(sqlmock.NewResult(88, 1))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleUpsertRuntimeQuery)).WithArgs(int64(7), int64(88), uint64(1), sqlmock.AnyArg(), now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleUpsertActiveQuery)).WithArgs(int64(7), int64(88), now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO account_room_suggestions (account_id, room_id, suggested_at) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE room_id = VALUES(room_id), suggested_at = VALUES(suggested_at)")).WithArgs(int64(7), "12345", now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE migration_jobs SET keep_room_suggestion = ?, rollback_config_version_id = ?, rollback_runtime_json = ?, rollback_expires_at = ?, applied_config_version_id = ?, status = 'applied', applied_at = ? WHERE id = ? AND account_id = ? AND status IN ('previewed', 'pending')")).WithArgs(true, nil, nil, now.Add(7*24*time.Hour), int64(88), now, int64(19), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	job, err := NewRepository(database).(lifecycleRepository).Apply(context.Background(), applyCommand{AccountID: 7, JobID: 19, KeepRoomSuggestion: true, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != jobApplied || !job.RollbackExpiresAt.Equal(now.Add(7*24*time.Hour)) {
		t.Fatalf("job = %#v", job)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryStagesLiveSessionWithoutChangingConfiguration(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	candidate := sealedCandidateForTest(t, emptyDefinition(), emptyRuntime())
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(88, 3, 6, []byte(`{"attributeValues":{"health":1}}`)))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobPreviewed, now.Add(time.Hour), 0, nil, nil, nil, nil, []byte(`{"attributes":[]}`), []byte(`{"attributeValues":{}}`), "12345"))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleBaseQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"base_config_version_number", "base_state_revision"}).AddRow(3, 6))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleOpenSessionQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(12))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE migration_jobs SET status = 'pending', keep_room_suggestion = ?, definition_json = ?, runtime_json = ? WHERE id = ? AND account_id = ? AND status = 'previewed'")).WithArgs(true, mustJSON(t, candidate.Definition), mustJSON(t, candidate.Runtime), int64(19), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	job, err := NewRepository(database).(lifecycleRepository).Apply(context.Background(), applyCommand{AccountID: 7, JobID: 19, KeepRoomSuggestion: true, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != jobPending {
		t.Fatalf("job = %#v", job)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryStageApplyFreezesCandidateWithoutTouchingConfiguration(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	candidate := sealedCandidateForTest(t, emptyDefinition(), emptyRuntime())
	frozen, err := freezeCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(88, 3, 6, []byte(`{"attributeValues":{"health":1}}`)))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobPreviewed, now.Add(time.Hour), 0, nil, nil, nil, nil, []byte(`{"attributes":[]}`), []byte(`{"attributeValues":{}}`), "12345"))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleBaseQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"base_config_version_number", "base_state_revision"}).AddRow(3, 6))
	mock.ExpectExec(regexp.QuoteMeta(stageApplyQuery)).WithArgs(true, frozen.SealedDefinition, frozen.RuntimeJSON, int64(19), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	job, err := NewRepository(database).(stagedApplyRepository).StageApply(context.Background(), applyCommand{AccountID: 7, JobID: 19, KeepRoomSuggestion: true, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != jobPending {
		t.Fatalf("StageApply() = %#v", job)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryAppliesPendingJobAfterSessionWithPersistedRoomDecision(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 10, 15, 0, 0, time.UTC)
	fence := OwnerFence{AccountID: 7, Token: [32]byte{1, 2, 3}, Epoch: 4}
	pendingDefinition, pendingRuntime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "exe", Name: "EXE"}}, map[string]float64{"exe": 8})
	pendingDefinition.GeneralSettings = &configuration.GeneralSettings{ConfigurationMode: "simple"}
	pendingDefinition.CropPresets = []configuration.CropPreset{{ID: "gift:1", Crop: configuration.Crop{Width: 1, Height: 1}}}
	pendingCandidate := sealedCandidateForTest(t, pendingDefinition, pendingRuntime)
	pendingDefinitionJSON, pendingRuntimeJSON := mustJSON(t, pendingCandidate.Definition), mustJSON(t, pendingCandidate.Runtime)
	activeDefinitionJSON := activeDefinitionJSONForTest(t, pendingCandidate)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(88, 3, 6, []byte(`{"attributeValues":{"health":1}}`)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT expires_at > UTC_TIMESTAMP(6) FROM runtime_account_owners WHERE account_id = ? AND owner_token = ? AND fencing_epoch = ? FOR UPDATE")).WithArgs(int64(7), fence.Token[:], fence.Epoch).WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobPending, now.Add(time.Hour), 1, nil, nil, nil, nil, pendingDefinitionJSON, pendingRuntimeJSON, "12345"))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleBaseQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"base_config_version_number", "base_state_revision"}).AddRow(3, 6))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleOpenSessionQuery)).WithArgs(int64(7)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleNextVersionQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number"}).AddRow(4))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleInsertVersionQuery)).WithArgs(int64(7), uint64(4), activeDefinitionJSON, "migration", now).WillReturnResult(sqlmock.NewResult(99, 1))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleUpsertRuntimeQuery)).WithArgs(int64(7), int64(99), uint64(7), pendingRuntimeJSON, now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleUpsertActiveQuery)).WithArgs(int64(7), int64(99), now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO account_room_suggestions (account_id, room_id, suggested_at) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE room_id = VALUES(room_id), suggested_at = VALUES(suggested_at)")).WithArgs(int64(7), "12345", now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE migration_jobs SET keep_room_suggestion = ?, rollback_config_version_id = ?, rollback_runtime_json = ?, rollback_expires_at = ?, applied_config_version_id = ?, status = 'applied', applied_at = ? WHERE id = ? AND account_id = ? AND status IN ('previewed', 'pending')")).WithArgs(true, int64(88), sqlmock.AnyArg(), now.Add(7*24*time.Hour), int64(99), now, int64(19), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	job, err := NewRepository(database).(lifecycleRepository).ApplyPendingAfterSession(context.Background(), fence, 19)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != jobApplied {
		t.Fatalf("job = %#v", job)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryRollbackRestoresSavedStateOnlyWhenAppliedVersionIsCurrent(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 10, 30, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(88, 4, 8, []byte(`{"attributeValues":{"health":5}}`)))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobApplied, now.Add(time.Hour), 0, 77, []byte(`{"attributeValues":{"health":1}}`), now.Add(7*24*time.Hour), 88, []byte(`{"attributes":[]}`), []byte(`{"attributeValues":{"health":5}}`), nil))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleOpenSessionQuery)).WithArgs(int64(7)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT definition_json FROM account_config_versions WHERE account_id = ? AND id = ?")).WithArgs(int64(7), int64(77)).WillReturnRows(sqlmock.NewRows([]string{"definition_json"}).AddRow([]byte(`{"attributes":[]}`)))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleNextVersionQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number"}).AddRow(5))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleInsertVersionQuery)).WithArgs(int64(7), uint64(5), sqlmock.AnyArg(), "rollback", now).WillReturnResult(sqlmock.NewResult(99, 1))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleUpsertRuntimeQuery)).WithArgs(int64(7), int64(99), uint64(9), sqlmock.AnyArg(), now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleUpsertActiveQuery)).WithArgs(int64(7), int64(99), now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE migration_jobs SET status = 'rolled_back', rolled_back_at = ? WHERE id = ? AND account_id = ? AND status = 'applied'")).WithArgs(now, int64(19), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	job, err := NewRepository(database).(lifecycleRepository).Rollback(context.Background(), 7, 19)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != jobRolledBack {
		t.Fatalf("job = %#v", job)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryLoadsRollbackCandidateWithoutChangingHistory(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	definition, runtime := attributeConfiguration([]configuration.AttributeDefinition{{ID: "hosted", Name: "Hosted"}}, map[string]float64{"hosted": 3})
	mock.ExpectQuery(regexp.QuoteMeta(rollbackCandidateQuery)).WithArgs(int64(19), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "definition_json", "rollback_runtime_json"}).AddRow(jobApplied, mustJSON(t, definition), mustJSON(t, runtime)))

	gotDefinition, gotRuntime, err := NewRepository(database).(rollbackCandidateRepository).LoadRollbackCandidate(context.Background(), 7, 19)
	if err != nil {
		t.Fatal(err)
	}
	if gotDefinition.Attributes[0].ID != "hosted" || gotRuntime.AttributeValues["hosted"] != 3 {
		t.Fatalf("rollback candidate = %#v %#v", gotDefinition, gotRuntime)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryPreauthorizeRollbackAllowsOpenSessionForLiveBarrier(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 29, 12, 20, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(88, 4, 8, []byte(`{"attributeValues":{"health":5}}`)))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobApplied, now.Add(time.Hour), 0, 77, []byte(`{"attributeValues":{"health":1}}`), now.Add(7*24*time.Hour), 88, []byte(`{"attributes":[]}`), []byte(`{"attributeValues":{"health":5}}`), nil))
	expectPreviewNow(mock, now)
	mock.ExpectCommit()

	job, err := NewRepository(database).(lifecycleRepository).PreauthorizeRollback(context.Background(), 7, 19)
	if err != nil {
		t.Fatalf("PreauthorizeRollback() error = %v", err)
	}
	if job.Status != jobApplied {
		t.Fatalf("PreauthorizeRollback() = %#v", job)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryPendingCompletionRejectsPreviewedJobAfterAccountLock(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 11, 0, 0, 0, time.UTC)
	fence := OwnerFence{AccountID: 7, Token: [32]byte{4, 5, 6}, Epoch: 2}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(nil, 0, 0, nil))
	mock.ExpectQuery("SELECT expires_at > UTC_TIMESTAMP").WithArgs(int64(7), fence.Token[:], fence.Epoch).WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobPreviewed, now.Add(time.Hour), 0, nil, nil, nil, nil, []byte(`{"attributes":[]}`), []byte(`{"attributeValues":{}}`), nil))
	expectPreviewNow(mock, now)
	mock.ExpectRollback()
	_, err = NewRepository(database).(lifecycleRepository).ApplyPendingAfterSession(context.Background(), fence, 19)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("ApplyPendingAfterSession() error = %v, want conflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryPendingCompletionRejectsStaleOwnerBeforeJobLockOrWrites(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	fence := OwnerFence{AccountID: 7, Token: [32]byte{7, 8, 9}, Epoch: 6}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(88, 3, 6, []byte(`{"attributeValues":{"health":1}}`)))
	mock.ExpectQuery("SELECT expires_at > UTC_TIMESTAMP").WithArgs(int64(7), fence.Token[:], fence.Epoch).WillReturnRows(sqlmock.NewRows([]string{"active"}))
	mock.ExpectRollback()
	_, err = NewRepository(database).(lifecycleRepository).ApplyPendingAfterSession(context.Background(), fence, 19)
	if !errors.Is(err, ErrOwnershipConflict) || errors.Is(err, ErrConflict) {
		t.Fatalf("ApplyPendingAfterSession stale owner error = %v, want distinct ownership conflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryExpiresPreviewWithCommittedGuardRelease(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 11, 30, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(nil, 0, 0, nil))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobPreviewed, now, 0, nil, nil, nil, nil, []byte(`{"attributes":[]}`), []byte(`{"attributeValues":{}}`), nil))
	expectPreviewNow(mock, now)
	mock.ExpectExec(regexp.QuoteMeta(previewExpireQuery)).WithArgs(int64(19), int64(7), now).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	_, err = NewRepository(database).(lifecycleRepository).Apply(context.Background(), applyCommand{AccountID: 7, JobID: 19})
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("Apply() error = %v, want expired", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryCancelAllowsOnlyCancelledTerminalState(t *testing.T) {
	now := time.Date(2026, 8, 17, 11, 45, 0, 0, time.UTC)
	for _, test := range []struct {
		name, status string
		want         error
		commit       bool
	}{
		{name: "cancelled idempotent", status: jobCancelled, commit: true},
		{name: "applied conflict", status: jobApplied, want: ErrConflict},
		{name: "rolled back conflict", status: jobRolledBack, want: ErrConflict},
		{name: "expired conflict", status: jobExpired, want: ErrConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(nil, 0, 0, nil))
			mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(test.status, now.Add(time.Hour), 0, nil, nil, nil, nil, []byte(`{"attributes":[]}`), []byte(`{"attributeValues":{}}`), nil))
			expectPreviewNow(mock, now)
			if test.commit {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			job, err := NewRepository(database).(lifecycleRepository).Cancel(context.Background(), 7, 19)
			if !errors.Is(err, test.want) {
				t.Fatalf("Cancel() error=%v want=%v", err, test.want)
			}
			if test.commit && job.Status != jobCancelled {
				t.Fatalf("job=%#v", job)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSQLRepositoryCancelRequiresExactlyOneTransitionRow(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 11, 50, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(nil, 0, 0, nil))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobPreviewed, now.Add(time.Hour), 0, nil, nil, nil, nil, []byte(`{"attributes":[]}`), []byte(`{"attributeValues":{}}`), nil))
	expectPreviewNow(mock, now)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE migration_jobs SET status = 'cancelled', cancelled_at = ? WHERE id = ? AND account_id = ? AND status IN ('previewed', 'pending')")).WithArgs(now, int64(19), int64(7)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	_, err = NewRepository(database).(lifecycleRepository).Cancel(context.Background(), 7, 19)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Cancel() error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryGetCommitsExpiredPreviewBeforeReturningStableStatus(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(nil, 0, 0, nil))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobPreviewed, now, 0, nil, nil, nil, nil, []byte(`{"attributes":[]}`), []byte(`{"attributeValues":{}}`), nil))
	expectPreviewNow(mock, now)
	mock.ExpectExec(regexp.QuoteMeta(previewExpireQuery)).WithArgs(int64(19), int64(7), now).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	job, err := NewRepository(database).(lifecycleRepository).Get(context.Background(), 7, 19)
	if err != nil || job.Status != jobExpired {
		t.Fatalf("Get() job=%#v err=%v", job, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryPublicApplyReturnsExistingPendingWithoutWrites(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 12, 5, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(88, 3, 6, []byte(`{"attributeValues":{"health":1}}`)))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobPending, now.Add(time.Hour), 1, nil, nil, nil, nil, []byte(`{"attributes":[]}`), []byte(`{"attributeValues":{}}`), nil))
	expectPreviewNow(mock, now)
	mock.ExpectCommit()
	job, err := NewRepository(database).(lifecycleRepository).Apply(context.Background(), applyCommand{AccountID: 7, JobID: 19})
	if err != nil || job.Status != jobPending {
		t.Fatalf("Apply() job=%#v err=%v", job, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryPreauthorizeApplyCommitsExpiredPending(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 12, 10, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(88, 3, 6, []byte(`{"attributeValues":{"health":1}}`)))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobPending, now, 1, nil, nil, nil, nil, []byte(`{"attributes":[]}`), []byte(`{"attributeValues":{}}`), nil))
	expectPreviewNow(mock, now)
	mock.ExpectExec(regexp.QuoteMeta(previewExpireQuery)).WithArgs(int64(19), int64(7), now).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	_, err = NewRepository(database).(lifecycleRepository).PreauthorizeApply(context.Background(), 7, 19)
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("PreauthorizeApply() error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type recordingPreviewRepository struct {
	command previewCommand
	result  storedPreview
	err     error
}

type recordingOBSOutputRepository struct {
	recordingPreviewRepository
	stored storedAppliedDefinition
}

func (repository *recordingOBSOutputRepository) LoadAppliedDefinition(context.Context, int64, int64) (storedAppliedDefinition, error) {
	return repository.stored, nil
}

type recordingLifecycleRepository struct {
	recordingPreviewRepository
	apply                      applyCommand
	applyResult                storedJob
	applyErr                   error
	composition                storedComposition
	loadAccountID, loadJobID   int64
	applyCalls                 int
	stage                      applyCommand
	stageResult                storedJob
	stageCalls                 int
	getResult                  storedJob
	preauthorizeApplyResult    storedJob
	preauthorizeRollbackResult storedJob
	rollbackDefinition         configuration.Definition
	rollbackRuntime            configuration.RuntimeState
}

func (repository *recordingLifecycleRepository) Apply(_ context.Context, command applyCommand) (storedJob, error) {
	repository.apply = command
	repository.applyCalls++
	return repository.applyResult, repository.applyErr
}
func (repository *recordingLifecycleRepository) LoadComposition(_ context.Context, accountID, jobID int64) (storedComposition, error) {
	repository.loadAccountID, repository.loadJobID = accountID, jobID
	return repository.composition, nil
}
func (repository *recordingLifecycleRepository) StageApply(_ context.Context, command applyCommand) (storedJob, error) {
	repository.stage, repository.stageCalls = command, repository.stageCalls+1
	frozen, err := freezeCandidate(command.Candidate)
	if err != nil {
		return storedJob{}, err
	}
	repository.composition.Status = jobPending
	repository.composition.KeepRoomSuggestion = command.KeepRoomSuggestion
	repository.composition.Imported.Definition = frozen.Definition
	repository.composition.Imported.Runtime = frozen.Runtime
	return repository.stageResult, nil
}
func (repository *recordingLifecycleRepository) ApplyPendingAfterSession(context.Context, OwnerFence, int64) (storedJob, error) {
	return storedJob{}, ErrUnavailable
}
func (repository *recordingLifecycleRepository) PreauthorizeApply(context.Context, int64, int64) (storedJob, error) {
	if repository.preauthorizeApplyResult.ID != 0 {
		return repository.preauthorizeApplyResult, nil
	}
	return storedJob{}, ErrUnavailable
}
func (repository *recordingLifecycleRepository) PreauthorizeRollback(context.Context, int64, int64) (storedJob, error) {
	if repository.preauthorizeRollbackResult.ID != 0 {
		return repository.preauthorizeRollbackResult, nil
	}
	return storedJob{}, ErrUnavailable
}
func (repository *recordingLifecycleRepository) LoadRollbackCandidate(context.Context, int64, int64) (configuration.Definition, configuration.RuntimeState, error) {
	if repository.rollbackDefinition.Attributes != nil || repository.rollbackRuntime.AttributeValues != nil {
		return repository.rollbackDefinition, repository.rollbackRuntime, nil
	}
	return configuration.Definition{}, configuration.RuntimeState{}, ErrUnavailable
}
func (repository *recordingLifecycleRepository) Cancel(context.Context, int64, int64) (storedJob, error) {
	return storedJob{}, ErrUnavailable
}
func (repository *recordingLifecycleRepository) Rollback(context.Context, int64, int64) (storedJob, error) {
	return storedJob{}, ErrUnavailable
}
func (repository *recordingLifecycleRepository) Get(context.Context, int64, int64) (storedJob, error) {
	if repository.getResult.ID != 0 {
		return repository.getResult, nil
	}
	return storedJob{}, ErrUnavailable
}

type recordingConfigurationBarrier struct {
	candidate    BarrierCandidate
	boundary     configuration.Boundary
	err          error
	calls        int
	pendingCalls int
	pendingOwner OwnerFence
}

func (barrier *recordingConfigurationBarrier) ApplyConfigurationBarrier(_ context.Context, _ int64, candidate BarrierCandidate) (configuration.Boundary, error) {
	barrier.calls++
	barrier.candidate = candidate
	return barrier.boundary, barrier.err
}

func (barrier *recordingConfigurationBarrier) ApplyPendingConfigurationBarrier(_ context.Context, owner OwnerFence, candidate BarrierCandidate) (configuration.Boundary, error) {
	barrier.pendingCalls++
	barrier.pendingOwner = owner
	barrier.candidate = candidate
	return barrier.boundary, barrier.err
}

func (repository *recordingPreviewRepository) Preview(_ context.Context, command previewCommand) (storedPreview, error) {
	repository.command = command
	return repository.result, repository.err
}

func decodedEnvelope(t *testing.T) Envelope {
	t.Helper()
	result, _, err := Decode(jsonReader(t, validEnvelopeWire()), 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func previewCommandForTest(t *testing.T, now time.Time) previewCommand {
	t.Helper()
	envelope := decodedEnvelope(t)
	return previewCommand{AccountID: 7, Definition: envelope.Definition, Runtime: envelope.Runtime, RoomSuggestion: envelope.RoomSuggestion, Source: envelope.Source, Counts: envelope.Counts, Report: Report{}, Hash: envelope.Hash, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
}

func sealedCandidateForTest(t *testing.T, definition configuration.Definition, runtime configuration.RuntimeState) ComposeCandidate {
	t.Helper()
	definition.MigrationHash = ""
	normalizedDefinition, normalizedRuntime, err := configuration.Normalize(definition, runtime)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(struct {
		Definition configuration.Definition   `json:"definition"`
		Runtime    configuration.RuntimeState `json:"runtime"`
	}{normalizedDefinition, normalizedRuntime})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(canonical)
	normalizedDefinition.MigrationHash = hex.EncodeToString(hash[:])
	return ComposeCandidate{Definition: normalizedDefinition, Runtime: normalizedRuntime, Ready: true, Hash: hash}
}

func activeDefinitionJSONForTest(t *testing.T, candidate ComposeCandidate) []byte {
	t.Helper()
	definition := candidate.Definition
	definition.MigrationHash = ""
	return mustJSON(t, definition)
}

func expectPreviewNow(mock sqlmock.Sqlmock, now time.Time) {
	mock.ExpectQuery(regexp.QuoteMeta(previewDatabaseNowQuery)).WillReturnRows(sqlmock.NewRows([]string{"now"}).AddRow(now))
}
func activePreviewRows(id int64, status string, expiry time.Time, command previewCommand) *sqlmock.Rows {
	report, _ := json.Marshal(Report{Counts: command.Counts, Warnings: []string{"persisted"}, Ignored: []string{"/persisted"}})
	return sqlmock.NewRows([]string{"id", "status", "expires_at", "request_hash", "source_app_version", "source_schema_version", "room_suggestion", "report_json"}).AddRow(id, status, expiry, command.Hash[:], "persisted-app", 4, "persisted-room", report)
}
