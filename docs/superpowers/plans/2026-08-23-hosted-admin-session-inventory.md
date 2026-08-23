# Hosted Administrator Session Inventory Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add privacy-bounded authorized-device inventory, individual session revocation, and recent administrator login records to the Hosted settings page.

**Architecture:** A forward-only MySQL migration adds an opaque public ID and safe client summaries to administrator sessions plus a bounded login-event table. `adminidentity` owns capture and session mutation; `adminsettings` exposes only allowlisted projections. The frontend consumes strict DTOs and renders devices and login events with the same shared interaction primitives proven in the local simulator.

**Tech Stack:** Go 1.26, MySQL 8.4, TypeScript, DOM APIs, Vitest, sqlmock, Docker Compose integration tests.

**Spec:** `docs/superpowers/specs/2026-08-23-hosted-admin-local-iteration-design.md`

## Global Constraints

- Never store or return raw User-Agent, full IP, email code, session token, Cookie, TOTP, or Bilibili credential.
- Device label is an allowlisted OS/browser summary capped at 80 UTF-8 bytes.
- Client network is masked before storage: IPv4 `/24` as `A.B.C.*`; IPv6 first four hextets followed by `::*`; unknown is `—`.
- Login records retain only the latest 100 rows through insert-time pruning and return at most 50.
- Current session cannot be revoked by the individual-device endpoint.
- All mutations require an authenticated current administrator session, exact Origin, and CSRF.
- Every behavior begins with a failing test; migrations are forward-only and pass real MySQL 8.4 integration.
- Do not deploy until the local simulator is user-approved and every full gate passes.

---

### Task 1: Safe client summary parser

**Files:**
- Create: `goserver/internal/hosted/adminidentity/client_summary.go`
- Test: `goserver/internal/hosted/adminidentity/client_summary_test.go`

**Interfaces:**
- Produces: `type ClientSummary struct { DeviceLabel string; ClientNetwork string }`
- Produces: `func SummarizeClient(userAgent string, address net.IP) ClientSummary`

- [ ] **Step 1: Write failing table tests**

```go
func TestSummarizeClientAllowlistAndNetworkMasking(t *testing.T) {
  tests := []struct{ ua, ip, label, network string }{
    {iphoneSafari, "203.0.113.45", "iPhone · Safari", "203.0.113.*"},
    {windowsEdge, "198.51.100.9", "Windows · Edge", "198.51.100.*"},
    {androidChrome, "2001:db8:abcd:1234:5678::1", "Android · Chrome", "2001:db8:abcd:1234::*"},
    {"secret custom agent", "not-an-ip", "其他设备 · 其他浏览器", "—"},
  }
  for _, test := range tests {
    result := SummarizeClient(test.ua, net.ParseIP(test.ip))
    if result.DeviceLabel != test.label || result.ClientNetwork != test.network { t.Fatalf("%#v", result) }
    if strings.Contains(result.DeviceLabel, "secret") { t.Fatal("raw user agent crossed projection") }
  }
}
```

- [ ] **Step 2: Run and verify RED**

Run: `go test ./internal/hosted/adminidentity -run TestSummarizeClient -count=1`

Expected: FAIL because `SummarizeClient` is undefined.

- [ ] **Step 3: Implement allowlisted parsing and masking**

Choose OS in order `iPhone`, `iPad`, `Android`, `Windows`, `macOS`, `Linux`, fallback `其他设备`. Choose browser in order `Edge`, `Firefox`, `Chrome`, `Safari`, fallback `其他浏览器`. Do not copy version strings. Use `net.IP.To4` for IPv4 and the first eight bytes for the IPv6 prefix.

- [ ] **Step 4: Run package tests**

Run: `go test ./internal/hosted/adminidentity -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add goserver/internal/hosted/adminidentity/client_summary.go goserver/internal/hosted/adminidentity/client_summary_test.go
git commit -m "feat(hosted): summarize administrator login clients"
```

### Task 2: Session-device and login-event persistence

**Files:**
- Create: `goserver/internal/hosted/store/mysqlstore/migrations/0012_admin_session_inventory.sql`
- Modify: `goserver/internal/hosted/adminidentity/service.go`
- Modify: `goserver/internal/hosted/adminidentity/service_test.go`
- Modify: `goserver/internal/hosted/store/mysqlstore/store_test.go`
- Modify: `goserver/internal/hosted/store/mysqlstore/integration_test.go`

**Interfaces:**
- Add to `site_sessions`: `public_id BINARY(16)`, `device_label VARCHAR(80)`, `client_network VARCHAR(64)`, `last_seen_at DATETIME(6)`.
- Create `admin_login_events(id, result, device_label, client_network, occurred_at)` with result check `success|failure`.
- Produces repository methods `CreateAdminSession`, `TouchAdminSession`, `ListAdminSessions`, `RevokeAdminSession`, `RecordAdminLoginEvent`, and `ListAdminLoginEvents`.

- [ ] **Step 1: Add failing migration and repository tests**

```go
func TestSQLRepositoryListsSafeAdministratorSessions(t *testing.T) {
  rows := sqlmock.NewRows([]string{"public_id", "device_label", "client_network", "created_at", "last_seen_at", "expires_at", "is_current"}).
    AddRow("00112233445566778899aabbccddeeff", "iPhone · Safari", "203.0.113.*", created, active, expires, true)
  mock.ExpectQuery("SELECT HEX\\(s.public_id\\)").WithArgs(sessionHash).WillReturnRows(rows)
  sessions, err := repository.ListAdminSessions(context.Background(), sessionHash, now)
  if err != nil || len(sessions) != 1 || !sessions[0].Current { t.Fatalf("%#v %v", sessions, err) }
}
```

Add an integration assertion that migration `0012_admin_session_inventory` creates the columns, unique public ID, login-event result check, and retention index on `occurred_at`.

- [ ] **Step 2: Run focused tests and verify RED**

Run: `go test ./internal/hosted/adminidentity ./internal/hosted/store/mysqlstore -run 'SessionInventory|LoginEvent' -count=1`

Expected: FAIL because migration and repository methods are absent.

- [ ] **Step 3: Write migration 0012**

Add nullable columns, populate each existing admin session public ID with `UUID_TO_BIN(UUID())`, set safe defaults `其他设备 · 其他浏览器`, `—`, and `created_at`, then make columns non-null and add unique/index constraints. Create the login-event table and index `(occurred_at DESC, id DESC)`.

- [ ] **Step 4: Implement transactional repository operations**

`CreateAdminSession` inserts the safe summary and returns the session. `TouchAdminSession` updates only when `last_seen_at < now - 5 minutes`. `RevokeAdminSession` locks current and target sessions, rejects equal IDs, and sets `revoked_at` once. `RecordAdminLoginEvent` inserts one row and deletes rows older than the newest 100 IDs in the same transaction.

- [ ] **Step 5: Preserve published migration checksums**

Add only the new migration checksum expectation. Do not modify bytes or expected hashes for `0001` through `0011`.

- [ ] **Step 6: Run package and real MySQL tests**

Run:

```text
go test ./internal/hosted/adminidentity ./internal/hosted/store/mysqlstore -count=1
node scripts/test-hosted-mysql.mjs
```

Expected: PASS with MySQL 8.4.

- [ ] **Step 7: Commit**

```bash
git add goserver/internal/hosted/store/mysqlstore/migrations/0012_admin_session_inventory.sql goserver/internal/hosted/adminidentity/service.go goserver/internal/hosted/adminidentity/service_test.go goserver/internal/hosted/store/mysqlstore/store_test.go goserver/internal/hosted/store/mysqlstore/integration_test.go
git commit -m "feat(hosted): persist administrator session inventory"
```

### Task 3: Capture login summaries and expose session APIs

**Files:**
- Modify: `goserver/internal/hosted/adminidentity/http.go`
- Modify: `goserver/internal/hosted/adminidentity/http_test.go`
- Modify: `goserver/internal/hosted/adminidentity/service.go`
- Modify: `goserver/internal/hosted/adminsettings/service.go`
- Modify: `goserver/internal/hosted/adminsettings/service_test.go`
- Modify: `goserver/internal/hosted/adminsettings/http.go`
- Modify: `goserver/internal/hosted/adminsettings/http_test.go`
- Modify: `goserver/internal/hosted/app/app.go`
- Modify: `goserver/internal/hosted/app/app_test.go`

**Interfaces:**
- Add `POST /api/admin/auth/email/challenges` client summary capture without returning it.
- Add `GET /api/admin/sessions`, `DELETE /api/admin/sessions/{publicId}`, and `GET /api/admin/login-events?limit=N`.
- Session DTO exact keys: `id, deviceLabel, clientNetwork, createdAt, lastSeenAt, expiresAt, current`.
- Login-event DTO exact keys: `result, deviceLabel, clientNetwork, occurredAt`.

- [ ] **Step 1: Write failing HTTP contract tests**

```go
func TestSessionInventoryRoutesRequireCurrentAdminAndRejectSecrets(t *testing.T) {
  get := httptest.NewRequest(http.MethodGet, "/api/admin/sessions", nil)
  get.AddCookie(&http.Cookie{Name: identity.SiteSessionCookie, Value: "current"})
  response := httptest.NewRecorder()
  handler.ServeHTTP(response, get)
  if response.Code != http.StatusOK { t.Fatalf("status=%d", response.Code) }
  for _, forbidden := range []string{"token_hash", "raw_user_agent", "email_code", "203.0.113.45"} {
    if strings.Contains(response.Body.String(), forbidden) { t.Fatalf("leaked %s", forbidden) }
  }
}
```

Add exact method tests proving `POST /api/admin/sessions/{id}` is 405, current-session DELETE is 409 `current_session`, missing target is 404, wrong Origin/CSRF is 403, and limit outside `1..50` is 400.

- [ ] **Step 2: Run and verify RED**

Run: `go test ./internal/hosted/adminidentity ./internal/hosted/adminsettings ./internal/hosted/app -run 'SessionInventory|LoginEvent' -count=1`

Expected: FAIL with route not found and missing service methods.

- [ ] **Step 3: Capture summary at challenge creation and verification**

Resolve client IP with the existing trusted resolver. Store the summary in the in-memory email challenge. On every verification attempt record `success` or `failure`; never record the six digits. Successful session creation passes the summary to `CreateAdminSession`. `RequireSession` performs a throttled touch.

- [ ] **Step 4: Implement strict adminsettings projections and routes**

Validate public ID as exactly 32 lowercase hex characters. Return timestamps in UTC RFC3339. Sort active sessions current-first then last-seen descending. Sort login events newest-first. `POST /api/admin/sessions/revoke-others` remains supported and returns 204.

- [ ] **Step 5: Mount exact app routes and verify route isolation**

Mount only the three new method-routes. Tests prove they do not fall through identity, invitation, or generic admin prefixes.

- [ ] **Step 6: Run focused and full Go tests**

Run:

```text
go test ./internal/hosted/adminidentity ./internal/hosted/adminsettings ./internal/hosted/app -count=1
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add goserver/internal/hosted/adminidentity/http.go goserver/internal/hosted/adminidentity/http_test.go goserver/internal/hosted/adminidentity/service.go goserver/internal/hosted/adminsettings/service.go goserver/internal/hosted/adminsettings/service_test.go goserver/internal/hosted/adminsettings/http.go goserver/internal/hosted/adminsettings/http_test.go goserver/internal/hosted/app/app.go goserver/internal/hosted/app/app_test.go
git commit -m "feat(hosted): expose administrator device sessions"
```

### Task 4: Strict frontend API and settings view

**Files:**
- Modify: `src/hosted/api.ts`
- Rewrite: `src/hosted/admin/settings.ts`
- Modify: `src/hosted/shell.css`
- Modify locally, do not stage: `.cache/admin-preview/mock-api.ts`
- Test: `tests/hosted-admin-settings.test.ts`
- Test: `tests/hosted-admin-settings-view.test.ts`

**Interfaces:**
- Produces `AdminDeviceSession`, `AdminLoginEvent`, `adminSessions()`, `revokeAdminSession(id)`, and `adminLoginEvents(limit=20)`.
- Settings view API Pick adds these methods while preserving settings, diagnostics, bulk revoke, and logout.

- [ ] **Step 1: Write failing strict API tests**

```ts
it('accepts only redacted device sessions and login records', async () => {
  const api = await connect({ sessions:[device], events:[login] });
  await expect(api.adminSessions()).resolves.toEqual([device]);
  await expect(api.adminLoginEvents()).resolves.toEqual([login]);
  await expect(connect({ sessions:[{...device, token:'secret'}] }).then((x)=>x.adminSessions()))
    .rejects.toMatchObject({ code:'invalid_response' });
});
```

- [ ] **Step 2: Write failing settings DOM tests**

Assert current-device badge, two non-current device rows, per-device “退出此设备”, bulk count confirmation, successful row removal, failed row retention, and login success/failure labels.

- [ ] **Step 3: Run and verify RED**

Run: `node node_modules/vitest/vitest.mjs run tests/hosted-admin-settings.test.ts tests/hosted-admin-settings-view.test.ts`

Expected: FAIL because DTOs and view sections do not exist.

- [ ] **Step 4: Implement strict API parsing**

Require exact keys and valid RFC3339 timestamps. Reject full IPv4/IPv6 addresses by requiring `*` or `—` in `clientNetwork`. Serialize DELETE with CSRF and same-origin credentials.

- [ ] **Step 5: Implement the settings sections**

Render profile summary, device cards, login-event table, collapsed diagnostics, and logout. Use `mountAdminNotice` and `runAdminAction`. Confirm bulk revoke with the exact non-current device count. The current device has no revoke button.

- [ ] **Step 6: Update local mock scenarios and review locally**

Add iPhone/Safari current, Windows/Edge, and Android/Chrome sessions plus success/failure login events. Verify per-device and bulk revoke update mock state.

- [ ] **Step 7: Run tests and commit**

Run: `node node_modules/vitest/vitest.mjs run tests/hosted-admin-settings.test.ts tests/hosted-admin-settings-view.test.ts tests/hosted-admin-view.test.ts`

```bash
git add src/hosted/api.ts src/hosted/admin/settings.ts src/hosted/shell.css tests/hosted-admin-settings.test.ts tests/hosted-admin-settings-view.test.ts
git commit -m "feat(hosted): manage administrator device sessions"
```

### Task 5: Full local and release gate

**Files:**
- Modify only for discovered regressions: files owned by Tasks 1–4.
- Keep reports and screenshots untracked under `.cache/admin-preview/`.

**Interfaces:**
- Consumes both implementation plans.
- Produces a user-approved simulator and verified release candidate; does not deploy.

- [ ] **Step 1: Complete user desktop/mobile review**

Open the local simulator at desktop and 390×844. Iterate only by first adding a failing test for each requested correction. Require explicit user acceptance of overview, accounts, invitations, Bilibili service, and settings.

- [ ] **Step 2: Run frontend gates**

```text
node node_modules/vitest/vitest.mjs run
node node_modules/typescript/bin/tsc --noEmit
node node_modules/vite/bin/vite.js build --config vite.hosted.config.ts
```

Expected: all tests pass and the production manifest excludes simulator files.

- [ ] **Step 3: Run backend gates**

```text
go -C goserver test ./... -count=1
go -C goserver vet ./...
go -C goserver test -race ./internal/hosted/adminidentity ./internal/hosted/adminsettings ./internal/hosted/app -count=1
node scripts/test-hosted-mysql.mjs
```

Expected: all pass against MySQL 8.4.

- [ ] **Step 4: Run image gates**

```text
node scripts/build-hosted.mjs
node scripts/build-hosted.mjs --verify-reproducible
```

Expected: build succeeds and both no-cache images have identical SHA-256 IDs.

- [ ] **Step 5: Audit repository and deployment boundary**

Verify `git diff --check`, no staged `.cache` files, no simulator entry in Hosted manifest, branch includes latest `origin/master`, and production remains unchanged. Record known `/internal/metrics` runbook gap separately; do not claim it fixed by this work.

- [ ] **Step 6: Stop for deployment authorization**

Report commit range, tests, image digest, migration head, user acceptance, rollback boundary, and exact Hong Kong deployment scope. Do not push or deploy until the user explicitly authorizes the concrete GitHub, COS backup, and Hong Kong host destinations.
