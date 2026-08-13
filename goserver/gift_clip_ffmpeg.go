package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
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

type giftClipEncodingUpdate struct {
	Progress float64
	Mode     giftClipEncoderMode
	Retrying bool
}

type giftClipEncoder interface {
	Encode(context.Context, giftClipEncodeRequest, func(giftClipEncodingUpdate)) error
}

type giftClipProcessRunner interface {
	Run(context.Context, string, []string, io.Writer, io.Writer) error
}

type giftClipFFmpegEncoderOptions struct {
	ForceSoftware bool
}

type giftClipFFmpegEncoder struct {
	payload       *giftClipPayload
	runner        giftClipProcessRunner
	diagnostics   *diagnosticLogger
	forceSoftware bool

	mu          sync.Mutex
	useSoftware bool
}

func newGiftClipFFmpegEncoder(payload *giftClipPayload, runner giftClipProcessRunner, diagnostics *diagnosticLogger, options giftClipFFmpegEncoderOptions) giftClipEncoder {
	if runner == nil {
		runner = newGiftClipProcessRunner()
	}
	return &giftClipFFmpegEncoder{
		payload:       payload,
		runner:        runner,
		diagnostics:   diagnostics,
		forceSoftware: options.ForceSoftware,
	}
}

func (encoder *giftClipFFmpegEncoder) Encode(ctx context.Context, request giftClipEncodeRequest, notify func(giftClipEncodingUpdate)) error {
	if encoder == nil || encoder.payload == nil || encoder.runner == nil {
		return errors.New("gift clip ffmpeg encoder is unavailable")
	}
	path, err := encoder.payload.Prepare(ctx)
	if err != nil {
		return err
	}
	mode := encoder.initialMode()
	err, stderr := encoder.runAttempt(ctx, path, request, mode, notify)
	if err == nil {
		return nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if mode != giftClipEncoderHardware || !shouldRetryGiftClipSoftware(err, stderr) {
		return err
	}
	encoder.rememberSoftwareMode()
	if notify != nil {
		notify(giftClipEncodingUpdate{Mode: giftClipEncoderSoftware, Retrying: true})
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err, _ = encoder.runAttempt(ctx, path, request, giftClipEncoderSoftware, notify)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
	}
	return err
}

func (encoder *giftClipFFmpegEncoder) initialMode() giftClipEncoderMode {
	if encoder.forceSoftware {
		return giftClipEncoderSoftware
	}
	encoder.mu.Lock()
	defer encoder.mu.Unlock()
	if encoder.useSoftware {
		return giftClipEncoderSoftware
	}
	return giftClipDefaultEncoderMode
}

func (encoder *giftClipFFmpegEncoder) rememberSoftwareMode() {
	encoder.mu.Lock()
	encoder.useSoftware = true
	encoder.mu.Unlock()
}

func (encoder *giftClipFFmpegEncoder) runAttempt(ctx context.Context, path string, request giftClipEncodeRequest, mode giftClipEncoderMode, notify func(giftClipEncodingUpdate)) (error, string) {
	args, err := buildGiftClipFFmpegArgs(request, mode)
	if err != nil {
		return err, ""
	}
	stdoutReader, stdoutWriter := io.Pipe()
	progressDone := make(chan error, 1)
	go func() {
		parser := newGiftClipProgressParser(request.Profile.Duration)
		scanner := bufio.NewScanner(stdoutReader)
		scanner.Buffer(make([]byte, 1024), 256*1024)
		for scanner.Scan() {
			if progress, ok := parser.Consume(scanner.Text()); ok && notify != nil {
				notify(giftClipEncodingUpdate{Progress: progress, Mode: mode})
			}
		}
		progressDone <- scanner.Err()
		_ = stdoutReader.Close()
	}()
	stderr := &giftClipStderrTail{}
	runErr := encoder.runner.Run(ctx, path, args, stdoutWriter, stderr)
	_ = stdoutWriter.Close()
	scanErr := <-progressDone
	if runErr == nil && scanErr != nil {
		runErr = scanErr
	}
	stderrText := stderr.String()
	if runErr != nil && encoder.diagnostics != nil {
		encoder.diagnostics.Error("gift_clip_ffmpeg_failed", "mode", mode, "stderr_tail", sanitizeGiftClipDiagnosticStderr(stderrText))
	}
	return runErr, stderrText
}

const giftClipStderrTailLimit = 32 * 1024

type giftClipStderrTail struct {
	buffer []byte
}

func (tail *giftClipStderrTail) Write(data []byte) (int, error) {
	written := len(data)
	if len(data) >= giftClipStderrTailLimit {
		tail.buffer = append(tail.buffer[:0], data[len(data)-giftClipStderrTailLimit:]...)
		return written, nil
	}
	overflow := len(tail.buffer) + len(data) - giftClipStderrTailLimit
	if overflow > 0 {
		copy(tail.buffer, tail.buffer[overflow:])
		tail.buffer = tail.buffer[:len(tail.buffer)-overflow]
	}
	tail.buffer = append(tail.buffer, data...)
	return written, nil
}

func (tail *giftClipStderrTail) String() string {
	return string(tail.buffer)
}

func sanitizeGiftClipDiagnosticStderr(stderr string) string {
	var output strings.Builder
	for index := 0; index < len(stderr); {
		pathStart, quoted := index, false
		if stderr[index] == '"' && index+1 < len(stderr) && startsGiftClipAbsolutePath(stderr, index+1) {
			pathStart, quoted = index+1, true
		} else if !startsGiftClipAbsolutePath(stderr, index) {
			output.WriteByte(stderr[index])
			index++
			continue
		}
		end := endGiftClipDiagnosticPath(stderr, pathStart, quoted)
		output.WriteString("[PATH]")
		index = end
	}
	return output.String()
}

func startsGiftClipAbsolutePath(text string, index int) bool {
	if index >= len(text) {
		return false
	}
	if index+2 < len(text) && giftClipPathBoundary(text, index) && ((text[index] >= 'A' && text[index] <= 'Z') || (text[index] >= 'a' && text[index] <= 'z')) && text[index+1] == ':' && (text[index+2] == '\\' || text[index+2] == '/') {
		return true
	}
	if text[index] == '/' {
		return giftClipPathBoundary(text, index) && !giftClipPathStartsInURIScheme(text, index) && !giftClipPathStartsDotRelative(text, index)
	}
	return index+1 < len(text) && text[index] == '\\' && text[index+1] == '\\' && giftClipPathBoundary(text, index)
}

func giftClipPathBoundary(text string, index int) bool {
	if index == 0 {
		return true
	}
	return !giftClipASCIIWordContinuation(text[index-1])
}

func giftClipASCIIWordContinuation(character byte) bool {
	return character >= 'a' && character <= 'z' ||
		character >= 'A' && character <= 'Z' ||
		character >= '0' && character <= '9' || character == '_'
}

func giftClipPathStartsInURIScheme(text string, index int) bool {
	colon := -1
	if index > 0 && text[index-1] == ':' {
		colon = index - 1
	} else if index > 1 && text[index-1] == '/' && text[index-2] == ':' {
		colon = index - 2
	}
	if colon < 1 {
		return false
	}
	schemeStart := colon - 1
	for schemeStart > 0 && giftClipURISchemeTokenContinuation(text[schemeStart-1]) {
		schemeStart--
	}
	if !((text[schemeStart] >= 'a' && text[schemeStart] <= 'z') || (text[schemeStart] >= 'A' && text[schemeStart] <= 'Z')) {
		return false
	}
	for schemeIndex := schemeStart + 1; schemeIndex < colon; schemeIndex++ {
		if !giftClipURISchemeContinuation(text[schemeIndex]) {
			return false
		}
	}
	return true
}

func giftClipURISchemeContinuation(character byte) bool {
	return character >= 'a' && character <= 'z' ||
		character >= 'A' && character <= 'Z' ||
		character >= '0' && character <= '9' ||
		character == '+' || character == '-' || character == '.'
}

func giftClipURISchemeTokenContinuation(character byte) bool {
	return giftClipURISchemeContinuation(character) || character == '_'
}

func giftClipPathStartsDotRelative(text string, index int) bool {
	dotStart := index
	for dotStart > 0 && text[dotStart-1] == '.' {
		dotStart--
	}
	dots := index - dotStart
	return (dots == 1 || dots == 2) && giftClipPathBoundary(text, dotStart)
}

func endGiftClipDiagnosticPath(text string, start int, quoted bool) int {
	if quoted {
		if end := strings.IndexByte(text[start:], '"'); end >= 0 {
			return start + end + 1
		}
	}
	for index := start; index < len(text); index++ {
		if text[index] == '\r' || text[index] == '\n' || text[index] == '"' ||
			(text[index] == ':' && index+1 < len(text) && (text[index+1] == ' ' || text[index+1] == '\t')) {
			return index
		}
	}
	return len(text)
}

var _ io.Writer = (*giftClipStderrTail)(nil)

func buildGiftClipFFmpegArgs(request giftClipEncodeRequest, mode giftClipEncoderMode) ([]string, error) {
	if mode != giftClipEncoderHardware && mode != giftClipEncoderSoftware {
		return nil, errors.New("gift clip encoder mode is invalid")
	}
	if err := validateGiftClipEncodeRequest(request); err != nil {
		return nil, err
	}

	args := appendGiftClipSourceInput(nil, request.Source)
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

func appendGiftClipSourceInput(args []string, source giftClipSource) []string {
	switch source.Playback {
	case giftClipPlaybackSingleGIF:
		return append(args, "-c:v", "gif", "-f", "image2", "-loop", "1", "-framerate", strconv.Itoa(giftClipFPS), "-i", source.Path)
	case giftClipPlaybackAnimatedGIF:
		return append(args, "-f", "gif", "-ignore_loop", "0", "-i", source.Path)
	case giftClipPlaybackStaticWebP:
		return append(args, "-stream_loop", "-1", "-f", "webp_pipe", "-i", source.Path)
	case giftClipPlaybackAnimatedWebP:
		return append(args, "-f", "webp_anim", "-ignore_loop", "0", "-i", source.Path)
	case giftClipPlaybackEffect:
		return append(args, "-i", source.Path)
	default:
		return args
	}
}

func appendStaticGiftClipImageInput(args []string, path string) []string {
	return append(args, "-f", "image2", "-loop", "1", "-framerate", strconv.Itoa(giftClipFPS), "-i", path)
}

func validateGiftClipEncodeRequest(request giftClipEncodeRequest) error {
	if !validGiftClipSourcePlayback(request.Source.Kind, request.Source.Playback) {
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

func validGiftClipSourcePlayback(kind giftClipSourceKind, playback giftClipPlaybackMode) bool {
	switch kind {
	case giftClipSourceGIF:
		return playback == giftClipPlaybackSingleGIF || playback == giftClipPlaybackAnimatedGIF
	case giftClipSourceWebP:
		return playback == giftClipPlaybackStaticWebP || playback == giftClipPlaybackAnimatedWebP
	case giftClipSourceEffect:
		return playback == giftClipPlaybackEffect
	default:
		return false
	}
}

func validateGiftClipLocalPath(name, path string) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || strings.ContainsAny(path, "\x00\r\n") || strings.HasPrefix(strings.ToLower(trimmed), "http://") || strings.HasPrefix(strings.ToLower(trimmed), "https://") || !isAbsoluteGiftClipPath(trimmed) {
		return fmt.Errorf("gift clip %s path must be a local absolute path", name)
	}
	return nil
}

func isAbsoluteGiftClipPath(path string) bool {
	if strings.HasPrefix(path, `\`) || strings.HasPrefix(path, "//") {
		return false
	}
	if strings.HasPrefix(path, "/") {
		return true
	}
	return len(path) >= 3 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) && path[1] == ':' && (path[2] == '\\' || path[2] == '/')
}

func giftClipFilterGraph(request giftClipEncodeRequest) (string, error) {
	if request.Source.Kind == giftClipSourceEffect {
		layout := request.Source.Layout
		return fmt.Sprintf(
			"[0:v]setpts=PTS-STARTPTS,fps=%d,split=2[rgb0][alpha0];[rgb0]crop=%d:%d:%d:%d,scale=%d:%d[rg];[alpha0]crop=%d:%d:%d:%d,scale=%d:%d,format=gray[a];[rg][a]alphamerge,crop=%d:%d:%d:%d[anim];[1:v]format=rgba[bg];[bg][anim]overlay=0:0:format=auto[mid];[2:v]format=rgba[ol];[mid][ol]overlay=0:0:format=auto,fps=%d,format=nv12[out]",
			giftClipFPS,
			layout.RGBFrame[2], layout.RGBFrame[3], layout.RGBFrame[0], layout.RGBFrame[1], request.Source.VisualWidth, request.Source.VisualHeight,
			layout.AlphaFrame[2], layout.AlphaFrame[3], layout.AlphaFrame[0], layout.AlphaFrame[1], request.Source.VisualWidth, request.Source.VisualHeight,
			request.Crop.Width, request.Crop.Height, request.Crop.X, request.Crop.Y, giftClipFPS,
		), nil
	}
	return fmt.Sprintf(
		"[0:v]setpts=PTS-STARTPTS,crop=%d:%d:%d:%d,format=rgba,fps=%d[anim];[1:v]format=rgba[bg];[bg][anim]overlay=0:0:format=auto[mid];[2:v]format=rgba[ol];[mid][ol]overlay=0:0:format=auto,fps=%d,format=nv12[out]",
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
		"invalid data found", "corrupt input", "input is corrupt", "malformed input", "invalid input",
		"error opening input", "error while opening input", "could not open input", "failed to open input",
		"decode error", "decoding error", "failed to decode", "could not decode", "demux", "could not find codec parameters", "unsupported input format",
		"no such file or directory", "path not found", "failed to open output", "error opening output", "output is not writable", "output path", "output directory", "output file", "could not write output",
		"muxer", "muxing", "write header", "write error", "error writing", "failed to write",
	} {
		if strings.Contains(message, excluded) {
			return false
		}
	}
	for _, clause := range strings.FieldsFunc(message, func(character rune) bool {
		return character == '\r' || character == '\n' || character == ';' || character == '|'
	}) {
		normalized := strings.Join(strings.Fields(clause), " ")
		for _, pattern := range giftClipHardwareFailurePatterns {
			if pattern.MatchString(normalized) {
				return true
			}
		}
	}
	return false
}

const (
	giftClipHardwareContextPattern = `(\bh264_mf\b(\s+@\s+[0-9a-fx]+)?\]?|\bmedia foundation\b|\bmft\b|\bmf\b)`
	giftClipHardwareTargetPattern  = `((hardware\s+)?encoder|encoding|(hardware|gpu)\s+device|mft(\s+encoder)?|transform|session|(input|output)\s+(type|pin))`
	giftClipFailureActionPattern   = `(((failed|unable)(\s+to)?|could not)\s+(initialize|open|create|start|encode)\w*|error(\s+while)?\s+(initializing|opening|creating|starting|encoding))`
	giftClipUnavailablePattern     = `(is\s+)?(unavailable|not available|unsupported)`
)

var giftClipHardwareFailurePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)` + giftClipHardwareContextPattern + `\s+` + giftClipHardwareTargetPattern + `\s+` + giftClipFailureActionPattern + `(\s+(the\s+)?` + giftClipHardwareTargetPattern + `)?\s*[.!]?$`),
	regexp.MustCompile(`(?i)` + giftClipHardwareContextPattern + `\s+` + giftClipFailureActionPattern + `\s+(the\s+)?` + giftClipHardwareTargetPattern + `\s*[.!]?$`),
	regexp.MustCompile(`(?i)` + giftClipFailureActionPattern + `\s+` + giftClipHardwareContextPattern + `\s+` + giftClipHardwareTargetPattern + `\s*[.!]?$`),
	regexp.MustCompile(`(?i)` + giftClipFailureActionPattern + `\s+(the\s+)?` + giftClipHardwareTargetPattern + `\s+(in|for|with)\s+` + giftClipHardwareContextPattern + `\s*[.!]?$`),
	regexp.MustCompile(`(?i)` + giftClipHardwareContextPattern + `\s+` + giftClipHardwareTargetPattern + `\s+` + giftClipUnavailablePattern + `\s*[.!]?$`),
	regexp.MustCompile(`(?i)\bhardware (encoder|encoding)\b\s+(` + giftClipFailureActionPattern + `|failed|failure|` + giftClipUnavailablePattern + `)\s*[.!]?$`),
	regexp.MustCompile(`(?i)` + giftClipFailureActionPattern + `\s+hardware (encoder|encoding)\s*[.!]?$`),
	regexp.MustCompile(`(?i)\b(hardware|gpu) device\b\s+(lost|removed|failed|` + giftClipUnavailablePattern + `)\s+(for|while using)\s+hardware (encoder|encoding)\s*[.!]?$`),
	regexp.MustCompile(`(?i)` + giftClipFailureActionPattern + `\s+(hardware|gpu) device\s+for\s+hardware (encoder|encoding)\s*[.!]?$`),
	regexp.MustCompile(`(?i)\bh264_mf\b(\s+@\s+[0-9a-fx]+)?\]?\s+error while opening encoder for output stream\b`),
	regexp.MustCompile(`(?i)\bmedia foundation\b\s+encoder\s+failed\s+(after|with|because of)\s+(dxgi_error_device_(removed|hung|reset)|d3d11 device (lost|removed|failed))\s*[.!]?$`),
}
