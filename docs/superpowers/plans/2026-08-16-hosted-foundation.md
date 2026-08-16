# Hosted Foundation and Shared Gameplay Core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create the isolated hosted-service branch, extract one privacy-safe shared gameplay module, and deliver a Linux hosted server skeleton with a versioned MySQL schema and authenticated UI shell seams.

**Architecture:** Keep the existing desktop executable at `goserver/package main`, but move pure rule, timer, activity, and gift-target transitions behind the small `gameplay.Engine` interface. The local executable uses an adapter that preserves its existing history and viewer features; the hosted command uses the same engine with MySQL-backed configuration and state. A new `cmd/hosted` command owns only composition and process lifecycle.

**Tech Stack:** Go 1.26, `net/http`, `log/slog`, MySQL 8, `github.com/go-sql-driver/mysql`, TypeScript, Vite 5, Vitest 2.

## Global Constraints

- Execute in a worktree created from the current local `master` after verifying it contains approved design commit `50635da` and these plan documents; do not develop hosted code on `master`.
- Keep `master` as the desktop EXE branch; do not remove or weaken existing desktop behavior.
- `gameplay` must not accept or return viewer UID, nickname, avatar, Cookie, token, log entry, gift receipt, or contribution ledger data.
- The hosted command must compile on Linux amd64 and must not import Windows-only update, tray, FFmpeg, or DPAPI code.
- MySQL must never listen on a public interface in deployment.
- Every behavior change uses RED→GREEN tests and an independent commit.
- Do not stage, delete, or overwrite unrelated local experimental and untracked files.

---

## File Map

- `goserver/internal/gameplay/model.go`: exported privacy-safe gameplay snapshot, event, effect, transition, and rule-limit enforcement state types.
- `goserver/internal/gameplay/engine.go`: the deep `Engine` interface implementation for gifts, timers, activity transitions, and target progress.
- `goserver/internal/gameplay/*_test.go`: interface-level parity and privacy-contract tests.
- `goserver/gameplay_adapter.go`: desktop `appState`/`giftEvent` adapter; viewer history stays outside the shared module.
- `goserver/background_runtime.go`, `activity_runtime.go`, `formula.go`, `gift_targets.go`: delegate pure state transitions to `gameplay` while retaining desktop HTTP and persistence adapters.
- `goserver/cmd/hosted/main.go`: hosted process composition, signals, and graceful shutdown.
- `goserver/internal/hosted/app/app.go`: HTTP router and lifecycle interface.
- `goserver/internal/hosted/platform/config.go`: strict environment loading.
- `goserver/internal/hosted/store/mysqlstore/migrations/*.sql`: schema migrations.
- `goserver/internal/hosted/store/mysqlstore/store.go`: connection, migration runner, and health checks.
- `src/hosted/main.ts`, `src/hosted/shell.ts`, `hosted.html`, `vite.hosted.config.ts`: hosted UI entry and signed-out shell.
- `tests/hosted-build.test.ts`: build and asset-boundary contracts.
- `package.json`: hosted build/typecheck commands.

---

### Task 1: Create and verify the isolated implementation worktree

**Files:**
- No product files changed.

**Interfaces:**
- Consumes: the current local `master`, with `50635da` in its ancestry and no hosted implementation.
- Produces: branch `codex/hosted-service` in a separate worktree.

- [ ] **Step 1: Use the required worktree skill**

Invoke `superpowers:using-git-worktrees`. Resolve the absolute target and verify it is outside `E:\bilibili`; preferred target is `E:\bilibili-hosted`.

- [ ] **Step 2: Create the branch from the approved baseline**

First verify the approved design is an ancestor, then create from `master`:

```powershell
git -c safe.directory=E:/bilibili merge-base --is-ancestor 50635da master
git -c safe.directory=E:/bilibili worktree add E:\bilibili-hosted -b codex/hosted-service master
```

Expected: a new worktree on `codex/hosted-service`; `E:\bilibili` remains on `master`.

- [ ] **Step 3: Install frontend dependencies in the new worktree**

Run:

```powershell
npm ci
```

Expected: exit 0 with `package-lock.json` unchanged.

- [ ] **Step 4: Record the green baseline**

Run:

```powershell
npm test
npm run typecheck
go -C goserver test ./... -count=1 -timeout=300s
node scripts/verify-go-linux-compile.mjs
git status --short
```

Expected: all verification commands pass; only pre-existing approved files, if any, appear in status. Stop and diagnose any baseline failure before Task 2.

---

### Task 2: Define the privacy-safe gameplay module interface

**Files:**
- Create: `goserver/internal/gameplay/model.go`
- Create: `goserver/internal/gameplay/model_test.go`

**Interfaces:**
- Produces: `gameplay.Snapshot`, `GiftInfo`, `Gift`, `Effect`, `Transition`, and `Engine`.
- Invariant: none of these types contain viewer identity or persistence-specific history.

- [ ] **Step 1: Write the failing privacy and shape tests**

Create a reflection test that rejects forbidden JSON names and proves JSON round-trip stability:

```go
func TestPublicModelExcludesViewerAndCredentialFields(t *testing.T) {
    forbidden := regexp.MustCompile(`(?i)uid|uname|nickname|avatar|cookie|token|receipt|contribution|log`)
    for _, value := range []any{Snapshot{}, Gift{}, Effect{}, Transition{}} {
        typ := reflect.TypeOf(value)
        for i := 0; i < typ.NumField(); i++ {
            field := typ.Field(i)
            if forbidden.MatchString(field.Name + " " + field.Tag.Get("json")) {
                t.Fatalf("%s exposes forbidden field %s", typ.Name(), field.Name)
            }
        }
    }
}
```

Add `TestSnapshotJSONRoundTrip` with one attribute, rule, activity, gift target, timer, and referenced gift.

Run: `go -C goserver test ./internal/gameplay -run 'TestPublicModel' -count=1`

Expected: FAIL because the package does not exist.

- [ ] **Step 2: Define the exact external interface**

Use this small interface:

```go
type Engine struct{}

func (Engine) ApplyGift(current Snapshot, gift Gift, now time.Time) (Transition, error)
func (Engine) ApplyTimers(current Snapshot, dueRuleIDs []string, now time.Time) (Transition, error)
func (Engine) TransitionActivity(current Snapshot, activityID, action string, now time.Time) (Transition, error)

type Transition struct {
    Next    Snapshot `json:"next"`
    Effects []Effect `json:"effects"`
    Changed bool     `json:"changed"`
}

type Gift struct {
    GiftID int     `json:"giftId"`
    BlindGiftID int `json:"blindGiftId,omitempty"`
    Count int      `json:"count"`
    Price float64  `json:"price"`
    IdentityRank int `json:"identityRank"`
    EventID string `json:"eventId,omitempty"`
}
```

`Snapshot` contains room ID, configuration definitions, attributes with current values, activity state, gift-target progress, referenced catalog, and simple-play metadata. It contains no recent gifts, daily stats, logs, receipts, viewer ledger, or internal ingestion keys.

- [ ] **Step 3: Add bounded normalization**

Implement `Normalize(Snapshot) (Snapshot, error)` and reject duplicate/blank IDs, non-finite numbers, more than 200 attributes, 500 rules, 100 timers, 100 activities, 100 panels, 200 items per panel, and strings longer than 4,096 runes. Deep-copy all maps/slices before returning.

- [ ] **Step 4: Run the model tests**

Run:

```powershell
go -C goserver test ./internal/gameplay -count=1
go -C goserver test -race ./internal/gameplay -count=3
```

Expected: PASS.

- [ ] **Step 5: Commit the model seam**

```powershell
git add -- goserver/internal/gameplay/model.go goserver/internal/gameplay/model_test.go
git diff --cached --check
git commit -m "feat: define shared gameplay model"
```

---

### Task 3: Move gameplay transitions behind `gameplay.Engine`

**Files:**
- Modify: `goserver/internal/gameplay/model.go`
- Modify: `goserver/internal/gameplay/model_test.go`
- Create: `goserver/internal/gameplay/engine.go`
- Create: `goserver/internal/gameplay/engine_test.go`
- Create: `goserver/gameplay_adapter.go`
- Modify: `goserver/background_runtime.go`
- Modify: `goserver/activity_runtime.go`
- Modify: `goserver/formula.go`
- Modify: `goserver/gift_targets.go`
- Modify only as required for aliases/conversion: `goserver/state.go`

**Interfaces:**
- Consumes: `gameplay.Engine` and model types from Task 2.
- Produces: `snapshotFromAppState(appState) gameplay.Snapshot` and `applyGameplayTransition(*appState, gameplay.Transition)`.
- Plan repair discovered during execution: daily-limit parity requires a privacy-safe `RuleLimitState` in `Snapshot`, containing only the current local date and per-rule applied counts. It is gameplay enforcement state, not analytics: it must not contain gift totals, viewer identity, logs, receipts, contributions, or historical days.

- [ ] **Step 1: Lock cross-adapter parity with failing tests**

Add table cases that run the same fixture through the legacy desktop entry and through `gameplay.Engine`, then compare attributes, activities, target counts, and effects:

```go
func TestGameplayAdapterMatchesGiftTransition(t *testing.T) {
    state := semanticState(giftRule{ID: "r1", GiftID: 1, AttributeName: "积分", Formula: "积分+count"}, 2)
    gift := giftEvent{GiftID: 1, Num: 3, Price: 100, Rnd: "event-1"}
    want := state
    applyGiftEvent(&want, gift)

    got := state
    transition, err := gameplay.Engine{}.ApplyGift(snapshotFromAppState(got), gameplayGift(gift), time.Unix(100, 0))
    if err != nil { t.Fatal(err) }
    applyGameplayTransition(&got, transition)
    assertGameplayFieldsEqual(t, got, want)
}
```

Cover minimum price, cap, daily limit (including date rollover through `RuleLimitState`), disabled rules, timer rules, activity gates/milestones/timeouts, blind-box parent target matching, reset/start/lock/settle, invalid formula isolation, and zero-count normalization.

Run: `go -C goserver test ./... -run 'TestGameplayAdapter' -count=1`

Expected: FAIL because the engine implementation and adapter do not exist.

- [ ] **Step 2: Move pure formula and activity logic**

Move parsing/evaluation and activity transition implementation into `internal/gameplay`; retain root wrappers only where desktop HTTP tests call the old unexported functions. The wrappers must convert once, call the engine, and copy the resulting gameplay fields back.

- [ ] **Step 3: Move gift-rule, timer, and target transitions**

Implement `Engine.ApplyGift` and `ApplyTimers` as pure copy-in/copy-out operations. `Engine.ApplyGift` resets `RuleLimitState` when `now` enters a new local date, enforces `DailyLimit`, and increments only successfully applied rule counts. `Effect` contains only `RuleID`, `AttributeName`, `Delta`, `ValueAfter`, `TriggerName`, and target/activity notices. Desktop gift totals, multi-day stats, log, receipt, animation, profile lookup, and contribution updates remain in the root adapter after the transition returns.

- [ ] **Step 4: Run parity, race, and full regression**

Run:

```powershell
go -C goserver test ./... -run 'TestGameplayAdapter|TestApplyGiftEvent|TestApplyTimerRules|TestActivity|TestGiftTarget' -count=10
go -C goserver test -race ./... -run 'TestGameplayAdapter|TestApplyGiftEvent|TestActivity' -count=3
go -C goserver test ./... -count=1 -timeout=300s
npm test
npm run typecheck
```

Expected: PASS with unchanged desktop behavior.

- [ ] **Step 5: Commit the shared engine**

```powershell
git add -- goserver/internal/gameplay goserver/gameplay_adapter.go goserver/background_runtime.go goserver/activity_runtime.go goserver/formula.go goserver/gift_targets.go goserver/state.go docs/superpowers/plans/2026-08-16-hosted-foundation.md
git diff --cached --check
git commit -m "refactor: extract shared gameplay engine"
```

---

### Task 4: Add the hosted server skeleton and MySQL migration runner

**Files:**
- Create: `goserver/cmd/hosted/main.go`
- Create: `goserver/internal/hosted/app/app.go`
- Create: `goserver/internal/hosted/app/app_test.go`
- Create: `goserver/internal/hosted/platform/config.go`
- Create: `goserver/internal/hosted/platform/config_test.go`
- Create: `goserver/internal/hosted/store/mysqlstore/store.go`
- Create: `goserver/internal/hosted/store/mysqlstore/store_test.go`
- Create: `goserver/internal/hosted/store/mysqlstore/migrations/0001_foundation.sql`
- Modify: `goserver/go.mod`
- Modify: `goserver/go.sum`

**Interfaces:**
- Produces: `app.New(app.Dependencies) http.Handler`.
- Produces: `mysqlstore.Open(ctx, dsn) (*Store, error)` and `(*Store).Migrate(ctx) error`.

- [ ] **Step 1: Write failing config and health tests**

Test strict required variables and a non-secret health response:

```go
func TestHealthDoesNotExposeConfiguration(t *testing.T) {
    handler := app.New(app.Dependencies{DB: fakeHealth{Err: nil}})
    response := httptest.NewRecorder()
    handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
    if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "MYSQL") {
        t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
    }
}
```

`platform.Load` must require `HOSTED_LISTEN_ADDR=127.0.0.1:12500`, `HOSTED_MYSQL_DSN`, `HOSTED_ENCRYPTION_KEY_FILE`, and `HOSTED_HMAC_KEY_FILE`; reject wildcard/public listen addresses.

- [ ] **Step 2: Add the first idempotent migration**

`0001_foundation.sql` creates only `schema_migrations` and a `service_health_markers` table. The runner embeds migrations, obtains MySQL advisory lock `GET_LOCK('gift_panel_schema', 30)`, applies each file in a transaction where supported, records SHA-256, and refuses a changed checksum.

- [ ] **Step 3: Implement composition and shutdown**

`cmd/hosted/main.go` loads config, opens MySQL, migrates, starts an `http.Server` with 5-second read-header, 15-second read/write, 60-second idle timeouts, handles `SIGINT/SIGTERM`, and allows 30 seconds for shutdown. Log with `slog`; never log DSNs or key contents.

- [ ] **Step 4: Verify the hosted command**

Run:

```powershell
go -C goserver get github.com/go-sql-driver/mysql
go -C goserver test ./internal/hosted/... -count=1
go -C goserver test ./... -count=1 -timeout=300s
$env:CGO_ENABLED='0'; $env:GOOS='linux'; $env:GOARCH='amd64'; go -C goserver build ./cmd/hosted
```

Expected: all tests pass and a Linux binary builds without Windows imports. Remove the local test binary after inspection; do not commit it.

- [ ] **Step 5: Commit the hosted skeleton**

```powershell
git add -- goserver/cmd/hosted goserver/internal/hosted goserver/go.mod goserver/go.sum
git diff --cached --check
git commit -m "feat: add hosted server foundation"
```

---

### Task 5: Add the hosted UI entry and reproducible build contract

**Files:**
- Create: `hosted.html`
- Create: `src/hosted/main.ts`
- Create: `src/hosted/shell.ts`
- Create: `src/hosted/shell.css`
- Create: `vite.hosted.config.ts`
- Create: `tests/hosted-build.test.ts`
- Modify: `package.json`

**Interfaces:**
- Produces: `renderHostedShell(root, session)` and `npm run build:hosted`.

- [ ] **Step 1: Write the failing build contract**

Assert the hosted entry exists, uses a separate Vite config, emits to `goserver/cmd/hosted/dist`, and does not import desktop-only gift-clip/update modules:

```ts
it('keeps hosted UI free of desktop-only imports', () => {
  const source = readFileSync('src/hosted/main.ts', 'utf8') + readFileSync('src/hosted/shell.ts', 'utf8');
  expect(source).not.toMatch(/gift-clip|autoUpdate|electron|ffmpeg/i);
});
```

Run: `npx vitest run tests/hosted-build.test.ts`

Expected: FAIL because files/scripts do not exist.

- [ ] **Step 2: Implement the signed-out shell**

Render only brand, service status, “使用 B 站账号登录” action, privacy summary, and a noindex marker. Do not add registration or admin behavior yet; buttons dispatch typed callbacks from `renderHostedShell` rather than issuing fetches directly.

- [ ] **Step 3: Configure the hosted build**

Add scripts:

```json
{
  "build:hosted": "vite build --config vite.hosted.config.ts",
  "test:hosted": "vitest run tests/hosted-build.test.ts"
}
```

Configure a multi-file hosted build; do not use `vite-plugin-singlefile`. Copy no FFmpeg or desktop update assets.

- [ ] **Step 4: Run all frontend verification**

Run:

```powershell
npx vitest run tests/hosted-build.test.ts
npm run build:hosted
npm run typecheck
npm test
git diff --check
```

Expected: PASS; hosted output contains only the hosted entry and its referenced assets.

- [ ] **Step 5: Commit the UI foundation**

```powershell
git add -- hosted.html src/hosted vite.hosted.config.ts tests/hosted-build.test.ts package.json package-lock.json
git diff --cached --check
git commit -m "feat: add hosted web shell"
```

---

## Plan Completion Gate

Run from the hosted worktree:

```powershell
npm test
npm run typecheck
npm run build:hosted
go -C goserver test ./... -count=1 -timeout=300s
node scripts/verify-go-linux-compile.mjs
git diff --check
git status --short
```

Expected: all commands pass; no generated binary, key, DSN, `.env`, database volume, or unrelated file is staged. Request a code review before starting the identity plan.
