# Domestic Update API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build and deploy a mainland-China update API that serves a compliant ICP root page, mirrors signed Windows releases to a private Tencent COS bucket, and lets the desktop client prefer the domestic source with GitHub fallback.

**Architecture:** A standalone Go service in `updateapi/` reads a validated stable-channel manifest from private COS, caches the last valid metadata, and returns the client's existing GitHub-compatible release JSON with a 10-minute COS pre-signed URL. Nginx terminates HTTPS, serves the minimal ICP page, rate-limits the two public API routes, and keeps the Go listener and health endpoint private. GitHub Actions publishes immutable versioned objects and writes the stable pointer last; the Windows client verifies size, SHA-256, and Authenticode publisher before installation.

**Tech Stack:** Go 1.26, `github.com/tencentyun/cos-go-sdk-v5` v0.7.75, Node.js 22, TypeScript/Vitest, Nginx, systemd, GitHub Actions, Tencent Cloud Lighthouse and COS.

## Global Constraints

- Keep the COS bucket private; do not configure anonymous read or broad CORS.
- The public API supports only stable Windows x64 releases and accepts no object-key, platform, architecture, account, device, or upload input.
- `GET/HEAD /api/v1/releases/latest` returns the existing GitHub Release-compatible shape and a COS URL valid for exactly 10 minutes.
- `GET/HEAD /api/v1/changelog` returns the existing `schemaVersion: 1` changelog document.
- The client checks the domestic source first, GitHub second, and still selects the highest stable SemVer across all successful sources.
- Installation requires size, SHA-256, valid Authenticode trust, and the configured publisher subject; any failure tries the next same-version source and never replaces the current executable.
- The root path is a minimal HTML page with the real ICP number centered at the bottom and linked to `https://beian.miit.gov.cn/`.
- The Go server listens only on `127.0.0.1:12450`; `/healthz` is not publicly reachable.
- Nginx limits the API to 10 requests per minute per source IP with burst 20; logs are retained for 7 days and never contain response bodies or pre-signed URLs.
- Versioned COS objects are immutable. `channels/stable/latest.json` is written only after every versioned object has been uploaded and verified.
- Do not add CDN in this implementation. Revisit it only under the trigger conditions in the approved design.
- Do not stage, commit, delete, or overwrite unrelated local experimental and untracked files.

---

## File Map

### New standalone update service

- `updateapi/go.mod`, `updateapi/go.sum`: independent Go 1.26 module with the pinned COS SDK.
- `updateapi/internal/release/manifest.go`: private channel schema, public response schema, validation, and JSON parsing.
- `updateapi/internal/release/manifest_test.go`: schema and trust-boundary tests.
- `updateapi/internal/cosstore/client.go`: bounded object reads, object metadata, immutable writes, and GET pre-signing.
- `updateapi/internal/cosstore/client_test.go`: SDK-adapter tests using a fake COS HTTP server.
- `updateapi/internal/service/service.go`: one-minute metadata cache, last-valid fallback, changelog loading, and URL generation.
- `updateapi/internal/service/service_test.go`: cache, failure, and signing tests with a fake store and clock.
- `updateapi/internal/httpapi/handler.go`: API routes, HEAD semantics, stable JSON errors, and request IDs.
- `updateapi/internal/httpapi/handler_test.go`: HTTP contract tests.
- `updateapi/internal/publish/publish.go`: immutable release upload and stable-pointer-last transaction.
- `updateapi/internal/publish/publish_test.go`: upload ordering, idempotency, and mismatch tests.
- `updateapi/cmd/server/main.go`: environment parsing, localhost binding, timeouts, signals, and graceful shutdown.
- `updateapi/cmd/publish/main.go`: release-workflow CLI.

### Deployment and verification assets

- `deploy/update-api/index.html.template`: minimal noindex ICP page rendered with `ICP_NUMBER`.
- `deploy/update-api/nginx.conf.template`: HTTP redirect, TLS, exact routes, rate limits, private health check, and JSON 404.
- `deploy/update-api/gift-panel-update-api.service`: hardened non-root systemd unit.
- `deploy/update-api/gift-panel-update-api.env.example`: variable names only, with no credentials or production values.
- `deploy/update-api/logrotate.conf`: seven-day Nginx access/error log rotation.
- `deploy/update-api/README.md`: CAM, COS, DNS, certificate, deployment, rotation, and rollback runbook.
- `scripts/build-update-api.mjs`: reproducible Linux amd64 build into `dist/gift-panel-update-api-linux-amd64`.
- `tests/update-api-deploy.test.ts`: static contracts for templates and service hardening.
- `package.json`: `build:update-api` and `test:update-api` scripts.

### Existing desktop client and release workflow

- `goserver/auto_update.go`, `goserver/auto_update_test.go`: domestic release source, fallback, and signature-verifier hook.
- `goserver/auto_update_signature_windows.go`, `goserver/auto_update_signature_other.go`: Authenticode query and non-Windows boundary.
- `goserver/auto_update_signature_windows_test.go`: trusted, unsigned, wrong-publisher, and malformed-output tests.
- `goserver/changelog.go`, `goserver/changelog_test.go`: ordered changelog sources and cached fallback.
- `scripts/build-go.mjs`: embed the expected publisher subject as UTF-8 hex in release builds.
- `.github/workflows/release.yml`: verify publisher subject and mirror GitHub Release assets to COS.
- `tests/release-workflow.test.ts`: workflow ordering, secret-name, publisher, and stable-pointer contracts.
- `README.md`: domestic-first update behavior and GitHub fallback.

---

### Task 1: Create the update-service module and manifest trust boundary

**Files:**
- Create: `updateapi/go.mod`
- Create: `updateapi/internal/release/manifest.go`
- Create: `updateapi/internal/release/manifest_test.go`

**Interfaces:**
- Produces: `release.ParseChannelManifest([]byte) (release.ChannelManifest, error)`
- Produces: `release.ChannelManifest.Validate() error`
- Produces: `release.ChannelManifest.Public(downloadURL string) release.PublicRelease`
- Produces: `release.ParseChangelog([]byte) (release.ChangelogDocument, error)`

- [ ] **Step 1: Add the independent Go module**

Create `updateapi/go.mod`:

```go
module github.com/brainfk123/bilibili-live-gift-panel/updateapi

go 1.26
```

Run: `go -C updateapi mod tidy`

Expected: exit 0. The COS dependency and `go.sum` are added when Task 2 first imports the SDK.

- [ ] **Step 2: Write failing manifest tests**

Define the accepted private channel document in tests:

```go
func validChannelManifest() release.ChannelManifest {
    return release.ChannelManifest{
        SchemaVersion: 1,
        TagName:       "v0.4.4",
        PublishedAt:   "2026-08-14T12:00:00Z",
        Asset: release.AssetManifest{
            Name:      "gift-panel-windows-x64.exe",
            ObjectKey: "releases/v0.4.4/gift-panel-windows-x64.exe",
            Size:      12_345_678,
            SHA256:    strings.Repeat("a", 64),
        },
        ChangelogObjectKey: "releases/v0.4.4/gift-panel-changelog.json",
    }
}
```

Add table tests that reject schema versions other than 1, prerelease/non-SemVer tags, wrong asset names, size `0`, size above `256<<20`, non-hex SHA-256, path traversal, a release prefix that does not match the tag, and a changelog key outside the same version directory. Add a success test proving `Public(url)` emits `tag_name`, `draft:false`, `prerelease:false`, and one asset whose digest is `sha256:` followed by the exact 64-character manifest hash.

Run: `go -C updateapi test ./internal/release -run 'TestChannelManifest|TestParseChangelog' -count=1`

Expected: FAIL because the package and types do not exist.

- [ ] **Step 3: Implement the schema and validation**

Use these exact types:

```go
type ChannelManifest struct {
    SchemaVersion      int           `json:"schemaVersion"`
    TagName            string        `json:"tagName"`
    PublishedAt        string        `json:"publishedAt"`
    Asset              AssetManifest `json:"asset"`
    ChangelogObjectKey string        `json:"changelogObjectKey"`
}

type AssetManifest struct {
    Name      string `json:"name"`
    ObjectKey string `json:"objectKey"`
    Size      int64  `json:"size"`
    SHA256    string `json:"sha256"`
}

type PublicRelease struct {
    TagName    string        `json:"tag_name"`
    Draft      bool          `json:"draft"`
    Prerelease bool          `json:"prerelease"`
    Assets     []PublicAsset `json:"assets"`
}

type PublicAsset struct {
    Name        string `json:"name"`
    DownloadURL string `json:"browser_download_url"`
    Size        int64  `json:"size"`
    Digest      string `json:"digest"`
}
```

Parse with `json.Decoder.DisallowUnknownFields()`, reject a second JSON value, parse `PublishedAt` as RFC3339, and use the same three-component stable SemVer rules as `goserver/auto_update.go`. Changelog parsing accepts only `schemaVersion == 1`, a non-empty `releases` array, and at most 2 MiB of input.

- [ ] **Step 4: Run focused and module tests**

Run: `go -C updateapi test ./internal/release -count=1`

Expected: PASS.

Run: `go -C updateapi test ./... -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the manifest boundary**

```bash
git add updateapi/go.mod updateapi/internal/release
git commit -m "feat: define domestic release manifest"
```

---

### Task 2: Add the COS adapter and cached release service

**Files:**
- Create: `updateapi/internal/cosstore/client.go`
- Create: `updateapi/internal/cosstore/client_test.go`
- Create: `updateapi/internal/service/service.go`
- Create: `updateapi/internal/service/service_test.go`
- Modify: `updateapi/go.mod`
- Create: `updateapi/go.sum`

**Interfaces:**
- Consumes: `release.ParseChannelManifest`, `release.ParseChangelog`, `ChannelManifest.Public`
- Produces: `service.Store` with `Get` and `PresignGet`
- Produces: `service.New(store Store, channelKey string, now func() time.Time) *Service`
- Produces: `(*Service).Latest(context.Context) (release.PublicRelease, error)`
- Produces: `(*Service).Changelog(context.Context) (service.Document, error)`

- [ ] **Step 1: Write failing service tests with a fake store and clock**

Use this seam:

```go
type Store interface {
    Get(ctx context.Context, key string, maxBytes int64) (body []byte, etag string, err error)
    PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
}

type Document struct {
    Body []byte
    ETag string
}
```

Test that two calls within 60 seconds read the channel object once but receive separately generated 10-minute URLs. Advance the fake clock beyond 60 seconds, make `Get` fail, and prove the last valid manifest is still used. Test cold-start failure, invalid refreshed metadata preserving the last valid manifest, signer failure, changelog size/schema rejection, and `Changelog` returning the upstream ETag.

Run: `go -C updateapi test ./internal/service -count=1`

Expected: FAIL because the service package does not exist.

- [ ] **Step 2: Implement the cache and last-valid fallback**

Use `channels/stable/latest.json`, a 64 KiB manifest limit, a 60-second freshness window, a 2 MiB changelog limit, and `10*time.Minute` for every signed URL. Protect refreshes with a dedicated mutex so simultaneous expiry requests issue one COS read. Copy cached byte slices before returning them.

Return typed sentinel errors:

```go
var (
    ErrReleaseUnavailable  = errors.New("release unavailable")
    ErrReleaseInvalid      = errors.New("release invalid")
    ErrDownloadUnavailable = errors.New("download unavailable")
)
```

- [ ] **Step 3: Write failing COS-adapter tests**

Use `httptest.Server` as a fake COS endpoint. Verify `Get` sends an authorized GET, rejects bodies above `maxBytes`, returns `ETag`, and preserves a bounded timeout. Verify `PresignGet` signs `GET`, includes the exact key and expiration, and rejects keys outside `releases/`.

Run: `go -C updateapi test ./internal/cosstore -count=1`

Expected: FAIL because the adapter does not exist.

- [ ] **Step 4: Implement the COS adapter**

Construct the bucket URL with `fmt.Sprintf("https://%s.cos.%s.myqcloud.com", bucket, region)`. The constructor is:

```go
func New(bucket, region, secretID, secretKey string, httpClient *http.Client) (*Client, error)
```

Run `go get github.com/tencentyun/cos-go-sdk-v5@v0.7.75` and `go mod tidy` in `updateapi`, then use `cos.AuthorizationTransport` for reads and `Object.GetPresignedURL(ctx, http.MethodGet, key, secretID, secretKey, 10*time.Minute, nil)` for downloads. Do not log credentials or URLs. Validate the object prefix in both `cosstore` and `release` so neither can become an arbitrary signer alone.

- [ ] **Step 5: Run service, COS, and race tests**

Run: `go -C updateapi test ./internal/service ./internal/cosstore -count=1`

Expected: PASS.

Run: `go -C updateapi test ./... -race -count=1`

Expected: PASS with no race reports.

- [ ] **Step 6: Commit the COS read path**

```bash
git add updateapi/go.mod updateapi/go.sum updateapi/internal/cosstore updateapi/internal/service
git commit -m "feat: read and sign private COS releases"
```

---

### Task 3: Implement the localhost HTTP API server

**Files:**
- Create: `updateapi/internal/httpapi/handler.go`
- Create: `updateapi/internal/httpapi/handler_test.go`
- Create: `updateapi/cmd/server/main.go`
- Modify: `package.json`

**Interfaces:**
- Consumes: `(*service.Service).Latest`, `(*service.Service).Changelog`
- Produces: `httpapi.New(service ReleaseService, requestID func() string, logger Logger) http.Handler`
- Produces: the environment contract below

- [ ] **Step 1: Write failing HTTP contract tests**

Define:

```go
type ReleaseService interface {
    Latest(context.Context) (release.PublicRelease, error)
    Changelog(context.Context) (service.Document, error)
}

type Logger interface {
    Error(requestID, code string, cause error)
}
```

Cover GET/HEAD latest; GET/HEAD changelog; `Cache-Control: private, no-store` for latest; `ETag` and `public, max-age=300` for changelog; typed 503 errors; JSON 404; 405 with `Allow: GET, HEAD`; valid 32-hex inbound request IDs; replacement of invalid inbound IDs; private error details; captured logs omitting response JSON/pre-signed URLs; and `200 ok` from `/healthz`.

Run: `go -C updateapi test ./internal/httpapi -count=1`

Expected: FAIL because the HTTP package does not exist.

- [ ] **Step 2: Implement exact routes and stable errors**

Register only `/api/v1/releases/latest`, `/api/v1/changelog`, and `/healthz`. Use compact JSON and `X-Content-Type-Options: nosniff`. Accept `X-Request-ID` only when it is exactly 32 lowercase hexadecimal characters; otherwise generate 16 random bytes and encode them as lowercase hex. Never reflect request paths, query values, COS keys, credentials, upstream bodies, or successful response JSON into logs or client errors.

- [ ] **Step 3: Write the server entry point**

Parse:

```text
UPDATE_API_LISTEN    default 127.0.0.1:12450; reject non-loopback hosts
COS_BUCKET           required
COS_REGION           required
COS_SECRET_ID        required
COS_SECRET_KEY       required
COS_CHANNEL_KEY      default channels/stable/latest.json
```

Configure `ReadHeaderTimeout: 5s`, `ReadTimeout: 10s`, `WriteTimeout: 15s`, `IdleTimeout: 60s`, `MaxHeaderBytes: 16<<10`, and a 10-second graceful shutdown on `SIGINT/SIGTERM`. Log only startup, shutdown, request ID, stable error code, and wrapped server-side cause.

- [ ] **Step 4: Add package scripts**

Add:

```json
"test:update-api": "go -C updateapi test ./... -count=1",
"build:update-api": "node scripts/build-update-api.mjs"
```

The build script is added in Task 5. Run only the test script here.

- [ ] **Step 5: Run tests and compile**

Run: `npm run test:update-api`

Expected: PASS.

Run: `go -C updateapi build ./cmd/server`

Expected: exit 0.

- [ ] **Step 6: Commit the HTTP service**

```bash
git add updateapi/internal/httpapi updateapi/cmd/server package.json
git commit -m "feat: expose domestic update API"
```

---

### Task 4: Implement immutable COS publishing

**Files:**
- Modify: `updateapi/internal/cosstore/client.go`
- Modify: `updateapi/internal/cosstore/client_test.go`
- Create: `updateapi/internal/publish/publish.go`
- Create: `updateapi/internal/publish/publish_test.go`
- Create: `updateapi/cmd/publish/main.go`

**Interfaces:**
- Consumes: `release.ChannelManifest.Validate`
- Produces: `publish.Store` with `Head`, `Put`, and `Get`
- Produces: `publish.Run(context.Context, Store, publish.Input) error`
- Produces: CLI flags `--tag`, `--asset`, `--checksum`, `--changelog`

- [ ] **Step 1: Write failing publisher transaction tests**

Use:

```go
type Input struct {
    Tag           string
    AssetPath     string
    ChecksumPath  string
    ChangelogPath string
    PublishedAt   time.Time
}
```

Test the operation order `HEAD/PUT/verify` for the EXE, checksum, changelog and `release.json`, followed by `PUT channels/stable/latest.json` and a stable readback. Test idempotent matching objects, mismatched existing objects, bad checksum files, malformed changelogs, failed versioned uploads, and stable readback mismatch. Every failure before the stable PUT must leave stable untouched.

Run: `go -C updateapi test ./internal/publish -count=1`

Expected: FAIL because the publisher does not exist.

- [ ] **Step 2: Extend the COS adapter for publishing**

Add:

```go
type ObjectInfo struct {
    Size   int64
    SHA256 string
    ETag   string
}

func (c *Client) Head(ctx context.Context, key string) (ObjectInfo, error)
func (c *Client) Put(ctx context.Context, key string, body io.Reader, size int64, contentType, sha256 string) error
```

Set `x-cos-meta-sha256` on versioned objects. Treat COS 404 as typed not-found; preserve permission and network errors.

- [ ] **Step 3: Implement stable-pointer-last publishing**

Recompute the EXE SHA-256, compare it with the checksum file, parse the changelog with Task 1, construct the approved private manifest, upload versioned objects idempotently, verify each object, then write stable last. Re-read stable and compare exact tag, size, digest, and keys. Never print secrets or pre-signed URLs.

- [ ] **Step 4: Implement the CLI**

Read the same COS variables as the server. Require all four flags, exit nonzero on any validation/upload/readback error, and print only the tag and verified object keys.

- [ ] **Step 5: Run publisher and race tests**

Run: `go -C updateapi test ./internal/publish ./internal/cosstore -count=1`

Expected: PASS.

Run: `go -C updateapi test ./... -race -count=1`

Expected: PASS.

- [ ] **Step 6: Commit the publisher**

```bash
git add updateapi/internal/cosstore updateapi/internal/publish updateapi/cmd/publish
git commit -m "feat: publish immutable releases to COS"
```

---

### Task 5: Add hardened deployment assets

**Files:**
- Create: `deploy/update-api/index.html.template`
- Create: `deploy/update-api/nginx.conf.template`
- Create: `deploy/update-api/gift-panel-update-api.service`
- Create: `deploy/update-api/gift-panel-update-api.env.example`
- Create: `deploy/update-api/logrotate.conf`
- Create: `deploy/update-api/README.md`
- Create: `scripts/build-update-api.mjs`
- Create: `tests/update-api-deploy.test.ts`
- Modify: `package.json`

**Interfaces:**
- Consumes: server environment contract from Task 3
- Produces: `dist/gift-panel-update-api-linux-amd64`
- Produces: render inputs `PUBLIC_DOMAIN`, `ICP_NUMBER`, certificate paths, and COS variables

- [ ] **Step 1: Write failing deployment-template tests**

Assert:

```ts
expect(index).toContain('name="robots" content="noindex,nofollow"');
expect(index).toContain('https://beian.miit.gov.cn/');
expect(index).toContain('${ICP_NUMBER}');
expect(nginx).toContain('rate=10r/m');
expect(nginx).toContain('burst=20');
expect(nginx).toMatch(/location = \/api\/v1\/releases\/latest/);
expect(nginx).toMatch(/location = \/api\/v1\/changelog/);
expect(nginx).toMatch(/location = \/healthz[\s\S]*allow 127\.0\.0\.1;[\s\S]*deny all;/);
expect(nginx).toContain('error_page 429 = @rate_limited');
expect(nginx).toContain('Content-Type application/json');
expect(nginx).toContain('X-Frame-Options DENY');
expect(nginx).toContain('Content-Security-Policy');
expect(service).toContain('User=gift-panel-update');
expect(service).toContain('NoNewPrivileges=true');
expect(service).toContain('ProtectSystem=strict');
expect(logrotate).toContain('rotate 7');
expect(logrotate).toContain('daily');
```

Run: `npm test -- tests/update-api-deploy.test.ts --reporter=dot`

Expected: FAIL because the templates do not exist.

- [ ] **Step 2: Create the minimal ICP page**

Include only a service label and this centered footer link:

```html
<a href="https://beian.miit.gov.cn/" rel="nofollow">${ICP_NUMBER}</a>
```

Use UTF-8, viewport, semantic HTML, `noindex,nofollow`, and a restrictive CSP meta tag. Include no scripts, analytics, images, downloads, personal details, or external styles.

- [ ] **Step 3: Create the Nginx template**

Define one rate zone at `10r/m`, redirect HTTP to HTTPS except ACME challenges, and proxy only the two exact API routes plus loopback-only health. Set proxy timeouts below Go timeouts, disable request-body use, preserve `Host`, set `X-Request-ID`, and return fixed JSON 404 from catch-all. Map rate-limit responses through `error_page 429 = @rate_limited` to the stable JSON error instead of Nginx HTML. Add the approved nosniff, no-referrer, frame-denial, permissions-policy, and CSP headers. Do not proxy `/api/` broadly.

- [ ] **Step 4: Create the systemd unit and environment example**

Use:

```ini
User=gift-panel-update
Group=gift-panel-update
EnvironmentFile=/etc/gift-panel-update-api.env
ExecStart=/opt/gift-panel-update-api/current/gift-panel-update-api
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictSUIDSGID=true
```

The example lists variable names with empty values and states that production is root-owned `0600` and never committed. Add a dedicated Nginx log file for these routes and a `logrotate.conf` that rotates daily, keeps seven archives, compresses old logs, and signals Nginx to reopen files.

- [ ] **Step 5: Add the Linux build script**

Follow `scripts/build-go.mjs` process style. Build with `GOOS=linux`, `GOARCH=amd64`, `CGO_ENABLED=0`, `-trimpath`, and `-ldflags=-s -w`; output `dist/gift-panel-update-api-linux-amd64`. Print path and byte size only.

- [ ] **Step 6: Write the deployment runbook**

Document exact directories, `envsubst` rendering, service-user creation, binary permissions, root-owned environment installation, `systemd-analyze verify`, `nginx -t`, local health curl, HTTPS API curl, log rotation, credential rotation, backup creation, stable-manifest rollback, and Nginx rollback. List required GitHub variables/secrets and server variables by name only.

- [ ] **Step 7: Run tests and cross-build**

Run: `npm test -- tests/update-api-deploy.test.ts --reporter=dot`

Expected: PASS.

Run: `npm run build:update-api`

Expected: exit 0 and the Linux binary exists.

Run: `go version -m dist/gift-panel-update-api-linux-amd64`

Expected: updateapi module and COS SDK v0.7.75.

- [ ] **Step 8: Commit deployment assets**

```bash
git add deploy/update-api scripts/build-update-api.mjs tests/update-api-deploy.test.ts package.json
git commit -m "ops: add update API deployment assets"
```

---

### Task 6: Make the desktop updater prefer the configured domestic source

**Files:**
- Modify: `goserver/auto_update.go`
- Modify: `goserver/auto_update_test.go`
- Modify: `scripts/build-go.mjs`

**Interfaces:**
- Produces: linker variable `main.updateAPIBaseURLHex`
- Produces: `domesticUpdateReleaseURL() string`
- Preserves: `updateReleaseSource{Name, URL, GitHub}` and the GitHub-compatible decoder
- Preserves: highest-version selection and same-version source fallback

- [ ] **Step 1: Write failing source-configuration tests**

Temporarily set `updateAPIBaseURLHex` to the UTF-8 hex of `https://updates.example.test`, restore it with `t.Cleanup`, and assert:

```go
sources := defaultUpdateReleaseSources()
if len(sources) != 2 { t.Fatalf("sources = %d, want 2", len(sources)) }
if sources[0].Name != "国内镜像" || sources[0].URL != "https://updates.example.test/api/v1/releases/latest" || sources[0].GitHub {
    t.Fatalf("domestic source = %#v", sources[0])
}
if sources[1].Name != "GitHub" || sources[1].URL != updateGitHubReleaseURL || !sources[1].GitHub {
    t.Fatalf("GitHub source = %#v", sources[1])
}
```

Add table cases that reject non-HTTPS URLs, credentials, query strings, fragments, and non-root paths. A blank value in a dev build must produce GitHub-only sources. Add a same-version test where the domestic asset is truncated and the GitHub asset succeeds.

Run: `go -C goserver test . -run 'TestDefaultUpdateSources|TestDomesticUpdateURL|TestUpdaterFallsBack' -count=1`

Expected: FAIL because domestic configuration is absent.

- [ ] **Step 2: Implement validated build-time configuration**

Add:

```go
var updateAPIBaseURLHex = ""

func domesticUpdateReleaseURL() string
```

Decode lowercase or uppercase hex as UTF-8, parse with `url.Parse`, require `https`, a non-empty host, no userinfo/query/fragment, and path `""` or `"/"`; normalize the trailing slash and append `/api/v1/releases/latest`. Invalid configured values return an empty string and write a diagnostic error during updater initialization; they never replace GitHub fallback.

In `scripts/build-go.mjs`, read `APP_UPDATE_API_URL`. Non-dev builds require it, validate it with `new URL`, UTF-8 hex-encode it into `updateAPIBaseURLHex`, and append `-X main.updateAPIBaseURLHex=${updateAPIBaseURLHex}` to ldflags. Dev builds may leave it blank.

- [ ] **Step 3: Run updater and build-script tests**

Run: `go -C goserver test . -run 'TestDefaultUpdateSources|TestDomesticUpdateURL|TestUpdaterFallsBack|TestUpdaterChecksGitHub' -count=1`

Expected: PASS.

Run: `go -C goserver test ./... -count=1`

Expected: PASS.

- [ ] **Step 4: Commit the source preference**

```bash
git add goserver/auto_update.go goserver/auto_update_test.go scripts/build-go.mjs
git commit -m "feat: prefer domestic update releases"
```

---

### Task 7: Add domestic-first changelog fallback

**Files:**
- Modify: `goserver/changelog.go`
- Modify: `goserver/changelog_test.go`
- Modify: `goserver/main.go`

**Interfaces:**
- Consumes: validated build-time base URL from Task 6
- Produces: `hostedChangelogSource{Name, URL}`
- Produces: `defaultHostedChangelogSources() []hostedChangelogSource`
- Changes: `newHostedChangelogHandler(client *http.Client, sources []hostedChangelogSource) http.HandlerFunc`

- [ ] **Step 1: Write fallback and cache tests**

Add tests proving domestic success prevents a GitHub request; domestic 503 falls back; invalid domestic JSON falls back; after one successful fetch both sources failing returns cached releases; both failing without cache returns the existing 502 UI error; and the 30-minute cache suppresses all upstream requests.

Run: `go -C goserver test . -run 'TestHostedChangelog' -count=1`

Expected: FAIL because the handler accepts one URL.

- [ ] **Step 2: Implement ordered changelog sources**

Use the Task 6 base URL and append `/api/v1/changelog`. Keep the existing GitHub changelog URL second. Iterate sources, collect causes only for diagnostic logging, and never return source URLs or upstream bodies to the UI. Cache the first valid document exactly as today.

Update `main.go` to call:

```go
newHostedChangelogHandler(nil, defaultHostedChangelogSources())
```

- [ ] **Step 3: Run changelog and full Go tests**

Run: `go -C goserver test . -run 'TestHostedChangelog' -count=1`

Expected: PASS.

Run: `go -C goserver test ./... -count=1`

Expected: PASS.

- [ ] **Step 4: Commit changelog fallback**

```bash
git add goserver/changelog.go goserver/changelog_test.go goserver/main.go
git commit -m "feat: mirror hosted changelog"
```

---

### Task 8: Enforce Authenticode publisher verification before install

**Files:**
- Modify: `goserver/auto_update.go`
- Modify: `goserver/auto_update_test.go`
- Create: `goserver/auto_update_signature_windows.go`
- Create: `goserver/auto_update_signature_other.go`
- Create: `goserver/auto_update_signature_windows_test.go`
- Modify: `scripts/build-go.mjs`

**Interfaces:**
- Produces: `verifyAuthenticodePublisher(path, expectedSubject string) error`
- Produces: `autoUpdaterOptions.VerifyExecutable func(string) error`
- Produces: linker variable `main.updateExpectedPublisherHex`

- [ ] **Step 1: Write updater-level signature failure tests**

Inject `VerifyExecutable` through `autoUpdaterOptions`. Test a domestic candidate with correct SHA-256 but verifier error followed by a successful same-version GitHub candidate. Test all candidates failing verification leaves no pending EXE and sets state `error`.

Run: `go -C goserver test . -run 'TestUpdater.*Signature' -count=1`

Expected: FAIL because the verifier hook does not exist.

- [ ] **Step 2: Invoke verification after SHA-256 and before pending rename**

Use this order in `downloadAsset`:

```go
if err := verifyFileSHA256(temporaryPath, expectedSHA); err != nil { return nil, err }
if err := updater.verifyExecutable(temporaryPath); err != nil {
    return nil, fmt.Errorf("Authenticode 验证失败：%w", err)
}
```

The default verifier decodes `updateExpectedPublisherHex`. Empty is allowed only in dev/unit-test builds. Extend `scripts/build-go.mjs` so non-dev builds require `APP_UPDATE_PUBLISHER`, UTF-8 hex-encode it into `updateExpectedPublisherHex`, and append `-X main.updateExpectedPublisherHex=${updateExpectedPublisherHex}` to ldflags.

- [ ] **Step 3: Write Windows signature-query parser tests**

Use an injectable command runner and parse only:

```json
{"status":"Valid","subject":"CN=Expected Publisher, O=Expected Publisher"}
```

Test valid/exact subject, `NotSigned`, `HashMismatch`, empty certificate, wrong subject, malformed JSON, command failure, and missing system PowerShell.

Run on Windows: `go -C goserver test . -run 'TestVerifyAuthenticodePublisher' -count=1`

Expected: FAIL because the platform files do not exist.

- [ ] **Step 4: Implement Windows and non-Windows boundaries**

Invoke `%WINDIR%\System32\WindowsPowerShell\v1.0\powershell.exe` through `exec.Command` with `-NoProfile`, `-NonInteractive`, `-ExecutionPolicy Bypass`, and a fixed script calling `Get-AuthenticodeSignature -LiteralPath $args[0]`. Emit only status and signer subject as compressed JSON. Pass the path as an argument, never interpolate it into script text, and set `SysProcAttr.HideWindow = true`.

The `!windows` implementation returns an unsupported-platform error; production auto-update cannot call it because automatic update is Windows-only.

- [ ] **Step 5: Run signature, updater, and Linux compile gates**

Run: `go -C goserver test . -run 'TestVerifyAuthenticodePublisher|TestUpdater.*Signature' -count=1`

Expected: PASS on Windows.

Run: `npm run verify:go-linux-compile`

Expected: PASS.

Run: `go -C goserver test ./... -count=1`

Expected: PASS.

- [ ] **Step 6: Commit signature enforcement**

```bash
git add goserver/auto_update.go goserver/auto_update_test.go goserver/auto_update_signature_windows.go goserver/auto_update_signature_other.go goserver/auto_update_signature_windows_test.go scripts/build-go.mjs
git commit -m "security: verify update publisher"
```

---

### Task 9: Wire COS mirroring into the release workflow

**Files:**
- Modify: `.github/workflows/release.yml`
- Modify: `tests/release-workflow.test.ts`
- Modify: `README.md`

**Interfaces:**
- Consumes: publisher CLI from Task 4
- Consumes: build-time values from Tasks 6 and 8
- Requires GitHub variables: `UPDATE_API_BASE_URL`, `COS_BUCKET`, `COS_REGION`, `EVSIGN_EXPECTED_SUBJECT`
- Requires GitHub secrets: `COS_RELEASE_SECRET_ID`, `COS_RELEASE_SECRET_KEY`

- [ ] **Step 1: Write failing workflow-order and secret-contract tests**

Extend `ReleaseStep` with `env?: Record<string,string>`. Assert `Test domestic update tooling` runs `go -C updateapi test ./... -race -count=1` before GitHub Release; GitHub Release precedes `Mirror release to Tencent COS`; the mirror runs `go run ./cmd/publish`; COS ID/key use only the named secrets; bucket/region use only the named variables; the release build receives `APP_UPDATE_API_URL` and `APP_UPDATE_PUBLISHER`; and the signing step compares `SignerCertificate.Subject` with `EVSIGN_EXPECTED_SUBJECT`.

Run: `npm test -- tests/release-workflow.test.ts --reporter=dot`

Expected: FAIL because the mirror and build variables are absent.

- [ ] **Step 2: Pass and verify release configuration**

Add to `Build release executable`:

```yaml
APP_UPDATE_API_URL: ${{ vars.UPDATE_API_BASE_URL }}
APP_UPDATE_PUBLISHER: ${{ vars.EVSIGN_EXPECTED_SUBJECT }}
```

After EVSign, fail unless status is `Valid`, `SignerCertificate` exists, and `SignerCertificate.Subject` exactly equals the configured variable. Do not print private material or credential values.

- [ ] **Step 3: Add the update-service CI gate**

After Go setup and before release publication, add `Test domestic update tooling` running `go -C updateapi test ./... -race -count=1`. The step must fail the release job on any unit test or race failure.

- [ ] **Step 4: Add COS mirroring as the final release step**

Use `working-directory: updateapi` and:

```powershell
go run ./cmd/publish --tag $env:RELEASE_TAG --asset ../dist/gift-panel-windows-x64.exe --checksum ../dist/gift-panel-windows-x64.exe.sha256 --changelog ../dist/gift-panel-changelog.json
if ($LASTEXITCODE -ne 0) { throw "Tencent COS release mirror failed" }
```

Map only `COS_BUCKET`, `COS_REGION`, `COS_SECRET_ID`, and `COS_SECRET_KEY`. Do not add CDN, Lighthouse, or public-read credentials.

- [ ] **Step 5: Update release documentation**

Document required variables/secrets, GitHub-before-COS ordering, stable remaining on the previous version after mirror failure, and GitHub fallback. Link to `deploy/update-api/README.md` for setup and rotation.

- [ ] **Step 6: Run workflow and full TypeScript tests**

Run: `npm test -- tests/release-workflow.test.ts tests/update-api-deploy.test.ts --reporter=dot`

Expected: PASS.

Run: `npm test -- --reporter=dot`

Expected: PASS with zero failing test files.

- [ ] **Step 7: Commit release automation**

```bash
git add .github/workflows/release.yml tests/release-workflow.test.ts README.md
git commit -m "ci: mirror releases to Tencent COS"
```

---

### Task 10: Configure Tencent Cloud, deploy, and run end-to-end acceptance

**Files:**
- Modify only when verification exposes a product defect: files introduced in Tasks 1-9
- Do not commit: rendered production config, ICP page, environment files, credentials, certificate keys, command output, server binaries, or temporary releases

**Interfaces:**
- Consumes: the approved ICP domain and real ICP number supplied by the user
- Consumes: Tencent COS bucket in `ap-beijing`, CAM credentials, and GitHub variables/secrets
- Produces: HTTPS production API and verified domestic-first client behavior

- [ ] **Step 1: Create and harden the COS bucket**

Create or select a Beijing bucket, keep `private-read-write`, enable object versioning for stable rollback, and leave CDN/public-read disabled. Record bucket and region only in secret/config stores.

Create two CAM identities:

- CI: minimum Head/Get/Put on this bucket's `releases/*` and `channels/stable/latest.json`; no DeleteObject and no other bucket.
- Lighthouse: minimum Get on `channels/stable/*` and `releases/*`; no Put/Delete and no other bucket.

Verify one allowed and one denied operation for each identity.

- [ ] **Step 2: Configure GitHub repository values**

Set variables `UPDATE_API_BASE_URL`, `COS_BUCKET`, `COS_REGION`, `EVSIGN_EXPECTED_SUBJECT`. Set secrets `COS_RELEASE_SECRET_ID`, `COS_RELEASE_SECRET_KEY`. Confirm Actions masks secrets and no value enters issues, commits, artifacts, or chat.

- [ ] **Step 3: Build and inspect the Linux service**

Run: `npm run build:update-api`

Expected: exit 0.

Run: `go version -m dist/gift-panel-update-api-linux-amd64`

Expected: correct updateapi module and COS SDK v0.7.75.

Compute a local SHA-256 for transfer verification; do not commit the binary or digest.

- [ ] **Step 4: Back up and deploy through Tencent TAT**

Resolve current absolute targets, set `backup_dir=/root/site-backup-$(date -u +%Y%m%dT%H%M%SZ)`, and copy the active Nginx site plus `/var/www/gift-panel` into that directory. Compute `binary_sha256` from the transferred binary, install it at `/opt/gift-panel-update-api/releases/${binary_sha256}/gift-panel-update-api`, point `/opt/gift-panel-update-api/current` at that release, create the locked service user, install the root-owned `0600` environment file, render the real ICP page and Nginx config, and install the systemd unit.

Run: `systemd-analyze verify /etc/systemd/system/gift-panel-update-api.service`

Expected: exit 0 with no unit errors.

Run: `nginx -t`

Expected: syntax successful.

Start the service, run `curl --fail http://127.0.0.1:12450/healthz`, then reload Nginx. Do not reload on validation failure.

- [ ] **Step 5: Configure DNS and HTTPS**

Point the approved domain A record to `42.193.122.209`, wait for authoritative resolution, install a valid certificate, and verify automatic renewal plus Nginx reload. Keep firewall TCP 80/443 and SSH 22; do not open 12450.

Verify externally:

```text
HTTP root                    → 301 HTTPS
HTTPS root                   → 200 minimal ICP page
HTTPS unknown path           → 404 JSON
HTTPS /healthz               → inaccessible
HTTPS latest release API     → 200, or expected 503 before first publish
```

Confirm the exact ICP number/link and absence of personal details or downloads.

- [ ] **Step 6: Run a non-production-prefix publisher rehearsal**

Use a dedicated test prefix and version that production clients cannot request. Verify versioned objects are immutable and stable is written last. Remove only the exact test prefix through the Tencent console after resolving its bucket/path; do not use wildcard or broad recursive deletion.

- [ ] **Step 7: Run a real release-path acceptance test**

Using a normally signed release, verify GitHub Release precedes mirroring; COS has all versioned objects and stable; the domestic API returns the same tag/size/SHA-256; the URL expires in 10 minutes; the EXE signature is `Valid` with the expected subject; at least two mainland networks can download; blocking domestic falls back to GitHub; corrupt bytes fail SHA-256; and a wrong-publisher test binary fails Authenticode without installation.

- [ ] **Step 8: Exercise rollback**

Restore the previous COS version of stable and confirm newer clients do not downgrade. Switch the systemd symlink to the previous binary and validate health. Restore the previous Nginx config from backup, run `nginx -t`, reload, then reapply the new version and validate again.

- [ ] **Step 9: Run the full repository gate**

Run each command separately:

```text
npm run typecheck
npm test -- --reporter=dot
npm run build:ui
npm run verify:go-linux-compile
go -C goserver test ./... -count=1 -timeout=300s
go -C updateapi test ./... -race -count=1 -timeout=300s
npm run build:update-api
git diff --check
```

Expected: every command exits 0; tests report zero failures and race tests report no races.

- [ ] **Step 10: Commit only verified product fixes**

If acceptance required tracked fixes, commit each focused fix with its regression test. Never commit rendered production files, credentials, server binaries, logs, temporary releases, or unrelated experiments.

---

## Final Review Checklist

- [ ] Domestic release and changelog endpoints match client contracts exactly.
- [ ] The API cannot sign arbitrary keys and never proxies the EXE.
- [ ] The root page shows the real ICP number and only the minimal service label.
- [ ] COS remains private and versioned release objects cannot be overwritten.
- [ ] CI and Lighthouse use separate prefix-scoped credentials.
- [ ] Stable metadata is written last and can be rolled back through COS versioning.
- [ ] The client verifies size, SHA-256, Authenticode trust, and publisher subject.
- [ ] Domestic failures fall back to GitHub for updates and changelog.
- [ ] Port 12450 and `/healthz` are not public.
- [ ] No CDN, public bucket, broad CORS, device identity, telemetry, or client secret was introduced.
- [ ] Full Go, race, TypeScript, typecheck, build, workflow, and deployment checks pass.
