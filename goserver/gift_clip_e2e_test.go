package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	giftClipHarnessGIFReceiptID    = "gift-clip-e2e-gif"
	giftClipHarnessWebPReceiptID   = "gift-clip-e2e-webp"
	giftClipHarnessEffectReceiptID = "gift-clip-e2e-effect"
)

type giftClipFixtureMedia struct {
	contentType string
	data        []byte
}

type giftClipFixtureResolver struct {
	sources map[string]giftClipSource
	media   map[string]map[string]giftClipFixtureMedia
}

type giftClipHarnessJobs struct{ *giftClipJobManager }

type giftClipHarnessResponseWriter struct {
	http.ResponseWriter
	status int
}

func (writer *giftClipHarnessResponseWriter) WriteHeader(status int) {
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (jobs *giftClipHarnessJobs) Create(ctx context.Context, receiptID string, crop giftClipCrop, background, overlay []byte) (giftClipJobSnapshot, error) {
	snapshot, err := jobs.giftClipJobManager.Create(ctx, receiptID, crop, background, overlay)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gift clip harness create failed: receipt=%q crop=%+v background=%d overlay=%d: %v\n", receiptID, crop, len(background), len(overlay), err)
	}
	return snapshot, err
}

func (resolver *giftClipFixtureResolver) Resolve(ctx context.Context, receiptID, _ string) (giftClipSource, error) {
	if err := ctx.Err(); err != nil {
		return giftClipSource{}, err
	}
	if resolver == nil {
		return giftClipSource{}, errors.New("fixture gift receipt does not exist")
	}
	source, ok := resolver.sources[receiptID]
	if !ok {
		return giftClipSource{}, errors.New("fixture gift receipt does not exist")
	}
	return source, nil
}

func TestGiftClipE2E(t *testing.T) {
	if os.Getenv("GIFT_CLIP_FFMPEG_E2E") != "1" {
		t.Skip("set GIFT_CLIP_FFMPEG_E2E=1 to run the embedded FFmpeg E2E")
	}
	if runtime.GOOS != "windows" {
		t.Skip("the embedded h264_mf payload is a Windows executable")
	}
	ffprobe := requireGiftClipE2ETool(t, "FFPROBE_BIN")
	fullFFmpeg := giftClipE2EFullFFmpeg(t, ffprobe)
	repositoryRoot := giftClipE2ERepositoryRoot(t)
	fixtureRoot := filepath.Join(repositoryRoot, "tests", "fixtures", "gift-clip-media")

	payload, err := embeddedGiftClipPayload(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	encoder := newGiftClipFFmpegEncoder(payload, newGiftClipProcessRunner(), nil, giftClipFFmpegEncoderOptions{ForceSoftware: true})

	layout := readGiftClipFixtureLayout(t, filepath.Join(fixtureRoot, "packed-alpha-layout.json"))
	tests := []struct {
		name     string
		filename string
		fps      int
		source   func(string) giftClipSource
	}{
		{
			name: "gif-10fps", filename: "input-10fps.gif", fps: 10,
			source: func(path string) giftClipSource {
				return giftClipSource{Kind: giftClipSourceGIF, Playback: giftClipPlaybackAnimatedGIF, Path: path, VisualWidth: 320, VisualHeight: 180, Duration: 2 * time.Second}
			},
		},
		{
			name: "webp-20fps", filename: "input-20fps.webp", fps: 20,
			source: func(path string) giftClipSource {
				return giftClipSource{Kind: giftClipSourceWebP, Playback: giftClipPlaybackAnimatedWebP, Path: path, VisualWidth: 320, VisualHeight: 180, Duration: 2 * time.Second}
			},
		},
		{
			name: "packed-alpha-24fps", filename: "packed-alpha-24fps.mp4", fps: 24,
			source: func(path string) giftClipSource {
				return giftClipSource{Kind: giftClipSourceEffect, Playback: giftClipPlaybackEffect, Path: path, VisualWidth: 320, VisualHeight: 180, Duration: 2 * time.Second, Layout: &layout}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			background := filepath.Join(directory, "background.png")
			overlay := filepath.Join(directory, "overlay.png")
			writeGiftClipE2EPNG(t, background, 320, 180, color.NRGBA{R: 18, G: 24, B: 38, A: 255})
			writeGiftClipE2EPNG(t, overlay, 320, 180, color.NRGBA{})
			output := filepath.Join(directory, test.name+".mp4")
			sourcePath, err := filepath.Abs(filepath.Join(fixtureRoot, test.filename))
			if err != nil {
				t.Fatal(err)
			}
			source := test.source(sourcePath)
			crop := giftClipCrop{Width: 320, Height: 180}
			profile, err := newGiftClipOutputProfile(crop, source.VisualWidth, source.VisualHeight, source.Duration)
			if err != nil {
				t.Fatal(err)
			}
			request := giftClipEncodeRequest{
				Source: source, Crop: crop, Profile: profile,
				BackgroundPath: background, OverlayPath: overlay, OutputPath: output,
			}
			args, err := buildGiftClipFFmpegArgs(request, giftClipEncoderSoftware)
			if err != nil {
				t.Fatal(err)
			}
			assertGiftClipE2EBitrateArgs(t, args, profile)
			t.Logf("software FFmpeg args: %q", args)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			if err := encoder.Encode(ctx, request, nil); err != nil {
				t.Fatalf("encode real %s fixture: %v", test.name, err)
			}
			preserveGiftClipE2EOutput(t, output, test.name+".mp4")
			assertGiftClipE2EProbe(t, ffprobe, output, profile)
			frames := giftClipE2EFrameMD5(t, fullFFmpeg, output)
			quantizedFrames := giftClipE2EQuantizedFrameMD5(t, fullFFmpeg, output)
			fingerprints := giftClipE2EFrameFingerprints(t, fullFFmpeg, output)
			assertGiftClipTimestampSampling(t, frames, fingerprints, test.fps, profile.FPS, profile.Frames)
			if err := validateGiftClipFrameMD5TimestampPattern(quantizedFrames, test.fps, profile.FPS); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGiftClipHarnessServer(t *testing.T) {
	portFile := strings.TrimSpace(os.Getenv("GIFT_CLIP_HARNESS_PORT_FILE"))
	if portFile == "" {
		t.Skip("set GIFT_CLIP_HARNESS_PORT_FILE to run the browser harness")
	}
	if runtime.GOOS != "windows" {
		t.Skip("the embedded h264_mf payload is a Windows executable")
	}
	stopFile := strings.TrimSpace(os.Getenv("GIFT_CLIP_HARNESS_STOP_FILE"))
	if stopFile == "" {
		t.Fatal("GIFT_CLIP_HARNESS_STOP_FILE is required with GIFT_CLIP_HARNESS_PORT_FILE")
	}
	repositoryRoot := giftClipE2ERepositoryRoot(t)
	resolver := newGiftClipFixtureResolver(t, repositoryRoot)
	payload, err := embeddedGiftClipPayload(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	encoder := newGiftClipFFmpegEncoder(payload, newGiftClipProcessRunner(), nil, giftClipFFmpegEncoderOptions{ForceSoftware: true})
	server, listener, err := newGiftClipHarnessServer(t.TempDir(), resolver, encoder)
	if err != nil {
		t.Fatal(err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Errorf("shutdown gift clip harness: %v", err)
		}
		if err := <-serveResult; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve gift clip harness: %v", err)
		}
	}()

	writeGiftClipHarnessPortFile(t, portFile, "http://"+listener.Addr().String())
	deadline := time.Now().Add(10 * time.Minute)
	for {
		if _, err := os.Stat(stopFile); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("inspect harness stop file: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for browser harness stop file")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func newGiftClipFixtureResolver(t *testing.T, repositoryRoot string) *giftClipFixtureResolver {
	t.Helper()
	fixtureRoot := filepath.Join(repositoryRoot, "tests", "fixtures", "gift-clip-media")
	layoutData := readGiftClipE2EFile(t, filepath.Join(fixtureRoot, "packed-alpha-layout.json"))
	var layout giftEffectLayout
	if err := json.Unmarshal(layoutData, &layout); err != nil {
		t.Fatalf("decode fixture layout: %v", err)
	}
	gifPath, err := filepath.Abs(filepath.Join(fixtureRoot, "input-10fps.gif"))
	if err != nil {
		t.Fatal(err)
	}
	webpPath, err := filepath.Abs(filepath.Join(fixtureRoot, "input-20fps.webp"))
	if err != nil {
		t.Fatal(err)
	}
	effectPath, err := filepath.Abs(filepath.Join(fixtureRoot, "packed-alpha-24fps.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	avatar := giftClipFixtureMedia{contentType: "image/png", data: giftClipE2EPNG(t, 96, 96, color.NRGBA{R: 251, G: 114, B: 153, A: 255})}
	gif := giftClipFixtureMedia{contentType: "image/gif", data: readGiftClipE2EFile(t, gifPath)}
	webp := giftClipFixtureMedia{contentType: "image/webp", data: readGiftClipE2EFile(t, webpPath)}
	return &giftClipFixtureResolver{
		sources: map[string]giftClipSource{
			giftClipHarnessGIFReceiptID: {
				Kind: giftClipSourceGIF, Playback: giftClipPlaybackAnimatedGIF, Path: gifPath,
				VisualWidth: 320, VisualHeight: 180, Duration: 2 * time.Second,
			},
			giftClipHarnessWebPReceiptID: {
				Kind: giftClipSourceWebP, Playback: giftClipPlaybackAnimatedWebP, Path: webpPath,
				VisualWidth: 320, VisualHeight: 180, Duration: 2 * time.Second,
			},
			giftClipHarnessEffectReceiptID: {
				Kind: giftClipSourceEffect, Playback: giftClipPlaybackEffect, Path: effectPath,
				VisualWidth: 320, VisualHeight: 180, Duration: 2 * time.Second, Layout: &layout,
			},
		},
		media: map[string]map[string]giftClipFixtureMedia{
			giftClipHarnessGIFReceiptID:  {"animation": gif, "avatar": avatar},
			giftClipHarnessWebPReceiptID: {"animation": webp, "avatar": avatar},
			giftClipHarnessEffectReceiptID: {
				"animation": gif, "avatar": avatar,
				"effect-video":  {contentType: "video/mp4", data: readGiftClipE2EFile(t, effectPath)},
				"effect-layout": {contentType: "application/json", data: layoutData},
			},
		},
	}
}

func newGiftClipHarnessServer(root string, resolver giftClipSourceResolver, encoder giftClipEncoder) (*http.Server, net.Listener, error) {
	root = strings.TrimSpace(root)
	if root == "" || resolver == nil || encoder == nil {
		return nil, nil, errors.New("gift clip harness dependencies are incomplete")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve gift clip harness root: %w", err)
	}
	fixtureResolver, ok := resolver.(*giftClipFixtureResolver)
	if !ok || fixtureResolver == nil {
		return nil, nil, errors.New("gift clip harness requires the fixture resolver")
	}
	listener, err := listenGiftClipHarness()
	if err != nil {
		return nil, nil, err
	}

	manager := newGiftClipJobManager(absoluteRoot, resolver, encoder, nil)
	clipHandler := newGiftClipHTTPHandler(&giftClipHarnessJobs{giftClipJobManager: manager})
	mux := http.NewServeMux()
	loggedClipHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writer := &giftClipHarnessResponseWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if recovered := recover(); recovered != nil {
				fmt.Fprintf(os.Stderr, "gift clip harness panic: method=%s path=%s content_length=%d panic=%v\n", r.Method, r.URL.Path, r.ContentLength, recovered)
				panic(recovered)
			}
			fmt.Fprintf(os.Stderr, "gift clip harness response: method=%s path=%s content_length=%d status=%d\n", r.Method, r.URL.Path, r.ContentLength, writer.status)
		}()
		clipHandler.ServeHTTP(writer, r)
	})
	mux.Handle("/api/gift-clips", loggedClipHandler)
	mux.Handle("/api/gift-clips/", loggedClipHandler)
	mux.Handle("/api/gift-receipts/media", http.HandlerFunc(fixtureResolver.serveMedia))
	if uiDirectory := strings.TrimSpace(os.Getenv("GIFT_CLIP_HARNESS_UI_DIR")); uiDirectory != "" {
		uiDirectory, err = filepath.Abs(uiDirectory)
		if err != nil {
			_ = listener.Close()
			manager.Close()
			return nil, nil, fmt.Errorf("resolve packaged UI directory: %w", err)
		}
		if info, statErr := os.Stat(filepath.Join(uiDirectory, "ui-assets.json")); statErr != nil || !info.Mode().IsRegular() {
			_ = listener.Close()
			manager.Close()
			return nil, nil, errors.New("gift clip harness packaged UI manifest is missing")
		}
		mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("/api/runtime", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"code": 0, "runtime": map[string]any{"state": "idle", "roomId": ""}})
		})
		mux.HandleFunc("/api/auth/status", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"code": 0, "auth": map[string]any{"state": "anonymous"}})
		})
		mux.HandleFunc("/api/changelog", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"code": 0, "releases": []any{}})
		})
		mux.HandleFunc("/api/update", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{
				"code": 0,
				"update": map[string]any{
					"state": "development", "currentVersion": "dev", "message": "E2E harness",
					"autoUpdate": false, "restartRequired": false,
				},
			})
		})
		mux.HandleFunc("/api/pages/presence/stream", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-store")
			if flusher, ok := w.(http.Flusher); ok {
				_, _ = io.WriteString(w, ": connected\n\n")
				flusher.Flush()
			}
			<-r.Context().Done()
		})
		mux.Handle("/", newEmbeddedPageHandler(os.DirFS(uiDirectory)))
	}
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	server.RegisterOnShutdown(manager.Close)
	return server, listener, nil
}

func listenGiftClipHarness() (net.Listener, error) {
	for attempt := 0; attempt < 32; attempt++ {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("listen for gift clip harness: %w", err)
		}
		tcpAddress, ok := listener.Addr().(*net.TCPAddr)
		if !ok || !tcpAddress.IP.IsLoopback() || tcpAddress.Port == 0 {
			_ = listener.Close()
			return nil, errors.New("gift clip harness did not receive a dynamic loopback address")
		}
		if tcpAddress.Port < 12450 || tcpAddress.Port > 12459 {
			return listener, nil
		}
		_ = listener.Close()
	}
	return nil, errors.New("gift clip harness could not reserve a dynamic port outside 12450-12459")
}

func (resolver *giftClipFixtureResolver) serveMedia(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	query := request.URL.Query()
	mediaByKind, ok := resolver.media[query.Get("id")]
	if !ok {
		http.NotFound(w, request)
		return
	}
	kind := query.Get("kind")
	media, ok := mediaByKind[kind]
	if !ok {
		http.NotFound(w, request)
		return
	}
	w.Header().Set("Content-Type", media.contentType)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, request, "gift-clip-fixture-"+kind, time.Unix(0, 0), bytes.NewReader(media.data))
}

func readGiftClipFixtureLayout(t *testing.T, path string) giftEffectLayout {
	t.Helper()
	var layout giftEffectLayout
	if err := json.Unmarshal(readGiftClipE2EFile(t, path), &layout); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if layout != (giftEffectLayout{VideoWidth: 640, VideoHeight: 180, RGBFrame: [4]int{0, 0, 320, 180}, AlphaFrame: [4]int{320, 0, 320, 180}, FPS: 24, Frames: 48}) {
		t.Fatalf("unexpected packed-alpha fixture layout: %#v", layout)
	}
	return layout
}

func giftClipE2ERepositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "package.json")); err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	return root
}

func requireGiftClipE2ETool(t *testing.T, environmentName string) string {
	t.Helper()
	path := strings.TrimSpace(os.Getenv(environmentName))
	if path == "" {
		t.Fatalf("%s must point to the required local executable", environmentName)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(absolute); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("%s is not a regular executable: %s", environmentName, absolute)
	}
	return absolute
}

func giftClipE2EFullFFmpeg(t *testing.T, ffprobe string) string {
	t.Helper()
	candidates := []string{strings.TrimSpace(os.Getenv("FFMPEG_FULL_BIN")), filepath.Join(filepath.Dir(ffprobe), "ffmpeg.exe"), `D:\Program Files\ffmpeg\bin\ffmpeg.exe`}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	t.Fatal("FFMPEG_FULL_BIN or an ffmpeg.exe beside FFPROBE_BIN is required for framemd5")
	return ""
}

func writeGiftClipE2EPNG(t *testing.T, path string, width, height int, fill color.NRGBA) {
	t.Helper()
	if err := os.WriteFile(path, giftClipE2EPNG(t, width, height, fill), 0o600); err != nil {
		t.Fatal(err)
	}
}

func giftClipE2EPNG(t *testing.T, width, height int, fill color.NRGBA) []byte {
	t.Helper()
	frame := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			frame.SetNRGBA(x, y, fill)
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, frame); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

type giftClipE2EProbe struct {
	Streams []struct {
		CodecType   string `json:"codec_type"`
		CodecName   string `json:"codec_name"`
		PixelFormat string `json:"pix_fmt"`
		Width       int    `json:"width"`
		Height      int    `json:"height"`
		AverageRate string `json:"avg_frame_rate"`
		Duration    string `json:"duration"`
		Bitrate     string `json:"bit_rate"`
		FrameCount  string `json:"nb_frames"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
		Size     string `json:"size"`
		Bitrate  string `json:"bit_rate"`
	} `json:"format"`
}

func assertGiftClipE2EProbe(t *testing.T, ffprobe, output string, profile giftClipOutputProfile) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_streams", "-show_format", "-of", "json", output)
	data, err := command.Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", output, err)
	}
	var probe giftClipE2EProbe
	if err := json.Unmarshal(data, &probe); err != nil {
		t.Fatalf("decode ffprobe JSON: %v", err)
	}
	var video *struct {
		CodecType   string `json:"codec_type"`
		CodecName   string `json:"codec_name"`
		PixelFormat string `json:"pix_fmt"`
		Width       int    `json:"width"`
		Height      int    `json:"height"`
		AverageRate string `json:"avg_frame_rate"`
		Duration    string `json:"duration"`
		Bitrate     string `json:"bit_rate"`
		FrameCount  string `json:"nb_frames"`
	}
	for index := range probe.Streams {
		stream := &probe.Streams[index]
		if stream.CodecType == "audio" {
			t.Fatalf("%s contains an audio stream", output)
		}
		if stream.CodecType == "video" {
			if video != nil {
				t.Fatalf("%s contains multiple video streams", output)
			}
			video = stream
		}
	}
	if video == nil {
		t.Fatalf("unexpected ffprobe video contract for %s: %#v", output, video)
	}
	if err := validateGiftClipE2EVideoContract(video.CodecName, video.PixelFormat, video.Width, video.Height, video.AverageRate, video.FrameCount, profile); err != nil {
		t.Fatalf("unexpected ffprobe video contract for %s: %v", output, err)
	}
	duration := parseGiftClipE2EFloat(t, firstGiftClipE2ENonempty(video.Duration, probe.Format.Duration), "duration")
	if difference := duration - profile.Duration.Seconds(); difference < -0.05 || difference > 0.05 {
		t.Errorf("output duration = %.6fs, want %.6fs", duration, profile.Duration.Seconds())
	}
	bitrate := parseGiftClipE2EFloat(t, firstGiftClipE2ENonempty(video.Bitrate, probe.Format.Bitrate), "bitrate")
	targetBytes := float64(profile.AverageBitrate) * duration / 8
	actualBytes := bitrate * duration / 8
	maximumBytes := targetBytes*1.35 + 24*1024
	if actualBytes > maximumBytes {
		t.Errorf("output video bytes = %.0f (%.0f bit/s), want at most %.0f bytes around target %d bit/s with bounded 24 KiB startup/GOP overhead", actualBytes, bitrate, maximumBytes, profile.AverageBitrate)
	}
	size := parseGiftClipE2EFloat(t, probe.Format.Size, "size")
	t.Logf("ffprobe: stream_bitrate=%s format_bitrate=%s duration=%s size=%s frames=%s target_bytes=%.0f maximum_bytes=%.0f startup_average_allowance=%.0fbit/s", video.Bitrate, probe.Format.Bitrate, firstGiftClipE2ENonempty(video.Duration, probe.Format.Duration), probe.Format.Size, video.FrameCount, targetBytes, maximumBytes, 24*1024*8/duration)
	if size < 1_024 || size >= 1<<20 {
		t.Fatalf("output size = %.0f bytes, want a nontrivial sub-1 MiB fixture export", size)
	}
}

func validateGiftClipE2EVideoContract(codec, pixelFormat string, width, height int, averageRate, frameCount string, profile giftClipOutputProfile) error {
	if codec != "h264" {
		return fmt.Errorf("codec = %q, want h264", codec)
	}
	if pixelFormat != "yuv420p" {
		return fmt.Errorf("pixel format = %q, want yuv420p", pixelFormat)
	}
	if width != profile.Width || height != profile.Height {
		return fmt.Errorf("dimensions = %dx%d, want %dx%d", width, height, profile.Width, profile.Height)
	}
	if averageRate != "30/1" {
		return fmt.Errorf("average frame rate = %q, want 30/1", averageRate)
	}
	if frameCount != strconv.Itoa(profile.Frames) {
		return fmt.Errorf("frame count = %q, want %d", frameCount, profile.Frames)
	}
	return nil
}

func assertGiftClipE2EBitrateArgs(t *testing.T, args []string, profile giftClipOutputProfile) {
	t.Helper()
	if err := validateGiftClipE2EBitrateArgs(args, profile); err != nil {
		t.Fatal(err)
	}
}

func validateGiftClipE2EBitrateArgs(args []string, profile giftClipOutputProfile) error {
	if profile.Width == 320 && profile.Height == 180 && profile.AverageBitrate != 450_000 {
		return fmt.Errorf("production minimum bitrate changed to %d, want 450000", profile.AverageBitrate)
	}
	if err := validateGiftClipE2EOutputCodec(args); err != nil {
		return err
	}
	for _, want := range []struct {
		option string
		value  string
	}{
		{"-rate_control", "pc_vbr"},
		{"-compression_level", "75"},
		{"-b:v", strconv.FormatInt(profile.AverageBitrate, 10)},
		{"-maxrate", strconv.FormatInt(profile.PeakBitrate, 10)},
		{"-bufsize", strconv.FormatInt(profile.VBVBuffer, 10)},
	} {
		if err := validateGiftClipE2EExactOption(args, want.option, want.value); err != nil {
			return err
		}
	}
	for _, arg := range args {
		if arg == "-quality" {
			return fmt.Errorf("production FFmpeg args unexpectedly contain -quality: %q", args)
		}
	}
	return nil
}

func validateGiftClipE2EOutputCodec(args []string) error {
	outputCodecs := 0
	for index, arg := range args {
		if arg != "-c:v" {
			continue
		}
		if index+1 == len(args) {
			return fmt.Errorf("production FFmpeg arg %s is missing its value: %q", arg, args)
		}
		if args[index+1] == "h264_mf" {
			outputCodecs++
		}
	}
	if outputCodecs != 1 {
		return fmt.Errorf("production FFmpeg args contain %d -c:v h264_mf pairs, want 1: %q", outputCodecs, args)
	}
	return nil
}

func validateGiftClipE2EExactOption(args []string, option, want string) error {
	occurrences := 0
	for index, arg := range args {
		if arg != option {
			continue
		}
		if index+1 == len(args) {
			return fmt.Errorf("production FFmpeg arg %s is missing its value: %q", option, args)
		}
		if args[index+1] != want {
			return fmt.Errorf("production FFmpeg arg %s = %q, want %q: %q", option, args[index+1], want, args)
		}
		occurrences++
	}
	if occurrences != 1 {
		return fmt.Errorf("production FFmpeg args contain %d %s %s pairs, want 1: %q", occurrences, option, want, args)
	}
	return nil
}

func TestValidateGiftClipE2EBitrateArgsRequiresAmendedVBRContract(t *testing.T) {
	profile := giftClipOutputProfile{Width: 320, Height: 180, AverageBitrate: 450_000, PeakBitrate: 675_000, VBVBuffer: 900_000}
	valid := []string{
		"-c:v", "h264_mf", "-rate_control", "pc_vbr", "-compression_level", "75",
		"-b:v", "450000", "-maxrate", "675000", "-bufsize", "900000",
	}
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"valid contract", valid, false},
		{"GIF input codec is allowed", append([]string{"-c:v", "gif"}, valid...), false},
		{"missing compression level", []string{"-c:v", "h264_mf", "-rate_control", "pc_vbr", "-b:v", "450000", "-maxrate", "675000", "-bufsize", "900000"}, true},
		{"wrong codec", append([]string{"-c:v", "libx264"}, valid[2:]...), true},
		{"wrong rate control", append([]string{"-c:v", "h264_mf", "-rate_control", "cbr"}, valid[4:]...), true},
		{"wrong compression level", append([]string{"-c:v", "h264_mf", "-rate_control", "pc_vbr", "-compression_level", "50"}, valid[6:]...), true},
		{"duplicate output codec", append(append([]string(nil), valid...), "-c:v", "h264_mf"), true},
		{"duplicate compression level", append(append([]string(nil), valid...), "-compression_level", "75"), true},
		{"dangling compression level", append(append([]string(nil), valid...), "-compression_level"), true},
		{"dangling codec", append(append([]string(nil), valid...), "-c:v"), true},
		{"quality option", append(append([]string(nil), valid...), "-quality", "75"), true},
		{"duplicate bitrate", append(append([]string(nil), valid...), "-b:v", "450000"), true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateGiftClipE2EBitrateArgs(test.args, profile)
			if (err != nil) != test.want {
				t.Fatalf("validateGiftClipE2EBitrateArgs() error = %v, want error %t", err, test.want)
			}
		})
	}
}

type giftClipE2EMD5Frame struct {
	PTS      int64
	Duration int64
	Hash     string
}

func TestGiftClipFrameMD5TimestampPatternRejectsWrongBoundaryOrMissingRepeats(t *testing.T) {
	valid := []giftClipE2EMD5Frame{
		{PTS: 0, Duration: 1, Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{PTS: 1, Duration: 1, Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{PTS: 2, Duration: 1, Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{PTS: 3, Duration: 1, Hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{PTS: 4, Duration: 1, Hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{PTS: 5, Duration: 1, Hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}
	if err := validateGiftClipFrameMD5TimestampPattern(valid, 10, 30); err != nil {
		t.Fatalf("valid timestamp hash pattern: %v", err)
	}
	wrongBoundary := append([]giftClipE2EMD5Frame(nil), valid...)
	wrongBoundary[3].Hash = wrongBoundary[2].Hash
	if err := validateGiftClipFrameMD5TimestampPattern(wrongBoundary, 10, 30); err == nil {
		t.Fatal("static hash across a source timestamp boundary was accepted")
	}
	missingRepeats := append([]giftClipE2EMD5Frame(nil), valid...)
	for index := range missingRepeats {
		missingRepeats[index].Hash = fmt.Sprintf("%032x", index+1)
	}
	if err := validateGiftClipFrameMD5TimestampPattern(missingRepeats, 10, 30); err == nil {
		t.Fatal("sequence with no observed repeated hashes was accepted")
	}
}

func TestGiftClipE2EVideoContractRejectsNonYUV420P(t *testing.T) {
	profile := giftClipOutputProfile{Width: 320, Height: 180, FPS: 30, Frames: 60}
	if err := validateGiftClipE2EVideoContract("h264", "yuv444p", 320, 180, "30/1", "60", profile); err == nil {
		t.Fatal("non-yuv420p output was accepted")
	}
}

func giftClipE2EFrameMD5(t *testing.T, ffmpeg, output string) []giftClipE2EMD5Frame {
	t.Helper()
	return giftClipE2EFrameMD5WithFilter(t, ffmpeg, output, "")
}

func giftClipE2EQuantizedFrameMD5(t *testing.T, ffmpeg, output string) []giftClipE2EMD5Frame {
	t.Helper()
	return giftClipE2EFrameMD5WithFilter(t, ffmpeg, output, "scale=32:18:flags=area,format=gray,lut=y='floor(val/16)*16'")
}

func giftClipE2EFrameMD5WithFilter(t *testing.T, ffmpeg, output, filter string) []giftClipE2EMD5Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := []string{"-v", "error", "-i", output}
	if filter != "" {
		args = append(args, "-vf", filter)
	}
	args = append(args, "-f", "framemd5", "-")
	command := exec.CommandContext(ctx, ffmpeg, args...)
	data, err := command.Output()
	if err != nil {
		t.Fatalf("framemd5 %s: %v", output, err)
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	timebase := ""
	frames := make([]giftClipE2EMD5Frame, 0, 60)
	for _, line := range lines {
		if strings.HasPrefix(line, "#tb 0: ") {
			timebase = strings.TrimSpace(strings.TrimPrefix(line, "#tb 0: "))
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) != 6 {
			t.Fatalf("unexpected framemd5 row: %q", line)
		}
		pts, err := strconv.ParseInt(strings.TrimSpace(fields[2]), 10, 64)
		if err != nil {
			t.Fatalf("parse framemd5 PTS %q: %v", fields[2], err)
		}
		duration, err := strconv.ParseInt(strings.TrimSpace(fields[3]), 10, 64)
		if err != nil {
			t.Fatalf("parse framemd5 duration %q: %v", fields[3], err)
		}
		hash := strings.TrimSpace(fields[5])
		if len(hash) != 32 {
			t.Fatalf("unexpected framemd5 hash %q", hash)
		}
		frames = append(frames, giftClipE2EMD5Frame{PTS: pts, Duration: duration, Hash: hash})
	}
	if timebase != "1/30" {
		t.Fatalf("framemd5 timebase = %q, want 1/30", timebase)
	}
	return frames
}

func validateGiftClipFrameMD5TimestampPattern(frames []giftClipE2EMD5Frame, sourceFPS, outputFPS int) error {
	if len(frames) < 2 || sourceFPS <= 0 || outputFPS <= 0 {
		return errors.New("framemd5 timestamp pattern inputs are invalid")
	}
	expectedRepeatedTicks := 0
	observedRepeatedHashes := 0
	for index := 1; index < len(frames); index++ {
		isRepeatedTimestamp := giftClipSourceIndexAtOutputTimestamp(index, sourceFPS, outputFPS) == giftClipSourceIndexAtOutputTimestamp(index-1, sourceFPS, outputFPS)
		hashRepeated := frames[index].Hash == frames[index-1].Hash
		if isRepeatedTimestamp {
			expectedRepeatedTicks++
			if hashRepeated {
				observedRepeatedHashes++
			}
		} else if hashRepeated {
			return fmt.Errorf("quantized framemd5 repeated across source timestamp boundary at output frame %d", index)
		}
	}
	if expectedRepeatedTicks == 0 || observedRepeatedHashes*2 < expectedRepeatedTicks {
		return fmt.Errorf("quantized framemd5 observed %d/%d expected repeated timestamp ticks, want at least half", observedRepeatedHashes, expectedRepeatedTicks)
	}
	return nil
}

func giftClipE2EFrameFingerprints(t *testing.T, ffmpeg, output string) [][]byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const width, height = 32, 18
	command := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-i", output,
		"-vf", fmt.Sprintf("scale=%d:%d:flags=area,format=gray", width, height),
		"-pix_fmt", "gray", "-f", "rawvideo", "-")
	data, err := command.Output()
	if err != nil {
		t.Fatalf("decoded frame fingerprints %s: %v", output, err)
	}
	frameSize := width * height
	if len(data)%frameSize != 0 {
		t.Fatalf("decoded fingerprint bytes = %d, not a multiple of %d", len(data), frameSize)
	}
	frames := make([][]byte, 0, len(data)/frameSize)
	for offset := 0; offset < len(data); offset += frameSize {
		frames = append(frames, data[offset:offset+frameSize])
	}
	return frames
}

func assertGiftClipTimestampSampling(t *testing.T, frames []giftClipE2EMD5Frame, fingerprints [][]byte, sourceFPS, outputFPS, wantFrames int) {
	t.Helper()
	if len(frames) != wantFrames {
		t.Fatalf("decoded frame count = %d, want %d", len(frames), wantFrames)
	}
	if len(fingerprints) != len(frames) {
		t.Fatalf("decoded fingerprint count = %d, want framemd5 count %d", len(fingerprints), len(frames))
	}
	groupCounts := make(map[int]int, sourceFPS*2)
	maxRepeatedDifference := 0.0
	minChangedDifference := 256.0
	previousGroup := -1
	for index, frame := range frames {
		if frame.PTS != int64(index) {
			t.Fatalf("decoded frame %d PTS = %d, want timestamp %d/%d", index, frame.PTS, index, outputFPS)
		}
		if frame.Duration != 1 {
			t.Fatalf("decoded frame %d duration = %d, want one %d-fps tick", index, frame.Duration, outputFPS)
		}
		sourceIndex := giftClipSourceIndexAtOutputTimestamp(index, sourceFPS, outputFPS)
		groupCounts[sourceIndex]++
		if index > 0 {
			difference := giftClipFingerprintDifference(fingerprints[index-1], fingerprints[index])
			if sourceIndex == previousGroup {
				if difference > maxRepeatedDifference {
					maxRepeatedDifference = difference
				}
			} else if difference < minChangedDifference {
				minChangedDifference = difference
			}
		}
		previousGroup = sourceIndex
	}
	wantSourceFrames := sourceFPS * 2
	if len(groupCounts) != wantSourceFrames {
		t.Fatalf("timestamp sampling observed %d source frames, want %d", len(groupCounts), wantSourceFrames)
	}
	for sourceIndex := 0; sourceIndex < wantSourceFrames; sourceIndex++ {
		firstOutput := giftClipRescaledSourceTimestamp(sourceIndex, sourceFPS, outputFPS)
		nextOutput := giftClipRescaledSourceTimestamp(sourceIndex+1, sourceFPS, outputFPS)
		if nextOutput > wantFrames {
			nextOutput = wantFrames
		}
		if got, want := groupCounts[sourceIndex], nextOutput-firstOutput; got != want {
			t.Fatalf("source frame %d sampled %d times, want %d from timestamps %d/%d..%d/%d", sourceIndex, got, want, firstOutput, outputFPS, nextOutput, outputFPS)
		}
	}
	if maxRepeatedDifference > 0.25 {
		t.Fatalf("timestamp sampling repeated-frame difference %.3f exceeds the fixed-fixture bound 0.25", maxRepeatedDifference)
	}
	if minChangedDifference < 1 {
		t.Fatalf("timestamp sampling changed-frame difference %.3f is below the fixed-fixture bound 1.0", minChangedDifference)
	}
	if maxRepeatedDifference >= minChangedDifference {
		t.Fatalf("timestamp sampling clusters overlap: repeated-frame difference %.3f >= changed-frame difference %.3f", maxRepeatedDifference, minChangedDifference)
	}
	t.Logf("timestamp sampling: source=%dfps output=%dfps frames=%d repeated-max=%.3f changed-min=%.3f", sourceFPS, outputFPS, len(frames), maxRepeatedDifference, minChangedDifference)
}

func giftClipFingerprintDifference(left, right []byte) float64 {
	if len(left) != len(right) || len(left) == 0 {
		return 256
	}
	total := 0
	for index := range left {
		difference := int(left[index]) - int(right[index])
		if difference < 0 {
			difference = -difference
		}
		total += difference
	}
	return float64(total) / float64(len(left))
}

func giftClipSourceIndexAtOutputTimestamp(outputIndex, sourceFPS, outputFPS int) int {
	sourceIndex := 0
	for candidate := 1; giftClipRescaledSourceTimestamp(candidate, sourceFPS, outputFPS) <= outputIndex; candidate++ {
		sourceIndex = candidate
	}
	return sourceIndex
}

func giftClipRescaledSourceTimestamp(sourceIndex, sourceFPS, outputFPS int) int {
	return (sourceIndex*outputFPS + sourceFPS/2) / sourceFPS
}

func parseGiftClipE2EFloat(t *testing.T, value, label string) float64 {
	t.Helper()
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		t.Fatalf("parse ffprobe %s %q: %v", label, value, err)
	}
	return parsed
}

func firstGiftClipE2ENonempty(values ...string) string {
	for _, value := range values {
		if value != "" && value != "N/A" {
			return value
		}
	}
	return ""
}

func readGiftClipE2EFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func preserveGiftClipE2EOutput(t *testing.T, source, name string) {
	t.Helper()
	directory := strings.TrimSpace(os.Getenv("GIFT_CLIP_E2E_OUTPUT_DIR"))
	if directory == "" {
		return
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(absolute, filepath.Base(name))
	if err := os.WriteFile(destination, readGiftClipE2EFile(t, source), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("preserved E2E output: %s", destination)
}

func writeGiftClipHarnessPortFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	partial := path + ".partial"
	if err := os.WriteFile(partial, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(partial, path); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("gift clip harness listening at %s\n", value)
}
