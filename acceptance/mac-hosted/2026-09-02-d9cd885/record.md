# Apple Silicon fresh-checkout acceptance — 2026-09-02

## Result

**PASS** for the documented Mac Hosted development and build boundary at commit
`d9cd885bcb798043eae45810ca6149e836e75f0b` on branch
`codex/mac-hosted-windows-compat`.

This record covers the Mac daily Hosted loop, the Linux deployment contract
container, the final `linux/amd64` Hosted image build, image reproducibility,
the real MySQL integration gate, a live final-image health smoke, and automated
Windows x64 CI/EXE startup smoke. It does not claim real Windows GPU, OBS,
driver, hardware encoding, production Hosted, or EXE/Hosted visual parity
evidence.

## Fresh-checkout conditions

- Cloned the branch from `https://github.com/brainfk123/bilibili-live-gift-panel.git`.
- Verified HEAD exactly matched the commit above before installing dependencies.
- The checkout initially contained no `node_modules`, `dist`, or generated assets.
- npm cache, Go build cache, and Go module cache were new directories dedicated
  to this acceptance run.
- Existing Playwright browser binaries and Colima/BuildKit layer cache were
  reused. Project dependencies, generated assets, and Go caches were not.
- Raw logs were mechanically sanitized before commit: the local user-home path
  and temporary acceptance-root path were replaced with placeholders. No other
  log content was rewritten.

## Machine and toolchain

| Item | Evidence |
| --- | --- |
| Architecture | `arm64` |
| macOS | `26.6.2` (`25G83`) |
| Xcode | `26.6` (`17F113`) |
| Apple Clang | `21.0.0` |
| Git | `2.50.1` (Apple Git-155) |
| Node.js | `22.23.2` |
| npm | `10.9.8` |
| Go | `1.26.7 darwin/arm64` |
| GNU Bash | `5.3.15 aarch64-apple-darwin25.4.0` |
| PowerShell | `7.6.5` |
| Colima | Apple Virtualization Framework, `aarch64`, Docker runtime, `virtiofs` |
| Docker client/server | `29.7.2` / `29.5.2` |
| Docker Compose | `5.5.0` |
| Docker Buildx | `0.36.1` |
| Playwright | `1.62.1`, Chromium and headless-shell build `1234` |

## Commands and results

Commands ran in the documented Daily Hosted loop order with
`BASH_BIN=/opt/homebrew/bin/bash`.

| Order | Command or check | Result | Log |
| --- | --- | --- | --- |
| 0 | Environment, checkout, Xcode, and Playwright inventory | PASS after the post-`npm ci` Playwright retry | `logs/00-environment.log`, `logs/00b-playwright.log`, `logs/00c-checkout-xcode.log` |
| 1 | `npm ci` | PASS; 60 packages installed | `logs/01-npm-ci.log` |
| 2 | `npm test` | PASS; 94/94 files, 1278 passed, 31 skipped | `logs/02-npm-test.log` |
| 3 | `npm run typecheck` | PASS | `logs/03-typecheck.log` |
| 4 | `npm run build:ui` | PASS; 95 modules transformed | `logs/04-build-ui.log` |
| 5 | `npm run prepare:go-assets` | PASS; 87 embedded UI assets | `logs/05-prepare-go-assets.log` |
| 6 | `npm run verify:go-linux-compile` | PASS; `GOOS=linux GOARCH=amd64` | `logs/06-verify-go-linux-compile.log` |
| 7 | `npm run build:hosted` | PASS; 49 modules transformed | `logs/07-build-hosted.log` |
| 8 | `go -C goserver test -race -count=1 ./cmd/hosted ./internal/...` | PASS | `logs/08-go-race.log` |
| 9 | `go -C goserver vet ./cmd/hosted ./internal/...` | PASS; zero output | `logs/09-go-vet.log` |
| 10 | `npm run test:update-api` | PASS | `logs/10-test-update-api.log` |
| 11 | `npm run build:hosted-server` | PASS | `logs/11-build-hosted-server.log` |
| 12 | `npm run verify:hosted-server-repro` | PASS | `logs/12-verify-hosted-repro.log` |
| 13 | Tracked-worktree, artifact, cache, and image metadata audit | PASS | `logs/13-final-audit.log` |
| 14 | `npm run test:hosted-mysql` | PASS; real MySQL integration tests completed in 28.433 seconds | `logs/14-test-hosted-mysql.log` |
| 15 | MySQL test container, volume, and network cleanup audit | PASS; no resources remained | `logs/15-mysql-cleanup.log` |
| 16 | Final `gift-panel-hosted:test` image startup smoke | PASS; MySQL healthy, `/healthz` OK, Hosted and OBS pages 200, identity-free metrics | `logs/16-hosted-image-smoke.log` |
| 17 | Final-image smoke Docker, port, and host-secret cleanup audit | PASS; no resources remained | `logs/17-hosted-image-cleanup.log` |
| 18 | PR CI and downloaded Windows artifact integrity summary | PASS | `logs/18-remote-ci-artifact.log` |

The final image was verified as `linux/amd64`, running as `65532:65532`, with
entrypoint `/usr/local/bin/hosted-entrypoint`. Two independent image builds
produced the same manifest digest:

`sha256:84674161fb6d8aede870da170359ddaa696f6247ae865fd35496f3d3ad02f814`

The final tracked worktree was clean. Generated outputs contained no detected
local absolute paths, GitHub credential patterns, private-key markers, or cache
directories covered by the acceptance scan.

## Remote CI and Windows artifact

The evidence-only follow-up commit `188fca7b9be2c218019d90eb09bf23cbe285d1a5`
was tested by [PR #4](https://github.com/brainfk123/bilibili-live-gift-panel/pull/4)
in [Mainline CI run 33604159570](https://github.com/brainfk123/bilibili-live-gift-panel/actions/runs/33604159570).

- `scope`: SUCCESS
- `hosted`: SUCCESS
- `hosted-mysql`: SKIPPED by scope; the real local MySQL gate above passed
- `windows-compat`: SUCCESS
- `ci-gate`: SUCCESS

The Windows job built and started the unsigned CI EXE, validated the config,
display, and config API routes, checked artifact closure, and uploaded
`windows-compat-5b5ad1accff47f822dda02d6439cd98c4777c366`.

- EXE SHA-256: `071e31a170c289b6606847c875946b83ba60a52e23bca05346eb10337fcae4e6`
- Smoke evidence SHA-256: `6faa60309d62c59230e3ba756e3f3ad437312f75600b54ac0c8f16556f3a2cfc`
- Smoke interval: `2026-09-02T07:43:31.254Z` through `2026-09-02T07:43:31.524Z`

## Residual findings and boundaries

- `npm ci` reported 7 dependency audit findings: 4 moderate, 2 high, and 1
  critical. No automatic audit fix was applied because that would change the
  dependency lock and may require a separate reviewed update.
- The first pre-install Playwright inventory probe failed because the package
  had not yet been installed. The post-`npm ci` probe passed and is recorded
  separately.
- The pinned MySQL 8.4.11 image is `linux/amd64`; Colima ran it through
  foreign-architecture emulation on this ARM host. The database integration
  test passed, but this is not native ARM performance evidence.
- Automated Windows x64 EXE startup smoke passed. Real GPU, OBS, driver,
  hardware encoding, and EXE/Hosted screenshot parity still require their
  documented target evidence.

## Evidence integrity

Sanitized logs are stored in [`logs/`](logs/). Their SHA-256 values are recorded
in [`logs/SHA256SUMS`](logs/SHA256SUMS).
