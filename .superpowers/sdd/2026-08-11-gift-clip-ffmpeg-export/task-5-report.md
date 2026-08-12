# Task 5 report: reproducible minimal FFmpeg payload and cache

## Result

Implemented the approved bounded FFmpeg 9.0 architecture: a pinned, signature-verified minimal Windows CLI; deterministic one-entry embedded ZIP and generated manifest; strict verified application-local cache extraction; release signing gates; exact component verification; and production-argv GIF/WebP/packed-alpha smoke coverage.

Final commit: this report's commit, with exact message `build: embed minimal ffmpeg runtime`. The immutable hash is returned to the parent after commit creation.

## RED / GREEN evidence

- RED: focused payload tests initially failed to compile because `giftClipPayload`, `embeddedGiftClipPayload`, and `Prepare` did not exist. GREEN: cache reuse/rebuild, 16-goroutine serialization, integrity failure, unsafe ZIP shape, cancellation, retained prior manifests, malformed manifest, embedded payload, and application-specific cache tests pass.
- RED: embedded payload test expected version 9.0 but the preserved draft manifest was 8.1.2. GREEN: the generated embedded manifest and actual CLI are 9.0.
- RED: packaging a full prebuilt FFmpeg exceeded the decimal 40,000,000-byte hard limit. GREEN: the minimized real build packages to 2,416,791 bytes, below the 30,000,000-byte target.
- RED: unsigned release packaging and release Go build were exercised and rejected. GREEN: dev packaging/build succeeds while both release gates fail before changing payload files.
- RED: the real smoke first rejected sub-second synthetic durations, then exposed late GIF loop metadata and a too-short packed-alpha fixture. The smoke harness was corrected; Task 3 fixed loop metadata placement in `0499477`. GREEN: single GIF, normalized no-loop GIF, normalized finite-loop GIF, static WebP, animated WebP, and packed-alpha H.264 all pass exact production argv and 30 fps duration/frame checks.

## Provenance and build

- Source: `https://ffmpeg.org/releases/ffmpeg-9.0.tar.xz`
- Source size: 12,032,020 bytes
- Source SHA-256: `7f607a00dd0d28a729d5a4811205812eef01cf6ef6155025febb6f36a9062d52`
- Detached signature: GPG `GOODSIG` and exact `VALIDSIG FCF986EA15E6E293A5644F10B4322F04D67658D8`
- Supplemental signed tag: `n9.0`, commit `d32b387f2b0a484599d4587d651891f0c63c4238`, signer `DD1EC9E8DE085C629B3E1846B18E8928B3948D64`
- Toolchain: MSYS2 UCRT64 GCC 16.2.0 (Rev3); build completed in 122 seconds.
- License: LGPL 2.1 or later; bundled `COPYING.LGPLv2.1` exactly matches signed source, SHA-256 `246041b6ecf9bc32d718a62c57877c78b5eb397b6467e74ed7ae2626ab189c30`.

Configured product surface: file/pipe; gif/webp_anim/image_webp_pipe/mov/image2; gif/webp_anim/webp/png/h264; gif/h264 parsers; h264_mf; mp4; crop/scale/format/split/alphamerge/overlay/fps/setpts; Media Foundation and D3D11VA infrastructure. Configure and runtime gates prohibit network, GPL, nonfree, and the cycle-caching `loop` filter. Runtime exact-set verification permits only the recorded FFmpeg auto-selected infrastructure aliases/components.

## Payload

- `ffmpeg.exe`: 6,203,392 bytes; SHA-256 `08cc33e3614b52e4fe3820557c92e0fdf38d2a5e40fc4d110e5ebdc41fc4175b`
- `ffmpeg.zip`: 2,416,791 bytes; SHA-256 `13cd9a2b510175b5c5b90227eca844b263a82c6af170e305f1c71a6eb41ff41e`
- Manifest: version 9.0, decimal size 6,203,392, `authenticode:false` for the development artifact.
- ZIP verifier and Go loader require exactly one UTF-8 `ffmpeg.exe` regular-file entry, no extras/comments/path forms/symlinks, exact CRC/size/hash, and safe boundaries.
- Cache extraction checks context cancellation, creates an application-specific LocalAppData path, writes and verifies a same-directory random partial, finalizes permissions/data before an atomic write-through replacement, coordinates independent in-process payload instances, and retains caches for other manifests.

## Verification outputs

- `npm run build:ffmpeg`: exit 0; exact pinned GPG `VALIDSIG`; configure/component gates pass; real `ffmpeg.exe` produced.
- Two clean builds with pinned `SOURCE_DATE_EPOCH=1785797913` produced the identical executable SHA-256 `08cc33e3...4175b`; two packaging runs produced the identical ZIP SHA-256 `13cd9a2b...ff41e`.
- `npm run verify:ffmpeg`: exit 0; `verified FFmpeg 9.0 ...`; production smoke Go route exit 0.
- `go test ./... -run "TestGiftClipPayload" -count=1`: exit 0.
- `go test -race ./... -run "TestGiftClipPayload" -count=1`: exit 0 (`ok`, 2.950s final run).
- `go test ./... -count=1` from `goserver`: exit 0 (`ok`, 2.349s final run).
- `npm run typecheck`: exit 0.
- `npm test`: 36 files passed; 427 tests passed, 31 skipped.
- `npm run build:exe`: exit 0; development `dist/gift-panel.exe` built.
- Node syntax checks for build/package/verify scripts: exit 0.
- Unsigned `APP_VERSION=1.0.0` package and Go release builds: both rejected; an attempted `FFMPEG_AUTHENTICODE=true` claim was rejected from actual `NotSigned` status; embedded ZIP/manifest hashes unchanged.
- `git diff --check`: exit 0.

## Risks / release handoff

- Independent review found no Critical issues and three Important issues. All were addressed and the re-review returned no Critical/Important findings: atomic independent-instance cache replacement, exhaustive configurable/runtime component gates, and TOCTOU-safe actual Authenticode `Valid` verification of a private copy of the exact buffered bytes during signed packaging/release verification.
- The committed payload is intentionally a development artifact (`authenticode:false`). Task 13 must Authenticode-sign the inner `ffmpeg.exe`, package again with `FFMPEG_AUTHENTICODE=true`; packaging and release verification now independently require the actual signature status to be `Valid`.
- Reproduction requires the recorded MSYS2 UCRT64 toolchain. The source, signature identity, flags, component gates, and output provenance are pinned; differing compiler/binutils revisions can change binary bytes.
- Upstream GCC emitted non-fatal warnings while compiling FFmpeg 9.0; configure, link, component verification, real smokes, and all repository gates completed successfully.

## Formal review fix round 1

- Pair publication now uses an exclusive same-directory package lock, durable randomized partials, rollback-capable backups, exact state-tracked rollback, and owned-file cleanup. Injected failures after new-file durability, each backup, and each publication step preserve the exact old pair (or prior absence); 12 concurrent publishers finish with one complete generation. Because Windows cannot atomically replace two fixed paths as one filesystem transaction across a crash, the manifest now binds the ZIP with `archive_sha256`; build verification rejects any mixed generation before embedding.
- The manifest also binds a deterministic generated component-gate record. It ties FFmpeg 9.0, source SHA-256, `SOURCE_DATE_EPOCH`, configure-flags SHA-256, every enabled configurable component, and Media Foundation/D3D11VA infrastructure. Official FFmpeg 9.0 has no `-parsers` CLI option, so the authorized build-time exact parser macro set is AC3/GIF/H264; verifier/runtime GIF and H.264 smokes provide executable coverage.
- Node verification now rejects archives above 40,000,000 decimal bytes before parsing, rejects declared compressed/uncompressed sizes above policy or manifest, validates compressed-region bounds, and inflates with `maxOutputLength: 40_000_000`. Adversarial huge-declaration and high-expansion tests pass.
- The Go loader now performs an exact EOCD/central/local ZIP record validation before `archive/zip`: one disk/entry, no comments/extras/prefix/trailing junk, UTF-8-only flags, DEFLATE only, regular external attrs, exact `ffmpeg.exe`, matching method/flags/CRC/sizes/names, and exact boundaries. It uses `bytes.NewReader`; the custom ReaderAt was removed.
- Go and Node manifest parsers now require exactly one JSON object, exact fields, no duplicates/unknowns/trailing JSON, and valid hashes/types. The Go loader additionally verifies the archive hash before extraction.
- Release verification now runs both `TestBuildGiftClipFFmpegArgsSelectsBoundedPlaybackInput` and the real production-argv smoke, preventing animated `-stream_loop` or a filter `loop` regression.
- RED/GREEN: strict Go ZIP/manifest APIs began undefined and are GREEN; transaction injection first lost an untouched target and is GREEN after exact state tracking; the verifier unknown-field adversary was RED and is GREEN after parser-level allowlisting.
- Fresh gates: package and verifier self-tests GREEN; `npm run verify:ffmpeg` GREEN; focused Go GREEN 1.271s; race GREEN 3.032s; full Go GREEN 2.309s; 427 frontend tests GREEN; typecheck/build:exe/release negatives/diff GREEN. Repeated package ZIP remains byte-identical SHA-256 `13cd9a2b510175b5c5b90227eca844b263a82c6af170e305f1c71a6eb41ff41e`.
- Re-review hardening binds the canonical component/toolchain record directly into the committed manifest, so a clean checkout verifies and builds without ignored `dist` evidence. Packaging still requires the fresh build record and rejects a record whose `binary_sha256` or decimal `binary_size` differs from the buffered input. The build publishes that record only after a successful binary build/copy.
- The exact proven MSYS2 package set is gated and recorded: GCC 16.2.0-3, binutils 2.47-3, make 4.4.1-3, diffutils 3.12-1, pkgconf 3.0.5-1, nasm 2.16.03-1, zlib 1.3.2-2, CRT/headers/winpthreads `14.0.0.r262.g5ea8e9fac-1`, plus exact `gcc`, `ld`, and `make` version strings. A wrong package-list ordering produced RED; the canonical sorted comparison then passed the full signed-source build in 120 seconds with the identical binary hash.
- Fixed-path pair publication now persists a same-directory transaction journal with owner metadata and old/new generation identity. A stale-lock successor deterministically restores the old pair for incomplete transactions or retains a hash-verified committed pair; backups are not deleted after incomplete rollback. Injected crash states after durability, each backup, each publish, plus rollback-rename failure all recover on the next invocation to a complete pair. The >40,000,000-byte binary gate now runs before compression, and an adversarial highly compressible oversized buffer is rejected without deflate.
- Strict manifest adversaries now also reject a trailing comma. Clean-checkout simulation removed `dist/ffmpeg-component-gate.txt`; `npm run build:exe` still verified the committed payload and built successfully, after which the fresh record was restored.
- Final review preserved the Task 13 order: after the package script proves the exact buffered binary has Authenticode status `Valid`, it refreshes only the record's binary hash/size fields to bind the signed bytes; an unsigned mismatch still fails. A crash-released OS lock elects exactly one stale-transaction recovery owner and blocks new publication while recovery runs; a 12-way concurrent stale-takeover test finishes with one complete generation.
- The signing derivation gate was further tightened: the build records PE Authenticode-content SHA-256 `7c06e37143bf10c1a4c814d61bff982769fe2ffce37781063981f4f381e06368` (normalizing only the PE checksum, certificate directory, and appended certificate table). Signed-only rebinding requires this digest to remain identical, preventing a different validly signed executable from inheriting provenance. Recovery/publication is serialized by a directory-specific Windows file handle opened with `FileShare.None` and held by a helper process, so owner termination releases it at the OS boundary; the persistent journal then restores or validates the fixed pair. A 12-way post-crash takeover remains GREEN.
- OS-lock release is guarded by an outer `finally`, including failures while reading/recovering an invalid journal; a regression test injects that failure and immediately completes a subsequent publication, proving the helper handle was released.
