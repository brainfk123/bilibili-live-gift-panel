# Hosted Operations, Security, Backup, and Pilot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Package and deploy the hosted service securely on one Hong Kong Linux Lighthouse, prove encrypted backup/restore and six-month log retention, and run a measured 50-registered/10-active invitation-only pilot.

**Architecture:** A reproducible multi-stage image runs the non-root Go service on a private Compose network with MySQL 8; host Nginx is the only public process and proxies to a loopback-bound application port. Unique encrypted database/log artifacts are uploaded to a write-only Hong Kong COS prefix. Operational verification covers schema constraints, restore, tenant privacy, network quality, B 站 stability, and rollback before wider invitations.

**Tech Stack:** Linux, Docker Compose, MySQL 8, Nginx, systemd, age encryption, Tencent COS CLI, PowerShell/Vitest deployment contract tests, Go integration tests.

## Global Constraints

- Run after all four product plans pass review and full regression.
- Use the independent Hong Kong online domain; do not proxy through the domestic personal-ICP node.
- Public ingress is 80/443 only; 80 redirects to HTTPS; MySQL and private health/metrics are not public.
- Run the application as non-root with a read-only filesystem and explicit writable temp directory.
- No secret, Cookie, UID plaintext, DSN, TOTP seed, application key, SMTP password, COS secret, or TLS private key enters Git, image layers, command output, or ordinary logs.
- Keep 30 days of hot logs and at least six natural months total through encrypted COS archives.
- Keep 7 daily, 4 weekly, and 6 monthly encrypted database backups; verify a real restore monthly.
- Production app/COS credentials cannot delete historical backups; retention deletion is a separate lifecycle/admin capability.
- The backup host stores only the age recipient public key; the age private key and application master keys are encrypted offline.
- Initial pilot is invitation-only, at most 50 registered and 10 active accounts.

---

## File Map

- `deploy/hosted/Dockerfile`: reproducible multi-stage Linux build.
- `deploy/hosted/docker-compose.yml`: private app/MySQL network, loopback app publish, health checks, volumes, secrets.
- `deploy/hosted/gift-panel-hosted.service`: systemd Compose lifecycle.
- `deploy/hosted/nginx.conf.template`: TLS, headers, route/body/rate limits, no token logging.
- `deploy/hosted/env.example`: non-secret variable names/defaults only.
- `deploy/hosted/backup.sh`, `archive-logs.sh`, `restore-drill.sh`: encrypted unique artifacts and restore verification.
- `deploy/hosted/cos-lifecycle.json`: separate daily/weekly/monthly/log retention rules.
- `deploy/hosted/backup.service`, `backup.timer`, `archive-logs.service`, `archive-logs.timer`: scheduling.
- `deploy/hosted/logrotate.conf`, `journald.conf`: 30-day hot retention.
- `deploy/hosted/README.md`: provision, deploy, rotate, restore, rollback, incident, and removal runbook.
- `tests/hosted-deploy.test.ts`: static hardening and topology contracts.
- `goserver/internal/hosted/store/mysqlstore/integration_test.go`: real MySQL migration/constraint tests.
- `deploy/hosted/docker-compose.test.yml`: disposable integration MySQL.
- `scripts/build-hosted.mjs`: deterministic Linux binary/UI/image context build.
- `docs/operations/hosted-pilot-checklist.md`: seven-day evidence form and go/no-go thresholds.

---

### Task 1: Add reproducible packaging and private single-host topology

**Files:**
- Create: `deploy/hosted/Dockerfile`
- Create: `deploy/hosted/docker-compose.yml`
- Create: `deploy/hosted/gift-panel-hosted.service`
- Create: `deploy/hosted/env.example`
- Create: `scripts/build-hosted.mjs`
- Create: `tests/hosted-deploy.test.ts`
- Modify: `package.json`

**Interfaces:**
- Produces: `npm run build:hosted-server` and a Compose project with app reachable only at `127.0.0.1:12500`.

- [ ] **Step 1: Write the failing deployment contract**

Assert Compose has no MySQL `ports`, app publishes only `127.0.0.1:12500:12500`, uses a non-root user, drops all capabilities, sets `no-new-privileges`, uses read-only root filesystem, mounts named DB data, references external secret files, and has health checks. Reject `latest` image tags and literal passwords/keys.

Run: `npx vitest run tests/hosted-deploy.test.ts`

Expected: FAIL because deployment files do not exist.

- [ ] **Step 2: Implement the multi-stage build**

Stage 1 runs `npm ci && npm run build:hosted`; stage 2 runs `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' ./cmd/hosted`; final stage is a pinned minimal Debian or distroless non-root image with CA certificates and only the binary/UI assets. Do not copy repository root, `.git`, local config, FFmpeg, or desktop executable.

- [ ] **Step 3: Implement Compose/systemd lifecycle**

Use private network `hosted_internal`; MySQL has `--skip-name-resolve`, utf8mb4, a 512 MiB InnoDB buffer pool, health check, and a 1.25 GiB container memory limit. Set the app to a 1 GiB container limit with `GOMEMLIMIT=768MiB`. The app depends on healthy DB and reads `/run/secrets/*`. The systemd unit runs `docker compose up -d --remove-orphans`, uses `docker compose stop -t 30` on shutdown, and does not contain credentials.

- [ ] **Step 4: Verify image contents and topology**

Run:

```powershell
npm run build:hosted-server
npx vitest run tests/hosted-deploy.test.ts
docker compose -f deploy/hosted/docker-compose.yml config
docker image history gift-panel-hosted:test --no-trunc
```

Expected: no secret values/layers; config exposes only loopback app port.

- [ ] **Step 5: Commit**

Stage only deployment/build/test/package files and commit `build: package hosted service`.

---

### Task 2: Add Nginx TLS, security headers, and abuse controls

**Files:**
- Create: `deploy/hosted/nginx.conf.template`
- Create: `deploy/hosted/logrotate.conf`
- Create: `deploy/hosted/journald.conf`
- Modify: `tests/hosted-deploy.test.ts`

**Interfaces:**
- Produces: public 80→443 redirect and HTTPS routes to loopback app.

- [ ] **Step 1: Extend failing static contracts**

Require TLS 1.2/1.3, `server_tokens off`, HSTS `max-age=15552000` without `includeSubDomains` until every subdomain is confirmed HTTPS, CSP `default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'`, nosniff, referrer policy, permissions policy, upload body 2 MiB, proxy buffering disabled only for SSE, and no proxy for `/healthz` or private metrics.

- [ ] **Step 2: Implement exact logging and token privacy**

Access log records request ID, status, duration, method, normalized route, source IP, and user agent but not query strings, fragments, request/response bodies, cookies, authorization, or upstream headers. OBS long token remains in fragment and therefore never reaches Nginx.

- [ ] **Step 3: Add rate limits**

Use separate zones: auth/QR 10 requests/minute/IP burst 5; invitations and migration 30/minute/account-facing connection burst 10 plus application limits; ordinary API 120/minute/IP burst 30; connection limits 20/IP. SSE routes use connection limits and application authentication rather than request-rate loops.

- [ ] **Step 4: Validate configuration**

Render `ONLINE_DOMAIN` and certificate paths into a temporary file, run `nginx -t -c <rendered>`, run the deployment Vitest, and inspect that `/healthz`, `/internal/*`, MySQL, and Docker socket are not routed.

- [ ] **Step 5: Commit**

Commit `deploy: harden hosted ingress` with Nginx/log configs and tests only.

---

### Task 3: Verify real MySQL migrations and tenant constraints

**Files:**
- Create: `deploy/hosted/docker-compose.test.yml`
- Create: `goserver/internal/hosted/store/mysqlstore/integration_test.go`
- Modify: `package.json`

**Interfaces:**
- Produces: `npm run test:hosted-mysql`.

- [ ] **Step 1: Write integration tests behind an explicit DSN**

Tests apply all embedded migrations twice, reject changed checksums, reject duplicate UID HMAC, negative quota, duplicate invitation hashes, two active bindings, stale configuration state revision, invalid foreign keys, and concurrent invitation redemption with exactly one commit.

- [ ] **Step 2: Add disposable test MySQL**

Compose test publishes MySQL only on loopback with an ephemeral named volume and a test-only password declared in that test file. It is never used by production Compose.

- [ ] **Step 3: Add a deterministic runner**

`test:hosted-mysql` starts the test DB, waits for `mysqladmin ping`, sets `HOSTED_MYSQL_TEST_DSN`, runs only integration-tag tests, and always stops the Compose project without deleting any non-test volume.

- [ ] **Step 4: Run database/full verification**

Run `npm run test:hosted-mysql`, full/race Go tests, all frontend tests, typecheck, builds, and `git diff --check`.

- [ ] **Step 5: Commit**

Commit `test: verify hosted MySQL contracts`.

---

### Task 4: Add encrypted database backup, log archive, and restore drill

**Files:**
- Create: `deploy/hosted/backup.sh`
- Create: `deploy/hosted/archive-logs.sh`
- Create: `deploy/hosted/restore-drill.sh`
- Create: `deploy/hosted/backup.service`
- Create: `deploy/hosted/backup.timer`
- Create: `deploy/hosted/archive-logs.service`
- Create: `deploy/hosted/archive-logs.timer`
- Create: `deploy/hosted/cos-lifecycle.json`
- Modify: `tests/hosted-deploy.test.ts`

**Interfaces:**
- Produces: unique `.sql.zst.age` and `.tar.zst.age` objects uploaded to configured Hong Kong COS prefixes.

- [ ] **Step 1: Write failing backup secrecy contracts**

Require `set -euo pipefail`, `umask 077`, `flock`, `mktemp -d`, trap cleanup, no password on command line, age recipient-only encryption, unique UTC timestamp+random suffix keys, SHA-256 sidecar, upload-before-success marker, and no `coscli rm`/delete command.

- [ ] **Step 2: Implement daily database backup**

Read MySQL credentials from root-readable files, run `mysqldump --single-transaction --quick --routines --events --hex-blob`, compress with zstd, encrypt to an offline age recipient, compute SHA-256, upload artifact and checksum with COS CLI, and delete local temporary files via trap. Every run writes a unique `daily/` object; Sunday also writes `weekly/`, and the first UTC day of a month also writes `monthly/`. The server never holds the age private key.

- [ ] **Step 3: Implement six-month log archive**

Archive only closed/rotated Nginx and application log files older than 30 days, encrypt identically, upload to a distinct prefix, and write a manifest containing time range, file count, sizes, and hashes but no log content. `cos-lifecycle.json` retains `daily/` for 7 days, `weekly/` for 28 days, `monthly/` for 183 days, and log archives for 190 days; production upload credentials cannot alter lifecycle or delete objects.

- [ ] **Step 4: Implement restore drill**

`restore-drill.sh` runs only on an isolated operator host that has the age private key. It downloads a selected backup, verifies checksum, decrypts, restores into disposable MySQL, runs schema/count/invariant queries, records RPO timestamp and elapsed RTO, then destroys only the explicitly named disposable Compose project/volume after confirming their resolved names begin with `gift-panel-restore-`.

- [ ] **Step 5: Verify and commit**

Run ShellCheck, deployment Vitest, a synthetic backup with fake COS adapter, and a real disposable restore. Commit `ops: add encrypted hosted backups`.

---

### Task 5: Add the production runbook, monitoring, rotation, and incident checks

**Files:**
- Create: `deploy/hosted/README.md`
- Create: `deploy/hosted/health-check.sh`
- Create: `docs/operations/hosted-pilot-checklist.md`
- Modify: `goserver/internal/hosted/app/app.go`
- Modify: `goserver/internal/hosted/app/app_test.go`
- Modify: `tests/hosted-deploy.test.ts`

**Interfaces:**
- Produces: private `/healthz` and `/internal/metrics` with no secrets/UIDs, plus executable deployment/rollback/rotation procedures.

- [ ] **Step 1: Write failing health/metrics privacy tests**

Metrics expose only aggregate CPU/memory process data, MySQL health/latency, HTTP status counts, active account count, distinct room-source count, queue depth, B 站 reconnect/risk/breaker state, migration counts, backup age, and certificate expiry. Assert no account ID, UID, room number, Cookie, token, nickname, URL query, or config JSON.

- [ ] **Step 2: Implement private monitoring**

Bind metrics through the loopback app only; Nginx must not route `/internal/metrics`. `health-check.sh` calls loopback health and checks disk, Compose health, backup age, certificate expiry, and log archive age, returning nonzero with stable codes suitable for Tencent monitoring alarms.

- [ ] **Step 3: Write exact operational procedures**

Document provisioning, DNS/TLS, firewall, secrets file modes, admin initialization, B 站 service credential binding, deploy, database migration, canary check, rollback to prior immutable image, app/HMAC/encryption key rotation, SMTP/COS/TLS rotation, backup restore, service-account compromise, B 站 breaker incident, disk-full handling, account disable, and complete server decommission.

- [ ] **Step 4: Add release and rollback verification**

The runbook requires recording image digest, migration head, backup object/checksum, health output, smoke-test IDs, and previous digest before deploy. Rollback never reverses an applied schema destructively; it restores the previous application image and uses forward-compatible migrations or the documented database restore decision.

- [ ] **Step 5: Verify and commit**

Run Go tests, deployment Vitest, ShellCheck, link/path checks, and `git diff --check`. Commit `docs: add hosted operations runbook`.

---

### Task 6: Execute the seven-day closed pilot and record the go/no-go result

**Files:**
- Create at execution time: `docs/operations/YYYY-MM-DD-hosted-pilot-result.md`
- Modify only for verified production defects: files named by the defect's own RED→GREEN task.

**Interfaces:**
- Produces: evidence-backed go/no-go decision for expanding invitations to the 50/10 baseline.

- [ ] **Step 1: Establish the pilot baseline**

Record server plan/region/image digest, domain/certificate expiry, migration head, backup checksum/restore result, application key custody confirmation, B 站 service credential version, and initial active invite count. Do not record secret values or UID plaintext.

- [ ] **Step 2: Run security and privacy acceptance**

Attempt cross-account configuration/migration/OBS access, stale CSRF, reused invite, revoked OBS token, oversized/deep migration JSON, wrong admin TOTP, expired recovery code, and public access to health/metrics/MySQL. Search database dumps and six-month log samples for seeded viewer UID/name/avatar and seeded secret markers; expected result is zero matches.

- [ ] **Step 3: Run functional/capacity acceptance**

Exercise 10 active accounts over at least 7 rooms, including 3 accounts sharing one room. Verify one upstream connection per canonical room, per-account state isolation, room-switch barrier, 10-minute natural shutdown, commit-before-OBS, database outage degradation, pending migration application, and rollback.

- [ ] **Step 4: Measure mainland-to-Hong-Kong and B 站 stability for seven days**

Collect China Telecom, China Unicom, and China Mobile samples during daytime and evening: DNS/TLS/connect/first-byte, packet loss, OBS SSE reconnects, B 站 long-connection uptime/reconnects, risk/429 counts, queue delay, CPU, RSS, MySQL p95, disk growth, and outgoing traffic. Do not invent SLA; preserve raw timestamps and aggregate percentiles.

- [ ] **Step 5: Apply go/no-go thresholds**

Go only when: no critical tenant/privacy/credential issue; backups restored successfully; no lost committed gameplay transitions; p95 event commit-to-OBS under 500 ms at 10 active accounts; queue never exceeds 50% capacity in normal operation; CPU p95 below 70%; RSS p95 below 80% of limit; disk forecast exceeds 90 days; and B 站 breaker/risk events are understood and recover without aggressive retries. Otherwise keep invitations capped, file a focused defect with reproduction evidence, and fix via its own TDD commit before restarting the seven-day window.

---

## Plan Completion Gate

Do not call the hosted service production-ready until the deployment contract, real MySQL constraints, encrypted backup and actual restore, public-route scan, seeded privacy search, 10-account capacity run, and full seven-day three-carrier/B 站 pilot all have recorded evidence. The domestic static node remains independent throughout.
