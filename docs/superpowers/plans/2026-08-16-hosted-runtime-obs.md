# Hosted Runtime, Bilibili Gateway, and OBS Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver one-room-per-account automatic runtimes, shared B 站 room connections, transaction-before-broadcast gameplay updates, and read-only OBS pages with fragment-token exchange.

**Architecture:** `BiliGateway` is the only seam to B 站 HTTP and credential use. `RoomSource.Manager` owns one upstream connection per normalized room and fans out immutable events. `Runtime.Manager` owns one serial queue per active account, strips viewer identity before calling the shared gameplay engine, commits identity-free state before publishing SSE snapshots, and keeps viewer information only in memory. Config and OBS SSE connections jointly hold the automatic runtime lease.

**Tech Stack:** Go 1.26, Gorilla WebSocket, `golang.org/x/sync/singleflight`, MySQL 8 transactions, Server-Sent Events, TypeScript/Vitest.

## Global Constraints

- Run after the foundation, identity, and configuration/migration plans pass review.
- One account has at most one active target room; room ownership is not inferred from login UID.
- Multiple accounts on the same normalized room share exactly one upstream B 站 connection.
- Use one administrator-controlled B 站 service account; never use streamer login credentials for room data.
- OBS receives an update only after the corresponding MySQL transaction commits.
- Viewer UID, nickname, avatar, and contribution rows live only in the current runtime memory and disappear on session end/restart.
- Long and short OBS credentials carry the account `credential_epoch` created by the identity plan and are rejected whenever it differs from the current account epoch.
- No manual start/stop route or UI control may exist.
- Config or OBS presence starts/keeps the runtime; both absent for 10 minutes ends the session.
- Database outage buffering is capped at 500 events per account and 60 seconds, whichever occurs first.
- Stable B 站 event IDs are hashed and retained only for 24-hour dedupe; events without stable IDs get process-local dedupe only.
- B 站 retry/limit policy is global+account+endpoint, with jittered backoff and an egress circuit breaker; do not add rotating proxies.

---

## File Map

- `goserver/internal/hosted/biligateway/*`: encrypted service credential, HTTP limiter/cache/singleflight, and room session creation.
- `goserver/internal/hosted/roomsource/*`: normalized room registry, one connection per room, immutable event fanout, reconnect and egress breaker.
- `goserver/internal/hosted/runtime/*`: leases, account queues, room switch barrier, ephemeral viewers, persistence-before-publish.
- `goserver/internal/hosted/obs/*`: long credential, fragment exchange, scoped short session, SSE snapshot stream.
- `goserver/internal/hosted/store/mysqlstore/migrations/0005_runtime_and_obs.sql`: rooms, session aggregates, dedupe, service credential, and OBS tables; it extends the `live_sessions` table introduced by configuration/migration `0004` rather than recreating it.
- `src/hosted/runtime.ts`, `room.ts`, `obs-settings.ts`: account runtime controls/status without stop action.
- `src/hosted/obs/main.ts`, `obs/view.ts`, `obs.css`, `obs.html`: read-only OBS entry.
- `tests/hosted-runtime.test.ts`, `hosted-obs.test.ts`: UI/URL privacy contracts.

---

### Task 1: Add the encrypted B 站 service credential and controlled gateway

**Files:**
- Create: `goserver/internal/hosted/biligateway/credential.go`
- Create: `goserver/internal/hosted/biligateway/credential_test.go`
- Create: `goserver/internal/hosted/biligateway/gateway.go`
- Create: `goserver/internal/hosted/biligateway/gateway_test.go`
- Create: `goserver/internal/hosted/biligateway/limits.go`
- Create: `goserver/internal/hosted/biligateway/http.go`
- Create: `goserver/internal/hosted/biligateway/http_test.go`
- Create: `goserver/internal/hosted/store/mysqlstore/migrations/0005_runtime_and_obs.sql`
- Modify: `src/hosted/admin.ts`
- Create: `tests/hosted-bili-service-admin.test.ts`
- Modify: `goserver/go.mod`
- Modify: `goserver/go.sum`

**Interfaces:**
- Produces: `Gateway.RoomInfo`, `GiftCatalog`, `OpenRoom`, and `Status`.
- Produces: `CredentialStore.Replace` and `Load`, encrypted with purpose `bili_service_credential`.

- [ ] **Step 1: Write failing credential and redaction tests**

Assert Cookie plaintext is absent from SQL args/log capture after encryption, replacement increments credential version, and decryption failure returns `credential_unavailable` without ciphertext or key details.

- [ ] **Step 2: Define the true-external gateway seam**

```go
type Gateway interface {
    RoomInfo(context.Context, string) (RoomInfo, error)
    GiftCatalog(context.Context, string) ([]gameplay.GiftInfo, error)
    OpenRoom(context.Context, string, Sink) (Connection, error)
    Status() Status
}
```

Only the production adapter may open B 站 HTTP/WebSocket connections. Tests use an in-memory adapter.

- [ ] **Step 3: Implement limits, cache, and singleflight**

Normalize room IDs before cache keys. Cache room info for 5 minutes and gift catalog for 10 minutes; coalesce identical misses. Apply token buckets at global, account, and endpoint levels. Parse non-2xx, bounded response bodies, `Retry-After`, and known risk responses into stable error kinds. Never log response bodies or Cookie headers.

- [ ] **Step 4: Implement circuit-breaker tests**

Open the egress breaker after 10 correlated risk/429 responses in 60 seconds across at least 3 accounts; hold open 2 minutes, allow one half-open probe, close after 3 successful probes. Network timeouts from one room do not open the global breaker unless the correlation condition is met.

Add administrator-only `POST /api/admin/bili-service/challenge` and `POST /api/admin/bili-service/replace` handlers. Replacement accepts a completed service-account QR challenge rather than Cookie text, requires TOTP verified within 5 minutes, encrypts the credential, revokes the old version, writes an audit event, and never returns or logs Cookie contents.

Add an administrator view that shows credential version/health/last verification only, starts the service-account QR challenge, requires recent TOTP before replacement, and never renders Cookie text.

- [ ] **Step 5: Verify and commit**

Run focused/race/full Go tests and commit `feat: add controlled Bilibili gateway`.

---

### Task 2: Implement one shared upstream source per normalized room

**Files:**
- Create: `goserver/internal/hosted/roomsource/manager.go`
- Create: `goserver/internal/hosted/roomsource/manager_test.go`
- Create: `goserver/internal/hosted/roomsource/event.go`
- Create: `goserver/internal/hosted/roomsource/reconnect.go`
- Create: `goserver/internal/hosted/roomsource/reconnect_test.go`

**Interfaces:**
- Produces: `Manager.Subscribe(ctx, roomID, accountID, Sink) (Subscription, error)`.
- Produces: immutable `roomsource.Event` with viewer data marked ephemeral.

- [ ] **Step 1: Write failing sharing/isolation tests**

Subscribe three accounts to the same canonical room through one short ID and one long ID. Assert the gateway opens once, each sink receives its own immutable copy, canceling one subscription keeps the source alive, and canceling the last closes it once.

- [ ] **Step 2: Implement normalization before registry lookup**

Resolve short/long IDs through `Gateway.RoomInfo`, then key the registry by canonical numeric string. Guard registry lifecycle with one mutex; never invoke sink callbacks or gateway I/O while holding it.

- [ ] **Step 3: Implement fanout backpressure**

Each subscription has a bounded 256-event channel. A slow account receives a `subscriber_backpressure` error and is detached without blocking the room reader or other subscribers. Event objects are value-copied; slices/maps are cloned before fanout.

- [ ] **Step 4: Implement reconnect semantics**

Use exponential delays 1s, 2s, 4s, 8s, 16s, capped at 60s with ±20% jitter. Stop retries while the gateway breaker is open. Reset attempts after 2 minutes of healthy frames. Tests use injected clock/timer and contain no sleeps.

- [ ] **Step 5: Verify and commit**

Run roomsource tests `-count=20`, race tests, full Go tests, and commit `feat: share hosted room sources`.

---

### Task 3: Implement automatic leases, sessions, and room switching

**Files:**
- Create: `goserver/internal/hosted/runtime/lease.go`
- Create: `goserver/internal/hosted/runtime/lease_test.go`
- Create: `goserver/internal/hosted/runtime/manager.go`
- Create: `goserver/internal/hosted/runtime/manager_test.go`
- Create: `goserver/internal/hosted/runtime/session.go`
- Create: `goserver/internal/hosted/runtime/http.go`
- Create: `goserver/internal/hosted/runtime/http_test.go`
- Modify: `goserver/internal/hosted/app/app.go`

**Interfaces:**
- Produces: `Manager.Acquire(accountID, LeaseKind)`, `SetRoom`, `Status`, and `Shutdown`.
- Consumes: `roomsource.Manager`, configuration repository, and migration pending-applier.

- [ ] **Step 1: Write failing lease state-machine tests**

With an injected clock, cover config-only, OBS-only, both, one disconnecting, both disconnecting for 9:59, natural end at 10:00, reconnect before expiry, process shutdown, and absence of any stop command.

- [ ] **Step 2: Implement connection-owned leases**

`LeaseKind` is `config` or `obs`. An open authenticated SSE connection owns a lease; closing it releases that lease. When the last lease disappears, schedule one 10-minute idle timer. A new lease cancels the timer. No public `Stop` method exists.

Before acquiring or renewing a lease, load account status; reject disabled accounts. An administrator disable event calls the manager's internal account-closure hook to drain the queue, close the live session, release the room subscription, and reject subsequent leases without exposing a public stop route.

- [ ] **Step 3: Implement session creation and room switch barrier**

Starting a runtime creates one `live_sessions` row containing account, canonical room, config version, start time, and status. `SetRoom` rejects blank/invalid rooms, closes admission to the old serial queue, drains committed work, closes the old session, applies pending migration if present, updates target room, and only then subscribes to the new room/create a new session.

- [ ] **Step 4: Add status/SSE HTTP behavior**

```text
PUT /api/runtime/room
GET /api/runtime/events
GET /api/runtime/status
```

There is intentionally no start or stop route. The config SSE route holds a config lease and sends `status`, `snapshot`, and `degraded` events with keepalive comments every 20 seconds.

- [ ] **Step 5: Verify and commit**

Run runtime state tests with `-count=20`, race tests, full Go tests, and commit `feat: manage hosted runtime leases`.

---

### Task 4: Persist gameplay transitions before broadcasting and keep viewers ephemeral

**Files:**
- Create: `goserver/internal/hosted/runtime/processor.go`
- Create: `goserver/internal/hosted/runtime/processor_test.go`
- Create: `goserver/internal/hosted/runtime/viewers.go`
- Create: `goserver/internal/hosted/runtime/viewers_test.go`
- Create: `goserver/internal/hosted/runtime/publisher.go`
- Modify: `goserver/internal/hosted/configuration/repository.go`

**Interfaces:**
- Produces: `Processor.Accept(roomsource.Event) error` and identity-free `DisplaySnapshot`.

- [ ] **Step 1: Write the transaction-before-publish test**

```go
func TestProcessorPublishesOnlyAfterCommit(t *testing.T) {
    order := []string{}
    repo := fakeRepo{Commit: func() error { order = append(order, "commit"); return nil }}
    pub := fakePublisher{Publish: func(DisplaySnapshot) { order = append(order, "publish") }}
    p := newProcessor(repo, pub, gameplay.Engine{})
    if err := p.Accept(giftFixture()); err != nil { t.Fatal(err) }
    if diff := cmp.Diff([]string{"commit", "publish"}, order); diff != "" { t.Fatal(diff) }
}
```

Add commit-failure/no-publish, duplicate/no-second-effect, per-account serial order, wrong-room rejection, and one-account-failure isolation tests.

- [ ] **Step 2: Implement privacy transformation**

Use the full room event only to update the in-memory viewer ledger and construct real-time display rows. Build `gameplay.Gift` without UID/name/avatar, calculate the transition, and persist only state, identity-free aggregate deltas, and SHA-256 of a stable event ID. Do not persist process-local fingerprints.

- [ ] **Step 3: Implement outage buffering and degradation**

On transient database failure, retain at most 500 events and at most 60 seconds per account, expose degraded status, and retry with bounded backoff. When either limit is exceeded, reject new mutations with `persistence_unavailable`, retain connection health visibility, and alert. Never write raw buffered events to disk.

- [ ] **Step 4: Test restart/privacy boundaries**

Serialize every repository argument and log event during a gift from viewer `UID=123`, `Uname=secret`, `Avatar=https://secret`; assert none contains those values. Recreate the processor from repository state and prove attributes/activities/targets recover while viewer ledger is empty.

- [ ] **Step 5: Verify and commit**

Run processor tests `-count=20`, race tests, full Go tests, and commit `feat: persist hosted gameplay events`.

---

### Task 5: Add OBS credentials, fragment exchange, and read-only SSE

**Files:**
- Create: `goserver/internal/hosted/obs/service.go`
- Create: `goserver/internal/hosted/obs/service_test.go`
- Create: `goserver/internal/hosted/obs/http.go`
- Create: `goserver/internal/hosted/obs/http_test.go`
- Create: `obs.html`
- Create: `src/hosted/obs/main.ts`
- Create: `src/hosted/obs/view.ts`
- Create: `src/hosted/obs/obs.css`
- Create: `tests/hosted-obs.test.ts`
- Modify: `vite.hosted.config.ts`
- Modify: `src/hosted/api.ts`

**Interfaces:**
- Produces: long credential creation/reset, 12-hour short OBS sessions, and `/obs/{publicID}/events` SSE.

- [ ] **Step 1: Write failing token secrecy tests**

Assert long token appears only in the one-time create/reset response, DB sees only SHA-256 plus the non-secret issuing `credential_epoch`, the generated URL is `https://host/obs/{publicID}#token=...`, request targets/logs never contain it, reset revokes old long and short sessions, an OBS session cannot access another public ID, and account disable/rebind epoch changes invalidate every earlier long and short credential.

- [ ] **Step 2: Implement fragment exchange**

The page reads `location.hash`, POSTs the token body to `/obs/{publicID}/exchange`, immediately clears the fragment with `history.replaceState`, and clears its token variable after success. The server sets an opaque, hashed short-session cookie with `Secure`, `HttpOnly`, `SameSite=Strict`, and `Path=/obs/{publicID}` so multiple OBS sources remain path-isolated.

- [ ] **Step 3: Implement read-only SSE and lease ownership**

`GET /obs/{publicID}/events` validates the scoped short session, acquires an OBS lease for the stream lifetime, sends an initial display snapshot then increments, and uses 20-second keepalives. No OBS route accepts PUT/PATCH/DELETE or configuration commands.

- [ ] **Step 4: Implement OBS rendering and tests**

Reuse display formatting/theme modules only. Do not import config editors, desktop update, gift-clip, migration, or admin modules. Test fragment clearing before EventSource creation, reconnect with scoped cookie, read-only routes, token-reset behavior, and no horizontal overflow at 1920×1080 and 1280×720 fixtures.

- [ ] **Step 5: Verify and commit**

Run OBS/runtime Go tests, hosted OBS Vitest, typecheck, hosted build, all tests, and commit `feat: add secure hosted OBS views`.

---

### Task 6: Add room/runtime controls and capacity tests

**Files:**
- Create: `src/hosted/room.ts`
- Create: `src/hosted/runtime.ts`
- Create: `tests/hosted-runtime.test.ts`
- Create: `goserver/internal/hosted/runtime/capacity_test.go`
- Modify: `src/hosted/main.ts`
- Modify: `src/hosted/api.ts`

**Interfaces:**
- Produces: room selection, automatic status, degradation display, and no manual start/stop affordance.

- [ ] **Step 1: Write failing UI and capacity tests**

UI tests require arbitrary valid room input, pending/connected/degraded/reconnecting states, room-switch confirmation, and zero buttons or requests containing start/stop. Go capacity test starts 10 accounts over 7 distinct rooms including 3 on one room and asserts exactly 7 gateway opens.

- [ ] **Step 2: Implement room/runtime UI**

Open one authenticated config SSE stream after login. Treat its connection as presence; reconnect with jitter. Room changes call only `PUT /api/runtime/room`. Explain that leaving both configuration and OBS pages offline for ten minutes ends the session automatically.

- [ ] **Step 3: Add deterministic load assertions**

Feed 100 events per room through fake sources. Assert per-account ordering, shared-room fanout, no cross-account state, bounded queue depth, and no viewer identity in repository captures. Use injected clocks and channels, not sleeps.

- [ ] **Step 4: Run full verification**

Run runtime/roomsource/OBS tests with race detection, hosted frontend tests/build/typecheck, full Go and Vitest suites, and `git diff --check`.

- [ ] **Step 5: Commit**

Commit `feat: expose hosted runtime status` with only the listed UI/capacity files.

---

## Plan Completion Gate

Prove one source per normalized room, one active room per account, commit-before-publish, viewer-memory-only behavior, no manual stop interface, fragment token absence from logs, and 10-account/7-room deterministic capacity. Request reliability and security review before deployment work.
