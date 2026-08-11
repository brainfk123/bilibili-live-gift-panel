package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBuildGiftClipFFmpegArgsCreatesDeterministicShortAnimationTimeline(t *testing.T) {
	request := giftClipEncodeFixture(giftClipSource{
		Kind: giftClipSourceWebP, Path: `C:\task\source.webp`, VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond,
	})
	args, err := buildGiftClipFFmpegArgs(request, giftClipEncoderHardware)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-stream_loop -1", "-ignore_loop 1", "-f webp", "crop=960:540:101:53", "fps=30",
		"-c:v h264_mf", "-hw_encoding 1", "-rate_control pc_vbr",
		"-b:v 500000", "-maxrate 750000", "-bufsize 1000000",
		"-pix_fmt nv12", "-fps_mode cfr", "-movflags +faststart", "-progress pipe:1",
		"-an", "-map [out]", "-t 2.2",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "http://") || strings.Contains(joined, "https://") || strings.Contains(joined, " -map 0:a") || strings.Contains(joined, "\"'") {
		t.Fatalf("unsafe or audio argument in %s", joined)
	}
}

func TestBuildGiftClipFFmpegArgsReconstructsPackedAlphaBeforeUserCrop(t *testing.T) {
	request := giftClipEncodeFixture(giftClipSource{
		Kind: giftClipSourceEffect, Path: `C:\task\effect.mp4`, VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond,
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
	request := giftClipEncodeFixture(giftClipSource{Kind: giftClipSourceGIF, Path: "https://example.test/source.gif", VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond})
	if _, err := buildGiftClipFFmpegArgs(request, giftClipEncoderHardware); err == nil {
		t.Fatal("remote source was accepted")
	}
	request = giftClipEncodeFixture(giftClipSource{Kind: giftClipSourceGIF, Path: `C:\task\source.gif`, VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond})
	request.BackgroundPath = ""
	if _, err := buildGiftClipFFmpegArgs(request, giftClipEncoderHardware); err == nil {
		t.Fatal("missing background was accepted")
	}
}

func TestBuildGiftClipFFmpegArgsRejectsNetworkPathsForEveryFile(t *testing.T) {
	request := giftClipEncodeFixture(giftClipSource{Kind: giftClipSourceGIF, Path: `C:\task\source.gif`, VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond})
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
	request := giftClipEncodeFixture(giftClipSource{Kind: giftClipSourceGIF, Path: "/task/source.gif", VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond})
	request.BackgroundPath = "/task/background.png"
	request.OverlayPath = "/task/overlay.png"
	request.OutputPath = "/task/output.mp4"
	if _, err := buildGiftClipFFmpegArgs(request, giftClipEncoderHardware); err != nil {
		t.Fatalf("POSIX local paths were rejected: %v", err)
	}
}

func TestBuildGiftClipFFmpegArgsRejectsWindowsDeviceNamespaces(t *testing.T) {
	request := giftClipEncodeFixture(giftClipSource{Kind: giftClipSourceGIF, Path: `C:\task\source.gif`, VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond})
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

func TestBuildGiftClipFFmpegArgsRejectsUnknownEncoderMode(t *testing.T) {
	request := giftClipEncodeFixture(giftClipSource{Kind: giftClipSourceGIF, Path: `C:\task\source.gif`, VisualWidth: 1920, VisualHeight: 1080, Duration: 2200 * time.Millisecond})
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

func TestShouldRetryGiftClipSoftwareClassifiesHardwareFailures(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		stderr string
		want   bool
	}{
		{name: "h264 mf initialization", err: errors.New("exit status 1"), stderr: "Error initializing h264_mf hardware encoder", want: true},
		{name: "media foundation device failure", err: errors.New("exit status 1"), stderr: "Media Foundation encoder failed after DXGI_ERROR_DEVICE_REMOVED", want: true},
		{name: "generic exit", err: errors.New("exit status 1"), stderr: "conversion failed", want: false},
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
