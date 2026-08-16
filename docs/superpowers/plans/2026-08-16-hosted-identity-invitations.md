# Hosted Identity, Administrator, and Invitations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver B 站 UID-based streamer login, the unique TOTP-protected administrator, and concurrency-safe invitation quotas and one-time registration.

**Architecture:** `identity` owns temporary B 站 verification challenges and hashed site sessions; `adminidentity` composes the same B 站 identity proof with TOTP and encrypted recovery delivery. `invitation` owns quota and code lifecycle in MySQL transactions. HTTP handlers obtain the current account from middleware and never accept a caller-selected tenant ID.

**Tech Stack:** Go 1.26, MySQL 8, `net/http`, `crypto/rand`, AES-GCM, HMAC-SHA-256, `golang.org/x/crypto/scrypt`, `github.com/pquerna/otp/totp`, TypeScript/Vitest.

## Global Constraints

- Run only after `2026-08-16-hosted-foundation.md` passes its completion gate.
- A streamer account binds exactly one verified B 站 UID; login UID and target room are unrelated.
- Temporary streamer B 站 credentials exist only in memory until UID verification and are then destroyed.
- Site sessions, invitation codes, recovery codes, and OBS credentials are stored only as hashes.
- Every future OBS credential/session is bound to the account `credential_epoch`; disabling or rebinding increments the epoch so credentials can be invalidated before the OBS tables exist.
- B 站 UID plaintext is AEAD-encrypted; a separate HMAC key provides equality lookup.
- The unique administrator logs in with the configured B 站 UID plus TOTP; high-risk actions require recent TOTP.
- Invitation quota is deducted when a streamer generates a code; revoke/expire/use never refunds it.
- Complete invitation and recovery codes are displayed exactly once.
- No handler may log Cookie, QR secret, UID plaintext, TOTP secret/code, site token, invitation code, or recovery code.

---

## File Map

- `goserver/internal/hosted/security/keys.go`: purpose-separated AEAD, HMAC, hashing, and random-token helpers.
- `goserver/internal/hosted/identity/model.go`, `repository.go`, `service.go`: accounts, bindings, challenges, and site sessions.
- `goserver/internal/hosted/identity/biliqr/adapter.go`: true-external B 站 QR adapter with injected HTTP client.
- `goserver/internal/hosted/identity/http.go`: login/register/session routes and middleware.
- `goserver/internal/hosted/adminidentity/*`: initialization, TOTP, recent verification, recovery archive, and SMTP port.
- `goserver/internal/hosted/identity/admin.go`: administrator-only disable/enable and exception rebind commands.
- `goserver/internal/hosted/invitation/*`: quota ledger, one-time codes, redemption transaction, and HTTP handlers.
- `goserver/internal/hosted/store/mysqlstore/migrations/0002_identity_and_invitations.sql`: identity/admin/invitation schema.
- `src/hosted/auth.ts`, `admin.ts`, `invitations.ts`: thin HTTP adapters and views.
- `tests/hosted-auth.test.ts`, `hosted-invitations.test.ts`: browser contracts.

---

### Task 1: Add purpose-separated cryptography and identity schema

**Files:**
- Create: `goserver/internal/hosted/security/keys.go`
- Create: `goserver/internal/hosted/security/keys_test.go`
- Create: `goserver/internal/hosted/store/mysqlstore/migrations/0002_identity_and_invitations.sql`
- Create: `goserver/internal/hosted/identity/model.go`
- Create: `goserver/internal/hosted/identity/repository.go`
- Create: `goserver/internal/hosted/identity/repository_test.go`

**Interfaces:**
- Produces: `security.Keyring.Seal`, `Open`, `Lookup`, `HashToken`, and `NewToken`.
- Produces: `identity.Repository` account/binding/session methods.

- [ ] **Step 1: Write failing crypto tests**

Use distinct 32-byte AEAD and HMAC keys and assert round-trip, randomized ciphertext, purpose binding, token hashing, and no plaintext leakage:

```go
func TestKeyringBindsCiphertextToPurpose(t *testing.T) {
    keys := fixedKeyring()
    sealed, err := keys.Seal("bili_uid", []byte("123456"))
    if err != nil { t.Fatal(err) }
    if bytes.Contains(sealed, []byte("123456")) { t.Fatal("plaintext leaked") }
    if _, err := keys.Open("totp_secret", sealed); err == nil { t.Fatal("wrong purpose accepted") }
}
```

Run: `go -C goserver test ./internal/hosted/security -count=1`

Expected: FAIL because the package does not exist.

- [ ] **Step 2: Implement the keyring**

Use AES-256-GCM with a fresh nonce, purpose as additional authenticated data, HMAC-SHA-256 for lookup, SHA-256 for high-entropy token hashes, and `crypto/rand` for 32-byte tokens. Encode public tokens as unpadded base64url. Include a one-byte key version in ciphertext and reject unknown versions.

- [ ] **Step 3: Add schema constraints**

Migration `0002` creates `streamer_accounts`, `bili_uid_bindings`, `site_sessions`, `admin_identity`, `admin_totp`, `admin_recovery_codes`, `invitation_quotas`, `invitation_quota_events`, `invitations`, and `audit_events`. `streamer_accounts` includes a positive `credential_epoch` starting at 1. Enforce unique UID HMAC, one active binding per account, unique token/code hash, non-negative quota, immutable audit IDs, and foreign keys. Do not store UID plaintext columns.

- [ ] **Step 4: Implement and test repository transactions**

Define:

```go
type Repository interface {
    FindAccountByUIDLookup(context.Context, []byte) (Account, error)
    CreateBoundAccount(context.Context, EncryptedUID) (Account, error)
    CreateSession(context.Context, Session) error
    FindSessionByHash(context.Context, []byte, time.Time) (Session, error)
    RevokeSession(context.Context, []byte) error
}
```

Tests use `go-sqlmock` to require account/binding creation in one transaction and reject duplicate UID lookup without exposing database text to the caller.

- [ ] **Step 5: Verify and commit**

Run:

```powershell
go -C goserver test ./internal/hosted/security ./internal/hosted/identity -count=1
go -C goserver test -race ./internal/hosted/security ./internal/hosted/identity -count=3
git diff --check
```

Commit:

```powershell
git add -- goserver/internal/hosted/security goserver/internal/hosted/identity/model.go goserver/internal/hosted/identity/repository.go goserver/internal/hosted/identity/repository_test.go goserver/internal/hosted/store/mysqlstore/migrations/0002_identity_and_invitations.sql goserver/go.mod goserver/go.sum
git commit -m "feat: add hosted identity storage"
```

---

### Task 2: Implement ephemeral B 站 verification and site sessions

**Files:**
- Create: `goserver/internal/hosted/identity/service.go`
- Create: `goserver/internal/hosted/identity/service_test.go`
- Create: `goserver/internal/hosted/identity/http.go`
- Create: `goserver/internal/hosted/identity/http_test.go`
- Create: `goserver/internal/hosted/identity/biliqr/adapter.go`
- Create: `goserver/internal/hosted/identity/biliqr/adapter_test.go`
- Modify: `goserver/internal/hosted/app/app.go`

**Interfaces:**
- Produces: `identity.BiliVerifier` port, `Service.Begin`, `Poll`, `Login`, `Logout`, and `RequireSession`.

- [ ] **Step 1: Write failing service tests against an in-memory verifier**

Use the real seam:

```go
type BiliVerifier interface {
    Begin(context.Context) (Challenge, error)
    Poll(context.Context, string) (Verification, error)
    Forget(string)
}

type Verification struct { UID string; CompletedAt time.Time }
```

Test pending, expired, successful existing-account login, unknown-account registration intent, duplicate UID, challenge single-use, and `Forget` being called on every terminal result. Prove returned site session tokens are absent from repository arguments except as hashes.

- [ ] **Step 2: Extract the production QR adapter**

Move only B 站 QR creation/polling/UID discovery behavior needed from the desktop login flow into `identity/biliqr`. Store QR key and temporary Cookie in an in-memory map with a 5-minute TTL; delete on success, expiry, cancellation, and process shutdown. The adapter returns UID only and never exposes Cookie through `Verification`.

- [ ] **Step 3: Add HTTP/session middleware**

Routes:

```text
POST /api/auth/bili/challenges
GET  /api/auth/bili/challenges/{id}
POST /api/auth/session
DELETE /api/auth/session
GET  /api/auth/session
```

Set the site token in `__Host-gift_panel_session` with `Secure`, `HttpOnly`, `Path=/`, `SameSite=Lax`, and an absolute expiry. Middleware hashes the cookie, loads a non-expired session, injects `AccountID` into context, and never accepts `accountId` from request JSON/query.

- [ ] **Step 4: Add rate-limit and response tests**

Use an injected limiter port and assert per-IP/global challenge limits, generic authentication failures, no UID/Cookie in errors, CSRF/Origin rejection on session creation, and logout revocation.

- [ ] **Step 5: Verify and commit**

Run `go -C goserver test ./internal/hosted/identity/... ./internal/hosted/app -count=1`, then `go -C goserver test -race ./internal/hosted/identity/... -count=3` and the full Go suite. Commit as `feat: add Bilibili hosted login` with only the listed files.

---

### Task 3: Add the TOTP administrator and encrypted email recovery

**Files:**
- Create: `goserver/internal/hosted/adminidentity/service.go`
- Create: `goserver/internal/hosted/adminidentity/service_test.go`
- Create: `goserver/internal/hosted/adminidentity/recovery.go`
- Create: `goserver/internal/hosted/adminidentity/recovery_test.go`
- Create: `goserver/internal/hosted/adminidentity/http.go`
- Create: `goserver/internal/hosted/adminidentity/http_test.go`
- Create: `goserver/internal/hosted/adminidentity/smtp.go`
- Modify: `goserver/internal/hosted/app/app.go`
- Modify: `goserver/internal/hosted/platform/config.go`
- Modify: `goserver/cmd/hosted/main.go`
- Modify: `goserver/cmd/hosted/main_test.go`

**Interfaces:**
- Produces: `adminidentity.Service.Initialize`, `VerifyLogin`, `RequireRecentTOTP`, `SendRecovery`, and `CompleteRecovery`.
- Produces: `MailSender.Send(context.Context, Message) error` with SMTP and in-memory adapters.

- [ ] **Step 1: Write failing TOTP lifecycle tests**

Use an injected clock and TOTP validator. Test one-time local initialization, wrong configured UID, correct UID+TOTP, replay-window rejection for high-risk confirmation, session-wide revocation after recovery, and exactly one active administrator identity.

- [ ] **Step 2: Implement initialization and recent verification**

Initialization is available only to the local CLI:

```text
gift-panel-hosted admin init --uid <uid> --email <address>
```

The command prints the `otpauth://` URI and initial recovery package password once; it does not expose an HTTP initialization route. Store the TOTP secret with `Keyring.Seal("admin_totp", ...)`. Record `totp_verified_at` on the admin session and require it to be within 5 minutes for high-risk operations.

- [ ] **Step 3: Implement the recovery archive**

Generate ten 16-byte recovery codes. Store only SHA-256 hashes. Build a binary envelope containing version, random salt, scrypt parameters, nonce, and AES-256-GCM ciphertext. Derive the archive key with `scrypt.Key(password, salt, 32768, 8, 1, 32)`. Email only the encrypted attachment; return the random 20-character decryption password to the current local/admin page once.

- [ ] **Step 4: Test recovery secrecy and SMTP composition**

Assert the attachment contains no code plaintext, wrong password fails authentication, sent mail contains no password, successful recovery requires a fresh matching admin B 站 UID proof, consumes one code, rotates TOTP, invalidates every old recovery code, and revokes every admin session.

- [ ] **Step 5: Verify and commit**

Run focused, race, and full Go tests; run `git diff --check`. Commit as `feat: secure hosted administrator` and include the pinned new Go dependencies.

---

### Task 4: Add administrator account disable and exception rebind

**Files:**
- Create: `goserver/internal/hosted/identity/admin.go`
- Create: `goserver/internal/hosted/identity/admin_test.go`
- Modify: `goserver/internal/hosted/identity/http.go`
- Modify: `goserver/internal/hosted/identity/http_test.go`
- Modify: `goserver/internal/hosted/app/app.go`

**Interfaces:**
- Produces: `identity.Service.DisableAccount`, `EnableAccount`, and `RebindVerifiedUID`.

- [ ] **Step 1: Write failing authorization and transaction tests**

Test that only an administrator session with TOTP verified within 5 minutes can disable, enable, or rebind; a streamer cannot call these routes; a disabled account cannot create a new site session; and failed audit/session-revocation writes roll back the status/binding/credential-epoch change.

- [ ] **Step 2: Implement disable and enable**

Disabling sets `disabled_at` and a required administrator reason, increments `credential_epoch`, revokes all current site sessions in the same transaction, and writes an audit event. No OBS table exists in this phase; every later OBS credential/session must store the issuing epoch and fail validation after the increment. Enabling clears `disabled_at` but does not restore revoked sessions or invitations. Runtime plan Task 3 must reject/close leases for disabled accounts.

- [ ] **Step 3: Implement verified UID rebind**

Require a fresh B 站 verification whose UID differs from the current binding and is not bound elsewhere. In one transaction close the old binding, insert the encrypted new UID/HMAC binding, increment `credential_epoch`, revoke all site sessions, and write old/new UID lookup hashes, timestamp, administrator ID, and reason to audit. The epoch increment invalidates future OBS credentials without referencing a table that is not created until the runtime plan. Do not consume or create an invitation.

- [ ] **Step 4: Add stable HTTP contracts**

```text
POST /api/admin/accounts/{id}/disable
POST /api/admin/accounts/{id}/enable
POST /api/admin/accounts/{id}/rebind
```

Rebind body contains only verification challenge ID and reason; it never accepts UID plaintext directly. Responses expose internal account ID/status only, not UID plaintext.

- [ ] **Step 5: Verify and commit**

Run focused tests with `-count=10`, race tests, full Go tests, and commit `feat: manage hosted account identity`.

---

### Task 5: Implement invitation quotas, generation, revocation, and redemption

**Files:**
- Create: `goserver/internal/hosted/invitation/model.go`
- Create: `goserver/internal/hosted/invitation/service.go`
- Create: `goserver/internal/hosted/invitation/service_test.go`
- Create: `goserver/internal/hosted/invitation/http.go`
- Create: `goserver/internal/hosted/invitation/http_test.go`
- Modify: `goserver/internal/hosted/app/app.go`

**Interfaces:**
- Produces: `Service.AdjustQuota`, `Generate`, `Revoke`, `List`, and `Redeem`.

- [ ] **Step 1: Write failing transaction tests**

Tests must prove:

```go
func TestGenerateDeductsQuotaAtCreation(t *testing.T) {}
func TestRevokeAndExpireNeverRefundQuota(t *testing.T) {}
func TestConcurrentRedeemHasExactlyOneWinner(t *testing.T) {}
func TestAdminGenerationDoesNotConsumeQuota(t *testing.T) {}
func TestRevokedInvitationRemainsListable(t *testing.T) {}
```

Use repository transaction fakes that fail if quota read/decrement and code insert are not in one transaction.

- [ ] **Step 2: Implement generation and listing**

Generate 32 random bytes, return unpadded base64url once, save SHA-256 only, and expose a four-character masked suffix. For a streamer, lock its quota row `FOR UPDATE`, reject zero, decrement, append a quota event, and insert the invitation atomically. Admin generation bypasses quota but still records creator and audit event.

- [ ] **Step 3: Implement revoke, expire, and redeem**

Revocation changes only `active -> revoked`. Expiration changes only `active -> expired`. Redemption first verifies B 站 UID, then locks the invitation by hash and atomically creates the account/binding, sets `used`, records invited account, and writes audit. Duplicate UID or second redemption rolls back every write.

- [ ] **Step 4: Add HTTP authorization tests**

Routes expose current account quota/list/generate and admin quota adjustment. Streamers cannot adjust quota or inspect other creators. Full codes appear only in the successful generation response; all list responses contain masked hint only. High-risk admin adjustment requires recent TOTP.

- [ ] **Step 5: Verify and commit**

Run invitation tests with `-count=10`, race tests, full Go tests, and `git diff --check`. Commit as `feat: add invitation registration`.

---

### Task 6: Add hosted authentication, invitation, and administrator views

**Files:**
- Create: `src/hosted/api.ts`
- Create: `src/hosted/auth.ts`
- Create: `src/hosted/invitations.ts`
- Create: `src/hosted/admin.ts`
- Create: `tests/hosted-auth.test.ts`
- Create: `tests/hosted-invitations.test.ts`
- Modify: `src/hosted/main.ts`
- Modify: `src/hosted/shell.ts`

**Interfaces:**
- Produces: typed `HostedAPI` methods and views that never retain B 站 Cookie or complete codes after the current render lifecycle.

- [ ] **Step 1: Write failing browser contract tests**

Test QR pending/success/expiry, invite-required registration, one-time code reveal with copy button, masked history including revoked rows, quota display, TOTP second step, recent verification prompt, recovery archive download/password separation, account disable/enable, and exception rebind requiring a fresh verification challenge and reason.

- [ ] **Step 2: Implement one HTTP adapter**

All hosted UI fetches go through `HostedAPI`, which sets `credentials:'same-origin'`, supplies CSRF header from a non-secret bootstrap value, rejects non-JSON responses, maps stable error codes, and never logs bodies.

- [ ] **Step 3: Implement views with secret lifecycle controls**

Complete invitation/recovery strings exist only in closure-local state, are replaced with masked text when dialogs close, and are not written to `localStorage`, `sessionStorage`, URL, analytics, or console. QR challenges cancel when their view unmounts.

- [ ] **Step 4: Run frontend and full verification**

Run `npx vitest run tests/hosted-auth.test.ts tests/hosted-invitations.test.ts`, `npm run typecheck`, `npm run build:hosted`, `npm test`, full Go tests, and `git diff --check`.

- [ ] **Step 5: Commit the identity UI**

Stage only `src/hosted`, the two hosted tests, and any intentional package lock change. Commit as `feat: add hosted account onboarding`.

---

## Plan Completion Gate

Verify the five invariants with focused tests: temporary B 站 credentials are forgotten, UID uniqueness is database-backed, administrator recovery revokes old credentials, quota deducts at generation, and one code has one redemption winner. Then run all Go/frontend tests and request security-focused code review before starting configuration and migration.
