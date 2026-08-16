# Hosted Configuration and Desktop Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver versioned online configuration, durable current gameplay state, and a safe preview/apply/rollback path from the local EXE to the authenticated online account.

**Architecture:** `configuration` stores immutable normalized definitions separately from optimistic, frequently updated gameplay state. `migration` accepts a dedicated bounded JSON envelope, reconstructs a `gameplay.Snapshot` from allowlisted fields, computes a server-side canonical hash, and produces a preview before any write. Applying switches configuration and state atomically or waits for the current live session to end.

**Tech Stack:** Go 1.26, MySQL 8 JSON columns and transactions, shared `gameplay` module, TypeScript, Vite, Vitest.

## Global Constraints

- Run after the foundation and identity/invitations plans pass review.
- Configuration belongs to the authenticated account and follows it across room changes.
- Definitions are append-only versions; current state has an optimistic revision.
- Migration replaces configuration and current gameplay state as one unit; it never field-merges.
- Raw migration upload exists only for the request duration and is never written to disk, logs, object storage, or backups.
- Accept JSON only, maximum 2 MiB, with bounded depth/count/string lengths.
- Migrate attribute values, activity progress, and gift-target received counts; exclude credentials, viewer identity, history, logs, media paths, crop data, update/tutorial settings, caches, WAL, and dedupe keys.
- A local room ID is a suggestion requiring separate confirmation.
- Running accounts stage migration until the current session naturally ends; no manual stop control is introduced.
- Preview drafts expire after 24 hours; rollback snapshots expire after 7 days; each account gets at most 5 previews per day.

---

## File Map

- `goserver/internal/hosted/configuration/model.go`: normalized definition/state split and revisions.
- `goserver/internal/hosted/configuration/repository.go`: append config, CAS state, activate version, rollback snapshot.
- `goserver/internal/hosted/configuration/http.go`: authenticated read/save endpoints.
- `goserver/internal/hosted/migration/envelope.go`: bounded format v1 decoding and normalization.
- `goserver/internal/hosted/migration/service.go`: preview, apply, pending activation, cancel, rollback, idempotency.
- `goserver/internal/hosted/migration/http.go`: multipart-free JSON upload and confirmation routes.
- `goserver/internal/hosted/store/mysqlstore/migrations/0004_configuration_and_migration.sql`: config/state/jobs/session-status tables.
- `src/migration.ts`: desktop-only safe migration exporter.
- `src/ui/config/config.ts`: desktop “迁移到在线版” download action.
- `tests/migration.test.ts`: exporter privacy and schema contract.
- `src/hosted/configuration.ts`, `src/hosted/migration.ts`: online edit and migration preview UI.
- `tests/hosted-configuration.test.ts`, `tests/hosted-migration.test.ts`: hosted browser contracts.

---

### Task 1: Add immutable configuration versions and optimistic runtime state

**Files:**
- Create: `goserver/internal/hosted/configuration/model.go`
- Create: `goserver/internal/hosted/configuration/model_test.go`
- Create: `goserver/internal/hosted/configuration/repository.go`
- Create: `goserver/internal/hosted/configuration/repository_test.go`
- Create: `goserver/internal/hosted/store/mysqlstore/migrations/0004_configuration_and_migration.sql`
- Modify: `goserver/internal/hosted/store/mysqlstore/store_test.go`

**Interfaces:**
- Produces: `configuration.Split(gameplay.Snapshot)` and `Join(Definition, RuntimeState)`.
- Produces: `Repository.CreateVersion`, `LoadActive`, `CompareAndSwapState`, and `Activate`.

- [ ] **Step 1: Write failing split/join tests**

Prove definitions contain no current values and state contains no executable formulas:

```go
func TestSplitSeparatesDefinitionFromMutableState(t *testing.T) {
    snapshot := fixtureSnapshot()
    definition, state := Split(snapshot)
    definitionJSON, _ := json.Marshal(definition)
    stateJSON, _ := json.Marshal(state)
    if bytes.Contains(definitionJSON, []byte(`"value":42`)) { t.Fatal("value leaked into definition") }
    if bytes.Contains(stateJSON, []byte(`"formula"`)) { t.Fatal("formula leaked into runtime state") }
    if got := Join(definition, state); !reflect.DeepEqual(got, snapshot) { t.Fatal("join mismatch") }
}
```

Run: `go -C goserver test ./internal/hosted/configuration -count=1`

Expected: FAIL because the package does not exist.

- [ ] **Step 2: Define version and state records**

Use:

```go
type Version struct { ID, AccountID int64; Number uint64; Definition Definition; Source string; CreatedAt time.Time }
type State struct { AccountID, ConfigVersionID int64; Revision uint64; Runtime RuntimeState; UpdatedAt time.Time }
var ErrRevisionConflict = errors.New("configuration revision conflict")
```

`Source` accepts only `manual`, `migration`, and `rollback`.

- [ ] **Step 3: Add database schema and repository semantics**

Create `account_config_versions`, `account_runtime_state`, `account_room_suggestions`, `migration_jobs`, and `live_sessions`. Use `BIGINT UNSIGNED` IDs consistently with `streamer_accounts`, unique `(account_id, number)`, one state/suggestion row per account, foreign keys, JSON validity checks where MySQL supports them, and indexed job hash/status/expiry. A room suggestion is untrusted input awaiting the later runtime `SetRoom` confirmation; saving or applying it never sets a target room or opens a session. `CreateVersion` allocates number under account-row lock. `CompareAndSwapState` updates only when the submitted revision matches and increments it once.

- [ ] **Step 4: Test atomic activation**

With `go-sqlmock`, require one transaction to insert a version, replace current runtime state, point `streamer_accounts.active_config_version_id`, and optionally mark a migration applied. Roll back all steps on any error.

- [ ] **Step 5: Verify and commit**

Run focused/race tests and full Go tests; commit the listed files as `feat: version hosted configuration`.

---

### Task 2: Add authenticated configuration read and save endpoints

**Files:**
- Create: `goserver/internal/hosted/configuration/service.go`
- Create: `goserver/internal/hosted/configuration/service_test.go`
- Create: `goserver/internal/hosted/configuration/http.go`
- Create: `goserver/internal/hosted/configuration/http_test.go`
- Modify: `goserver/internal/hosted/app/app.go`
- Modify: `goserver/internal/hosted/app/app_test.go`
- Modify: `goserver/cmd/hosted/main.go`
- Modify: `goserver/cmd/hosted/main_test.go`

**Interfaces:**
- Produces: `Service.Load`, `SaveDefinition`, `SaveStateCommand`, and `SuggestRoom`.

- [ ] **Step 1: Write failing tenant/revision tests**

Test current-account derivation from context, missing session, stale definition number, stale state revision, invalid gameplay normalization, unrelated-account record injection, and room suggestion not automatically starting a session.

- [ ] **Step 2: Implement service commands**

Use explicit commands without account fields:

```go
type SaveDefinitionCommand struct { ExpectedVersion uint64; Definition Definition }
type SaveStateCommand struct { ExpectedRevision uint64; Runtime RuntimeState }
type RoomSuggestionCommand struct { RoomID string }
```

The service receives `accountID int64` separately from trusted middleware context. Normalize through `gameplay.Normalize(Join(...))` before any write. `SuggestRoom` only creates/replaces the account's pending suggestion and never changes a target room or `live_sessions`.

- [ ] **Step 3: Add HTTP routes**

```text
GET  /api/configuration
PUT  /api/configuration/definition
PUT  /api/configuration/state
PUT  /api/configuration/room-suggestion
```

Limit bodies to 2 MiB, reject second JSON values, require CSRF/Origin for writes, return `409 revision_conflict` on stale edits, and return the new version/revision after success.

Wire the repository/service/HTTP handler in the real `cmd/hosted` composition root using the borrowed shared MySQL pool and `identity.HTTPHandler.Authenticate`; pass a dedicated Configuration handler through `app.Dependencies`. Main-level tests must prove all four method-routes are reachable and do not fall through broader auth/admin prefixes.

- [ ] **Step 4: Verify contract and full regression**

Run configuration service/HTTP tests with `-count=10`, race tests, then full Go tests.

- [ ] **Step 5: Commit**

Stage only configuration/app files and commit `feat: add hosted configuration API`.

---

### Task 3: Add the desktop migration exporter as a cherry-pickable local feature

**Files:**
- Create: `src/migration.ts`
- Create: `tests/migration.test.ts`
- Modify: `src/ui/config/config.ts`

**Interfaces:**
- Produces: `createOnlineMigration(state: AppState, appVersion: string, exportedAt: Date): OnlineMigrationV1`.

- [ ] **Step 1: Write the failing allowlist test**

Build a state containing every sensitive/local field and assert the serialized package excludes them:

```ts
const text = JSON.stringify(createOnlineMigration(state, '0.4.4', new Date('2026-08-16T00:00:00Z')));
for (const forbidden of ['recentGifts', 'stats', 'log', 'giftReceipts', 'contributions', 'giftClipCrops', 'autoUpdate', 'tutorialCompletedLessons', 'cookie', 'senderUid', 'uname']) {
  expect(text).not.toContain(forbidden);
}
expect(JSON.parse(text)).toMatchObject({ kind: 'gift-panel-online-migration', migrationVersion: 1 });
```

Also assert attribute values, activity timestamps/milestones, and each gift-target `received` value are present; only referenced catalog gifts are exported. Strip every `imageUrl`, `imgBasic`, `gif`, `webp`, `effectMp4`, and `effectMp4Json` field so a migration package cannot introduce an arbitrary remote asset.

Run: `npx vitest run tests/migration.test.ts`

Expected: FAIL because the module does not exist.

- [ ] **Step 2: Implement a fresh allowlist**

Do not spread `state.settings` or reuse raw `AppState`. Construct `payload.definition`, `payload.runtime`, and `payload.roomSuggestion` field by field. Appearance settings include theme/font/accent/alignment/opacity/connection visibility only. Referenced gift metadata contains ID/name/price/coin type but no asset URL; the hosted B 站 gateway refreshes official resource URLs later. Deep-clone output before returning.

- [ ] **Step 3: Add the desktop action**

In the existing “配置与数据” card, add `迁移到在线版` next to ordinary export. Download `gift-panel-migration-v1-YYYY-MM-DD.json`; show copy explaining that local data remains unchanged and the file contains no login or viewer history.

- [ ] **Step 4: Run desktop verification**

Run:

```powershell
npx vitest run tests/migration.test.ts tests/storage.test.ts tests/wizard.test.ts
npm run typecheck
npm test
npm run build:ui
git diff --check
```

Expected: PASS.

- [ ] **Step 5: Make the isolated desktop commit**

```powershell
git add -- src/migration.ts src/ui/config/config.ts tests/migration.test.ts
git commit -m "feat: export online migration package"
```

This commit contains no hosted server/UI files and is the only migration-plan commit intended to be reviewed and cherry-picked back to `master` so future local EXE releases can generate migration packages.

---

### Task 4: Implement bounded migration preview and idempotency

**Files:**
- Create: `goserver/internal/hosted/migration/envelope.go`
- Create: `goserver/internal/hosted/migration/envelope_test.go`
- Create: `goserver/internal/hosted/migration/service.go`
- Create: `goserver/internal/hosted/migration/service_test.go`

**Interfaces:**
- Produces: `Decode(io.Reader, maxBytes int64) (Envelope, Report, error)`.
- Produces: `Service.Preview(context.Context, accountID int64, Envelope) (Preview, error)`.

- [ ] **Step 1: Write failing parser limit tests**

Test wrong kind/version, more than 2 MiB, JSON depth over 32, more than 200 attributes/500 rules/100 activities/100 panels/200 items per panel, strings over 4,096 runes, non-finite numeric encodings, second JSON value, forbidden fields, unknown fields reported as ignored, and a newer configuration schema rejected.

- [ ] **Step 2: Implement format-v1 decoding**

Parse top-level `json.RawMessage`, copy only known keys, and record unknown JSON-pointer paths in `Report.Ignored`. Decode definition/runtime into explicit wire structs, strip desktop-only settings again server-side, join and normalize through `gameplay.Normalize`, then marshal the normalized structs to compute SHA-256.

- [ ] **Step 3: Implement preview records**

Rate-limit to 5 successful preview creations per account per UTC day. Reuse an unexpired job with the same account/hash rather than inserting a duplicate. Save only normalized definition/runtime, counts/warnings/hash/source versions, and optional room suggestion; never save raw bytes.

- [ ] **Step 4: Test preview immutability and expiry**

Assert preview creates no config version/state write, belongs only to the current account, expires after 24 hours, and does not include configuration body in audit/log output.

- [ ] **Step 5: Verify and commit**

Run migration tests with `-count=10`, race tests, full Go tests, and commit `feat: preview desktop migrations`.

---

### Task 5: Apply, stage, cancel, and roll back migrations

**Files:**
- Modify: `goserver/internal/hosted/migration/service.go`
- Modify: `goserver/internal/hosted/migration/service_test.go`
- Create: `goserver/internal/hosted/migration/http.go`
- Create: `goserver/internal/hosted/migration/http_test.go`
- Modify: `goserver/internal/hosted/app/app.go`
- Modify: `goserver/internal/hosted/app/app_test.go`
- Modify: `goserver/cmd/hosted/main.go`
- Modify: `goserver/cmd/hosted/main_test.go`
- Modify: `goserver/internal/hosted/identity/service.go`
- Modify: `goserver/internal/hosted/identity/service_test.go`

**Interfaces:**
- Produces: `Apply`, `ApplyPendingAfterSession`, `Cancel`, and `Rollback`.

- [ ] **Step 1: Write failing lifecycle tests**

Cover inactive immediate apply, active-session pending apply, cancellation, natural session end apply, duplicate confirmation idempotency, matching/mismatched/reused Bilibili account proof and proof older than 15 minutes, rollback within 7 days, rollback expiry, and preservation of historical session aggregates.

- [ ] **Step 2: Implement atomic apply**

For an inactive account, one transaction captures old config/state IDs, inserts migration version/state, switches active version, stores rollback snapshot/expiry, and sets job `applied`. For an active account, set only `pending`; `ApplyPendingAfterSession` performs the same transaction after the session row is closed.

- [ ] **Step 3: Implement cancellation and rollback**

Only the owning account can cancel `previewed/pending`. Rollback creates a new version with source `rollback`, restores the saved runtime state, leaves historical sessions unchanged, and marks the job `rolled_back`. It never reuses or deletes old version rows.

- [ ] **Step 4: Add HTTP routes and recent-login enforcement**

```text
POST   /api/migrations/preview
POST   /api/migrations/{id}/apply
DELETE /api/migrations/{id}
POST   /api/migrations/{id}/rollback
GET    /api/migrations/{id}
```

Preview accepts `application/json` directly, not multipart. Apply/rollback require a B 站 identity proof completed within 15 minutes and CSRF/Origin validation.

Add a narrow identity seam `ConsumeAccountProof(ctx, challengeID string, accountID int64, maxAge time.Duration) error`. It consumes exactly one existing-account challenge in the verified/login-ready stage, compares the repository-derived bound account ID, checks the stored Bilibili completion time against `maxAge`, and erases the challenge on every terminal path. It returns no UID or Cookie. Migration apply/rollback accepts a `challengeId` and calls this seam in the same request; pending/unavailable results remain retryable, while success/mismatch/expiry/reuse are terminal. Wire the migration repository/service/HTTP handler in real `cmd/hosted` composition using the same DB, identity service, Authenticate middleware, resolver, and limiter, and expose only the five exact migration method-routes through `app.Dependencies`.

- [ ] **Step 5: Verify and commit**

Run lifecycle tests with `-count=10`, full/race Go tests, and commit `feat: apply hosted migrations`.

---

### Task 6: Add online configuration and migration views

**Files:**
- Create: `src/hosted/configuration.ts`
- Create: `src/hosted/migration.ts`
- Create: `tests/hosted-configuration.test.ts`
- Create: `tests/hosted-migration.test.ts`
- Modify: `src/hosted/api.ts`
- Modify: `src/hosted/main.ts`

**Interfaces:**
- Produces: revision-aware editing and preview/apply/cancel/rollback UI.

- [ ] **Step 1: Write failing view tests**

Test optimistic conflict refresh, file type/size precheck, server preview group rendering, explicit room suggestion checkbox, active-session pending state, cancel, 7-day rollback countdown, and errors never echoing uploaded JSON.

- [ ] **Step 2: Implement revision-aware configuration adapters**

Send `expectedVersion`/`expectedRevision`; on `revision_conflict`, preserve the unsaved draft in memory, fetch authoritative state, and show a compare/retry prompt. Never put configuration JSON in URL or browser storage.

- [ ] **Step 3: Implement migration UX**

Accept `.json`, enforce the client-side 2 MiB hint, upload to preview, render counts and ignored paths, require an explicit replacement confirmation, and keep room suggestion unchecked by default. Client validation is advisory; render only server-authoritative results.

- [ ] **Step 4: Run frontend/full verification**

Run the two hosted tests, migration/storage desktop tests, typecheck, hosted build, all Vitest tests, all Go tests, and `git diff --check`.

- [ ] **Step 5: Commit**

Stage hosted UI/API/test files only and commit `feat: add configuration migration UI`.

---

## Plan Completion Gate

Demonstrate with tests that the raw file is never persisted, server-side filtering repeats the desktop allowlist, preview is write-free, active sessions stage rather than hot-swap, duplicate files are idempotent, and rollback restores both configuration and current state without touching history. Then request review before runtime/OBS work.
