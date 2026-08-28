package migration

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/configuration"
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
	candidateHash := [32]byte{1}
	definition.MigrationHash = hex.EncodeToString(candidateHash[:])
	candidate := ComposeCandidate{Definition: definition, Runtime: runtime, Ready: true, Hash: candidateHash}
	definitionJSON, runtimeJSON := mustJSON(t, definition), mustJSON(t, runtime)
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
	candidateHash := [32]byte{2}
	definition.MigrationHash = hex.EncodeToString(candidateHash[:])
	candidate := ComposeCandidate{Definition: definition, Runtime: runtime, Ready: true, Hash: candidateHash}
	definitionJSON, runtimeJSON := mustJSON(t, definition), mustJSON(t, runtime)
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

	job, err := NewRepository(database).(lifecycleRepository).Apply(context.Background(), applyCommand{AccountID: 7, JobID: 19, KeepRoomSuggestion: true})
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
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(88, 3, 6, []byte(`{"attributeValues":{"health":1}}`)))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobPreviewed, now.Add(time.Hour), 0, nil, nil, nil, nil, []byte(`{"attributes":[]}`), []byte(`{"attributeValues":{}}`), "12345"))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleBaseQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"base_config_version_number", "base_state_revision"}).AddRow(3, 6))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleOpenSessionQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(12))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE migration_jobs SET status = 'pending', keep_room_suggestion = ? WHERE id = ? AND account_id = ? AND status = 'previewed'")).WithArgs(true, int64(19), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	job, err := NewRepository(database).(lifecycleRepository).Apply(context.Background(), applyCommand{AccountID: 7, JobID: 19, KeepRoomSuggestion: true})
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
	pendingHash := [32]byte{3}
	pendingDefinition.MigrationHash = hex.EncodeToString(pendingHash[:])
	pendingDefinitionJSON, pendingRuntimeJSON := mustJSON(t, pendingDefinition), mustJSON(t, pendingRuntime)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(88, 3, 6, []byte(`{"attributeValues":{"health":1}}`)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT expires_at > UTC_TIMESTAMP(6) FROM runtime_account_owners WHERE account_id = ? AND owner_token = ? AND fencing_epoch = ? FOR UPDATE")).WithArgs(int64(7), fence.Token[:], fence.Epoch).WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobPending, now.Add(time.Hour), 1, nil, nil, nil, nil, pendingDefinitionJSON, pendingRuntimeJSON, "12345"))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleBaseQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"base_config_version_number", "base_state_revision"}).AddRow(3, 6))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleOpenSessionQuery)).WithArgs(int64(7)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleNextVersionQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number"}).AddRow(4))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleInsertVersionQuery)).WithArgs(int64(7), uint64(4), pendingDefinitionJSON, "migration", now).WillReturnResult(sqlmock.NewResult(99, 1))
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

type recordingLifecycleRepository struct {
	recordingPreviewRepository
	apply                    applyCommand
	applyResult              storedJob
	applyErr                 error
	composition              storedComposition
	loadAccountID, loadJobID int64
	applyCalls               int
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
func (repository *recordingLifecycleRepository) ApplyPendingAfterSession(context.Context, OwnerFence, int64) (storedJob, error) {
	return storedJob{}, ErrUnavailable
}
func (repository *recordingLifecycleRepository) PreauthorizeApply(context.Context, int64, int64) (storedJob, error) {
	return storedJob{}, ErrUnavailable
}
func (repository *recordingLifecycleRepository) PreauthorizeRollback(context.Context, int64, int64) (storedJob, error) {
	return storedJob{}, ErrUnavailable
}
func (repository *recordingLifecycleRepository) Cancel(context.Context, int64, int64) (storedJob, error) {
	return storedJob{}, ErrUnavailable
}
func (repository *recordingLifecycleRepository) Rollback(context.Context, int64, int64) (storedJob, error) {
	return storedJob{}, ErrUnavailable
}
func (repository *recordingLifecycleRepository) Get(context.Context, int64, int64) (storedJob, error) {
	return storedJob{}, ErrUnavailable
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

func expectPreviewNow(mock sqlmock.Sqlmock, now time.Time) {
	mock.ExpectQuery(regexp.QuoteMeta(previewDatabaseNowQuery)).WillReturnRows(sqlmock.NewRows([]string{"now"}).AddRow(now))
}
func activePreviewRows(id int64, status string, expiry time.Time, command previewCommand) *sqlmock.Rows {
	report, _ := json.Marshal(Report{Counts: command.Counts, Warnings: []string{"persisted"}, Ignored: []string{"/persisted"}})
	return sqlmock.NewRows([]string{"id", "status", "expires_at", "request_hash", "source_app_version", "source_schema_version", "room_suggestion", "report_json"}).AddRow(id, status, expiry, command.Hash[:], "persisted-app", 4, "persisted-room", report)
}
