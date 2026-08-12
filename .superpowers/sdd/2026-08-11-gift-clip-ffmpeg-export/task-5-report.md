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
