# Task 13 execution report

Date: 2026-08-13 (Asia/Shanghai)

Historical Task 13 start base: `a4ec5469090cd2f6e30d799b62f72b5fb3c8bc7d`

Integrated Task 13 base: `333f146` (mainline integration advanced while Task 13 was in progress)

Initial Task 13 scoped commit/diff: `333f146..5eed0ed1b12ada75eedc1a48899274438c5a000a`

Fixed-review round 1 base: `5eed0ed1b12ada75eedc1a48899274438c5a000a`

Worktree: `C:\Users\brain\.codex\worktrees\21fa\bilibili`

Commit message reserved by the brief: `test: verify deterministic gift clip exports`

State at report write: initial Task 13 commit `5eed0ed1b12ada75eedc1a48899274438c5a000a` exists; fixed-review round 1 implementation and all sequential gates are complete; exact round-1 staging/commit is still pending.

## Scope ledger and protected pre-task state

Initial `git status --short` before Task 13 work:

```text
 M package-lock.json
?? scripts/diagnose-gift-clip-stutter.mjs
?? tests/fixtures/gift-clip-stutter.html
```

- Task 13 originally began with HEAD at the historical `a4ec5469` base. While the task was in progress, mainline integration advanced to `333f146`; the final initial Task 13 gates were executed serially on that integrated final tree, and its task-scoped commit is exactly `333f146..5eed0ed`. This history correction avoids implying that final commands ran on the earlier pre-integration tree.
- The two old untracked diagnostic/fixture files were explicitly authorized as Task 13 Add files. Their pre-task content and intent were preserved; neither was deleted or replaced.
- `package-lock.json` initially had an index/worktree blob of `fd103030afc07083d6368558424936b7b1d08601` and an empty content diff. The initial `M` was stat-only. The user subsequently authorized the exact repo-local Playwright dependency and normally generated lock delta. The final content diff is limited to Playwright 1.62.1, playwright-core 1.62.1, and the Darwin-only optional `fsevents` edge.
- No version, tag, push, merge, cherry-pick, release, upload, or final integration was performed.
- No production bitrate profile edit exists. In particular, `goserver/gift_clip_profile.go` remains unchanged and the 320x180 minimum target remains 150,000 bit/s.

Authorized Step 4 defect-fix expansion beyond the original file list:

- `goserver/gift_clip_ffmpeg.go` and `goserver/gift_clip_ffmpeg_test.go`: terminal packed-effect frame loss.
- `src/ui/config/gift-clip-studio-controller.ts` and `tests/gift-clip-studio.test.ts`: normalized crop was incorrectly sent to the integer-pixel Go API.
- `vite.config.ts`: isolated proxy CSRF/reset behavior and Vite artifact watcher crash.
- `package-lock.json`: explicitly authorized exact Playwright dependency.

## Step 1 — deterministic offline fixtures

`scripts/generate-gift-clip-fixtures.ps1` uses only the fixed lavfi commands in the brief. It selects `FFMPEG_FULL_BIN` or the known local full binary, verifies `libwebp_anim` and `libx264`, never downloads media, emits canonical one-line LF JSON, and rejects every generated file (including the layout) at or above 1 MiB.

Generator identity:

```text
ffmpeg version 2023-05-04-git-4006c71d19-full_build-www.gyan.dev
```

Repeated generator runs produced byte-identical files:

| Fixture | Bytes | SHA-256 |
| --- | ---: | --- |
| `input-10fps.gif` | 104140 | `f63292b332ac1411e0aa5109e10fc65d2f0abd6babf14b2a35d2bc0502a8f91e` |
| `input-20fps.webp` | 158186 | `1faa58f95ede4b57af5415df2e1c252e0036c43ae324f8cf497eabeb7f7330b0` |
| `packed-alpha-24fps.mp4` | 110477 | `068500d6d5a4a2fe5142f8d52a67ffb2653fd730f22295a4d14889a310a3e68e` |
| `packed-alpha-layout.json` | 112 | `f315edfe14c064a4171ad44062cdd2d3391307bab0ed8439934502ed9e126859` |

Layout bytes decode to exactly:

```json
{"videoWidth":640,"videoHeight":180,"rgbFrame":[0,0,320,180],"alphaFrame":[320,0,320,180],"fps":24,"frames":48}
```

## Steps 2–4 — real encoder, timing and TDD defect fixes

### Harness RED/GREEN

RED: `gift_clip_e2e_test.go` first referenced the nonexistent `newGiftClipHarnessServer`; Go compilation failed on that symbol.

GREEN: the test-only implementation uses the real `newGiftClipJobManager`, `newGiftClipHTTPHandler`, embedded Task 5 payload, and `ForceSoftware: true` encoder.

The harness:

- binds `tcp4` to `127.0.0.1:0` only and explicitly rejects 12450–12459;
- publishes its dynamic address through a private atomic port file;
- stops through a private stop file, bounded lifetime, `Shutdown`, and job-manager cleanup;
- serves animation/avatar/effect-video/effect-layout from the same checked-in fixtures used by the backend resolver;
- adds no production route or backdoor;
- uses production-shaped read-only startup responses only when the test supplies a packaged UI directory.

Manual media-route audit earlier in the task returned 200 for the GIF animation (104140 bytes), generated avatar PNG, packed MP4 (110477 bytes), and layout (112 bytes), then exited on the private stop file.

### Packed-effect terminal frame defect

Initial real RED:

- GIF: 60 frames / 2.000000 s.
- WebP: 60 frames / 2.000000 s.
- packed effect: 59 frames / 1.966667 s.

Filter-graph bisect found two interacting boundaries:

1. `alphamerge` framesync did not flush the last 24 fps interval when `fps=30` followed the merge.
2. `overlay=...:shortest=1` allowed either finite auxiliary stream to truncate the terminal output tick even though `-t 2` already owned duration.

TDD RED was added to `TestBuildGiftClipFFmpegArgsReconstructsPackedAlphaBeforeUserCrop`: require `setpts,fps=30,split` before alpha reconstruction, reject old post-merge timing normalization, and reject both `shortest=1` flags. The old graph failed. Minimal GREEN moves FPS before the effect split/alphamerge and removes both overlay shortest flags. It does not append an unconditional frame and keeps `-t` as the duration owner.

Final real Go E2E evidence:

| Input | Frames | Duration | Repeated max MAE | Changed min MAE | Stream bitrate | File size |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| GIF 10 fps | 60 | 2.000000 s | 0.012 | 4.623 | 216460 bit/s | 55191 B |
| WebP 20 fps | 60 | 2.000000 s | 0.009 | 2.392 | 272100 bit/s | 69101 B |
| packed alpha 24 fps | 60 | 2.000000 s | 0.012 | 1.595 | 249484 bit/s | 63447 B |

All are H.264, 30/1 fps, 320x180, one video stream, no audio, and below 1 MiB.

### Timestamp-correct frame acceptance

Raw decoded frame MD5 equality within one source-frame repetition group is invalid for lossy H.264: inter-frame reconstruction can introduce tiny per-output-frame differences even when the timestamp mapping repeats one source instant. The acceptance therefore does not assume fragile identical hashes for source repetitions.

It still runs full `framemd5` and requires:

- time base exactly `1/30`;
- exactly 60 rows, PTS `0..59`, duration one tick, and valid MD5 shape;
- timestamp-rescaled source indices with the exact expected repeat count for every 10/20/24 fps source frame;
- deterministic 32x18 area-scaled grayscale fingerprints where repeated-group MAE is at most 0.25, changed-boundary MAE at least 1.0, and the clusters never overlap.

The measured margins above reject a static sequence and a wrongly sampled sequence while tolerating measured lossy reconstruction noise.

### 150 kbit/s contract and bounded short-clip overhead

The original proportional-only ceiling was intentionally run first and was RED for all three two-second fixtures:

```text
target: 150000 bit/s
old band: 97500..202500 bit/s
actual: 216460 / 272100 / 249484 bit/s
```

Diagnostics compared the same command through the full and minimal FFmpeg binaries and swept longer durations. Container overhead was small; the excess decayed with duration and matched fixed Media Foundation startup/GOP cost on these short clips. The user chose to preserve the approved production target.

The final acceptance verifies both layers:

1. Production profile and arguments remain `AverageBitrate=150000`, `-b:v 150000`, `-maxrate 225000`, `-bufsize 300000`.
2. Measured bytes use the original ±35% proportional window plus only a fixed 24 KiB upper startup/GOP allowance:

```text
targetBytes = 150000 * duration / 8
lowerBytes  = targetBytes * 0.65
upperBytes  = targetBytes * 1.35 + 24 * 1024
```

For two seconds this is 24,375..75,201 bytes of encoded video. The lower bound is unchanged. This is not a new product bitrate and not a general percentage relaxation: the fixed allowance contributes `24*1024*8/duration` bit/s, so its average-rate effect decreases as `1/duration` (98,304 bit/s at 2 s, 19,661 bit/s at 10 s, etc.). Both the real Go E2E and browser E2E apply this byte formula.

### Studio pixel-crop defect

Browser RED: the real Studio converted normalized `{x:0,y:0,width:1,height:1}` to a 640x360 pixel canvas but sent the normalized crop to the Go API, which expects integer source pixels. The real handler returned 400.

TDD RED changed the existing Studio expectation to `{x:0,y:0,width:640,height:360}` and failed against the old controller. Minimal GREEN changes only the create-job payload to `crop: pixels`; persisted/editing crop remains normalized. Focused test and the full 24-test Studio file passed, followed by full Vitest and browser E2E.

### Vite proxy/reset diagnosis

Minimal reproduction with the initial object proxy default showed:

```text
Origin: http://127.0.0.1:<vite-port>
Host:   127.0.0.1:<go-port>
Go same-origin result: 403
Vite client result: 500 / ECONNRESET
```

Root cause was Vite/http-proxy `changeOrigin: true`: the browser-visible Origin remained the Vite endpoint but the proxied Host became the Go endpoint, so the real Go CSRF guard correctly rejected the request. `changeOrigin: false` preserves the logical browser Host and matches the packaged same-origin topology. The proxy target remains the exact environment target or production fallback `http://localhost:12450`.

The first successful download also caused Windows Vite file watching to hit `EBUSY` on an artifact opened by Playwright. Ignoring only `**/artifacts/gift-clip-export/**` prevents that test-output watcher crash without changing production assets.

## Browser stall and packaged-UI acceptance

### Reproducible browser dependency

Official npm registry lookup on 2026-08-13 returned Playwright `1.62.1`. `package.json` uses the exact pin with no range; `npm install --save-dev --save-exact` produced the reviewed lock delta.

```text
playwright 1.62.1
playwright-core 1.62.1
Chromium revision 1234
Chrome for Testing 151.0.7922.34
```

`npm exec -- playwright install chromium` installs the browser revision selected by this exact package into the Playwright test cache. Windows CI does not run a floating third-party installer or `--with-deps`. The browser is not copied into `dist`, `goserver/dist`, the EXE, release assets, or application data. Final recursive product-dist audit found zero `playwright`, `chromium`, or `chrome-headless` file names; the release asset list contains none; `build-go` embeds only the 79 UI manifest assets plus the already packaged FFmpeg component.

### Packaged UI / manifest / OBS route

The browser runner mirrors the freshly built `dist` through the same `ui-assets.mjs` manifest contract into a private runtime directory, verifies every manifest hash, and serves it with `newEmbeddedPageHandler`.

Final packaged-UI evidence:

```text
manifest files: 79
canonical config chunk: modules/ui/config/config-entry-CYA6YCCS.js
OBS route config-chunk requests: 0
all packaged HTTP >=400 responses: 0
packaged console/page errors: 0
```

This corrects the stale plan name: Task 5's actual committed release gate is `dist/ffmpeg-component-gate.txt`, not the brief's obsolete JSON name. The workflow consumes/uploads the real `.txt` artifact.

### Real three-variant stall run

The fixture page imports the real `openGiftClipStudio` and CSS; it does not copy Studio DOM, export logic, or encoding logic. Each variant runs in independent baseline and immediate 180 ms busy-loop contexts. The complete decoded `framemd5` row sequence is compared, not only a summary hash.

Final run (after all assertion strengthening):

| Variant | Baseline/stalled complete sequence | SHA-256 of sequence evidence | Bitrate | Size |
| --- | --- | --- | ---: | ---: |
| GIF | identical, 60 rows | `7b514bc28072228861a5bdccb01db8079bc727451c17068a3905c3c49030d6d7` | 168848 bit/s | 43288 B |
| WebP | identical, 60 rows | `3db6cca78a50e8cc9c86c923bc0f7b96e1dc0de6c01128d2fe4903307c150869` | 207700 bit/s | 53001 B |
| packed effect | identical, 60 rows | `e20ef8f58ea345f97e19bb2f6c06d26141db37a0b35612ac53593e9bf751a7ca` | 184608 bit/s | 47228 B |

Every output is H.264, 30/1 fps, 60 frames, 2.000 s, 320x180, no audio, nontrivial, below 1 MiB, and inside the unchanged-target byte budget. Console errors/page errors: 0. Maximum document overflow: 0. Maximum dialog overflow: 0.

Artifacts:

```text
artifacts/gift-clip-export/gif-baseline.mp4
artifacts/gift-clip-export/gif-stalled.mp4
artifacts/gift-clip-export/webp-baseline.mp4
artifacts/gift-clip-export/webp-stalled.mp4
artifacts/gift-clip-export/effect-baseline.mp4
artifacts/gift-clip-export/effect-stalled.mp4

artifacts/gift-clip-export/gif-editing.png
artifacts/gift-clip-export/gif-encoding.png
artifacts/gift-clip-export/gif-ready.png
artifacts/gift-clip-export/webp-editing.png
artifacts/gift-clip-export/webp-encoding.png
artifacts/gift-clip-export/webp-ready.png
artifacts/gift-clip-export/effect-editing.png
artifacts/gift-clip-export/effect-encoding.png
artifacts/gift-clip-export/effect-ready.png
```

Final process audit used only recorded direct child identities:

```text
Go harness PID 17496, dynamic loopback port 11769
Vite PID 644, dynamic loopback port 11770
both outside 12450..12459
both absent after finally cleanup
```

The runner never enumerates or kills by port. It records each direct child PID before use, signals the private harness stop file, and only if bounded graceful cleanup fails calls `taskkill /pid <exact-recorded-pid> /t /f`. The reserved user port range is never probed, bound, or killed.

## Release workflow audit

The final workflow parses as YAML with 20 release steps. The relevant order is:

1. Clean checkout, fixed Node 22 and Go from `goserver/go.mod`, version/tag gate.
2. `npm ci`, Vitest, typecheck, frontend build.
3. `msys2/setup-msys2@v2` only establishes UCRT64 with `update:false`, `cache:false`; it does not request floating packages.
4. `build:ffmpeg -InstallPinnedToolchain` consumes the exact committed 35-package Task 5 lock. The build script verifies lock schema/closure, exact archive and `.sig` hashes, trusted GPG signatures, then installs to an isolated root with `pacman -U --noconfirm --nodeps --noscriptlet`. It rechecks the exact installed package set and compiler/linker/build-tool versions before building.
5. `verify:ffmpeg` validates the component and canonical gate.
6. Sign inner `dist/ffmpeg/ffmpeg.exe`; require Authenticode `Valid`.
7. Package with `FFMPEG_AUTHENTICODE=true` and release `APP_VERSION`; verify again, including manifest auth state.
8. `build:exe`; this is intentionally before backend tests on a clean checkout because `goserver/dist` is ignored and `build-go` mirrors the full built UI tree needed by `go:embed`.
9. Run backend tests after the embed tree exists.
10. Copy/sign the outer release EXE; require Authenticode `Valid`. The workflow does not print signer subject or secret values.
11. Install the exact Playwright package's Chromium revision; run the real browser E2E after both inner packaging/auth verification and outer signature verification.
12. Prepare, attest, and upload release assets.

Exact compliance assets in both upload branches:

```text
dist/ffmpeg-source/ffmpeg-9.0.tar.xz
dist/ffmpeg-source/ffmpeg-9.0.tar.xz.asc
dist/ffmpeg-build-config.txt
dist/ffmpeg-component-gate.txt
third_party/ffmpeg/toolchain-lock.json
third_party/ffmpeg/NOTICE.md
third_party/ffmpeg/COPYING.LGPLv2.1
```

No browser binary/cache is a release asset. No release workflow was executed locally because it requires signing secrets and would publish; only its local build/verification contracts and syntax were exercised.

Workflow/static evidence:

```text
Python/PyYAML parse: steps=20, exit 0
npm ls playwright playwright-core: exact 1.62.1 / 1.62.1, exit 0
node --check scripts/verify-gift-clip-export.mjs: exit 0
npm run build:ffmpeg -- -SelfTest: exit 0 (earlier Task 13 checkpoint)
npm run verify:ffmpeg -- --self-test: exit 0 (earlier Task 13 checkpoint)
```

## Documentation

README now documents fixed 30 FPS H.264 MP4 output, timestamp-adaptive GIF/WebP/effect inputs, hardware-first encoding with automatic compatibility fallback, first-use LocalAppData verification/preparation of the embedded component, no user FFmpeg/PATH requirement, LGPL 2.1+ terms, notices/license, and upstream source/signature links.

`package.json` adds the E2E script and exact Playwright dev dependency. It does not change application version or runtime dependencies.

## Final sequential gates

Commands were run serially in the brief's order. No gate below ran in parallel.

| Gate | Result |
| --- | --- |
| `npm run typecheck` | exit 0 |
| `npm test -- --reporter=dot` | exit 0; 40 files, 491 passed, 31 skipped |
| `goserver: go test ./... -count=1` | exit 0; 46.233 s |
| `goserver: go test -race ./... -run 'TestGiftClip' -count=1` | exit 0; 16.896 s |
| `npm run build:ui` | exit 0; 88 modules, 79 manifest assets |
| `npm run verify:ffmpeg` | exit 0; FFmpeg 9.0 binary 6,209,536 B; ZIP 2,415,506 B; SHA-256 `19247e960c50adcf107bc04e8a20435fd67d098e06b227d8772f0d1b8027e03c` |
| `npm run verify:gift-clip-export` | exit 0; packaged UI + six exports + three exact stall comparisons |
| `npm run build:exe` | exit 0; `dist/gift-panel.exe` 12,279,808 B; SHA-256 `2db1734075111c184f63f41bc867e94adc60db475143f39b88b6a13258c064eb` |
| `git diff --check` | exit 0 |
| exact final browser PID audit | both children cleaned |
| recursive browser-in-product-dist audit | zero matches |

After the self-review added an explicit layout-size check and explicit packaged-HTTP failure assertion, the fixture generator, Node syntax check, and complete real browser E2E were rerun and all exited 0. The focused real `TestGiftClipE2E` was also rerun verbosely after fixture regeneration and exited 0 with the final values recorded above.

## Final scope reconciliation before staging

Expected Task 13 paths:

```text
.github/workflows/release.yml
README.md
goserver/gift_clip_e2e_test.go
goserver/gift_clip_ffmpeg.go
goserver/gift_clip_ffmpeg_test.go
package.json
package-lock.json
scripts/diagnose-gift-clip-stutter.mjs
scripts/generate-gift-clip-fixtures.ps1
scripts/verify-gift-clip-export.mjs
src/ui/config/gift-clip-studio-controller.ts
tests/gift-clip-studio.test.ts
tests/fixtures/gift-clip-export.html
tests/fixtures/gift-clip-media/input-10fps.gif
tests/fixtures/gift-clip-media/input-20fps.webp
tests/fixtures/gift-clip-media/packed-alpha-24fps.mp4
tests/fixtures/gift-clip-media/packed-alpha-layout.json
tests/fixtures/gift-clip-stutter.html
vite.config.ts
.superpowers/sdd/2026-08-11-gift-clip-ffmpeg-export/task-13-report.md
```

- Old untracked diagnose script: present, intention preserved, explicitly included.
- Old untracked stutter HTML: present, intention preserved, explicitly included.
- `package-lock.json`: authorized dependency delta only; no version or unrelated graph change.
- Production bitrate profile: unchanged.
- Generated `dist`, `goserver/dist`, `artifacts`, browser cache, and runtime temp files: ignored and not to be staged.
- No unrelated tracked or untracked path is authorized for staging.

## Fixed-review round 1 — reproducible full test tools, pixel format, and framemd5 evidence

Review base: `5eed0ed1b12ada75eedc1a48899274438c5a000a`.

This round addresses only the four open fixed-review findings. It does not change the 150,000 bit/s product profile, install another browser dependency, change the fixtures, execute a release, or alter version/tag state. The reviewer's minor note about unconditional cleanup of `artifacts/gift-clip-export` remains recorded for final review and was not expanded into this round.

### RED/GREEN ledger

Test-tool provenance and browser probe contract RED:

```text
npm test -- --run tests/gift-clip-test-tools.test.ts tests/gift-clip-export-contract.test.ts --reporter=dot
exit 1
tests/gift-clip-test-tools.test.ts: missing ../scripts/gift-clip-test-tools.mjs
tests/gift-clip-export-contract.test.ts: missing ../scripts/gift-clip-export-contract.mjs
```

The new tests require an exact-provenance manifest, reject a same-size tampered binary, reject a source/toolchain provenance mismatch, and pass an observable `pixelFormat: yuv444p` probe which the old browser assertions did not inspect. GREEN after the minimal verifier/contract modules:

```text
npm test -- --run tests/gift-clip-test-tools.test.ts tests/gift-clip-export-contract.test.ts tests/ui-assets.test.ts --reporter=dot
exit 0; 3 files, 6 tests
```

Go pixel-format and framemd5 RED:

```text
go test ./... -run 'TestGiftClip(FrameMD5TimestampPatternRejectsWrongBoundaryOrMissingRepeats|E2EVideoContractRejectsNonYUV420P)$' -count=1
build failed: undefined validateGiftClipFrameMD5TimestampPattern
build failed: undefined validateGiftClipE2EVideoContract
```

`TestGiftClipE2EVideoContractRejectsNonYUV420P` supplies `yuv444p`, the previously unobserved decoded format, and requires rejection. `TestGiftClipFrameMD5TimestampPatternRejectsWrongBoundaryOrMissingRepeats` supplies one valid sequence, a static hash extended across a source timestamp boundary, and an all-unique sequence with every expected repeat missing. GREEN:

```text
go test ./... -run 'TestGiftClip(FrameMD5TimestampPatternRejectsWrongBoundaryOrMissingRepeats|E2EVideoContractRejectsNonYUV420P)$' -count=1
exit 0
```

The first real browser attempt after building the isolated toolchain exposed a packaging integration RED before Chromium was launched:

```text
Error: UI asset manifest path is invalid: msys2-toolchain-root/ucrt64/bin/c++.exe
exit 1
```

A focused fixture then reproduced the leak and failed with that same path. GREEN adds only `gift-clip-test-tools` and `msys2-toolchain-root` to the existing non-UI top-level set; the fixture proves neither directory is mirrored into the Go embed tree. The final full product-tree and executable audits are recorded below.

Typecheck also correctly caught the new `.mjs` test seams before final gates:

```text
TS7016: no declaration for gift-clip-export-contract.mjs
TS7016: no declaration for gift-clip-test-tools.mjs
TS7006: implicit-any callback path
```

Minimal `.d.mts` contracts and an explicit callback parameter type made the same `npm run typecheck` gate GREEN without changing runtime behavior.

### Clean-runner full ffmpeg/ffprobe contract

The release job no longer calls `Get-Command ffmpeg.exe` or `Get-Command ffprobe.exe` and does not depend on runner `PATH`. The same `build:ffmpeg` invocation that produces the product component now receives `-BuildGiftClipTestTools`. It first performs the existing Task 5 controls:

- exact committed 35-package `third_party/ffmpeg/toolchain-lock.json` closure;
- each package archive SHA-256, package signature and trusted signer verification;
- isolated/root `pacman -U --noconfirm --nodeps --noscriptlet`;
- exact installed package-set and pinned gcc/ld/make checks;
- FFmpeg 9.0 source archive SHA-256 plus detached release signature and supplemental signed-tag verification.

Only after those checks, a second fixed static configuration builds test-only `ffmpeg.exe` and `ffprobe.exe`. Its flag-file SHA-256 is `2bbd2048081e7ca1d87b88509f1c0d1362dc2ec54c49dd7821e3a81e298d0886`. It contains the H.264/MP4 decoder/probe and deterministic framemd5/rawvideo filters needed by the E2E, but no product Media Foundation encoder. The generated manifest binds every binary to all source, signer, signed-tag, toolchain-lock, configure-set, size, and binary-hash fields. `scripts/gift-clip-test-tools.mjs` rejects extra/missing schema fields, provenance differences, path changes, size/hash changes, or a non-9.0 `-version`, and returns absolute paths only after verification.

Final manifest evidence:

| Field | Value |
| --- | --- |
| FFmpeg version | `9.0` |
| source SHA-256 | `7f607a00dd0d28a729d5a4811205812eef01cf6ef6155025febb6f36a9062d52` |
| source signer | `FCF986EA15E6E293A5644F10B4322F04D67658D8` |
| supplemental tag | `n9.0`, commit `d32b387f2b0a484599d4587d651891f0c63c4238` |
| supplemental signer | `DD1EC9E8DE085C629B3E1846B18E8928B3948D64` |
| canonical toolchain-lock SHA-256 | `00936d04bc060022b87a532bac396efe69f551b7bcf7ff791f71a1236ab77e67` |
| test configure SHA-256 | `2bbd2048081e7ca1d87b88509f1c0d1362dc2ec54c49dd7821e3a81e298d0886` |
| `ffmpeg.exe` | 5,443,072 B; `19f9b1cf38a0706faf7a882fc63fe406f1e1571bbd168ac3333e19d663fa2233` |
| `ffprobe.exe` | 5,240,320 B; `441d8a74ca232c43436460f04cd18da21aa8ea13db432db26583e0fed6365376` |

`node scripts/gift-clip-test-tools.mjs` independently rehashed both final binaries, executed both `-version` commands, and exited 0. The workflow converts its JSON response to two explicit `[IO.Path]::GetFullPath(...)` environment values before invoking the browser verifier.

The first clean-install local attempt verified and built the 6,209,536-byte product binary, then the 20-minute tool wrapper timed out while the original child was in the test-tool `configure` phase. A process audit showed that exact child briefly active; no duplicate was launched. After the wrapper job removed the child and a second audit found no build/make process, one recovery invocation reused the already installed exact toolchain and already verified source and completed in 214.7 seconds, exit 0. This is a local wrapper-limit event, not a workflow fallback or weakened trust check.

The test tools and isolated build roots are explicitly excluded by `ui-assets.mjs`. They are not copied into `goserver/dist`, `gift-panel.exe`, application data, release assets, or the product FFmpeg payload. Final evidence:

```text
goserver/dist recursive test-tool/browser/build-root filename matches: 0
gift-panel.exe ASCII markers for gift-clip-test-tools, chromium-1234, ffprobe version 9.0: 0
gift-panel.exe: 12,279,808 B
gift-panel.exe SHA-256: 2db1734075111c184f63f41bc867e94adc60db475143f39b88b6a13258c064eb
embedded UI manifest: 79 product UI assets
release upload lists: no gift-clip-test-tools, ffprobe, Chromium, Playwright, or isolated toolchain root
```

### Decoded yuv420p contract

Both real acceptance paths now request `pix_fmt` from ffprobe and require exactly `yuv420p`, in addition to the existing h264, `30/1`, 60 frames, 2.000-second duration, 320x180 dimensions, no-audio, size, and unchanged 150 kbit/s short-clip byte-budget assertions. Final browser output probes:

| Variant | Pixel format | Bitrate | Size |
| --- | --- | ---: | ---: |
| GIF | `yuv420p` | 168,848 bit/s | 43,288 B |
| WebP | `yuv420p` | 207,700 bit/s | 53,001 B |
| packed effect | `yuv420p` | 184,608 bit/s | 47,228 B |

The product encoding arguments and profile remain unchanged (`-b:v 150000`, `-maxrate 225000`, `-bufsize 300000`, output NV12 consumed as decoded yuv420p). The existing byte window remains `targetBytes*0.65 .. targetBytes*1.35 + 24KiB`; the additive allowance remains bounded and amortizes as `1/duration`.

### Direct framemd5 timestamp/repetition evidence

The ordinary full-resolution decoded framemd5 sequence remains the complete browser baseline/stall equality evidence. It is deliberately not described as requiring lossy H.264 duplicates to have identical decoded raw hashes.

For a direct, falsifiable Go timing assertion, a second deterministic decoded stream uses area downscale to 32x18 gray and 16-level luminance quantization, then framemd5. The assertion derives source-frame groups from each output timestamp using the same rational rescale as the 10/20/24-to-30 fps contract. An adjacent equal hash is forbidden across a source timestamp boundary; at least half of expected repeated timestamp ticks must remain directly observable as adjacent equal quantized hashes. This rejects a static/wrong-boundary sequence and an all-unique/missing-repeat sequence while tolerating actual lossy H.264 variation. The pre-existing full grayscale perceptual fingerprint assertion remains separately responsible for low-MAE within-group and high-MAE changed-group evidence.

Final 60-row quantized framemd5 evidence:

| Variant | Expected repeated ticks | Adjacent equal hashes inside group | Equal hashes across source boundary |
| --- | ---: | ---: | ---: |
| GIF 10→30 | 40 | 40 | 0 |
| WebP 20→30 | 20 | 12 | 0 |
| effect 24→30 | 12 | 10 | 0 |

The final real Go E2E also retained the perceptual evidence: repeated maximum MAE `0.012/0.009/0.012`, changed minimum MAE `4.622/2.392/1.590` for GIF/WebP/effect respectively.

### Final real browser E2E after the final local build chain

Exact dependencies: `playwright@1.62.1`, `playwright-core@1.62.1`, Playwright-managed Chromium revision 1234. The final run used repository-built, manifest-verified absolute ffmpeg/ffprobe paths and exited 0 in 25.0 seconds.

```text
harness PID 20588, dynamic loopback port 9689
Vite PID 14140, dynamic loopback port 9690
packaged manifest files 79
canonical config chunk modules/ui/config/config-entry-CYA6YCCS.js
OBS config requests 0
console/page errors 0
document/dialog overflow 0/0 for every variant
three editing/encoding/ready screenshots for every variant
both exact PIDs absent after finally cleanup
```

Full-file baseline/stalled SHA-256 equality after the 180 ms immediate stall:

| Variant | Full MP4 SHA-256 | Equal |
| --- | --- | --- |
| GIF | `f2f1e41f6fe04ad6ab8c64ec01425f2e87af04a2e9f558d9549b29334f2d77f0` | yes |
| WebP | `55ef9b631e02da7cdd830d61ad733d98388a7ed3bdcb236853c584762c823770` | yes |
| effect | `38613342e6a406292113adea80b7b413f7e75efb10af20935e49646a35112804` | yes |

The run bound neither the protected 12450..12459 range nor any fixed production port. Cleanup targeted only PIDs recorded from the two child processes started by this run.

### Release workflow fixed-review audit

The workflow still parses as YAML with 20 steps. The existing signing/package order is unchanged: build and verify the inner product FFmpeg, require inner Authenticode `Valid`, package with `FFMPEG_AUTHENTICODE=true`, verify the authenticated package, build the outer EXE, run backend tests after `build:exe` has prepared ignored `goserver/dist`, sign the outer EXE and require `Valid`, install the exact Playwright package's Chromium, then run the real browser E2E. The E2E now consumes the build-produced manifest, verifies both full test tools, and passes their explicit absolute paths. Assets remain the exact Task 5 canonical `.txt` component gate and existing compliance set. No secret, signer subject, binary, or test-tool path is printed by a signing step.

No signing or release workflow was executed locally because signing secrets and publication are outside this task. The signed-chain placement, exact inputs, YAML syntax, and all non-secret local build/verification contracts were audited; the real E2E remains after both signature checks in CI.

### Fixed-review round 1 final sequential gates

No gates in this table ran in parallel.

| Gate | Result |
| --- | --- |
| focused Vitest provenance/probe/UI exclusions | exit 0; 3 files, 6 tests |
| focused Go pixel-format/framemd5 validators | exit 0 |
| `npm run typecheck` | initial declaration RED; final exit 0 |
| `npm test -- --reporter=dot` | exit 0; 42 files, 496 passed, 31 skipped |
| `go test ./... -count=1` | exit 0; 49.691 s |
| `go test -race ./... -run 'TestGiftClip' -count=1` | exit 0; 17.077 s |
| `npm run build:ui` | exit 0; 88 modules |
| clean `build:ffmpeg -InstallPinnedToolchain -BuildGiftClipTestTools` | product build complete; local wrapper exit 124 during test-tool configure, exact child audited; no trust bypass |
| recovery `build:ffmpeg -BuildGiftClipTestTools` after no child remained | exit 0; 214.7 s |
| `npm run verify:ffmpeg` | exit 0; FFmpeg 9.0, 6,209,536 B; ZIP 2,415,506 B; component SHA `19247e960c50adcf107bc04e8a20435fd67d098e06b227d8772f0d1b8027e03c` |
| `node scripts/gift-clip-test-tools.mjs` | exit 0; both final size/hash/version/provenance records verified |
| `npm run build:exe` | exit 0; 79 UI assets; 12,279,808 B EXE |
| backend `go test ./... -count=1` after `build:exe` | exit 0; 48.903 s |
| real `TestGiftClipE2E` | exit 0; all three 60-frame yuv420p outputs and direct framemd5 evidence |
| final real browser E2E | exit 0; 25.0 s; all three exact stall comparisons |
| build script `-SelfTest` | exit 0 |
| verifier `--self-test` | exit 0 |
| Node syntax checks for all three verifier/contract modules | exit 0 |
| Python/PyYAML workflow parse | exit 0; 20 steps |
| `npm ls playwright playwright-core` | exit 0; exact 1.62.1 / 1.62.1 |
| final exact browser PID audit | both absent |
| product tree / EXE test-tool-browser marker audit | zero matches |

Round-1 tracked paths authorized for staging:

```text
.github/workflows/release.yml
.superpowers/sdd/2026-08-11-gift-clip-ffmpeg-export/task-13-report.md
goserver/gift_clip_e2e_test.go
scripts/build-ffmpeg.ps1
scripts/gift-clip-export-contract.d.mts
scripts/gift-clip-export-contract.mjs
scripts/gift-clip-test-tools.d.mts
scripts/gift-clip-test-tools.mjs
scripts/ui-assets.mjs
scripts/verify-gift-clip-export.mjs
tests/gift-clip-export-contract.test.ts
tests/gift-clip-test-tools.test.ts
tests/ui-assets.test.ts
```

Generated `dist`, ignored `goserver/dist`, artifacts, the isolated toolchain, source/build trees, manifests, binaries, and browser cache remain untracked/ignored and must not be staged. No `package.json`, `package-lock.json`, product bitrate profile, fixture, version, tag, release asset, or unrelated source change belongs to this round.
