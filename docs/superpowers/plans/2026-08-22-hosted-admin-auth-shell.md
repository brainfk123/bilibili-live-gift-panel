# Hosted Administrator Authentication and Shell Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver a 30-day email-code administrator login, operation-scoped TOTP authorization primitive, reusable request feedback, and the five-item administrator shell.

**Architecture:** Keep the HttpOnly administrator session as the only browser login credential. Replace the cross-operation recent-TOTP window with a short-lived, single-use authorization token bound to one purpose and target; subsequent plans consume this primitive. Split login state, verification input, feedback, routes, and shell into focused frontend modules.

**Tech Stack:** Go 1.24, `net/http`, MySQL 8, TypeScript, DOM APIs, Vitest, Vite.

**Spec:** `docs/superpowers/specs/2026-08-22-hosted-admin-system-redesign.md`

## Global Constraints

- Administrator session lifetime is exactly 30 days.
- Session cookies remain `Secure`, `HttpOnly`, host-only, path `/`, and `SameSite=Lax`.
- No administrator token, full email, email code, TOTP, or credential enters browser persistence or logs.
- Session restoration stops owning the page after 3 seconds; a late result cannot replace a login already started.
- TOTP authorization is single-use and bound to one of `bili_service_replace`, `admin_email_change`, or `recovery_regenerate` plus its target.
- Do not push, merge, deploy, tag, or touch mainland update delivery.

---

### Task 1: Replace recent-TOTP windows with operation authorization

**Files:**
- Create: `goserver/internal/hosted/security/operation.go`
- Create: `goserver/internal/hosted/security/operation_test.go`
- Create: `goserver/internal/hosted/store/mysqlstore/migrations/0010_admin_operation_authorizations.sql`
- Modify: `goserver/internal/hosted/adminidentity/service.go`
- Modify: `goserver/internal/hosted/adminidentity/service_test.go`
- Modify: `goserver/internal/hosted/adminidentity/http.go`
- Modify: `goserver/internal/hosted/adminidentity/http_test.go`
- Modify: `goserver/internal/hosted/store/mysqlstore/store_test.go`

**Interfaces:**
- Produces: `type OperationPurpose string` with constants `bili_service_replace`, `admin_email_change`, `recovery_regenerate`.
- Produces: `AuthorizeOperation(ctx context.Context, sessionToken, totp string, purpose security.OperationPurpose, target string) (string, error)`.
- Produces: `ConsumeOperation(ctx context.Context, tx *sql.Tx, sessionToken, authorizationToken string, purpose security.OperationPurpose, target string, now time.Time) error`.
- Produces HTTP: `POST /api/admin/operation-authorizations` body `{ "totp": "123456", "purpose": "bili_service_replace", "target": "global" }`, response `{ "authorizationToken": "..." }`.

- [ ] **Step 1: Write migration and double-run tests**

Create a table with token hash only, administrator session ID, credential epoch, purpose, target, `expires_at`, `consumed_at`, and `created_at`; add unique token hash and expiry indexes. Extend migration tests to apply all migrations twice and assert the new table exists.

- [ ] **Step 2: Run the migration test and verify it fails**

Run: `go -C goserver test ./internal/hosted/store/mysqlstore -run 'Migration|Schema' -count=1`

Expected: FAIL because migration 0010 and the table are absent.

- [ ] **Step 3: Implement single-use authorization**

Use `security.Keyring.NewToken()` and `HashToken("admin_operation_authorization", ...)`. Set a five-minute expiry, bind the row to session ID, epoch, purpose, and target, and atomically set `consumed_at` inside the protected mutation transaction. Reject unknown purpose, empty target, expired, consumed, foreign-session, or epoch-mismatched tokens with authentication failure.

- [ ] **Step 4: Add service and HTTP tests**

Cover correct purpose/target, wrong target, replay, five-minute expiry, revoked session, epoch change, malformed request, no raw token persisted, and response/error JSON. Assert ordinary session probing never consumes an authorization.

- [ ] **Step 5: Run focused Go tests**

Run: `go -C goserver test ./internal/hosted/security ./internal/hosted/adminidentity ./internal/hosted/store/mysqlstore -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add goserver/internal/hosted/security/operation.go goserver/internal/hosted/security/operation_test.go goserver/internal/hosted/adminidentity goserver/internal/hosted/store/mysqlstore/migrations/0010_admin_operation_authorizations.sql goserver/internal/hosted/store/mysqlstore/store_test.go
git commit -m "refactor(hosted): scope administrator TOTP authorization"
```

### Task 2: Extend email sessions to 30 days

**Files:**
- Modify: `goserver/internal/hosted/adminidentity/service.go`
- Modify: `goserver/internal/hosted/adminidentity/service_test.go`
- Modify: `goserver/internal/hosted/adminidentity/http_test.go`
- Modify: `goserver/cmd/hosted/main_test.go`

**Interfaces:**
- Produces: `const DefaultAdministratorSessionTTL = 30 * 24 * time.Hour`.
- Preserves: `GET /api/admin/session`, `POST /api/admin/session/email`, and `DELETE /api/admin/session`.

- [ ] **Step 1: Change tests to require a 30-day expiry and cookie Max-Age**

Assert the service accepts a 30-day configured maximum, rejects anything longer, and the email login response emits the existing secure cookie with a 30-day lifetime.

- [ ] **Step 2: Run focused tests and verify failure**

Run: `go -C goserver test ./internal/hosted/adminidentity ./cmd/hosted -run 'Session|EmailLogin|Cookie' -count=1`

Expected: FAIL on the current seven-day maximum.

- [ ] **Step 3: Update the default and maximum TTL**

Replace the seven-day default and validation ceiling with `DefaultAdministratorSessionTTL`; do not alter ordinary streamer session TTLs.

- [ ] **Step 4: Run focused tests**

Run: `go -C goserver test ./internal/hosted/adminidentity ./cmd/hosted -run 'Session|EmailLogin|Cookie' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add goserver/internal/hosted/adminidentity goserver/cmd/hosted/main_test.go
git commit -m "feat(hosted): keep administrators signed in for thirty days"
```

### Task 3: Build the login state machine and verification feedback

**Files:**
- Create: `src/hosted/admin/login-controller.ts`
- Create: `src/hosted/admin/verification-input.ts`
- Create: `src/hosted/admin/request-feedback.ts`
- Modify: `src/hosted/admin-login.ts`
- Modify: `src/hosted/admin.ts`
- Modify: `src/hosted/api.ts`
- Test: `tests/hosted-admin-login.test.ts`
- Test: `tests/hosted-verification-code.test.ts`
- Test: `tests/hosted-auth.test.ts`

**Interfaces:**
- Produces: `createAdminLoginController(api, publish, options?: { restoreTimeoutMs?: number }): AdminLoginController`.
- Produces: `mountVerificationInput(document, options: { label: string; onComplete(code: string): void }): VerificationCodeControl`.
- Produces state union: `restoring | ready-to-send | sending | entering-code | verifying | network-error | code-error | rate-limited | authenticated`.
- Extends `HostedAPIError` mapping so network failure, invalid/expired code, and rate limit are distinguishable.

- [ ] **Step 1: Write failing controller tests**

Cover three-second restore timeout, valid session direct entry, unavailable probe without email send, late probe isolation, one send in flight, sixth-digit single submission, and disposal.

- [ ] **Step 2: Write failing verification input tests**

Cover ASCII digits only, paste, Backspace, `inputMode="numeric"`, `autocomplete="one-time-code"`, visible caret state, input lock during verification, network failure retaining six digits, and code rejection clearing/refocusing.

- [ ] **Step 3: Run focused Vitest and verify failure**

Run: `node_modules/.bin/vitest run tests/hosted-admin-login.test.ts tests/hosted-verification-code.test.ts tests/hosted-auth.test.ts`

Expected: FAIL because the new modules and states do not exist.

- [ ] **Step 4: Implement the controller and semantic single input**

Keep one real input over the six visual cells. On completion set `aria-busy`, lock editing, and publish `verifying`; on network failure retain the value and expose `retryVerification()`, while invalid/expired code calls `clear()` and `focus()`.

- [ ] **Step 5: Implement accessible progress and errors**

Render a reduced-motion-aware spinner plus visible “正在验证验证码…” text. Put network and code errors in a polite live region; render “重新验证” only for retained network failures and “重新发送验证码” for expired/invalid codes.

- [ ] **Step 6: Run focused tests and typecheck**

Run: `node_modules/.bin/vitest run tests/hosted-admin-login.test.ts tests/hosted-verification-code.test.ts tests/hosted-auth.test.ts`

Run: `node_modules/.bin/tsc --noEmit`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/hosted/admin/login-controller.ts src/hosted/admin/verification-input.ts src/hosted/admin/request-feedback.ts src/hosted/admin-login.ts src/hosted/admin.ts src/hosted/api.ts tests/hosted-admin-login.test.ts tests/hosted-verification-code.test.ts tests/hosted-auth.test.ts
git commit -m "feat(hosted): simplify administrator email login"
```

### Task 4: Replace the shell with five stable sections

**Files:**
- Modify: `src/hosted/admin/routes.ts`
- Modify: `src/hosted/admin/shell.ts`
- Modify: `src/hosted/shell.css`
- Test: `tests/hosted-admin-shell.test.ts`
- Test: `tests/hosted-admin-view.test.ts`

**Interfaces:**
- Produces: `type AdminSection = 'overview' | 'accounts' | 'invitations' | 'bili-service' | 'settings'`.
- Produces a shell with persistent desktop sidebar, mobile menu, page title, status region, and section host.

- [ ] **Step 1: Change shell tests to the five-section contract**

Assert exact labels `运营总览`, `主播账号`, `邀请码`, `B站服务账号`, `系统设置`; assert OBS and security are absent, `aria-current` tracks navigation, and disposal completes before mounting the next section.

- [ ] **Step 2: Add mobile navigation tests**

Assert menu trigger visibility at narrow mode, Escape close, focus trap, focus restoration, and close-on-selection.

- [ ] **Step 3: Run tests and verify failure**

Run: `node_modules/.bin/vitest run tests/hosted-admin-shell.test.ts tests/hosted-admin-view.test.ts`

Expected: FAIL on the current six routes and static mobile sidebar.

- [ ] **Step 4: Implement the route and shell contract**

Keep navigation state in `mountAdminShell`; do not let section modules mutate sidebar DOM. Add a small-screen menu button and off-canvas `nav`, while retaining current dispose-before-replace behavior.

- [ ] **Step 5: Add compact shell styles**

Use the approved navy/gray/blue tokens, minimum content widths, visible focus, reduced motion, and no horizontal page scrolling at 390 CSS pixels.

- [ ] **Step 6: Run focused tests, typecheck, and hosted build**

Run: `node_modules/.bin/vitest run tests/hosted-admin-shell.test.ts tests/hosted-admin-view.test.ts`

Run: `node_modules/.bin/tsc --noEmit`

Run: `node_modules/.bin/vite build --config vite.hosted.config.ts`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/hosted/admin/routes.ts src/hosted/admin/shell.ts src/hosted/shell.css tests/hosted-admin-shell.test.ts tests/hosted-admin-view.test.ts
git commit -m "feat(hosted): add compact administrator shell"
```

