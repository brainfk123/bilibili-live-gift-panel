# Hosted Administrator Invitations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver eight-character administrator invitations that remain viewable while active, clear recoverable ciphertext at lifecycle end, and support server-side sorting, filtering, batch creation, copy/share, and compact creation UI.

**Architecture:** Extend invitation persistence with purpose-separated AEAD ciphertext alongside the existing lookup digest and suffix. Add administrator-only paginated queries and batch creation while retaining the ordinary streamer invitation contract. End-state transitions atomically clear ciphertext before the response is committed.

**Tech Stack:** Go, MySQL, `security.Keyring`, TypeScript, Vitest.

**Spec:** `docs/superpowers/specs/2026-08-22-hosted-admin-system-redesign.md`

## Global Constraints

- Codes are exactly eight characters from one explicit uppercase alphanumeric alphabet.
- Active full codes are returned only to authenticated administrators.
- Used, revoked, or expired rows retain only the fixed mask and last four characters; ciphertext is NULL.
- Sorting and filtering happen on the server, not only within the current browser page.
- Do not push, merge, deploy, tag, or touch mainland update delivery.

---

### Task 1: Add recoverable active invitation persistence

**Files:**
- Create: `goserver/internal/hosted/store/mysqlstore/migrations/0011_recoverable_admin_invitations.sql`
- Modify: `goserver/internal/hosted/invitation/model.go`
- Modify: `goserver/internal/hosted/invitation/service.go`
- Modify: `goserver/internal/hosted/invitation/service_test.go`
- Modify: `goserver/internal/hosted/store/mysqlstore/store_test.go`
- Modify: `goserver/internal/hosted/store/mysqlstore/integration_test.go`

**Interfaces:**
- Produces invitation fields `CodeCiphertext []byte`, nullable `ExpiresAt`, and `UsedByAccountID` in internal models; JSON exposes `code` only on active administrator records.
- Uses key purposes `invitation_code_lookup` and `invitation_code_ciphertext`.

- [ ] **Step 1: Write migration double-run and lifecycle tests**

Require nullable `code_ciphertext`, nullable `expires_at`, and redemption account reference. Assert active rows have ciphertext and terminal rows have NULL ciphertext.

- [ ] **Step 2: Run focused tests and verify failure**

Run: `go -C goserver test ./internal/hosted/invitation ./internal/hosted/store/mysqlstore -run 'Migration|Cipher|Lifecycle|Terminal' -count=1`

Expected: FAIL because the columns and encryption path are absent.

- [ ] **Step 3: Implement the code alphabet and encryption**

Define one constant alphabet excluding ambiguous characters and generate eight unbiased characters with `crypto/rand` rejection sampling. Store lookup digest, last four characters, and `Keyring.Seal("invitation_code_ciphertext", code)` in the creation transaction.

- [ ] **Step 4: Clear ciphertext on all terminal paths**

Update redeem, revoke, and expiry cleanup statements to set `code_ciphertext = NULL` atomically with status timestamps. Decrypt only after administrator session validation; treat authentication failure as repository failure without returning partial rows.

- [ ] **Step 5: Run focused and MySQL integration tests**

Run: `go -C goserver test ./internal/hosted/invitation ./internal/hosted/store/mysqlstore -count=1`

Run: `node scripts/test-hosted-mysql.mjs`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add goserver/internal/hosted/invitation goserver/internal/hosted/store/mysqlstore/migrations/0011_recoverable_admin_invitations.sql goserver/internal/hosted/store/mysqlstore
git commit -m "feat(hosted): retain active administrator invitation codes"
```

### Task 2: Add administrator invitation query and batch API

**Files:**
- Modify: `goserver/internal/hosted/invitation/model.go`
- Modify: `goserver/internal/hosted/invitation/service.go`
- Modify: `goserver/internal/hosted/invitation/service_test.go`
- Modify: `goserver/internal/hosted/invitation/http.go`
- Modify: `goserver/internal/hosted/invitation/http_test.go`
- Modify: `goserver/cmd/hosted/main_test.go`

**Interfaces:**
- Produces: `GET /api/admin/invitations?query=&status=&sort=status|created_at&direction=asc|desc&cursor=&limit=`.
- Produces: `POST /api/admin/invitations` body `{ count: 1..50, validity: "7d" | "30d" | "permanent" }`, response `{ invitations: AdminInvitationRecord[] }`.
- Produces: `DELETE /api/admin/invitations/{id}` for administrator revocation.

- [ ] **Step 1: Write failing API contract tests**

Cover default `created_at desc`, both sort columns/directions, status order, combined search/filter/sort, active code visibility, terminal masking, permanent null expiry, count bounds, unique codes, revoke, and session failure.

- [ ] **Step 2: Run tests and verify failure**

Run: `go -C goserver test ./internal/hosted/invitation -run 'Admin|Sort|Batch|Permanent' -count=1`

Expected: FAIL on the old single-create and secret-free list contract.

- [ ] **Step 3: Implement paginated administrator list**

Use a stable secondary ID sort. Map status order explicitly with SQL `CASE`, never by localized label. Search only normalized code suffix and numeric redemption account ID.

- [ ] **Step 4: Implement batch create and administrator revoke**

Create all requested codes in one transaction; any collision retry remains bounded and any final failure rolls back the batch. Return full codes only after commit.

- [ ] **Step 5: Preserve streamer invitation compatibility**

Run existing ordinary `/api/invitations` tests and assert they still return masked history and single generated code according to streamer quota.

- [ ] **Step 6: Run focused tests**

Run: `go -C goserver test ./internal/hosted/invitation ./cmd/hosted -count=1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add goserver/internal/hosted/invitation goserver/cmd/hosted/main_test.go
git commit -m "feat(hosted): add administrator invitation inventory"
```

### Task 3: Build compact invitation management UI

**Files:**
- Create: `src/hosted/admin/invitations/model.ts`
- Create: `src/hosted/admin/invitations/view.ts`
- Create: `src/hosted/admin/invitations/create-panel.ts`
- Modify: `src/hosted/admin.ts`
- Modify: `src/hosted/api.ts`
- Modify: `src/hosted/shell.css`
- Test: `tests/hosted-admin-invitations.test.ts`
- Modify: `tests/hosted-auth.test.ts`
- Modify: `tests/hosted-admin-view.test.ts`

**Interfaces:**
- Produces `AdminInvitationQuery`, `AdminInvitationRecord`, `AdminInvitationPage`, and `mountAdminInvitationView`.
- Consumes the Task 2 API and browser `navigator.share` when present, with clipboard fallback.

- [ ] **Step 1: Write strict DTO and state tests**

Cover active full code, terminal masked code, nullable expiry, malformed full code on terminal state, query serialization, stale-page cancellation, and sort direction persistence.

- [ ] **Step 2: Write view interaction tests**

Cover status and creation-time sortable buttons with `aria-sort`, combined filter/search, copy, native share and fallback, revoke confirmation, compact create popover, defaults `1` and `7d`, batch results, auto-close, insertion/highlight, and dismissible “已创建并复制” feedback.

- [ ] **Step 3: Run focused tests and verify failure**

Run: `node_modules/.bin/vitest run tests/hosted-admin-invitations.test.ts tests/hosted-admin-view.test.ts tests/hosted-auth.test.ts`

Expected: FAIL because administrator invitation inventory modules are absent.

- [ ] **Step 4: Implement model and API parsing**

Reject active rows without an eight-character code and reject terminal rows with a full code. Keep sort/filter/search state in one immutable query object and reset cursor when any criterion changes.

- [ ] **Step 5: Implement compact UI**

Mount the create panel under the title button; close on success, Escape, outside click, or navigation. Insert returned rows at the top only when current query is `created_at desc`; otherwise refetch to preserve server order.

- [ ] **Step 6: Run focused tests, typecheck, and hosted build**

Run: `node_modules/.bin/vitest run tests/hosted-admin-invitations.test.ts tests/hosted-admin-view.test.ts tests/hosted-auth.test.ts`

Run: `node_modules/.bin/tsc --noEmit`

Run: `node_modules/.bin/vite build --config vite.hosted.config.ts`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/hosted/admin/invitations src/hosted/admin.ts src/hosted/api.ts src/hosted/shell.css tests/hosted-admin-invitations.test.ts tests/hosted-admin-view.test.ts tests/hosted-auth.test.ts
git commit -m "feat(hosted): add compact invitation inventory"
```

