# Hosted Administrator Email Authentication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make encrypted administrator email the sole daily-login identity, remove administrator Bilibili authentication and recovery, and retain TOTP only as a sliding inactivity guard for successful high-risk operations.

**Architecture:** Keep the singleton `admin_identity`, encrypted email, credential epoch, recovery material, and seven-day sessions. Add one immutable migration that makes legacy administrator UID columns nullable, issue email sessions without a TOTP step, remove administrator-only Bilibili proof wiring, and renew the persisted recent-TOTP timestamp only after a protected mutation succeeds.

**Tech Stack:** Go 1.26, MySQL 8.4, TypeScript, Vite, Vitest, Nginx, Docker Compose.

**Spec:** `docs/superpowers/specs/2026-08-20-hosted-admin-email-auth-design.md`

## Global Constraints

- Email code: six digits, 5-minute lifetime, at most 5 failed attempts, one-time use.
- Administrator session: 7 days with `Secure`, `HttpOnly`, and `SameSite=Lax`.
- TOTP never participates in daily login. Only a successful protected mutation renews its 10-minute inactivity window.
- Remove every administrator Bilibili login/recovery route and UI. Preserve streamer Bilibili login and Bilibili service-account replacement.
- Never expose email, codes, tokens, TOTP, SMTP values, or Bilibili credentials in responses, logs, persistence, or test output.
- Migrations `0001` through `0008` are immutable. Preserve unrelated dirty and untracked files.

---

### Task 1: Make administrator UID legacy-only

**Files:**
- Create: `goserver/internal/hosted/store/mysqlstore/migrations/0009_admin_email_identity.sql`
- Modify: `goserver/internal/hosted/store/mysqlstore/store_test.go`
- Modify: `goserver/internal/hosted/adminidentity/service.go`
- Test: `goserver/internal/hosted/adminidentity/service_test.go`

**Interfaces:**
- Produces nullable legacy `admin_identity.uid_ciphertext` and `uid_lookup`.
- Preserves `email_ciphertext`, `credential_epoch`, `admin_totp`, recovery codes, and sessions.

- [ ] **Step 1: Write RED migration tests** requiring a ninth migration, unchanged checksums for `0001`–`0008`, and one atomic statement:

```sql
ALTER TABLE admin_identity
    MODIFY COLUMN uid_ciphertext VARBINARY(512) NULL,
    MODIFY COLUMN uid_lookup BINARY(32) NULL;
```

- [ ] **Step 2: Verify RED**

```bat
go test ./internal/hosted/store/mysqlstore -run "Test.*Migration|Test.*Checksum" -count=1
```

Expected: FAIL because `0009_admin_email_identity.sql` is absent.

- [ ] **Step 3: Add `0009`** with exactly that atomic `ALTER TABLE`. Keep the legacy unique index; `NULL` values are compatible with the singleton.

- [ ] **Step 4: Make identity validation email-centric**. Require epoch, bounded encrypted email, and bounded encrypted TOTP. Accept UID fields only when both are empty or both form a valid legacy pair. New initialization inserts `NULL, NULL` for UID columns.

```go
legacyUID := (len(record.UIDCiphertext) == 0 && len(record.UIDLookup) == 0) ||
    (len(record.UIDCiphertext) > 0 && len(record.UIDCiphertext) <= 512 && len(record.UIDLookup) == sha256.Size)
```

- [ ] **Step 5: Verify and commit**

```bat
go test ./internal/hosted/store/mysqlstore ./internal/hosted/adminidentity -run "Test.*Migration|Test.*Identity|Test.*Initialization" -count=20
```

```bash
git add goserver/internal/hosted/store/mysqlstore/migrations/0009_admin_email_identity.sql goserver/internal/hosted/store/mysqlstore/store_test.go goserver/internal/hosted/adminidentity/service.go goserver/internal/hosted/adminidentity/service_test.go
git commit -m "feat: make administrator identity email-centric"
```

---

### Task 2: Issue sessions from the email code alone

**Files:**
- Modify: `goserver/internal/hosted/adminidentity/service.go`
- Modify: `goserver/internal/hosted/adminidentity/http.go`
- Test: `goserver/internal/hosted/adminidentity/service_test.go`
- Test: `goserver/internal/hosted/adminidentity/http_test.go`

**Interfaces:**
- Changes to `VerifyEmailLogin(context.Context, challengeID, emailCode string) (LoginResult, error)`.
- `POST /api/admin/session/email` accepts exact JSON `{ "challengeId": string, "emailCode": sixDigits }`.
- Adds `CreateEmailLoginSession(context.Context, EmailLoginSessionAttempt) error`; inserted sessions have `totp_verified_at = NULL`.

- [ ] **Step 1: Write RED tests** for code-only login, 7-day cookie, null TOTP timestamp, replay, expiry, fifth/sixth failures, epoch rotation, strict JSON rejecting `totp`, and no email/code response.

- [ ] **Step 2: Verify RED**

```bat
go test ./internal/hosted/adminidentity -run "TestEmailLogin|TestHTTPEmailLogin|TestSQLRepository.*Email" -count=1
```

Expected: FAIL because service and HTTP still require TOTP.

- [ ] **Step 3: Implement code-only verification**. Constant-time compare the code digest, re-read identity and exact epoch, consume the challenge once, generate/hash a token, and create a session through:

```go
type EmailLoginSessionAttempt struct {
    ExpectedCredentialEpoch int64
    TokenHash []byte
    CreatedAt time.Time
    ExpiresAt time.Time
}
```

The SQL transaction locks `admin_identity`, verifies epoch, inserts a null TOTP timestamp, and verifies an ambiguous commit by exact token hash, epoch, timestamps, and null timestamp.

- [ ] **Step 4: Narrow HTTP and retain security ordering**. Keep cheap structure/Origin/CSRF guards, all limit scopes, bounded JSON, stable errors, and map rate limiting to `rate_limited`/429.

- [ ] **Step 5: Verify and commit**

```bat
go test ./internal/hosted/adminidentity -run "TestEmailLogin|TestHTTPEmailLogin|TestSQLRepository.*Email" -count=20
go test -race ./internal/hosted/adminidentity -run "TestEmailLogin|TestHTTPEmailLogin" -count=5
```

```bash
git add goserver/internal/hosted/adminidentity/service.go goserver/internal/hosted/adminidentity/http.go goserver/internal/hosted/adminidentity/service_test.go goserver/internal/hosted/adminidentity/http_test.go
git commit -m "feat: sign in administrators with email codes"
```

---

### Task 3: Remove administrator Bilibili authentication and recovery

**Files:**
- Modify: `goserver/internal/hosted/adminidentity/service.go`, `http.go`, `recovery.go` and their tests
- Modify: `goserver/internal/hosted/app/app.go`, `app_test.go`
- Modify: `goserver/cmd/hosted/main.go`, `main_test.go`
- Modify: `deploy/hosted/nginx.conf.template`
- Test: `tests/hosted-deploy.test.ts`, `tests/hosted-auth.test.ts`

**Interfaces:**
- Removes administrator `BeginVerification`, `PollVerification`, `CancelVerification`, `VerifyLogin`, proof state, proof routes, and old Bilibili+TOTP login mutation.
- Preserves GET/DELETE `/api/admin/session`, `/api/admin/totp`, streamer `/api/auth/bili/challenges`, and `/api/admin/bili-service/*`.

- [ ] **Step 1: Write RED route/dependency tests**. Every method on `/api/admin/auth/bili/challenges` and children must return 404 with zero limiter/verifier/database calls. `POST /api/admin/session` must disappear while GET/DELETE remain exact. `NewService` must compile without a Bilibili verifier.

- [ ] **Step 2: Verify RED**

```bat
go test ./internal/hosted/adminidentity ./internal/hosted/app ./cmd/hosted -run "Test.*Admin.*Bili|Test.*Composition" -count=1
npm exec vitest run tests/hosted-deploy.test.ts tests/hosted-auth.test.ts
```

- [ ] **Step 3: Remove proof and UID authentication code**. Delete administrator proof maps/timers/methods and Bilibili recovery entry points. Keep offline recovery codes, encrypted archives, email delivery, TOTP rotation, and handoff confirmation. Expire legacy pending handoffs that depended on UID.

- [ ] **Step 4: Remove composition and ingress wiring**. Build admin identity without the verifier. Continue sharing the Bilibili verifier only for streamer identity and service-account replacement. Remove administrator Bilibili Nginx classifications while retaining email routes at 10/minute burst 5.

- [ ] **Step 5: Verify and commit**

```bat
go test ./internal/hosted/adminidentity ./internal/hosted/app ./cmd/hosted -count=20
npm exec vitest run tests/hosted-deploy.test.ts tests/hosted-auth.test.ts tests/hosted-bili-service-admin.test.ts
```

```bash
git add goserver/internal/hosted/adminidentity goserver/internal/hosted/app/app.go goserver/internal/hosted/app/app_test.go goserver/cmd/hosted/main.go goserver/cmd/hosted/main_test.go deploy/hosted/nginx.conf.template tests/hosted-deploy.test.ts tests/hosted-auth.test.ts
git commit -m "refactor: remove administrator Bilibili authentication"
```

---

### Task 4: Slide recent TOTP only after successful protected mutations

**Files:**
- Modify: `goserver/internal/hosted/adminidentity/service.go`, `http.go` and tests
- Modify: `goserver/internal/hosted/biligateway/http.go`, `http_test.go`, `credential.go`, `credential_test.go`
- Modify: `goserver/internal/hosted/obs/service.go`, `service_test.go`
- Modify: `goserver/internal/hosted/identity/admin.go`, `admin_test.go`, `http.go`, `repository.go`, `repository_test.go`
- Modify: `goserver/internal/hosted/invitation/http.go`, `service.go`, `service_test.go`
- Modify: `goserver/internal/hosted/obs/http.go`, `http_test.go`
- Create: `goserver/internal/hosted/security/sensitive.go`, with focused tests in `sensitive_test.go` if behavior is added there
- Modify: `goserver/cmd/hosted/main.go`, `main_test.go`

**Interfaces:**
- Keeps `RequireRecentTOTP(context.Context, sessionToken string) error` for read-only authorization checks that never renew.
- Adds a transaction-aware boundary used by every protected mutation:

```go
type SensitiveAuthorizer interface {
    AuthorizeRecentTOTP(context.Context, *sql.Tx, string, time.Time) (SensitiveSession, error)
    RenewRecentTOTP(context.Context, *sql.Tx, SensitiveSession, time.Time) error
}
```

`SensitiveSession` carries only exact session identity and credential epoch needed for the fenced update; it contains no raw token or principal data.

Place the transaction-aware interface and opaque fence in `internal/hosted/security` so `adminidentity`, `identity`, `invitation`, `biligateway`, and `obs` can share it without an import cycle. Keep the existing request-limiter contract in `identity`; no limiter refactor is needed.

- [ ] **Step 1: Write deterministic RED tests** using an injected clock: email login starts without TOTP; valid TOTP opens the window; successful protected mutation renews it; reads/failures do not; 9m59s remains valid; 10m idle expires; revoked/expired/wrong-epoch sessions cannot renew.

- [ ] **Step 2: Verify RED**

```bat
go test ./internal/hosted/adminidentity ./internal/hosted/biligateway ./internal/hosted/obs -run "Test.*RecentTOTP|Test.*Sensitive" -count=1
```

- [ ] **Step 3: Implement exact transaction-aware renewal**. In the caller's transaction, hash token, lock identity then exact session, require active epoch/session and a non-expired existing window, and return a fenced `SensitiveSession`. After the domain mutation and audit insert succeed, renew exactly one row in that same transaction:

```sql
UPDATE site_sessions SET totp_verified_at = ?
WHERE id = ? AND credential_epoch = ? AND revoked_at IS NULL;
```

Do not revive an expired window.

- [ ] **Step 4: Wire atomic success-only renewal**. Service-account replacement, OBS reset, account disable/enable, invitation quota adjustment, recovery archive/material rotation, and administrator email change must call `AuthorizeRecentTOTP` after beginning their existing mutation transaction, perform domain writes and audit insert, call `RenewRecentTOTP`, then commit. Any authorization, mutation, audit, or renewal error rolls back both the domain mutation and renewal. Reads, validation failures, and failed mutations never renew. Recovery SMTP delivery remains an external pre-transaction side effect: if later database rotation fails, the delivered material is unusable and the TOTP window is not renewed; do not introduce an outbox in this task.

- [ ] **Step 5: Verify and commit**

```bat
go test ./internal/hosted/adminidentity ./internal/hosted/biligateway ./internal/hosted/identity ./internal/hosted/invitation ./internal/hosted/obs -run "Test.*RecentTOTP|Test.*Sensitive" -count=50
go test -race ./internal/hosted/adminidentity ./internal/hosted/biligateway ./internal/hosted/identity ./internal/hosted/invitation ./internal/hosted/obs -run "Test.*RecentTOTP|Test.*Sensitive" -count=10
```

Commit exact touched files:

```bash
git commit -m "feat: slide recent TOTP after sensitive activity"
```

---

### Task 5: Simplify administrator UI to email only

**Files:**
- Modify: `src/hosted/api.ts`, `admin-login.ts`, `admin.ts`
- Test: `tests/hosted-admin-login.test.ts`, `hosted-auth.test.ts`, `hosted-bili-service-admin.test.ts`
- Preserve unless a focused RED requires correction: `src/hosted/verification-code.ts`, `shell.css`, `tests/hosted-verification-code.test.ts`

**Interfaces:**
- Changes to `adminEmailLogin(challengeId: string, emailCode: string): Promise<void>`.
- Removes administrator `beginAdminProof`, `pollAdminProof`, `cancelAdminProof`, and `adminLogin`.
- Preserves streamer login, Bili-service replacement, and protected-action `verifyRecentTOTP`.

- [ ] **Step 1: Write RED UI tests**. The signed-out admin view has one email action, no Bilibili button/QR/mobile link, and no login TOTP step. Six email digits call `adminEmailLogin('email-proof', '654321')` and sign in.

Add exact states:

```text
429 -> 操作过于频繁，请稍后重试
503 -> 邮件服务暂时不可用
401 verification -> 验证码错误或已失效
expired/refreshed challenge -> require resend; never reuse old challengeId
```

- [ ] **Step 2: Verify RED**

```bat
npm exec vitest run tests/hosted-admin-login.test.ts tests/hosted-auth.test.ts tests/hosted-bili-service-admin.test.ts
```

- [ ] **Step 3: Reduce login states** to checking, ready, requesting email, awaiting code, verifying, rate-limited, email error, and signed-in. Preserve generation/dispose fences, start/submit single-flight, secret erasure, and late-success logout compensation.

- [ ] **Step 4: Keep TOTP only in protected actions**. Remove it from `mountAdminLogin`; retain the six-cell TOTP prompt in administrator high-risk flows. Clear entered TOTP after every attempt; backend state decides whether the next action prompts.

- [ ] **Step 5: Verify and commit**

```bat
npm exec vitest run tests/hosted-admin-login.test.ts tests/hosted-auth.test.ts tests/hosted-bili-service-admin.test.ts tests/hosted-verification-code.test.ts
npm run typecheck
npm run build:hosted
```

```bash
git add src/hosted/api.ts src/hosted/admin-login.ts src/hosted/admin.ts tests/hosted-admin-login.test.ts tests/hosted-auth.test.ts tests/hosted-bili-service-admin.test.ts
git commit -m "feat: use email-only administrator login"
```

---

### Task 6: Fixed-point review and HK deployment

**Files:** Modify only exact files required by accepted findings. Keep reports local/ignored.

**Interfaces:** Consumes Tasks 1–5 and produces a reviewed reproducible image plus rollback-protected HK deployment.

- [ ] **Step 1: Focused stress gates**

```bat
go test ./internal/hosted/adminidentity ./internal/hosted/app ./internal/hosted/biligateway ./internal/hosted/obs ./internal/hosted/store/mysqlstore ./cmd/hosted -count=20
go test -race ./internal/hosted/adminidentity ./internal/hosted/app ./internal/hosted/biligateway ./internal/hosted/obs ./cmd/hosted -count=5
npm exec vitest run tests/hosted-admin-login.test.ts tests/hosted-auth.test.ts tests/hosted-bili-service-admin.test.ts tests/hosted-verification-code.test.ts tests/hosted-deploy.test.ts
```

- [ ] **Step 2: Full gates**

```bat
go test ./... -count=1
go vet ./...
npm exec vitest run
npm run typecheck
npm run build:hosted
git diff --check
```

- [ ] **Step 3: Independent review** of auth bypass, residual administrator Bilibili paths, migration safety, secret leakage, success-only TOTP renewal, and iOS input. Close every accepted finding with a deterministic RED, minimal GREEN, focused gates, and rereview until no P0–P2 remains.

- [ ] **Step 4: Reproducible image**

```bat
npm run build:hosted-server
npm run verify:hosted-server-repro
```

- [ ] **Step 5: Rollback-protected HK deployment**. Verify archive SHA-256, tag current rollback image, create digest release, atomically switch UI/current symlink, recreate only app, and require exact image plus healthy app/MySQL. Do not push, merge, tag, or update the mainland mirror.

- [ ] **Step 6: Production smoke**. Verify landing 200, public health 404, unauthenticated admin session 401, removed admin Bilibili routes 404, one user-authorized email challenge 201, user-entered code creates a 7-day session, protected action requires TOTP, successful protected activity slides the window, and 10 idle minutes expires it. Never read mailbox or TOTP.

- [ ] **Step 7: Cleanup and audit**. Remove only execution-created local/remote temporary archives, scripts, and copied SSH keys. Preserve rollback releases/images/UI and every pre-existing dirty/untracked file. Confirm staged diff is empty and report commits, digest, production evidence, and blockers.
