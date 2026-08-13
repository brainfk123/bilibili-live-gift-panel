# Final whole-branch review fix report

Date: 2026-08-13 (Asia/Shanghai)

Base: `805807c9a84b683a218559f508ef3b436dad0acb`

Worktree: `C:\Users\brain\.codex\worktrees\21fa\bilibili`

Scope: the four load-bearing findings from the final whole-branch review only. Deferred odd-crop, concurrent cold hardware probe, `RemoveAll` TOCTOU, and fixed-artifact cleanup minors were not changed. No merge, tag, push, publish, or release action was performed.

## Baseline

- `git rev-parse HEAD` → `805807c9a84b683a218559f508ef3b436dad0acb`; `git status --short` was empty.
- `npm test -- --reporter=dot` → exit 0; 42 files, 496 passed, 31 skipped.
- `goserver: go test ./... -count=1 -timeout=300s` → exit 0; `ok bilibili-live-gift-panel 49.445s`.

## Finding 1 — release Action supply chain

### Root cause and design

The `contents:write` / OIDC / attestation / signing release job parsed to one MSYS2 setup step whose consumed action ref was the mutable `msys2/setup-msys2@v2`. The minimal fix keeps the single job and all existing signed-package ordering, but pins that action to the currently audited v2 commit. A semantic workflow test parses the YAML AST, consumes the real release steps and the real toolchain lock, rejects a tag ref, and protects the exact 35-package lock plus setup/build/inner-sign/package/outer-sign/E2E order.

`yaml@2.8.1` was added as a locked dev dependency solely so the test parses workflow semantics rather than grepping source text. No `npm audit fix` or dependency widening was performed.

### Official primary-source verification

- Official release/tag page: `https://github.com/msys2/setup-msys2/releases/tag/v2`; its commit link resolves to the official repository commit `66cd2cce69caa17b53920067426061ca1de3a884` (`v2.32.0`).
- Direct official Git ref query: `git ls-remote https://github.com/msys2/setup-msys2.git refs/tags/v2 refs/tags/v2^{}` → `66cd2cce69caa17b53920067426061ca1de3a884 refs/tags/v2`.
- Workflow pin: `msys2/setup-msys2@66cd2cce69caa17b53920067426061ca1de3a884 # v2`.

### RED

Command:

```powershell
npx vitest run tests/release-workflow.test.ts --reporter=verbose
```

Observed exit 1: one contract passed (35-package lock and ordering), and the action contract failed exactly because received `msys2/setup-msys2@v2` instead of the audited 40-character SHA.

### GREEN

Same command after the one-line workflow pin → exit 0; 1 file, 2 tests passed. The YAML scalar's human-readable comment is also parsed and required to be `v2`.

Files: `.github/workflows/release.yml`, `tests/release-workflow.test.ts`, `package.json`, `package-lock.json`.

## Finding 2 — non-Windows payload compilation

### Root cause and design

The untagged common payload file imported `syscall`/`unsafe` and initialized `syscall.NewLazyDLL`, making the package uncompilable for `GOOS=linux`. The common prepare/extract/rebuild code remains platform-neutral. Windows owns the unchanged `MoveFileExW` implementation and injectable `giftClipMoveFileExW` seam with `REPLACE_EXISTING | WRITE_THROUGH`. `!windows` uses `os.Rename`; source partial and target are created in the same cache directory, so this is an atomic same-filesystem replacement on supported non-Windows targets, replaces an existing corrupt target, and returns the underlying error unchanged.

The compile gate creates an isolated system temp directory, runs `go test -c` with `GOOS=linux GOARCH=amd64 CGO_ENABLED=0`, never executes the Linux binary, and removes the binary in `finally`.

### RED

Command:

```powershell
npm run verify:go-linux-compile
```

Observed exit 1:

```text
gift_clip_payload.go:45:35: undefined: syscall.NewLazyDLL
gift_clip_payload.go:325:32: undefined: syscall.UTF16PtrFromString
gift_clip_payload.go:329:32: undefined: syscall.UTF16PtrFromString
```

### GREEN and adjacent verification

- `npm run verify:go-linux-compile` → exit 0; `Verified GOOS=linux GOARCH=amd64 compile-only gate.`
- `goserver: go test ./... -run '^TestGiftClipPayload' -count=10 -timeout=300s` → exit 0; `ok ... 2.407s`.
- `goserver: go test -race ./... -run '^TestGiftClipPayload' -count=3 -timeout=300s` → exit 0; `ok ... 4.020s`.

Files: `goserver/gift_clip_payload.go`, `goserver/gift_clip_payload_windows.go`, `goserver/gift_clip_payload_other.go`, `scripts/verify-go-linux-compile.mjs`, `package.json`.

## Finding 3 — startup inbox installation versus Reset

### Root cause and design

`Run` took its startup health snapshot and installed/published the inbox without the `resetGate` read side. A concurrent HTTP reset could clear the inbox before the captured startup snapshot was installed, allowing stale pending/capacity/revision health to be published afterward. The fix puts only the startup `current/open → Health/SnapshotHealth → install/publish` consistency window under `resetGate.RLock`, releases it before workers start, and preserves existing epoch/revision/generation guards. This follows the established lock order (`resetGate` then `mu`) used by producer/consumer paths; the window does not call Reset or configuration mutation callbacks.

The regression uses an epoch-zero preinstalled fake inbox. Its first `Health` captures revision 7 / pending 3 / capacity true and blocks on a channel. Reset is attempted concurrently; its fake durable `Reset` signal must not occur until the snapshot is released and installed. Once released, Reset changes health to revision 8 / empty and the final published status must contain no stale pending bytes, oldest timestamp, or capacity error. Channels create the interleaving; the timeout is only a bounded negative assertion/deadlock guard, not a sleep-driven ordering setup.

### RED

Command:

```powershell
go test ./... -run '^TestBackgroundRuntimeStartupInboxInstallSerializesWithReset$' -count=1 -timeout=30s
```

Observed exit 1 in 0.02s: `Reset entered cleanup before startup installed and published its captured health`.

### GREEN and adjacent verification

- Same focused test with `-count=20 -timeout=60s` → exit 0; `ok ... 2.788s`.
- Focused startup/reset/retired-epoch race set with `go test -race ... -count=10 -timeout=180s` → exit 0; `ok ... 5.668s`.

Files: `goserver/background_runtime.go`, `goserver/background_runtime_test.go`.

## Finding 4 — fractional full-effect duration

### Root cause and design

The resolver first calculated `frames/fps` as `time.Duration`, then converted it to integer milliseconds before the product clamp. For 61 frames at 59 fps this changed `1.033898305s` to `1.033s`; the 30 fps ceiling then produced 31 instead of 32 frames. The fix retains duration through the existing `time.Duration` nanosecond path and clamps only below 1 second or above 15 seconds. GIF/WebP timestamp/delay handling, bitrate/profile parameters, and FFmpeg filter semantics are unchanged. Existing FFmpeg formatting already uses a locale-independent, round-trippable duration string.

### RED

Command:

```powershell
go test ./... -run '^TestGiftClipEffectPreservesFractionalDurationThroughOutputBoundaryAndArgv$' -count=1 -timeout=30s
```

Observed exit 1: resolver returned `1.033s`; test expected exact `61*time.Second/59 = 1.033898305s`.

### GREEN and adjacent verification

- Resolver → profile → argv regression requires duration `1.033898305s`, output frame boundary 32, included frame time `31/30 < duration`, excluded `32/30 >= duration`, and FFmpeg `-t 1.033898305`.
- Focused resolver/profile/argv set with `-count=20 -timeout=120s` → exit 0; `ok ... 2.017s`.
- Focused race set with `-count=5 -timeout=120s` → exit 0; `ok ... 2.909s`.

Files: `goserver/gift_clip_source.go`, `goserver/gift_clip_source_test.go`.

## Final sequential verification

All commands below were run serially; commands that write `dist` were never concurrent.

| Gate | Result |
| --- | --- |
| final semantic release-workflow contract | exit 0; 1 file, 2 tests; audited 40-hex pin, YAML `# v2` comment, 35-package lock consumption, and signed-chain order |
| `npm run verify:go-linux-compile` | exit 0; `GOOS=linux GOARCH=amd64 CGO_ENABLED=0` compile-only; generated test binary removed without execution |
| focused four-finding Go set, `-count=10` | exit 0; `ok ... 3.346s` |
| focused payload/startup-reset/effect race set, `-count=3` | exit 0; `ok ... 4.919s` |
| `goserver: go test ./... -count=1 -timeout=300s` | exit 0; `ok ... 49.083s` |
| `goserver: go test -race ./... -count=1 -timeout=300s` | exit 0; `ok ... 95.331s` |
| `npm run typecheck` | exit 0 |
| `npm test -- --reporter=dot` | exit 0; 43 files, 498 passed, 31 skipped |
| `npm run build:ui` | exit 0; 88 modules; 2.41s |
| final `npm run verify:ffmpeg` | exit 0; FFmpeg 9.0, 6,209,536-byte binary, 2,415,506-byte ZIP, component SHA `19247e960c50adcf107bc04e8a20435fd67d098e06b227d8772f0d1b8027e03c` |
| pinned `build:ffmpeg -InstallPinnedToolchain -BuildGiftClipTestTools` | final exit 0; exact 35 packages / 563.62 MiB, trusted signatures, release signer, source archive, product FFmpeg, and test-only ffmpeg/ffprobe verified and built; 287.9s final run |
| `node scripts/gift-clip-test-tools.mjs` | exit 0; returned absolute built ffmpeg/ffprobe paths after provenance, size, SHA-256, and 9.0 version checks |
| `npm run build:exe` | exit 0; 79 UI assets; local dev EXE built |
| backend full suite after `build:exe` | exit 0; `ok ... 50.223s` |
| real `TestGiftClipE2E` | exit 0; GIF/WebP/effect each 60 frames, 2.000s, yuv420p and timestamp/repetition evidence |
| real browser `npm run verify:gift-clip-export` | exit 0; 24.9s; 79-file packaged UI, six exports, three baseline/stall comparisons, zero console errors and overflow |
| script/dependency/PID checks | `node --check` exit 0; exact `yaml@2.8.1`; browser harness PID 28996 and Vite PID 30496 absent after cleanup |

### Local trusted-build recovery evidence

`build:ui` correctly removed the ignored test-tool directory, so the first standalone test-tool verification failed closed with `Gift clip test-tool manifest is missing.` The first exact pinned rebuild then exited 1 after 509.6 seconds because the official MSYS2 endpoint delivered the 6.33 MiB pinned binutils archive more slowly than the build wrapper's two 120-second whole-file attempts. This was an external input-availability failure before toolchain installation, not a source/test failure.

No trust check was disabled or changed. The exact `.download` was resumed from byte 4,487,312 using the committed official URL; it was promoted only after its full SHA-256 matched `ba98af202fe71e0884bb51daf78ce10bd8a51c69528c700dcf5855e6ece06dd3`. The remaining missing package archives were handled identically; the largest, GCC 16.2.0-3, was 48,655,779 bytes and matched committed SHA-256 `1cd86e5817f6e0d7310d7cb2bb91f4a55e1256cb4ce580a0c5f2a74e013d144d`. The official FFmpeg 9.0 source archive was promoted only after matching `7f607a00dd0d28a729d5a4811205812eef01cf6ef6155025febb6f36a9062d52`.

The unmodified build script then downloaded/consumed the exact detached package signatures, required trusted package signers, installed and queried the exact 35-package set, checked pinned gcc/ld/make versions, verified the FFmpeg release signature and supplemental signed tag, and built both configurations. Its final run exited 0. All recovered files remain ignored build inputs/outputs under `dist` and are outside the staged scope.

Final browser probe summary:

| Variant | Frames / format | Bitrate | Size | Errors / overflow |
| --- | --- | ---: | ---: | --- |
| GIF | 60 / yuv420p | 168,848 bit/s | 43,288 B | 0 / 0 |
| WebP | 60 / yuv420p | 207,700 bit/s | 53,001 B | 0 / 0 |
| effect | 60 / yuv420p | 184,608 bit/s | 47,228 B | 0 / 0 |

## Post-review merge-gate stabilization — ingestion error retry observation

### Failure and root cause

The merge controller's fresh `goserver: go test ./... -count=1 -timeout=300s` failed after 55.2 seconds in `TestBackgroundRuntimeReportsIngestionFailureWithoutDisconnectAndClearsAfterRetry`: connection status remained correctly `connected` for `room-a`, but the test's five-second polling helper never observed the injected ingestion error.

Focused reproduction against `cc71c24f6bee6b3bd2e6d211544fcf3eaf13a5ea`:

```powershell
go test ./... -run '^TestBackgroundRuntimeReportsIngestionFailureWithoutDisconnectAndClearsAfterRetry$' -count=100 -timeout=180s
```

Observed RED: exit 1; 7 of 100 runs failed after approximately 5.08 seconds with empty `IngestionError` / `IngestionErrorKind` and otherwise healthy connected status. The complete focused run took 49.016 seconds.

The failure was a pre-existing test synchronization defect. The consumer recorded the first transaction failure as generation 1 / source `consumer`, then waited on a periodic 10ms retry ticker. Because the ticker starts with the worker rather than at the failure, the retry can begin anywhere from immediately to 10ms later. The successful retry acknowledges the still-pending record and clears the matching generation/source error. The test also sampled every 10ms, without a happens-before edge, so the complete set/clear interval could occur between two samples.

`git blame` attributes the test to `96588d73`; the retry, record, and clear paths also predate `cc71c24`. The latter changed only the startup inbox snapshot/install/publish read-side reset gate before workers start. It did not change this test or the retry/error state machine; at most it perturbed a scheduler/ticker phase and exposed the existing flake.

A temporary acknowledgement-barrier diagnostic was run ten times and then removed. Every run observed:

```text
before successful ack: failed=true generation=1 source="consumer" error="injected ingestion health failure" kind="transaction" pending=1
after successful retry: failed=true generation=1 source="" error="" pending=0
```

This ruled out a missing production error publication.

### Minimal test-only correction

The real inbox and real state transaction remain in use. The existing `writeAtomically` failure seam now fails the first `events.log` write, then signals `retryStarted` and blocks the second successful `events.log` write on `allowRetry`. Waiting for `retryStarted` establishes that the consumer recorded the failure and entered its retry. While the retry is blocked, the test deterministically requires connected state, empty connection error, exact ingestion error `injected ingestion health failure`, kind `transaction`, no disconnect notification, and one still-pending record. It then releases the retry and waits for the stable terminal condition: pending zero and both ingestion error fields empty.

The transient assertion copies `runtime.status` under `runtime.mu.RLock` because the second write barrier intentionally runs while the config store write lock is held. An initial diagnostic implementation called `runtime.Status()` at that point and correctly exposed a test-induced lock cycle: `Status → TransactionPending` waited for the store read lock while the consumer waited on `allowRetry` under the store write lock. No production lock or behavior was changed; full `Status()` is used again after releasing the barrier.

No sleep, `Gosched`, or periodic polling observes the transient error. Channels provide the ordering; polling is used only for the stable post-retry terminal condition.

### GREEN

| Gate | Result |
| --- | --- |
| focused single diagnostic run | exit 0; test 0.09s |
| focused `-count=100 -timeout=180s` | exit 0; `ok ... 13.320s` |
| focused race `-count=20 -timeout=180s` | exit 0; `ok ... 4.628s` |
| controller's failed full command, `go test ./... -count=1 -timeout=300s` | exit 0; `ok ... 49.443s` |

Only `goserver/background_runtime_test.go` and this report are changed by the stabilization commit. No production, npm, package, build, `dist`, or artifact path was changed or invoked.

## Scope

Authorized production/config paths:

```text
.github/workflows/release.yml
goserver/background_runtime.go
goserver/gift_clip_payload.go
goserver/gift_clip_payload_windows.go
goserver/gift_clip_payload_other.go
goserver/gift_clip_source.go
package.json
package-lock.json
scripts/verify-go-linux-compile.mjs
```

Authorized regression/report paths:

```text
goserver/background_runtime_test.go
goserver/gift_clip_source_test.go
tests/release-workflow.test.ts
.superpowers/sdd/2026-08-11-gift-clip-ffmpeg-export/final-review-fix-report.md
```

Ignored/generated `dist`, `goserver/dist`, `artifacts`, temp compile binaries, and browser/test-tool build caches are not staged. No unrelated tracked or untracked file is owned by this wave.
