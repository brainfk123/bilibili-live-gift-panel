package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGiftClipPayloadPrepareReusesOnlyHashVerifiedCache(t *testing.T) {
	root := t.TempDir()
	binary := []byte("MZ\x90\x00fixture-ffmpeg")
	payload := newTestGiftClipPayload(t, root, binary)

	first, err := payload.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(first) {
		t.Fatalf("path is not absolute: %s", first)
	}
	if err := os.WriteFile(first, []byte("corrupt"), 0o700); err != nil {
		t.Fatal(err)
	}
	second, err := payload.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, binary) {
		t.Fatalf("cache was not rebuilt: %q", got)
	}
	if first != second {
		t.Fatalf("cache path changed: %q != %q", first, second)
	}
}

func TestGiftClipPayloadPrepareSerializesConcurrentExtraction(t *testing.T) {
	payload := newTestGiftClipPayload(t, t.TempDir(), []byte("MZ\x90\x00concurrent-fixture"))
	const workers = 16
	paths := make(chan string, workers)
	errs := make(chan error, workers)
	var ready sync.WaitGroup
	ready.Add(workers)
	start := make(chan struct{})
	for range workers {
		go func() {
			ready.Done()
			<-start
			path, err := payload.Prepare(context.Background())
			paths <- path
			errs <- err
		}()
	}
	ready.Wait()
	close(start)

	var expected string
	for range workers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		path := <-paths
		if expected == "" {
			expected = path
		} else if path != expected {
			t.Fatalf("concurrent paths differ: %q != %q", path, expected)
		}
	}
	partials, err := filepath.Glob(filepath.Join(filepath.Dir(expected), ".partial-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(partials) != 0 {
		t.Fatalf("partial files remain: %v", partials)
	}
}

func TestGiftClipPayloadPrepareCoordinatesIndependentInstances(t *testing.T) {
	root := t.TempDir()
	binary := []byte("MZ\x90\x00independent-instances")
	seed := newTestGiftClipPayload(t, root, binary)
	target, err := seed.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("corrupt"), 0o700); err != nil {
		t.Fatal(err)
	}

	const workers = 16
	var wait sync.WaitGroup
	wait.Add(workers)
	start := make(chan struct{})
	errors := make(chan error, workers)
	for range workers {
		payload := newTestGiftClipPayload(t, root, binary)
		go func() {
			defer wait.Done()
			<-start
			_, prepareErr := payload.Prepare(context.Background())
			errors <- prepareErr
		}()
	}
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got, err := os.ReadFile(target); err != nil || !bytes.Equal(got, binary) {
		t.Fatalf("shared cache data=%q err=%v", got, err)
	}
	partials, err := filepath.Glob(filepath.Join(filepath.Dir(target), ".partial-*"))
	if err != nil || len(partials) != 0 {
		t.Fatalf("shared cache partials=%v err=%v", partials, err)
	}
}

func TestGiftClipPayloadPrepareRejectsIntegrityMismatchWithoutExecutable(t *testing.T) {
	root := t.TempDir()
	payload := newTestGiftClipPayload(t, root, []byte("MZ\x90\x00archive-bytes"))
	payload.Manifest.SHA256 = strings.Repeat("0", 64)

	path, err := payload.Prepare(context.Background())
	if !errors.Is(err, errGiftClipPayloadIntegrity) {
		t.Fatalf("error = %v, want integrity error", err)
	}
	if path != "" {
		t.Fatalf("path = %q, want empty", path)
	}
	var executables []string
	filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr == nil && !entry.IsDir() && strings.EqualFold(entry.Name(), "ffmpeg.exe") {
			executables = append(executables, path)
		}
		return walkErr
	})
	if len(executables) != 0 {
		t.Fatalf("integrity failure left executable: %v", executables)
	}
}

func TestGiftClipPayloadPrepareRejectsUnsafeZipShapes(t *testing.T) {
	tests := []struct {
		name    string
		entries []testZipEntry
	}{
		{name: "empty archive"},
		{name: "extra entry", entries: []testZipEntry{{name: "ffmpeg.exe", body: []byte("MZok")}, {name: "notice.txt", body: []byte("extra")}}},
		{name: "traversal", entries: []testZipEntry{{name: "../ffmpeg.exe", body: []byte("MZok")}}},
		{name: "absolute", entries: []testZipEntry{{name: "/ffmpeg.exe", body: []byte("MZok")}}},
		{name: "nested", entries: []testZipEntry{{name: "bin/ffmpeg.exe", body: []byte("MZok")}}},
		{name: "directory", entries: []testZipEntry{{name: "ffmpeg.exe/", mode: os.ModeDir}}},
		{name: "symlink", entries: []testZipEntry{{name: "ffmpeg.exe", body: []byte("target"), mode: os.ModeSymlink | 0o777}}},
		{name: "wrong name", entries: []testZipEntry{{name: "FFMPEG.EXE", body: []byte("MZok")}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			body := []byte("MZok")
			hash := sha256.Sum256(body)
			payload := &giftClipPayload{
				Archive:   testZipArchive(t, test.entries),
				Manifest:  giftClipFFmpegManifest{Version: "8.1.2", SHA256: fmt.Sprintf("%x", hash), Size: int64(len(body))},
				CacheRoot: root,
			}
			if _, err := payload.Prepare(context.Background()); !errors.Is(err, errGiftClipPayloadIntegrity) {
				t.Fatalf("error = %v, want integrity error", err)
			}
		})
	}
}

func TestGiftClipPayloadPrepareHonorsCancelledContextWithoutWriting(t *testing.T) {
	root := t.TempDir()
	payload := newTestGiftClipPayload(t, root, []byte("MZ\x90\x00cancelled"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := payload.Prepare(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("cancelled preparation wrote cache entries: %v", entries)
	}
}

func TestGiftClipPayloadPrepareRetainsOtherManifestCaches(t *testing.T) {
	root := t.TempDir()
	otherCache := filepath.Join(root, "8.0-othermanifest")
	if err := os.MkdirAll(otherCache, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(otherCache, "ffmpeg.exe")
	if err := os.WriteFile(marker, []byte("older payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	payload := newTestGiftClipPayload(t, root, []byte("MZ\x90\x00current"))
	if _, err := payload.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "older payload" {
		t.Fatalf("other manifest cache changed: %q, %v", got, err)
	}
}

func TestGiftClipPayloadPrepareRejectsMalformedManifest(t *testing.T) {
	archive := testZipArchive(t, []testZipEntry{{name: "ffmpeg.exe", body: []byte("MZok")}})
	tests := []giftClipFFmpegManifest{
		{Version: "", SHA256: strings.Repeat("0", 64), Size: 4},
		{Version: "../escape", SHA256: strings.Repeat("0", 64), Size: 4},
		{Version: "8.1.2", SHA256: "not-a-sha", Size: 4},
		{Version: "8.1.2", SHA256: strings.Repeat("0", 64), Size: 0},
	}
	for _, manifest := range tests {
		payload := &giftClipPayload{Archive: archive, Manifest: manifest, CacheRoot: t.TempDir()}
		if _, err := payload.Prepare(context.Background()); !errors.Is(err, errGiftClipPayloadIntegrity) {
			t.Fatalf("manifest %+v error = %v, want integrity error", manifest, err)
		}
	}
}

func TestGiftClipPayloadEmbeddedArchiveMatchesManifest(t *testing.T) {
	payload, err := embeddedGiftClipPayload(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if payload.Manifest.Version != "9.0" || payload.Manifest.Size <= 0 || len(payload.Archive) == 0 {
		t.Fatalf("embedded payload is incomplete: manifest=%+v archive=%d", payload.Manifest, len(payload.Archive))
	}
	path, err := payload.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !giftClipFileMatches(path, payload.Manifest) {
		t.Fatalf("prepared embedded executable does not match manifest: %s", path)
	}
}

func TestGiftClipPayloadFFmpegProductionArgvSmoke(t *testing.T) {
	executable := os.Getenv("GIFT_CLIP_FFMPEG_SMOKE_EXE")
	if executable == "" {
		t.Skip("set GIFT_CLIP_FFMPEG_SMOKE_EXE to run the real payload smoke")
	}
	version, err := exec.Command(executable, "-version").CombinedOutput()
	if err != nil || !strings.HasPrefix(string(version), "ffmpeg version 9.0") {
		t.Fatalf("payload version check failed: err=%v output=%s", err, version)
	}

	root := t.TempDir()
	background := filepath.Join(root, "background.png")
	overlay := filepath.Join(root, "overlay.png")
	writeGiftClipSmokePNG(t, background, color.RGBA{R: 12, G: 24, B: 48, A: 255})
	writeGiftClipSmokePNG(t, overlay, color.RGBA{R: 255, G: 255, B: 255, A: 32})

	imageCases := []struct {
		name, mediaType string
		data            []byte
		wantPlayback    giftClipPlaybackMode
	}{
		{name: "single-gif", mediaType: "image/gif", data: gifFixture(t, 64, 64, []int{4}, -1), wantPlayback: giftClipPlaybackSingleGIF},
		{name: "multi-gif-no-loop", mediaType: "image/gif", data: gifFixture(t, 64, 64, []int{2, 3}, -1), wantPlayback: giftClipPlaybackAnimatedGIF},
		{name: "multi-gif-finite-loop", mediaType: "image/gif", data: gifFixture(t, 64, 64, []int{2, 3}, 2), wantPlayback: giftClipPlaybackAnimatedGIF},
		{name: "static-webp", mediaType: "image/webp", data: mustDecodeGiftClipSmokeBase64(t, "UklGRiIAAABXRUJQVlA4TBYAAAAvP8APAAdQ5KhM/wNAQvj/Xovof+oH"), wantPlayback: giftClipPlaybackStaticWebP},
		{name: "animated-webp", mediaType: "image/webp", data: mustDecodeGiftClipSmokeBase64(t, "UklGRngAAABXRUJQVlA4WAoAAAASAAAAPwAAPwAAQU5JTQYAAAAAAAAAAgBBTk1GIgAAAAAAAAAAAD8AAD8AAMgAAAJWUDhMCgAAAC8/wA8AiP5H/wNBTk1GIgAAAAAAAAAAAD8AAD8AACwBAAJWUDhMCgAAAC8/wA8QiOh/AQM="), wantPlayback: giftClipPlaybackAnimatedWebP},
	}
	for _, test := range imageCases {
		t.Run(test.name, func(t *testing.T) {
			animation, err := inspectGiftClipShortAnimation(test.mediaType, test.data)
			if err != nil {
				t.Fatal(err)
			}
			if animation.Playback != test.wantPlayback {
				t.Fatalf("playback = %q, want %q", animation.Playback, test.wantPlayback)
			}
			sourcePath := filepath.Join(root, test.name+animation.Extension)
			if err := os.WriteFile(sourcePath, animation.Data, 0o600); err != nil {
				t.Fatal(err)
			}
			source := giftClipSource{Kind: animation.Kind, Playback: animation.Playback, Path: sourcePath, VisualWidth: animation.Width, VisualHeight: animation.Height, Duration: 1200 * time.Millisecond}
			runGiftClipFFmpegSmoke(t, executable, giftClipSmokeRequest(source, background, overlay, filepath.Join(root, test.name+".mp4")))
		})
	}

	packed := mustDecodeGiftClipSmokeBase64(t, "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")
	packedPath := filepath.Join(root, "packed.mp4")
	if err := os.WriteFile(packedPath, packed, 0o600); err != nil {
		t.Fatal(err)
	}
	packedSource := giftClipSource{Kind: giftClipSourceEffect, Playback: giftClipPlaybackEffect, Path: packedPath, VisualWidth: 64, VisualHeight: 64, Duration: 1200 * time.Millisecond, Layout: &giftEffectLayout{VideoWidth: 128, VideoHeight: 64, RGBFrame: [4]int{0, 0, 64, 64}, AlphaFrame: [4]int{64, 0, 64, 64}, FPS: 30, Frames: 36}}
	runGiftClipFFmpegSmoke(t, executable, giftClipSmokeRequest(packedSource, background, overlay, filepath.Join(root, "packed-smoke.mp4")))
}

func giftClipSmokeRequest(source giftClipSource, background, overlay, output string) giftClipEncodeRequest {
	return giftClipEncodeRequest{
		Source: source, Crop: giftClipCrop{Width: 64, Height: 64},
		Profile:        giftClipOutputProfile{Width: 64, Height: 64, FPS: 30, Frames: giftClipFrameCount(source.Duration), Duration: source.Duration, AverageBitrate: 150_000, PeakBitrate: 225_000, VBVBuffer: 300_000},
		BackgroundPath: background, OverlayPath: overlay, OutputPath: output,
	}
}

func runGiftClipFFmpegSmoke(t *testing.T, executable string, request giftClipEncodeRequest) {
	t.Helper()
	args, err := buildGiftClipFFmpegArgs(request, giftClipEncoderSoftware)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, args...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("FFmpeg failed: %v\n%s", err, output)
	}
	data, err := os.ReadFile(request.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	duration, frames, err := inspectGiftClipSmokeMP4(data)
	if err != nil {
		t.Fatal(err)
	}
	if duration != request.Profile.Duration || int(frames) != request.Profile.Frames {
		t.Fatalf("output duration/frames = %s/%d, want %s/%d", duration, frames, request.Profile.Duration, request.Profile.Frames)
	}
}

func inspectGiftClipSmokeMP4(data []byte) (time.Duration, uint32, error) {
	mvhd := bytes.Index(data, []byte("mvhd"))
	stsz := bytes.Index(data, []byte("stsz"))
	if mvhd < 4 || mvhd+24 > len(data) || stsz < 4 || stsz+16 > len(data) {
		return 0, 0, errors.New("smoke MP4 metadata is incomplete")
	}
	if data[mvhd+4] != 0 {
		return 0, 0, errors.New("smoke MP4 uses unsupported mvhd version")
	}
	timescale := binary.BigEndian.Uint32(data[mvhd+16 : mvhd+20])
	duration := binary.BigEndian.Uint32(data[mvhd+20 : mvhd+24])
	if timescale == 0 {
		return 0, 0, errors.New("smoke MP4 timescale is zero")
	}
	frames := binary.BigEndian.Uint32(data[stsz+12 : stsz+16])
	return time.Duration(duration) * time.Second / time.Duration(timescale), frames, nil
}

func writeGiftClipSmokePNG(t *testing.T, path string, fill color.RGBA) {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for index := range canvas.Pix {
		canvas.Pix[index] = []byte{fill.R, fill.G, fill.B, fill.A}[index%4]
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, canvas); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustDecodeGiftClipSmokeBase64(t *testing.T, encoded string) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGiftClipPayloadDefaultCacheIsApplicationSpecific(t *testing.T) {
	root := defaultGiftClipCacheRoot()
	if !filepath.IsAbs(root) {
		t.Fatalf("cache root is not absolute: %q", root)
	}
	normalized := strings.ToLower(filepath.ToSlash(root))
	if !strings.Contains(normalized, "bilibililivegiftpanel/gift-clip/ffmpeg") {
		t.Fatalf("cache root is not application-specific: %q", root)
	}
}

type testZipEntry struct {
	name string
	body []byte
	mode os.FileMode
}

func newTestGiftClipPayload(t *testing.T, root string, binary []byte) *giftClipPayload {
	t.Helper()
	hash := sha256.Sum256(binary)
	return &giftClipPayload{
		Archive: testZipArchive(t, []testZipEntry{{name: "ffmpeg.exe", body: binary, mode: 0o755}}),
		Manifest: giftClipFFmpegManifest{
			Version: "8.1.2", SHA256: fmt.Sprintf("%x", hash), Size: int64(len(binary)),
		},
		CacheRoot: root,
	}
}

func testZipArchive(t *testing.T, entries []testZipEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
