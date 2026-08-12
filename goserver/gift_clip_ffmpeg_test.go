package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBuildGiftClipFFmpegArgsSelectsBoundedPlaybackInput(t *testing.T) {
	tests := []struct {
		name       string
		source     giftClipSource
		wantPrefix []string
		forbid     []string
	}{
		{
			name: "single GIF", source: giftClipSource{Kind: giftClipSourceGIF, Playback: giftClipPlaybackSingleGIF, Path: `C:\task\source.gif`, VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond},
			wantPrefix: []string{"-c:v", "gif", "-f", "image2", "-loop", "1", "-framerate", "30", "-i", `C:\task\source.gif`},
			forbid:     []string{"-stream_loop", "-ignore_loop"},
		},
		{
			name: "animated GIF", source: giftClipSource{Kind: giftClipSourceGIF, Playback: giftClipPlaybackAnimatedGIF, Path: `C:\task\source.gif`, VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond},
			wantPrefix: []string{"-f", "gif", "-ignore_loop", "0", "-i", `C:\task\source.gif`},
			forbid:     []string{"-stream_loop"},
		},
		{
			name: "static WebP", source: giftClipSource{Kind: giftClipSourceWebP, Playback: giftClipPlaybackStaticWebP, Path: `C:\task\source.webp`, VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond},
			wantPrefix: []string{"-stream_loop", "-1", "-f", "webp_pipe", "-i", `C:\task\source.webp`},
			forbid:     []string{"-ignore_loop"},
		},
		{
			name: "animated WebP", source: giftClipSource{Kind: giftClipSourceWebP, Playback: giftClipPlaybackAnimatedWebP, Path: `C:\task\source.webp`, VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond},
			wantPrefix: []string{"-f", "webp_anim", "-ignore_loop", "0", "-i", `C:\task\source.webp`},
			forbid:     []string{"-stream_loop"},
		},
		{
			name: "packed alpha effect", source: giftClipSource{Kind: giftClipSourceEffect, Playback: giftClipPlaybackEffect, Path: `C:\task\effect.mp4`, VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond,
				Layout: &giftEffectLayout{VideoWidth: 3840, VideoHeight: 1080, RGBFrame: [4]int{0, 0, 1920, 1080}, AlphaFrame: [4]int{1920, 0, 1920, 1080}, FPS: 24, Frames: 53}},
			wantPrefix: []string{"-i", `C:\task\effect.mp4`},
			forbid:     []string{"-stream_loop", "-ignore_loop"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args, err := buildGiftClipFFmpegArgs(giftClipEncodeFixture(test.source), giftClipEncoderHardware)
			if err != nil {
				t.Fatal(err)
			}
			if len(args) < len(test.wantPrefix) || !reflect.DeepEqual(args[:len(test.wantPrefix)], test.wantPrefix) {
				t.Fatalf("input args = %#v, want prefix %#v", args, test.wantPrefix)
			}
			for _, token := range test.forbid {
				if giftClipArgsContain(args, token) {
					t.Fatalf("input args unexpectedly contain %q: %#v", token, args)
				}
			}
			if giftClipArgsContainLoopFilter(args) {
				t.Fatalf("filter graph unexpectedly uses the loop filter: %#v", args)
			}
		})
	}
}

func TestBuildGiftClipFFmpegArgsRejectsInvalidSourcePlaybackMatrix(t *testing.T) {
	tests := []struct {
		name   string
		source giftClipSource
	}{
		{name: "GIF has no playback", source: giftClipSource{Kind: giftClipSourceGIF}},
		{name: "GIF uses WebP playback", source: giftClipSource{Kind: giftClipSourceGIF, Playback: giftClipPlaybackAnimatedWebP}},
		{name: "WebP uses GIF playback", source: giftClipSource{Kind: giftClipSourceWebP, Playback: giftClipPlaybackSingleGIF}},
		{name: "effect uses GIF playback", source: giftClipSource{Kind: giftClipSourceEffect, Playback: giftClipPlaybackAnimatedGIF}},
		{name: "unknown playback", source: giftClipSource{Kind: giftClipSourceGIF, Playback: giftClipPlaybackMode("other")}},
		{name: "unknown kind", source: giftClipSource{Kind: giftClipSourceKind("other"), Playback: giftClipPlaybackSingleGIF}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := test.source
			source.Path = `C:\task\source.gif`
			source.VisualWidth, source.VisualHeight, source.Duration = 1920, 1080, 2200*time.Millisecond
			if _, err := buildGiftClipFFmpegArgs(giftClipEncodeFixture(source), giftClipEncoderHardware); err == nil {
				t.Fatal("invalid source playback matrix was accepted")
			}
		})
	}
}

func TestBuildGiftClipFFmpegArgsReconstructsPackedAlphaBeforeUserCrop(t *testing.T) {
	request := giftClipEncodeFixture(giftClipSource{
		Kind: giftClipSourceEffect, Playback: giftClipPlaybackEffect, Path: `C:\task\effect.mp4`, VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond,
		Layout: &giftEffectLayout{
			VideoWidth: 3840, VideoHeight: 1080,
			RGBFrame: [4]int{0, 0, 1920, 1080}, AlphaFrame: [4]int{1920, 0, 1920, 1080}, FPS: 24, Frames: 53,
		},
	})
	args, err := buildGiftClipFFmpegArgs(request, giftClipEncoderSoftware)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"split=2", "crop=1920:1080:0:0,scale=1920:1080[rg]",
		"crop=1920:1080:1920:0,scale=1920:1080,format=gray[a]",
		"[rg][a]alphamerge,crop=960:540:101:53,setpts=PTS-STARTPTS,fps=30[anim]",
		"-hw_encoding 0",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "http://") || strings.Contains(joined, "https://") || strings.Contains(joined, " -map 0:a") {
		t.Fatalf("unsafe or audio argument in %s", joined)
	}
}

func TestBuildGiftClipFFmpegArgsRejectsUntrustedOrIncompleteInputs(t *testing.T) {
	request := giftClipEncodeFixture(giftClipSource{Kind: giftClipSourceGIF, Playback: giftClipPlaybackSingleGIF, Path: "https://example.test/source.gif", VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond})
	if _, err := buildGiftClipFFmpegArgs(request, giftClipEncoderHardware); err == nil {
		t.Fatal("remote source was accepted")
	}
	request = giftClipEncodeFixture(giftClipSource{Kind: giftClipSourceGIF, Playback: giftClipPlaybackSingleGIF, Path: `C:\task\source.gif`, VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond})
	request.BackgroundPath = ""
	if _, err := buildGiftClipFFmpegArgs(request, giftClipEncoderHardware); err == nil {
		t.Fatal("missing background was accepted")
	}
}

func TestBuildGiftClipFFmpegArgsRejectsNetworkPathsForEveryFile(t *testing.T) {
	request := giftClipEncodeFixture(giftClipSource{Kind: giftClipSourceGIF, Playback: giftClipPlaybackSingleGIF, Path: `C:\task\source.gif`, VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond})
	tests := []struct {
		name string
		set  func(*giftClipEncodeRequest)
	}{
		{name: "source UNC", set: func(request *giftClipEncodeRequest) { request.Source.Path = `\\server\share\source.gif` }},
		{name: "background slash UNC", set: func(request *giftClipEncodeRequest) { request.BackgroundPath = `//server/share/background.png` }},
		{name: "overlay UNC", set: func(request *giftClipEncodeRequest) { request.OverlayPath = `\\server\share\overlay.png` }},
		{name: "output slash UNC", set: func(request *giftClipEncodeRequest) { request.OutputPath = `//server/share/output.mp4` }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := request
			test.set(&candidate)
			if _, err := buildGiftClipFFmpegArgs(candidate, giftClipEncoderHardware); err == nil {
				t.Fatal("network path was accepted")
			}
		})
	}
}

func TestBuildGiftClipFFmpegArgsAcceptsPOSIXAbsolutePaths(t *testing.T) {
	request := giftClipEncodeFixture(giftClipSource{Kind: giftClipSourceGIF, Playback: giftClipPlaybackSingleGIF, Path: "/task/source.gif", VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond})
	request.BackgroundPath = "/task/background.png"
	request.OverlayPath = "/task/overlay.png"
	request.OutputPath = "/task/output.mp4"
	if _, err := buildGiftClipFFmpegArgs(request, giftClipEncoderHardware); err != nil {
		t.Fatalf("POSIX local paths were rejected: %v", err)
	}
}

func TestBuildGiftClipFFmpegArgsRejectsWindowsDeviceNamespaces(t *testing.T) {
	request := giftClipEncodeFixture(giftClipSource{Kind: giftClipSourceGIF, Playback: giftClipPlaybackSingleGIF, Path: `C:\task\source.gif`, VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond})
	tests := []struct {
		name string
		set  func(*giftClipEncodeRequest)
	}{
		{name: "source NT namespace", set: func(request *giftClipEncodeRequest) { request.Source.Path = `\??\C:\task\source.gif` }},
		{name: "background globalroot", set: func(request *giftClipEncodeRequest) {
			request.BackgroundPath = `\GLOBALROOT\Device\HarddiskVolume1\background.png`
		}},
		{name: "overlay device namespace", set: func(request *giftClipEncodeRequest) { request.OverlayPath = `\\.\C:\task\overlay.png` }},
		{name: "output extended namespace", set: func(request *giftClipEncodeRequest) { request.OutputPath = `\\?\C:\task\output.mp4` }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := request
			test.set(&candidate)
			if _, err := buildGiftClipFFmpegArgs(candidate, giftClipEncoderHardware); err == nil {
				t.Fatal("Windows device path was accepted")
			}
		})
	}
}

func TestBuildGiftClipFFmpegArgsRejectsWindowsRootRelativeAndDriveRelativePaths(t *testing.T) {
	request := giftClipEncodeFixture(giftClipSource{Kind: giftClipSourceGIF, Playback: giftClipPlaybackSingleGIF, Path: `C:\task\source.gif`, VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond})
	tests := []struct {
		name string
		set  func(*giftClipEncodeRequest)
	}{
		{name: "source root relative", set: func(request *giftClipEncodeRequest) { request.Source.Path = `\Windows\source.gif` }},
		{name: "background dos devices", set: func(request *giftClipEncodeRequest) { request.BackgroundPath = `\DosDevices\C:\background.png` }},
		{name: "overlay global namespace", set: func(request *giftClipEncodeRequest) { request.OverlayPath = `\GLOBAL??\overlay.png` }},
		{name: "output device namespace", set: func(request *giftClipEncodeRequest) { request.OutputPath = `\Device\HarddiskVolume1\output.mp4` }},
		{name: "source drive relative", set: func(request *giftClipEncodeRequest) { request.Source.Path = `C:source.gif` }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := request
			test.set(&candidate)
			if _, err := buildGiftClipFFmpegArgs(candidate, giftClipEncoderHardware); err == nil {
				t.Fatal("Windows non-absolute path was accepted")
			}
		})
	}
}

func TestBuildGiftClipFFmpegArgsRejectsUnknownEncoderMode(t *testing.T) {
	request := giftClipEncodeFixture(giftClipSource{Kind: giftClipSourceGIF, Playback: giftClipPlaybackSingleGIF, Path: `C:\task\source.gif`, VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond})
	if _, err := buildGiftClipFFmpegArgs(request, giftClipEncoderMode("other")); err == nil {
		t.Fatal("unknown encoder mode was accepted")
	}
}

func TestGiftClipProgressParserIsMonotonicAndClamped(t *testing.T) {
	parser := newGiftClipProgressParser(2 * time.Second)
	got := []float64{}
	for _, line := range []string{"out_time_us=500000", "out_time_us=400000", "out_time_us=2500000", "progress=end"} {
		if value, ok := parser.Consume(line); ok {
			got = append(got, value)
		}
	}
	if !reflect.DeepEqual(got, []float64{0.25, 0.25, 1, 1}) {
		t.Fatalf("progress = %#v", got)
	}
}

func TestGiftClipFFmpegEncoderOptionsCanForceSoftware(t *testing.T) {
	runner := &fakeGiftClipRunner{}
	encoder := newGiftClipFFmpegEncoder(testGiftClipPayload(t), runner, nil, giftClipFFmpegEncoderOptions{ForceSoftware: true})
	if err := encoder.Encode(context.Background(), giftClipEncodeFixture(testGiftClipSource()), nil); err != nil {
		t.Fatal(err)
	}
	if got := runner.hardwareFlags(); !reflect.DeepEqual(got, []string{"0"}) {
		t.Fatalf("flags = %#v", got)
	}
}

type fakeRunResult struct {
	stdout string
	stderr string
	err    error
}

type fakeGiftClipRunner struct {
	mu      sync.Mutex
	results []fakeRunResult
	args    [][]string
}

func (runner *fakeGiftClipRunner) Run(_ context.Context, _ string, args []string, stdout, stderr io.Writer) error {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.args = append(runner.args, append([]string(nil), args...))
	result := fakeRunResult{}
	if index := len(runner.args) - 1; index < len(runner.results) {
		result = runner.results[index]
	}
	_, _ = io.WriteString(stdout, result.stdout)
	_, _ = io.WriteString(stderr, result.stderr)
	return result.err
}

func (runner *fakeGiftClipRunner) hardwareFlags() []string {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	flags := make([]string, 0, len(runner.args))
	for _, args := range runner.args {
		for index := range args {
			if args[index] == "-hw_encoding" && index+1 < len(args) {
				flags = append(flags, args[index+1])
				break
			}
		}
	}
	return flags
}

func TestGiftClipEncoderRetriesSoftwareOnceAndCachesTheDecision(t *testing.T) {
	if giftClipDefaultEncoderMode != giftClipEncoderHardware {
		t.Skip("hardware fallback is a Windows-only default")
	}
	runner := &fakeGiftClipRunner{results: []fakeRunResult{{stderr: "Error initializing h264_mf hardware encoder", err: errors.New("exit 1")}, {}, {}}}
	encoder := newGiftClipFFmpegEncoder(testGiftClipPayload(t), runner, nil, giftClipFFmpegEncoderOptions{})
	updates := []giftClipEncodingUpdate{}
	if err := encoder.Encode(context.Background(), giftClipEncodeFixture(testGiftClipSource()), func(update giftClipEncodingUpdate) { updates = append(updates, update) }); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(context.Background(), giftClipEncodeFixture(testGiftClipSource()), nil); err != nil {
		t.Fatal(err)
	}
	if got := runner.hardwareFlags(); !reflect.DeepEqual(got, []string{"1", "0", "0"}) {
		t.Fatalf("flags = %#v", got)
	}
	if !slices.ContainsFunc(updates, func(update giftClipEncodingUpdate) bool {
		return update.Retrying && update.Mode == giftClipEncoderSoftware
	}) {
		t.Fatalf("updates = %#v", updates)
	}
}

func TestGiftClipEncoderDoesNotRetryCanceledOrInputFailures(t *testing.T) {
	for _, test := range []struct {
		name, stderr string
		err          error
	}{
		{name: "canceled", err: context.Canceled},
		{name: "no space", stderr: "No space left on device", err: errors.New("exit 1")},
		{name: "bad input", stderr: "Invalid data found when processing input", err: errors.New("exit 1")},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeGiftClipRunner{results: []fakeRunResult{{stderr: test.stderr, err: test.err}}}
			encoder := newGiftClipFFmpegEncoder(testGiftClipPayload(t), runner, nil, giftClipFFmpegEncoderOptions{})
			if err := encoder.Encode(context.Background(), giftClipEncodeFixture(testGiftClipSource()), nil); !errors.Is(err, test.err) {
				t.Fatalf("Encode error = %v, want %v", err, test.err)
			}
			if got := len(runner.hardwareFlags()); got != 1 {
				t.Fatalf("runs = %d, want 1", got)
			}
		})
	}
}

func TestGiftClipEncoderDoesNotTryAThirdTimeWhenSoftwareFails(t *testing.T) {
	if giftClipDefaultEncoderMode != giftClipEncoderHardware {
		t.Skip("hardware fallback is a Windows-only default")
	}
	runner := &fakeGiftClipRunner{results: []fakeRunResult{
		{stderr: "Error initializing h264_mf hardware encoder", err: errors.New("hardware exit 1")},
		{stderr: "software encoder failed", err: errors.New("software exit 1")},
	}}
	encoder := newGiftClipFFmpegEncoder(testGiftClipPayload(t), runner, nil, giftClipFFmpegEncoderOptions{})
	if err := encoder.Encode(context.Background(), giftClipEncodeFixture(testGiftClipSource()), nil); err == nil {
		t.Fatal("Encode succeeded")
	}
	if got := runner.hardwareFlags(); !reflect.DeepEqual(got, []string{"1", "0"}) {
		t.Fatalf("flags = %#v", got)
	}
}

func TestGiftClipEncoderPublishesMonotonicStdoutProgress(t *testing.T) {
	runner := &fakeGiftClipRunner{results: []fakeRunResult{{stdout: "out_time_us=500000\nout_time_us=400000\nout_time_us=2500000\nprogress=end\n"}}}
	encoder := newGiftClipFFmpegEncoder(testGiftClipPayload(t), runner, nil, giftClipFFmpegEncoderOptions{})
	updates := []giftClipEncodingUpdate{}
	if err := encoder.Encode(context.Background(), giftClipEncodeFixture(testGiftClipSource()), func(update giftClipEncodingUpdate) { updates = append(updates, update) }); err != nil {
		t.Fatal(err)
	}
	progress := make([]float64, 0, len(updates))
	for _, update := range updates {
		progress = append(progress, update.Progress)
	}
	if !reflect.DeepEqual(progress, []float64{5.0 / 22, 5.0 / 22, 1, 1}) {
		t.Fatalf("progress = %#v", progress)
	}
}

func TestGiftClipEncoderTruncatesAndSanitizesDiagnosticStderr(t *testing.T) {
	logger, err := newDiagnosticLogger(t.TempDir() + "/runtime.log")
	if err != nil {
		t.Fatal(err)
	}
	stderr := append(bytes.Repeat([]byte("x"), 40*1024), []byte(" C:\\private\\source.gif")...)
	runner := &fakeGiftClipRunner{results: []fakeRunResult{{stderr: string(stderr), err: errors.New("exit 1")}}}
	encoder := newGiftClipFFmpegEncoder(testGiftClipPayload(t), runner, logger, giftClipFFmpegEncoderOptions{})
	_ = encoder.Encode(context.Background(), giftClipEncodeFixture(testGiftClipSource()), nil)
	data := logger.exportBytes()
	if bytes.Contains(data, []byte(`C:\private\source.gif`)) || len(data) > 2*1024 {
		t.Fatalf("diagnostic leaked path or excessive stderr: %d bytes", len(data))
	}
}

func testGiftClipPayload(t *testing.T) *giftClipPayload {
	t.Helper()
	return newTestGiftClipPayload(t, t.TempDir(), []byte("MZ test ffmpeg"))
}

func testGiftClipSource() giftClipSource {
	return giftClipSource{Kind: giftClipSourceGIF, Playback: giftClipPlaybackSingleGIF, Path: `C:\task\source.gif`, VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond}
}

func TestShouldRetryGiftClipSoftwareClassifiesHardwareFailures(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		stderr string
		want   bool
	}{
		{name: "h264 mf failure first", err: errors.New("exit status 1"), stderr: "Error initializing h264_mf hardware encoder", want: true},
		{name: "h264 mf context first", err: errors.New("exit status 1"), stderr: "h264_mf encoder failed to initialize", want: true},
		{name: "h264 mf MFT creation", err: errors.New("exit status 1"), stderr: "[h264_mf @ 000001] Failed to create MFT encoder", want: true},
		{name: "media foundation initialization signal first", err: errors.New("exit status 1"), stderr: "Could not initialize Media Foundation encoder", want: true},
		{name: "media foundation transform context first", err: errors.New("exit status 1"), stderr: "Media Foundation transform failed to start", want: true},
		{name: "media foundation unavailable context first", err: errors.New("exit status 1"), stderr: "Media Foundation MFT is unavailable", want: true},
		{name: "MFT context first", err: errors.New("exit status 1"), stderr: "MFT encoder failed to create", want: true},
		{name: "MFT failure first", err: errors.New("exit status 1"), stderr: "Unable to create MFT encoder", want: true},
		{name: "MF context first", err: errors.New("exit status 1"), stderr: "MF failed to initialize encoder", want: true},
		{name: "MF failure first", err: errors.New("exit status 1"), stderr: "Unable to initialize encoder in MF", want: true},
		{name: "canonical ffmpeg opening encoder", err: errors.New("exit status 1"), stderr: "[h264_mf @ 000001] Error while opening encoder for output stream #0:0", want: true},
		{name: "hardware encoder start failure", err: errors.New("exit status 1"), stderr: "Hardware encoder failed to start", want: true},
		{name: "hardware encoder failure first", err: errors.New("exit status 1"), stderr: "Unable to start hardware encoder", want: true},
		{name: "gpu device unavailable", err: errors.New("exit status 1"), stderr: "GPU device unavailable for hardware encoder", want: true},
		{name: "gpu device failure first", err: errors.New("exit status 1"), stderr: "Unable to open GPU device for hardware encoding", want: true},
		{name: "media foundation input type initialization", err: errors.New("exit status 1"), stderr: "Media Foundation MFT failed to initialize input type", want: true},
		{name: "MF input pin context first", err: errors.New("exit status 1"), stderr: "MF input pin failed to initialize", want: true},
		{name: "MF input pin failure first", err: errors.New("exit status 1"), stderr: "Could not initialize MF input pin", want: true},
		{name: "media foundation device failure", err: errors.New("exit status 1"), stderr: "Media Foundation encoder failed after DXGI_ERROR_DEVICE_REMOVED", want: true},
		{name: "generic exit", err: errors.New("exit status 1"), stderr: "conversion failed", want: false},
		{name: "h264 mf selected", err: errors.New("exit status 1"), stderr: "h264_mf selected", want: false},
		{name: "h264 mf and unrelated conversion error", err: errors.New("exit status 1"), stderr: "h264_mf selected; conversion error", want: false},
		{name: "technology and failure on separate lines", err: errors.New("exit status 1"), stderr: "h264_mf selected\nConversion failed", want: false},
		{name: "technology and filter failure in separate clauses", err: errors.New("exit status 1"), stderr: "Media Foundation available; filter graph failed to initialize", want: false},
		{name: "failure and technology in separate clauses", err: errors.New("exit status 1"), stderr: "Could not initialize filter graph; hardware encoder selected", want: false},
		{name: "technology next to unrelated initialization", err: errors.New("exit status 1"), stderr: "h264_mf selected while filter graph failed to initialize", want: false},
		{name: "h264 mf filter target", err: errors.New("exit status 1"), stderr: "h264_mf failed to initialize filter graph", want: false},
		{name: "media foundation decoder target", err: errors.New("exit status 1"), stderr: "Media Foundation failed to open decoder", want: false},
		{name: "MFT muxer target", err: errors.New("exit status 1"), stderr: "MFT failed to create muxer", want: false},
		{name: "MF conversion target", err: errors.New("exit status 1"), stderr: "MF failed to initialize conversion", want: false},
		{name: "hardware encoder filter target", err: errors.New("exit status 1"), stderr: "Unable to open filter graph for hardware encoder", want: false},
		{name: "standalone MF word boundary", err: errors.New("exit status 1"), stderr: "format failed to initialize encoder", want: false},
		{name: "media foundation selected", err: errors.New("exit status 1"), stderr: "Media Foundation selected", want: false},
		{name: "device lost without encoder context", err: errors.New("exit status 1"), stderr: "device lost", want: false},
		{name: "output failure mentioning h264 mf", err: errors.New("exit status 1"), stderr: "h264_mf selected; output is not writable", want: false},
		{name: "input failure mentioning media foundation", err: errors.New("exit status 1"), stderr: "Media Foundation selected; failed to decode input", want: false},
		{name: "mux failure mentioning hardware encoder", err: errors.New("exit status 1"), stderr: "hardware encoder selected; Error muxing a packet", want: false},
		{name: "canceled", err: context.Canceled, stderr: "Error while opening encoder", want: false},
		{name: "disk full", err: errors.New("exit status 1"), stderr: "No space left on device", want: false},
		{name: "invalid input", err: errors.New("exit status 1"), stderr: "Invalid data found when processing input", want: false},
		{name: "permission denied", err: errors.New("exit status 1"), stderr: "Permission denied", want: false},
		{name: "missing output directory", err: errors.New("exit status 1"), stderr: "No such file or directory", want: false},
		{name: "unreadable input", err: errors.New("exit status 1"), stderr: "Error opening input: Permission denied", want: false},
		{name: "muxer failure", err: errors.New("exit status 1"), stderr: "Could not write header for output file", want: false},
		{name: "write failure", err: errors.New("exit status 1"), stderr: "Error muxing a packet", want: false},
		{name: "payload integrity", err: errGiftClipPayloadIntegrity, stderr: "Error while opening encoder", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldRetryGiftClipSoftware(test.err, test.stderr); got != test.want {
				t.Fatalf("shouldRetryGiftClipSoftware(%v, %q) = %t, want %t", test.err, test.stderr, got, test.want)
			}
		})
	}
}

func giftClipArgsContain(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func giftClipArgsContainLoopFilter(args []string) bool {
	for index, arg := range args {
		if arg != "-filter_complex" || index+1 == len(args) {
			continue
		}
		for _, filter := range strings.FieldsFunc(args[index+1], func(r rune) bool { return r == ',' || r == ';' }) {
			if strings.HasPrefix(filter, "loop=") {
				return true
			}
		}
	}
	return false
}

func giftClipEncodeFixture(source giftClipSource) giftClipEncodeRequest {
	return giftClipEncodeRequest{
		Source: source,
		Crop:   giftClipCrop{X: 101, Y: 53, Width: 960, Height: 540},
		Profile: giftClipOutputProfile{
			Width: 960, Height: 540, FPS: 30, Frames: 66, Duration: 2200 * time.Millisecond,
			AverageBitrate: 500_000, PeakBitrate: 750_000, VBVBuffer: 1_000_000,
		},
		BackgroundPath: `C:\task\background.png`,
		OverlayPath:    `C:\task\overlay.png`,
		OutputPath:     `C:\task\output.mp4`,
	}
}
