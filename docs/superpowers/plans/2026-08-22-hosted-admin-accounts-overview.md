# Hosted Administrator Accounts and Overview Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the task-oriented overview and searchable account resource with account-owned OBS management and ordinary confirmation-only mutations.

**Architecture:** Add one administrator query service that returns overview and paginated account projections without exposing Bilibili credentials. Remove recent-TOTP dependencies from account status, invitation quota, and OBS issue paths while preserving transactionality and audit. Frontend account modules own selection, list state, detail state, and OBS actions independently of the shell.

**Tech Stack:** Go, MySQL, TypeScript, Vitest, DOM APIs.

**Spec:** `docs/superpowers/specs/2026-08-22-hosted-admin-system-redesign.md`

## Global Constraints

- OBS management exists only inside account detail; no independent OBS navigation.
- Account enable/disable, quota changes, room changes, and OBS reissue do not require TOTP.
- Batch actions must report every target result; mixed-state actions cannot silently partially succeed.
- UID, cookies, and raw audit JSON never enter administrator responses.
- Do not push, merge, deploy, tag, or touch mainland update delivery.

---

### Task 1: Add overview and account query models

**Files:**
- Create: `goserver/internal/hosted/adminconsole/model.go`
- Create: `goserver/internal/hosted/adminconsole/service.go`
- Create: `goserver/internal/hosted/adminconsole/service_test.go`
- Create: `goserver/internal/hosted/adminconsole/http.go`
- Create: `goserver/internal/hosted/adminconsole/http_test.go`
- Modify: `goserver/cmd/hosted/main.go`
- Modify: `goserver/cmd/hosted/main_test.go`

**Interfaces:**
- Produces: `GET /api/admin/overview` returning counts, prioritized attention items, and recent simplified events.
- Produces: `GET /api/admin/accounts?query=&status=&attention=&cursor=&limit=`.
- Produces: `GET /api/admin/accounts/{id}` with room, quota, OBS access state, and recent account events.
- Produces Go types `Overview`, `AccountSummary`, `AccountDetail`, `Page[T]`, and `AttentionItem`.

- [ ] **Step 1: Write failing service projection tests**

Cover active/disabled counts, missing-room attention, missing OBS attention, deterministic priority, search by numeric account ID/room, status filter, stable cursor pagination, and no raw UID/cookie/audit payload.

- [ ] **Step 2: Run tests and verify failure**

Run: `go -C goserver test ./internal/hosted/adminconsole -count=1`

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement query service**

Use explicit SQL projections and a maximum page size of 100. Encode cursors as opaque base64url `{createdAt,id}` data validated before SQL use. Map event types to administrator copy in Go instead of returning raw `event_data`.

- [ ] **Step 4: Implement authenticated HTTP routes**

Require only an active administrator session, validate query enums and limits, return `400` for malformed cursors, `401` for session failure, and `503` for repository failure.

- [ ] **Step 5: Mount routes and test composition**

Add `AdminConsole` to `app.Dependencies`; mount specific `/api/admin/overview` and `/api/admin/accounts` routes before broad admin handlers. Extend composition tests to prove routing.

- [ ] **Step 6: Run focused Go tests**

Run: `go -C goserver test ./internal/hosted/adminconsole ./internal/hosted/app ./cmd/hosted -count=1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add goserver/internal/hosted/adminconsole goserver/internal/hosted/app goserver/cmd/hosted
git commit -m "feat(hosted): add administrator overview and account queries"
```

### Task 2: Make routine account, quota, and OBS mutations session-only

**Files:**
- Modify: `goserver/internal/hosted/identity/admin.go`
- Modify: `goserver/internal/hosted/identity/admin_test.go`
- Modify: `goserver/internal/hosted/invitation/service.go`
- Modify: `goserver/internal/hosted/invitation/service_test.go`
- Modify: `goserver/internal/hosted/obs/service.go`
- Modify: `goserver/internal/hosted/obs/service_test.go`
- Modify: `goserver/internal/hosted/obs/http_test.go`

**Interfaces:**
- Consumes: active administrator session validation from `adminidentity.Service.RequireSession`.
- Preserves existing account mutation and OBS HTTP paths until the frontend migration is complete.

- [ ] **Step 1: Rewrite authorization tests**

Require active administrator session but assert no operation authorization/TOTP call for enable, disable, quota change, invitation generation, or OBS issue. Preserve tests for revoked session, epoch mismatch, transaction rollback, audit insertion, and old OBS invalidation.

- [ ] **Step 2: Run focused tests and verify failure**

Run: `go -C goserver test ./internal/hosted/identity ./internal/hosted/invitation ./internal/hosted/obs -run 'Admin|Quota|Issue|Sensitive|TOTP' -count=1`

Expected: FAIL because current services require recent TOTP.

- [ ] **Step 3: Replace sensitive authorization with session validation**

Validate the administrator session before beginning mutations; retain the existing row locks, atomic updates, credential epoch behavior, and audit writes. Remove only the recent-TOTP authorize/renew calls for these routine paths.

- [ ] **Step 4: Run focused tests**

Run: `go -C goserver test ./internal/hosted/identity ./internal/hosted/invitation ./internal/hosted/obs -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add goserver/internal/hosted/identity goserver/internal/hosted/invitation goserver/internal/hosted/obs
git commit -m "refactor(hosted): simplify routine administrator mutations"
```

### Task 3: Add batch account mutations

**Files:**
- Modify: `goserver/internal/hosted/adminconsole/model.go`
- Modify: `goserver/internal/hosted/adminconsole/service.go`
- Modify: `goserver/internal/hosted/adminconsole/service_test.go`
- Modify: `goserver/internal/hosted/adminconsole/http.go`
- Modify: `goserver/internal/hosted/adminconsole/http_test.go`

**Interfaces:**
- Produces: `POST /api/admin/accounts/batch` body `{ accountIds: number[], action: "enable" | "disable" | "set_invitation_quota", remainingQuota?: number, reason: string }`.
- Produces: `{ results: [{ accountId, status: "succeeded" | "failed", accountStatus?, error? }] }` in request order.
- Produces: `PUT /api/admin/accounts/{id}/room` body `{ roomId: string }`, authenticated by administrator session and returning the refreshed `AccountDetail` projection.

- [ ] **Step 1: Write failing validation and result tests**

Cover 1–100 unique positive IDs, duplicate rejection, missing quota, out-of-range quota, empty reason, mixed status, per-item rollback, stable result order, valid room update, invalid room ID, and room-update audit.

- [ ] **Step 2: Run focused tests and verify failure**

Run: `go -C goserver test ./internal/hosted/adminconsole -run Batch -count=1`

Expected: FAIL because the batch route is absent.

- [ ] **Step 3: Implement bounded per-account transactions**

Process validated IDs sequentially through existing domain services so one failure does not roll back successful neighbors. Never report top-level success without a result for every requested ID. Implement room change as a separate single-account transaction that locks the account, updates the authoritative room field, records the old and new room in the allowlisted audit projection, and returns refreshed detail.

- [ ] **Step 4: Run focused tests**

Run: `go -C goserver test ./internal/hosted/adminconsole -run Batch -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add goserver/internal/hosted/adminconsole
git commit -m "feat(hosted): add explicit batch account management"
```

### Task 4: Build overview, account list, and account detail frontend

**Files:**
- Create: `src/hosted/admin/overview.ts`
- Create: `src/hosted/admin/accounts/model.ts`
- Create: `src/hosted/admin/accounts/list.ts`
- Create: `src/hosted/admin/accounts/detail.ts`
- Create: `src/hosted/admin/accounts/selection.ts`
- Modify: `src/hosted/admin.ts`
- Modify: `src/hosted/api.ts`
- Modify: `src/hosted/shell.css`
- Test: `tests/hosted-admin-overview.test.ts`
- Test: `tests/hosted-admin-accounts.test.ts`
- Test: `tests/hosted-admin-view.test.ts`

**Interfaces:**
- Consumes API DTOs matching Task 1 and batch results from Task 3.
- Produces `mountAdminOverview(...)`, `mountAccountList(...)`, and `mountAccountDetail(...)` as `HostedView` implementations.

- [ ] **Step 1: Add strict API parser tests**

Assert exact keys, enum validation, cursor handling, malformed response rejection, account detail OBS URL parsing, and no UID/cookie tolerance.

- [ ] **Step 2: Add selection and list tests**

Cover row selection, header all/none toggle, no duplicate clear button, bulk toolbar visibility, disabled incompatible actions, search debounce cancellation, filters, pagination, and partial batch results.

- [ ] **Step 3: Add detail tests**

Cover room/quota actions, OBS copy/reissue confirmation, old-to-new address refresh, enable/disable, event list, drawer disposal, and mobile full-width presentation.

- [ ] **Step 4: Run tests and verify failure**

Run: `node_modules/.bin/vitest run tests/hosted-admin-overview.test.ts tests/hosted-admin-accounts.test.ts tests/hosted-admin-view.test.ts tests/hosted-auth.test.ts`

Expected: FAIL because the modules and DTOs are absent.

- [ ] **Step 5: Implement API parsers and state models**

Keep selection independent from fetched rows so pagination cannot accidentally mutate hidden accounts. Abort stale search/detail requests and ignore results after disposal.

- [ ] **Step 6: Implement approved UI and CSS**

Render overview metrics/attention/resources/recent events; render the compact account table and desktop drawer. At narrow width hide secondary columns and mount detail as a full-width panel without horizontal document scrolling.

- [ ] **Step 7: Run focused tests, typecheck, and hosted build**

Run: `node_modules/.bin/vitest run tests/hosted-admin-overview.test.ts tests/hosted-admin-accounts.test.ts tests/hosted-admin-view.test.ts tests/hosted-auth.test.ts`

Run: `node_modules/.bin/tsc --noEmit`

Run: `node_modules/.bin/vite build --config vite.hosted.config.ts`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add src/hosted/admin src/hosted/admin.ts src/hosted/api.ts src/hosted/shell.css tests/hosted-admin-overview.test.ts tests/hosted-admin-accounts.test.ts tests/hosted-admin-view.test.ts tests/hosted-auth.test.ts
git commit -m "feat(hosted): add overview and account workspace"
```
