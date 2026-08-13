# Gift Clip Hidden FFmpeg and Bitrate Uplift Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent the Windows FFmpeg child process from creating a console window and raise the complete gift-video bitrate curve to exactly three times its v0.4.2 values.

**Architecture:** Keep the existing suspended-process/Job Object runner and add `CREATE_NO_WINDOW` at the single command-construction seam. Keep the existing pixel-area profile and average/peak/VBV relationships, but express its average bitrate constants and rounding quantum at the exact three-times scale so every previously supported resolution receives exactly three times its former target.

**Tech Stack:** Go 1.26, Windows `syscall.SysProcAttr`, FFmpeg 9.0 CLI, Vitest/TypeScript release tests, Playwright release E2E, GitHub Actions Authenticode release workflow.

## Global Constraints

- FFmpeg starts with `CREATE_SUSPENDED | CREATE_NO_WINDOW`; Job Object assignment must still occur before the primary thread resumes.
- 1920×1080 average bitrate is exactly 6,000,000 bit/s; minimum is 450,000 and maximum is 48,000,000.
- Peak bitrate remains average × 3/2 and VBV buffer remains average × 2.
- Input frame rate remains adaptive and output remains H.264 yuv420p at 30 FPS with no audio.
- Existing hardware-first/software-fallback behavior, cancellation, diagnostics, embedded FFmpeg provenance, EV signing, and licensing remain unchanged.
- Preserve all unrelated tracked and untracked files; do not push or publish until all local verification gates pass.

---

### Task 1: Hide the Windows FFmpeg Child Console

**Files:**
- Modify: `goserver/gift_clip_process_windows.go`
- Test: `goserver/gift_clip_process_test.go`

**Interfaces:**
- Consumes: `startGiftClipProcessSuspended(path string, args []string, stdout, stderr io.Writer) (*giftClipStartedProcess, error)`.
- Produces: `configureGiftClipWindowsCommand(command *exec.Cmd)` and `giftClipProcessCreationFlags`, used only by the Windows runner.

- [ ] **Step 1: Write the failing Windows command-configuration test**

Add a test that constructs an inert `exec.Cmd`, calls the wished-for configuration seam, and asserts both required flags and no unrelated creation flags:

```go
func TestConfigureGiftClipWindowsCommandStartsSuspendedWithoutConsole(t *testing.T) {
	command := exec.Command(os.Args[0])
	configureGiftClipWindowsCommand(command)
	if command.SysProcAttr == nil {
		t.Fatal("SysProcAttr is nil")
	}
	want := uint32(createSuspended | createNoWindow)
	if command.SysProcAttr.CreationFlags != want {
		t.Fatalf("CreationFlags = %#x, want %#x", command.SysProcAttr.CreationFlags, want)
	}
}
```

- [ ] **Step 2: Run the test and confirm RED**

Run:

```powershell
Set-Location goserver
go test ./... -run '^TestConfigureGiftClipWindowsCommandStartsSuspendedWithoutConsole$' -count=1
```

Expected: compile failure because `configureGiftClipWindowsCommand` and `createNoWindow` do not exist. This proves the test can detect the missing no-window contract.

- [ ] **Step 3: Add the minimal command-construction seam**

In `gift_clip_process_windows.go`, define the documented Windows flag and one narrow helper:

```go
const (
	createSuspended = 0x00000004
	createNoWindow  = 0x08000000
)

const giftClipProcessCreationFlags = createSuspended | createNoWindow

func configureGiftClipWindowsCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: giftClipProcessCreationFlags}
}
```

Replace the inline `SysProcAttr` assignment in `startGiftClipProcessSuspended` with `configureGiftClipWindowsCommand(command)`. Do not change pipe creation, process suspension, Job Object assignment, resume order, cancellation, or cleanup.

- [ ] **Step 4: Run focused GREEN and lifecycle stress**

Run:

```powershell
Set-Location goserver
go test ./... -run '^TestConfigureGiftClipWindowsCommandStartsSuspendedWithoutConsole$' -count=1
go test ./... -run '^TestGiftClipWindowsProcessRunner' -count=10 -timeout=180s
go test -race ./... -run '^TestGiftClipWindowsProcessRunner' -count=3 -timeout=180s
```

Expected: all pass; suspended execution, assignment-before-resume, output drain, cancellation, and handle ownership remain green.

- [ ] **Step 5: Commit Task 1 independently**

```powershell
git add -- goserver/gift_clip_process_windows.go goserver/gift_clip_process_test.go
git diff --cached --check
git commit -m "fix: hide gift clip ffmpeg console"
```

---

### Task 2: Raise the Entire Bitrate Curve Exactly Threefold

**Files:**
- Modify: `goserver/gift_clip_profile.go`
- Test: `goserver/gift_clip_profile_test.go`
- Test: `goserver/gift_clip_e2e_test.go`

**Interfaces:**
- Consumes: `giftClipAverageBitrate(width, height int) int64` and `newGiftClipOutputProfile(...)`.
- Produces: the same interfaces with exact three-times output values; no caller API changes.

- [ ] **Step 1: Change profile expectations first and confirm RED**

Update `TestGiftClipBitrateScalesWithPixelArea` to expect exactly three times every v0.4.2 average:

```go
{64, 64, 450_000}, {648, 360, 750_000},
{512, 360, 600_000}, {640, 360, 600_000}, {960, 540, 1_500_000},
{1280, 720, 2_700_000}, {1920, 1080, 6_000_000},
{2560, 1440, 10_650_000}, {3840, 2160, 24_000_000},
{4096, 4096, 48_000_000},
```

Keep the existing assertions that peak equals average × 3/2 and VBV equals average × 2. In `assertGiftClipE2EBitrateArgs`, change the 320×180 production minimum guard from `150_000` to `450_000`.

Run:

```powershell
Set-Location goserver
go test ./... -run '^(TestGiftClipBitrateScalesWithPixelArea|TestGiftClipE2E)' -count=1
```

Expected: profile assertion fails with old values such as 150,000 and 2,000,000.

- [ ] **Step 2: Implement the exact three-times curve**

Set the profile constants to:

```go
minGiftClipBitrate = int64(450_000)
maxGiftClipBitrate = int64(48_000_000)
```

Calculate against a 6,000,000 bit/s 1080p baseline and round to 150,000 bit/s increments so the result is exactly three times the prior 50,000-aligned curve:

```go
baselinePixels := int64(1920 * 1080)
numerator := int64(6_000_000) * int64(width) * int64(height)
rounded := ((numerator + 75_000*baselinePixels) / (150_000 * baselinePixels)) * 150_000
return minInt64(maxGiftClipBitrate, maxInt64(minGiftClipBitrate, rounded))
```

Do not change the average/peak/VBV multipliers in `newGiftClipOutputProfile` or FFmpeg argv construction.

- [ ] **Step 3: Run focused GREEN and argv coverage**

Run:

```powershell
Set-Location goserver
go test ./... -run '^(TestGiftClipBitrateScalesWithPixelArea|TestNewGiftClipOutputProfile|TestBuildGiftClipFFmpegArgs)' -count=10
go test -race ./... -run '^(TestGiftClipBitrateScalesWithPixelArea|TestBuildGiftClipFFmpegArgs)' -count=3
```

Expected: all exact profile values and `-b:v`/`-maxrate`/`-bufsize` arguments pass.

- [ ] **Step 4: Run real embedded-FFmpeg export verification**

Run the existing real E2E with the verified embedded payload:

```powershell
Set-Location goserver
go test ./... -run '^TestGiftClipE2E' -count=1 -timeout=180s
```

Expected: GIF, animated WebP, and packed-alpha exports remain H.264/yuv420p, 30 FPS, exact frame count/duration, no audio, and fall within the existing short-GOP byte-budget tolerance around the new profile target.

- [ ] **Step 5: Commit Task 2 independently**

```powershell
git add -- goserver/gift_clip_profile.go goserver/gift_clip_profile_test.go goserver/gift_clip_e2e_test.go
git diff --cached --check
git commit -m "feat: triple gift clip export bitrate"
```

---

### Task 3: Full Verification and v0.4.3 Release Preparation

**Files:**
- Modify: `package.json`
- Modify: `package-lock.json`
- Modify: `gift-panel-changelog.json`
- Test: `tests/changelog.test.ts`
- Test: `tests/wizard.test.ts`

**Interfaces:**
- Consumes: the Task 1 Windows runner and Task 2 output profiles.
- Produces: a v0.4.3 release candidate whose bundled changelog describes hidden FFmpeg execution and higher video quality.

- [ ] **Step 1: Add failing v0.4.3 changelog/version expectations**

Update the current-release assertions to expect `0.4.3`, date `2026-08-13`, and a concise user-visible entry:

```text
礼物视频导出体验优化
生成礼物视频时不再弹出命令行窗口，并提高输出码率以改善画质。
```

Run:

```powershell
npm test -- --run tests/changelog.test.ts tests/wizard.test.ts --reporter=dot
```

Expected: RED because package/changelog still report 0.4.2.

- [ ] **Step 2: Prepare v0.4.3 metadata**

Set the root and lockfile package versions to `0.4.3`. Replace the single bundled current-release fallback in `gift-panel-changelog.json` with v0.4.3, keeping hosted history responsible for older releases. Update only matching version assertions in the two tests; do not rewrite unrelated lockfile content.

- [ ] **Step 3: Run complete local release gates sequentially**

Run each command only after the prior one succeeds:

```powershell
npm run typecheck
npm test -- --reporter=dot
Set-Location goserver; go test ./... -count=1 -timeout=300s; Set-Location ..
Set-Location goserver; go test -race ./... -run 'TestGiftClip' -count=1 -timeout=300s; Set-Location ..
npm run build:ui
npm run verify:ffmpeg
npm run build:exe
npm run verify:gift-clip-export
git diff --check
```

Additionally run the built EXE and perform a Windows observation during an actual export: no `conhost.exe`/console window attributable to the FFmpeg child may appear. Record the exported file's `ffprobe` codec, pixel format, FPS, frame count, duration, audio-stream count, and bitrate.

- [ ] **Step 4: Commit release preparation**

```powershell
git add -- package.json package-lock.json gift-panel-changelog.json tests/changelog.test.ts tests/wizard.test.ts
git diff --cached --check
git commit -m "chore: prepare v0.4.3 release"
```

- [ ] **Step 5: Merge, tag, and publish only after a clean final review**

Confirm the feature worktree is clean, merge its commits into local `master` with `--no-ff`, and preserve every existing untracked main-repository file. Push `master`, create annotated tag `v0.4.3` at the merge commit, and push only that tag. Monitor GitHub Actions through tests, pinned FFmpeg verification, inner and outer EV signing, real browser/export E2E, attestation, and Release creation.

After success, download the public `gift-panel-windows-x64.exe` and verify:

```powershell
Get-FileHash .\gift-panel-windows-x64.exe -Algorithm SHA256
Get-AuthenticodeSignature .\gift-panel-windows-x64.exe
```

The hash must match both the published `.sha256` and update manifest, and Authenticode status must be `Valid`.
