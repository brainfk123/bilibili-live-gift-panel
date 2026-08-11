package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type giftClipEncoderMode string

const (
	giftClipEncoderHardware giftClipEncoderMode = "hardware"
	giftClipEncoderSoftware giftClipEncoderMode = "software"
)

// errGiftClipPayloadIntegrity is deliberately shared with the payload
// installer so an integrity failure can never be mistaken for an encoder
// compatibility failure.
var errGiftClipPayloadIntegrity = errors.New("gift clip ffmpeg payload integrity failure")

type giftClipEncodeRequest struct {
	Source                                  giftClipSource
	Crop                                    giftClipCrop
	Profile                                 giftClipOutputProfile
	BackgroundPath, OverlayPath, OutputPath string
}

func buildGiftClipFFmpegArgs(request giftClipEncodeRequest, mode giftClipEncoderMode) ([]string, error) {
	if mode != giftClipEncoderHardware && mode != giftClipEncoderSoftware {
		return nil, errors.New("gift clip encoder mode is invalid")
	}
	if err := validateGiftClipEncodeRequest(request); err != nil {
		return nil, err
	}

	args := []string{"-stream_loop", "-1"}
	switch request.Source.Kind {
	case giftClipSourceGIF:
		args = append(args, "-ignore_loop", "1", "-f", "gif")
	case giftClipSourceWebP:
		args = append(args, "-ignore_loop", "1", "-f", "webp")
	}
	args = append(args, "-i", request.Source.Path)
	args = appendStaticGiftClipImageInput(args, request.BackgroundPath)
	args = appendStaticGiftClipImageInput(args, request.OverlayPath)

	filter, err := giftClipFilterGraph(request)
	if err != nil {
		return nil, err
	}
	hardwareEncoding := "0"
	if mode == giftClipEncoderHardware {
		hardwareEncoding = "1"
	}
	args = append(args,
		"-filter_complex", filter,
		"-map", "[out]",
		"-an",
		"-t", strconv.FormatFloat(request.Profile.Duration.Seconds(), 'f', -1, 64),
		"-c:v", "h264_mf",
		"-hw_encoding", hardwareEncoding,
		"-rate_control", "pc_vbr",
		"-b:v", strconv.FormatInt(request.Profile.AverageBitrate, 10),
		"-maxrate", strconv.FormatInt(request.Profile.PeakBitrate, 10),
		"-bufsize", strconv.FormatInt(request.Profile.VBVBuffer, 10),
		"-pix_fmt", "nv12",
		"-fps_mode", "cfr",
		"-movflags", "+faststart",
		"-progress", "pipe:1",
		"-nostats",
		"-y", request.OutputPath,
	)
	return args, nil
}

func appendStaticGiftClipImageInput(args []string, path string) []string {
	return append(args, "-f", "image2", "-loop", "1", "-framerate", strconv.Itoa(giftClipFPS), "-i", path)
}

func validateGiftClipEncodeRequest(request giftClipEncodeRequest) error {
	if request.Source.Kind != giftClipSourceGIF && request.Source.Kind != giftClipSourceWebP && request.Source.Kind != giftClipSourceEffect {
		return errors.New("gift clip source kind is invalid")
	}
	if err := validateGiftClipLocalPath("source", request.Source.Path); err != nil {
		return err
	}
	if err := validateGiftClipLocalPath("background", request.BackgroundPath); err != nil {
		return err
	}
	if err := validateGiftClipLocalPath("overlay", request.OverlayPath); err != nil {
		return err
	}
	if err := validateGiftClipLocalPath("output", request.OutputPath); err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Ext(request.BackgroundPath), ".png") || !strings.EqualFold(filepath.Ext(request.OverlayPath), ".png") || !strings.EqualFold(filepath.Ext(request.OutputPath), ".mp4") {
		return errors.New("gift clip layer and output extensions are invalid")
	}
	if request.Source.VisualWidth < 1 || request.Source.VisualHeight < 1 {
		return errors.New("gift clip source dimensions are invalid")
	}
	expected, err := newGiftClipOutputProfile(request.Crop, request.Source.VisualWidth, request.Source.VisualHeight, request.Source.Duration)
	if err != nil {
		return err
	}
	if request.Profile != expected {
		return errors.New("gift clip output profile does not match source and crop")
	}
	if request.Source.Kind == giftClipSourceEffect {
		if request.Source.Layout == nil {
			return errors.New("gift clip effect layout is missing")
		}
		layout := request.Source.Layout
		if !validGiftEffectFrame(layout.RGBFrame, layout.VideoWidth, layout.VideoHeight) || !validGiftEffectFrame(layout.AlphaFrame, layout.VideoWidth, layout.VideoHeight) ||
			layout.RGBFrame[2] != request.Source.VisualWidth || layout.RGBFrame[3] != request.Source.VisualHeight {
			return errors.New("gift clip effect layout is invalid")
		}
	}
	return nil
}

func validateGiftClipLocalPath(name, path string) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || strings.ContainsAny(path, "\x00\r\n") || strings.HasPrefix(strings.ToLower(trimmed), "http://") || strings.HasPrefix(strings.ToLower(trimmed), "https://") || !isAbsoluteGiftClipPath(trimmed) {
		return fmt.Errorf("gift clip %s path must be a local absolute path", name)
	}
	return nil
}

func isAbsoluteGiftClipPath(path string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(path, `\`, "/"))
	if strings.HasPrefix(normalized, "//") || strings.HasPrefix(normalized, "/?/") || strings.HasPrefix(normalized, "/??/") || strings.HasPrefix(normalized, "/globalroot/") || strings.HasPrefix(normalized, "/device/") {
		return false
	}
	if strings.HasPrefix(normalized, "/") || filepath.IsAbs(path) {
		return true
	}
	return len(path) >= 3 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) && path[1] == ':' && (path[2] == '\\' || path[2] == '/')
}

func giftClipFilterGraph(request giftClipEncodeRequest) (string, error) {
	if request.Source.Kind == giftClipSourceEffect {
		layout := request.Source.Layout
		return fmt.Sprintf(
			"[0:v]split=2[rgb0][alpha0];[rgb0]crop=%d:%d:%d:%d,scale=%d:%d[rg];[alpha0]crop=%d:%d:%d:%d,scale=%d:%d,format=gray[a];[rg][a]alphamerge,crop=%d:%d:%d:%d,setpts=PTS-STARTPTS,fps=%d[anim];[1:v]format=rgba[bg];[bg][anim]overlay=0:0:format=auto:shortest=1[mid];[2:v]format=rgba[ol];[mid][ol]overlay=0:0:format=auto:shortest=1,fps=%d,format=nv12[out]",
			layout.RGBFrame[2], layout.RGBFrame[3], layout.RGBFrame[0], layout.RGBFrame[1], request.Source.VisualWidth, request.Source.VisualHeight,
			layout.AlphaFrame[2], layout.AlphaFrame[3], layout.AlphaFrame[0], layout.AlphaFrame[1], request.Source.VisualWidth, request.Source.VisualHeight,
			request.Crop.Width, request.Crop.Height, request.Crop.X, request.Crop.Y, giftClipFPS, giftClipFPS,
		), nil
	}
	return fmt.Sprintf(
		"[0:v]setpts=PTS-STARTPTS,crop=%d:%d:%d:%d,format=rgba,fps=%d[anim];[1:v]format=rgba[bg];[bg][anim]overlay=0:0:format=auto:shortest=1[mid];[2:v]format=rgba[ol];[mid][ol]overlay=0:0:format=auto:shortest=1,fps=%d,format=nv12[out]",
		request.Crop.Width, request.Crop.Height, request.Crop.X, request.Crop.Y, giftClipFPS, giftClipFPS,
	), nil
}

type giftClipProgressParser struct {
	duration time.Duration
	last     float64
}

func newGiftClipProgressParser(duration time.Duration) *giftClipProgressParser {
	return &giftClipProgressParser{duration: duration}
}

func (parser *giftClipProgressParser) Consume(line string) (float64, bool) {
	if line == "progress=end" {
		parser.last = 1
		return 1, true
	}
	const prefix = "out_time_us="
	if !strings.HasPrefix(line, prefix) || parser.duration <= 0 {
		return 0, false
	}
	microseconds, err := strconv.ParseInt(strings.TrimPrefix(line, prefix), 10, 64)
	if err != nil || microseconds < 0 {
		return 0, false
	}
	progress := float64(microseconds) / float64(parser.duration/time.Microsecond)
	if progress > 1 {
		progress = 1
	}
	if progress < parser.last {
		progress = parser.last
	}
	parser.last = progress
	return progress, true
}

func shouldRetryGiftClipSoftware(err error, stderr string) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, errGiftClipPayloadIntegrity) {
		return false
	}
	message := strings.ToLower(err.Error() + "\n" + stderr)
	for _, excluded := range []string{
		"no space left on device", "disk full", "not enough space", "permission denied", "access is denied",
		"invalid data found", "corrupt", "malformed", "error opening input", "error while opening input", "could not open input",
		"no such file or directory", "path not found", "failed to open output", "error opening output",
		"muxer", "muxing", "write header", "write error", "error writing", "failed to write",
	} {
		if strings.Contains(message, excluded) {
			return false
		}
	}
	for _, diagnostic := range []string{
		"h264_mf", "media foundation", "hardware encoder", "hardware encoding",
		"dxgi_error_device", "device removed", "device lost", "device creation failed",
	} {
		if strings.Contains(message, diagnostic) {
			return true
		}
	}
	return false
}
