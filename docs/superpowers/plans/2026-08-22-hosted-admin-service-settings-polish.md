# Hosted Administrator Service Account, Settings, and Polish Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete the redesign with simple Bilibili service-account management, lightweight settings, operation-scoped TOTP flows, contextual audit/diagnostics, responsive polish, and production-grade verification.

**Architecture:** Extend current Bilibili credential APIs with a safe status projection, explicit health check, and atomic authorized replacement. Put email/session/TOTP/recovery controls behind one settings service and reuse the operation-authorization primitive from plan 1. Finish by deleting old administrator section code only after all replacement views are integrated and verified.

**Tech Stack:** Go, MySQL, TypeScript, Vitest, Playwright-compatible browser checks, Vite, Docker build verification.

**Spec:** `docs/superpowers/specs/2026-08-22-hosted-admin-system-redesign.md`

## Global Constraints

- Only service-account replacement, administrator-email change, and recovery regeneration use operation-scoped TOTP.
- New Bilibili credentials must pass verification before old credentials are replaced.
- Settings and diagnostics never return raw cookies, codes, recovery passwords, SMTP credentials, or raw server logs.
- Mobile layout must work at 390 CSS pixels without horizontal page scrolling.
- Do not push, merge, deploy, tag, or touch mainland update delivery.

---

### Task 1: Add service-account status, check, and atomic replacement authorization

**Files:**
- Modify: `goserver/internal/hosted/biligateway/http.go`
- Modify: `goserver/internal/hosted/biligateway/http_test.go`
- Modify: `goserver/internal/hosted/biligateway/credential.go`
- Modify: `goserver/internal/hosted/biligateway/credential_test.go`
- Modify: `goserver/internal/hosted/adminidentity/service.go`
- Modify: `goserver/cmd/hosted/main_test.go`

**Interfaces:**
- Produces: `GET /api/admin/bili-service/status` with `health`, masked UID, `lastVerifiedAt`, and `lastReplacedAt`.
- Produces: `POST /api/admin/bili-service/check` returning the same projection.
- Consumes header `X-Admin-Authorization` on `POST /api/admin/bili-service/replace`, purpose `bili_service_replace`, target `global`.

- [ ] **Step 1: Write failing status and redaction tests**

Cover healthy, missing, unavailable, verifier failure, masked UID, times, no cookie/response body leakage, and session authorization.

- [ ] **Step 2: Write failing replacement authorization tests**

Cover correct single-use token, wrong purpose, wrong target, replay, verification failure retaining old credential, successful verification and replacement in one transaction, and simplified status history event.

- [ ] **Step 3: Run focused tests and verify failure**

Run: `go -C goserver test ./internal/hosted/biligateway ./internal/hosted/adminidentity -count=1`

Expected: FAIL because status/check and operation-token consumption are absent.

- [ ] **Step 4: Implement status/check and atomic replacement**

Do not persist raw verifier responses. Keep old credential row locked until verification succeeds; consume authorization and replace inside the same transaction, then record a simplified audit event.

- [ ] **Step 5: Run focused tests**

Run: `go -C goserver test ./internal/hosted/biligateway ./internal/hosted/adminidentity ./cmd/hosted -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add goserver/internal/hosted/biligateway goserver/internal/hosted/adminidentity goserver/cmd/hosted/main_test.go
git commit -m "feat(hosted): simplify Bilibili service account management"
```

### Task 2: Add lightweight administrator settings service

**Files:**
- Create: `goserver/internal/hosted/adminsettings/model.go`
- Create: `goserver/internal/hosted/adminsettings/service.go`
- Create: `goserver/internal/hosted/adminsettings/service_test.go`
- Create: `goserver/internal/hosted/adminsettings/http.go`
- Create: `goserver/internal/hosted/adminsettings/http_test.go`
- Modify: `goserver/internal/hosted/adminidentity/recovery.go`
- Modify: `goserver/internal/hosted/adminidentity/recovery_test.go`
- Modify: `goserver/cmd/hosted/main.go`
- Modify: `goserver/cmd/hosted/main_test.go`

**Interfaces:**
- Produces: `GET /api/admin/settings` with masked email, session expiry, TOTP enabled, recovery generated time, and service health summary.
- Produces: `POST /api/admin/sessions/revoke-others`.
- Produces email-change prepare/confirm endpoints consuming purpose `admin_email_change` and a server-side HMAC lookup of the normalized new email as target; the browser sends the new email only in the protected request and never receives the digest.
- Changes recovery regeneration to consume purpose `recovery_regenerate`, target `global`.
- Performs TOTP reset only through the recovery-regeneration workflow, consuming purpose `recovery_regenerate`, target `totp_reset`; there is no unprotected standalone TOTP reset endpoint.
- Produces: `GET /api/admin/events` and `GET /api/admin/diagnostics` with simplified, redacted projections.

- [ ] **Step 1: Write failing settings projection and redaction tests**

Cover masked email, current session expiry, TOTP flag, recovery timestamp, revoke-other-sessions preserving current token, and absence of secrets/raw logs.

- [ ] **Step 2: Write failing email and recovery authorization tests**

Cover correct purpose/target, wrong/replayed token, email challenge verification, epoch increment, other-session revocation, recovery generation, and no recovery password in email/log/audit.

- [ ] **Step 3: Run tests and verify failure**

Run: `go -C goserver test ./internal/hosted/adminsettings ./internal/hosted/adminidentity -count=1`

Expected: FAIL because the settings package and scoped routes do not exist.

- [ ] **Step 4: Implement settings and contextual event projections**

Map only allowlisted event types and safe fields. Return bounded pages, not arbitrary log queries. Diagnostics report component status and timestamps, never environment values or raw error bodies.

- [ ] **Step 5: Mount routes and run focused tests**

Run: `go -C goserver test ./internal/hosted/adminsettings ./internal/hosted/adminidentity ./internal/hosted/app ./cmd/hosted -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add goserver/internal/hosted/adminsettings goserver/internal/hosted/adminidentity goserver/internal/hosted/app goserver/cmd/hosted
git commit -m "feat(hosted): add lightweight administrator settings"
```

### Task 3: Build service account and settings frontend

**Files:**
- Create: `src/hosted/admin/operation-authorization.ts`
- Create: `src/hosted/admin/bili-service.ts`
- Create: `src/hosted/admin/settings.ts`
- Create: `src/hosted/admin/events.ts`
- Modify: `src/hosted/admin.ts`
- Modify: `src/hosted/api.ts`
- Modify: `src/hosted/shell.css`
- Test: `tests/hosted-admin-operation-authorization.test.ts`
- Modify: `tests/hosted-bili-service-admin.test.ts`
- Create: `tests/hosted-admin-settings.test.ts`
- Modify: `tests/hosted-admin-view.test.ts`

**Interfaces:**
- Produces: `authorizeAdminOperation(api, { purpose, target, totp }): Promise<string>`.
- Produces: `mountBiliServiceView(...)` and `mountAdminSettingsView(...)`.
- Consumes all Task 1 and Task 2 APIs.

- [ ] **Step 1: Write operation dialog tests**

Cover six-digit TOTP, request lock, purpose/target binding, network retry retaining digits, invalid TOTP clearing, cancellation, secret wipe, and one mutation retry using the returned token.

- [ ] **Step 2: Write service-account view tests**

Cover status, check, unavailable copy, replacement three-step flow, old credential retained on failure, successful refresh, and short status history.

- [ ] **Step 3: Write settings view tests**

Cover masked email, 30-day session copy, revoke others, TOTP enabled, recovery age, three protected flows, advanced collapsed by default, bounded event/diagnostics display, and logout.

- [ ] **Step 4: Run focused tests and verify failure**

Run: `node_modules/.bin/vitest run tests/hosted-admin-operation-authorization.test.ts tests/hosted-bili-service-admin.test.ts tests/hosted-admin-settings.test.ts tests/hosted-admin-view.test.ts`

Expected: FAIL because replacement views and operation authorization are absent.

- [ ] **Step 5: Implement modules and integrate routes**

Use the shared verification input from plan 1. Mount TOTP only after a protected action is chosen; never render a countdown or global security-session state. Keep advanced settings closed until the user opens it.

- [ ] **Step 6: Run focused tests and typecheck**

Run: `node_modules/.bin/vitest run tests/hosted-admin-operation-authorization.test.ts tests/hosted-bili-service-admin.test.ts tests/hosted-admin-settings.test.ts tests/hosted-admin-view.test.ts tests/hosted-auth.test.ts`

Run: `node_modules/.bin/tsc --noEmit`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/hosted/admin src/hosted/admin.ts src/hosted/api.ts src/hosted/shell.css tests/hosted-admin-operation-authorization.test.ts tests/hosted-bili-service-admin.test.ts tests/hosted-admin-settings.test.ts tests/hosted-admin-view.test.ts tests/hosted-auth.test.ts
git commit -m "feat(hosted): complete lightweight administrator console"
```

### Task 4: Remove obsolete administrator UI and compatibility paths

**Files:**
- Modify: `src/hosted/admin.ts`
- Modify: `src/hosted/shell.css`
- Modify: `src/hosted/api.ts`
- Modify: `tests/hosted-admin-view.test.ts`
- Modify: `tests/hosted-auth.test.ts`
- Modify: `tests/hosted-admin-shell.test.ts`
- Modify: `goserver/internal/hosted/adminidentity/http.go`
- Modify: `goserver/internal/hosted/adminidentity/http_test.go`

**Interfaces:**
- Removes frontend recent-TOTP controller, OBS section, security/recovery section, one-time invitation dialog, and obsolete DTO branches.
- Removes `POST /api/admin/totp` only after all operation-authorization consumers are migrated.

- [ ] **Step 1: Add absence tests**

Assert no route labels `OBS` or `安全与恢复`, no `hosted-admin-recent-totp`, no administrator one-time invitation dialog, no calls to `/api/admin/totp`, and no dead administrator Bilibili-login compatibility route.

- [ ] **Step 2: Run tests and verify failure**

Run: `node_modules/.bin/vitest run tests/hosted-admin-view.test.ts tests/hosted-auth.test.ts tests/hosted-admin-shell.test.ts`

Run: `go -C goserver test ./internal/hosted/adminidentity -run HTTP -count=1`

Expected: FAIL while obsolete paths remain.

- [ ] **Step 3: Delete only obsolete code**

Remove old branches after proving every imported interface has a replacement. Preserve streamer invitation views, ordinary Bilibili login, OBS public routes, and recovery backend needed by the new settings flow.

- [ ] **Step 4: Run focused tests and dead-reference search**

Run: `rg -n "recent_totp_required|verifyRecentTOTP|AdminSection.*obs|security.*recovery" src/hosted tests/hosted-*`

Expected: only intentional backend compatibility/error history references remain; no frontend route or caller remains.

- [ ] **Step 5: Commit**

```bash
git add src/hosted goserver/internal/hosted/adminidentity tests/hosted-admin-view.test.ts tests/hosted-auth.test.ts tests/hosted-admin-shell.test.ts
git commit -m "refactor(hosted): remove obsolete administrator console paths"
```

### Task 5: Full verification and real responsive evidence

**Files:**
- Modify only if a failing product requirement is discovered; do not commit screenshots or experimental scripts.

**Interfaces:**
- Verifies the complete spec through real Hosted build output and browser routes.

- [ ] **Step 1: Run complete Go verification**

Run: `go -C goserver test ./... -count=1`

Run: `go -C goserver test -race ./internal/hosted/adminidentity ./internal/hosted/adminconsole ./internal/hosted/adminsettings ./internal/hosted/invitation ./internal/hosted/identity ./internal/hosted/obs ./internal/hosted/biligateway -count=1`

Run: `go -C goserver vet ./...`

Expected: PASS.

- [ ] **Step 2: Run complete frontend verification**

Run: `node_modules/.bin/vitest run`

Run: `node_modules/.bin/tsc --noEmit`

Run: `node_modules/.bin/vite build --config vite.hosted.config.ts`

Expected: PASS. If the update-api test cannot access the global Go cache, rerun that test with `GOCACHE` inside `.cache/` and report the environment-specific first failure.

- [ ] **Step 3: Verify reproducible Hosted server artifact**

Run: `node scripts/build-hosted.mjs --verify-reproducible`

Expected: two builds produce the same digest.

- [ ] **Step 4: Run desktop browser acceptance**

Use the actual built Hosted route. Verify login send/input/spinner/network retry, overview, account list/detail/OBS, invitation sort/create/revoke, service check/replace, settings, and logout at 1440×900. Capture local-only screenshots.

- [ ] **Step 5: Run mobile acceptance**

At 390×844 verify menu focus/close, no horizontal document scrolling, account detail full-width, invitation compact panel, iOS-style single input caret, paste/auto-fill event handling, spinner, network retry, and keyboard reachability. Repeat on real iOS Safari before deployment.

- [ ] **Step 6: Verify public-route isolation**

Confirm public `/healthz` and `/internal/metrics` remain unrouted, administrator APIs require session, streamer Bilibili login still works, and OBS public routes still use fragment exchange.

- [ ] **Step 7: Record final verification commit**

If verification required product fixes, commit each bounded fix separately after its focused regression test. If no changes were required, do not create an empty commit.
