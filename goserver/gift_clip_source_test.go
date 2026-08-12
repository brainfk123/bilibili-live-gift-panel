package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGiftClipGIFInfoUsesFrameDelays(t *testing.T) {
	width, height, cycle, err := giftClipGIFInfo(twoFrameGIF(t, 120, 80, []int{4, 7}))
	if err != nil {
		t.Fatal(err)
	}
	if width != 120 || height != 80 || cycle != 110*time.Millisecond {
		t.Fatalf("info = %dx%d %s", width, height, cycle)
	}
}

func TestGiftClipWebPInfoUsesANMFDelays(t *testing.T) {
	width, height, cycle, err := giftClipWebPInfo(validAnimatedWebPFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if width != 16 || height != 16 || cycle != 400*time.Millisecond {
		t.Fatalf("info = %dx%d %s", width, height, cycle)
	}
}

func TestGiftClipWebPInfoClampsShortFrameDelay(t *testing.T) {
	data := validAnimatedWebPFixture(t)
	copy(data[64:67], []byte{1, 0, 0})
	_, _, cycle, err := giftClipWebPInfo(data)
	if err != nil {
		t.Fatal(err)
	}
	if cycle != 210*time.Millisecond {
		t.Fatalf("cycle = %s", cycle)
	}
}

func TestGiftClipShortAnimationPlaybackModesAreExplicit(t *testing.T) {
	tests := []struct {
		name      string
		mediaType string
		data      []byte
		want      giftClipPlaybackMode
	}{
		{name: "single gif", mediaType: "image/gif", data: gifFixture(t, 16, 16, []int{2}, -1), want: giftClipPlaybackSingleGIF},
		{name: "animated gif", mediaType: "image/gif", data: gifFixture(t, 16, 16, []int{2, 3}, -1), want: giftClipPlaybackAnimatedGIF},
		{name: "static webp", mediaType: "image/webp", data: validStaticLossyWebPFixture(t), want: giftClipPlaybackStaticWebP},
		{name: "animated webp", mediaType: "image/webp", data: validAnimatedWebPFixture(t), want: giftClipPlaybackAnimatedWebP},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := inspectGiftClipShortAnimation(test.mediaType, test.data)
			if err != nil {
				t.Fatal(err)
			}
			if got.Playback != test.want {
				t.Fatalf("playback = %q, want %q", got.Playback, test.want)
			}
		})
	}
}

func TestGiftClipSingleGIFPreservesEveryByte(t *testing.T) {
	original := gifFixture(t, 3, 2, []int{7}, -1)
	input := bytes.Clone(original)
	got, err := inspectGiftClipShortAnimation("image/gif", input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != giftClipSourceGIF || got.Playback != giftClipPlaybackSingleGIF || got.Width != 3 || got.Height != 2 || got.Cycle != 70*time.Millisecond || got.Extension != ".gif" {
		t.Fatalf("animation = %#v", got)
	}
	if !bytes.Equal(input, original) || !bytes.Equal(got.Data, original) {
		t.Fatal("single-frame GIF bytes changed")
	}
}

func TestGiftClipAnimatedGIFInsertsOnlyTheStandardInfiniteLoopExtension(t *testing.T) {
	original := gifFixture(t, 4, 3, []int{2, 3}, -1)
	input := bytes.Clone(original)
	got, err := inspectGiftClipShortAnimation("image/gif", input)
	if err != nil {
		t.Fatal(err)
	}
	wantExtension := gifLoopExtension(0)
	if len(got.Data) != len(original)+len(wantExtension) {
		t.Fatalf("normalized length = %d, want %d", len(got.Data), len(original)+len(wantExtension))
	}
	trailer := len(original) - 1
	if !bytes.Equal(got.Data[:trailer], original[:trailer]) || !bytes.Equal(got.Data[trailer:trailer+len(wantExtension)], wantExtension) || got.Data[len(got.Data)-1] != 0x3b {
		t.Fatal("animated GIF changed bytes outside the inserted loop extension")
	}
	if !bytes.Equal(input, original) {
		t.Fatal("input GIF was mutated")
	}
	decoded, err := gif.DecodeAll(bytes.NewReader(got.Data))
	if err != nil || len(decoded.Image) != 2 || decoded.Delay[0] != 2 || decoded.Delay[1] != 3 {
		t.Fatalf("normalized GIF decode = %#v, err=%v", decoded, err)
	}
}

func TestGiftClipAnimatedGIFPatchesOnlyTheExistingLoopCount(t *testing.T) {
	original := gifFixture(t, 4, 3, []int{2, 3}, 7)
	input := bytes.Clone(original)
	loopStart := bytes.Index(original, gifLoopExtension(7))
	if loopStart < 0 {
		t.Fatal("fixture is missing its loop extension")
	}
	got, err := inspectGiftClipShortAnimation("image/gif", input)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Clone(original)
	want[loopStart+16], want[loopStart+17] = 0, 0
	if !bytes.Equal(got.Data, want) {
		t.Fatal("animated GIF changed bytes beyond the loop-count uint16")
	}
	if !bytes.Equal(input, original) {
		t.Fatal("input GIF was mutated")
	}
}

func TestGiftClipGIFRejectsDuplicateMalformedOrAmbiguousBlocks(t *testing.T) {
	valid := gifFixture(t, 4, 3, []int{2, 3}, 7)
	loopStart := bytes.Index(valid, gifLoopExtension(7))
	duplicateLoop := append(bytes.Clone(valid[:len(valid)-1]), append(gifLoopExtension(2), 0x3b)...)
	malformedLoop := bytes.Clone(valid)
	malformedLoop[loopStart+14] = 2
	tests := []struct {
		name string
		data []byte
	}{
		{name: "duplicate loop extension", data: duplicateLoop},
		{name: "malformed loop sub-block", data: malformedLoop},
		{name: "truncated extension", data: valid[:len(valid)-2]},
		{name: "second trailer", data: append(bytes.Clone(valid), 0x3b)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := inspectGiftClipShortAnimation("image/gif", test.data); err == nil {
				t.Fatal("expected malformed GIF to be rejected")
			}
		})
	}
}

func TestGiftClipStaticWebPDimensionsCoverVP8VP8LAndVP8X(t *testing.T) {
	lossy := validStaticLossyWebPFixture(t)
	tests := []struct {
		name          string
		data          []byte
		width, height int
	}{
		{name: "VP8", data: lossy, width: 16, height: 16},
		{name: "VP8L", data: validStaticLosslessWebPFixture(t), width: 16, height: 16},
		{name: "VP8X", data: staticWebPWithVP8X(t, lossy, 16, 16), width: 16, height: 16},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := inspectGiftClipShortAnimation("image/webp", test.data)
			if err != nil {
				t.Fatal(err)
			}
			if got.Kind != giftClipSourceWebP || got.Playback != giftClipPlaybackStaticWebP || got.Width != test.width || got.Height != test.height || got.Cycle != 0 || got.Extension != ".webp" || !bytes.Equal(got.Data, test.data) {
				t.Fatalf("animation = %#v", got)
			}
		})
	}
}

func TestGiftClipAnimatedWebPPatchesOnlyTheANIMLoopCount(t *testing.T) {
	original := validAnimatedWebPFixture(t)
	original[42], original[43] = 9, 0
	input := bytes.Clone(original)
	got, err := inspectGiftClipShortAnimation("image/webp", input)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Clone(original)
	want[42], want[43] = 0, 0
	if got.Kind != giftClipSourceWebP || got.Playback != giftClipPlaybackAnimatedWebP || got.Width != 16 || got.Height != 16 || got.Cycle != 400*time.Millisecond || !bytes.Equal(got.Data, want) {
		t.Fatalf("animation = %#v", got)
	}
	if !bytes.Equal(input, original) {
		t.Fatal("input WebP was mutated")
	}
}

func TestGiftClipWebPRejectsStaticAnimatedAmbiguityAndMalformedChunks(t *testing.T) {
	animated := validAnimatedWebPFixture(t)
	missingAnimationFlag := bytes.Clone(animated)
	missingAnimationFlag[20] &^= 0x02
	duplicateANIM := insertWebPChunk(t, animated, 44, "ANIM", make([]byte, 6))
	static := validStaticLossyWebPFixture(t)
	staticWithANIM := insertWebPChunk(t, static, 12, "ANIM", make([]byte, 6))
	staticWithDuplicateImage := appendWebPChunk(t, static, "VP8 ", static[20:])
	mismatchedVP8X := staticWebPWithVP8X(t, static, 2, 1)
	staticALPHWithoutVP8X := insertWebPChunk(t, static, 12, "ALPH", []byte{0})
	staticAlphaFlagWithoutAlpha := staticWebPWithVP8X(t, static, 16, 16)
	staticAlphaFlagWithoutAlpha[20] |= 0x10
	animatedAlphaWithoutFlag := insertFirstWebPFrameChunk(t, animated, "ALPH", []byte{0})
	tests := []struct {
		name string
		data []byte
	}{
		{name: "missing animation flag", data: missingAnimationFlag},
		{name: "duplicate ANIM", data: duplicateANIM},
		{name: "static with ANIM", data: staticWithANIM},
		{name: "static duplicate image", data: staticWithDuplicateImage},
		{name: "VP8X dimension mismatch", data: mismatchedVP8X},
		{name: "static ALPH without VP8X", data: staticALPHWithoutVP8X},
		{name: "static alpha flag without alpha", data: staticAlphaFlagWithoutAlpha},
		{name: "animated alpha without flag", data: animatedAlphaWithoutFlag},
		{name: "truncated RIFF", data: animated[:len(animated)-1]},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := inspectGiftClipShortAnimation("image/webp", test.data); err == nil {
				t.Fatal("expected ambiguous or malformed WebP to be rejected")
			}
		})
	}
}

func TestGiftClipShortAnimationRejectsOversizedInput(t *testing.T) {
	if _, err := inspectGiftClipShortAnimation("image/gif", make([]byte, maxGiftAnimationBytes+1)); err == nil {
		t.Fatal("expected oversized short animation to be rejected")
	}
}

func TestGiftClipShortAnimationRejectsCanvasAbove4096(t *testing.T) {
	animatedWebP := validAnimatedWebPFixture(t)
	putUint24(animatedWebP[24:27], 4096)
	tests := []struct {
		name      string
		mediaType string
		data      []byte
	}{
		{name: "GIF width", mediaType: "image/gif", data: gifFixture(t, 4097, 1, []int{1}, -1)},
		{name: "WebP width", mediaType: "image/webp", data: animatedWebP},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := inspectGiftClipShortAnimation(test.mediaType, test.data); err == nil {
				t.Fatal("expected oversized canvas to be rejected")
			}
		})
	}
}

func TestGiftClipGIFRejectsDecodedFrameMemoryAboveFixedBudget(t *testing.T) {
	data := gifFixture(t, 4096, 4096, []int{1, 1}, -1)
	if len(data) > maxGiftAnimationBytes {
		t.Fatalf("compressed fixture is unexpectedly large: %d bytes", len(data))
	}
	if _, err := inspectGiftClipShortAnimation("image/gif", data); err == nil {
		t.Fatal("expected decoded GIF frame budget to be enforced")
	}
}

func TestGiftClipWebPInfoRejectsIncompleteFramePayloads(t *testing.T) {
	unpaddedNestedChunk := rawUnpaddedWebPChunk("VP8 ", []byte{0})
	if len(unpaddedNestedChunk) != 9 {
		t.Fatalf("raw odd-length nested chunk size = %d", len(unpaddedNestedChunk))
	}
	tests := []struct {
		name string
		data []byte
	}{
		{name: "missing encoded frame", data: animatedWebPFrame(320, 180, 40, nil)},
		{name: "truncated container", data: animatedWebPHeader(320, 180, 40)[:len(animatedWebPHeader(320, 180, 40))-1]},
		{name: "missing nested padding", data: animatedWebPFrame(320, 180, 40, unpaddedNestedChunk)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, _, err := giftClipWebPInfo(test.data); err == nil {
				t.Fatal("expected malformed WebP to be rejected")
			}
		})
	}
}

func TestGiftClipSourceResolvesTrustedFullEffect(t *testing.T) {
	store, receipt := giftClipSourceStore(t, &giftReceiptAnimation{
		GIF: "https://i0.hdslb.com/gift.gif", MP4: "https://i0.hdslb.com/effect.mp4", MP4JSON: "https://i0.hdslb.com/effect.json",
	})
	media := newGiftReceiptAPI(store, giftClipMediaClient(map[string]giftClipMediaResponse{
		"/effect.mp4":  {contentType: "video/mp4", body: validGiftClipMP4()},
		"/effect.json": {contentType: "application/json", body: []byte(`{"info":{"videoW":1088,"videoH":1280,"rgbFrame":[0,0,720,1280],"aFrame":[724,0,360,640],"fps":30,"f":75}}`)},
	}))

	source, err := newGiftClipSourceResolver(store, media).Resolve(context.Background(), receipt.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if source.Kind != giftClipSourceEffect || source.Playback != giftClipPlaybackEffect || source.Layout == nil || source.VisualWidth != 720 || source.VisualHeight != 1280 || source.Duration != 2500*time.Millisecond {
		t.Fatalf("source = %#v", source)
	}
	if data, err := os.ReadFile(source.Path); err != nil || !bytes.Equal(data, validGiftClipMP4()) {
		t.Fatalf("effect file data=%q err=%v", data, err)
	}
}

func TestGiftClipSourceFallsBackToGIFWhenEffectVideoIsInvalid(t *testing.T) {
	gifData := twoFrameGIF(t, 160, 90, []int{4, 7})
	store, receipt := giftClipSourceStore(t, &giftReceiptAnimation{
		GIF: "https://i0.hdslb.com/gift.gif", MP4: "https://i0.hdslb.com/effect.mp4", MP4JSON: "https://i0.hdslb.com/effect.json",
	})
	media := newGiftReceiptAPI(store, giftClipMediaClient(map[string]giftClipMediaResponse{
		"/effect.mp4":  {contentType: "video/mp4", body: []byte("not an mp4")},
		"/effect.json": {contentType: "application/json", body: []byte(`{"info":{"videoW":1088,"videoH":1280,"rgbFrame":[0,0,720,1280],"aFrame":[724,0,360,640],"fps":30,"f":75}}`)},
		"/gift.gif":    {contentType: "image/gif", body: gifData},
	}))

	source, err := newGiftClipSourceResolver(store, media).Resolve(context.Background(), receipt.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if source.Kind != giftClipSourceGIF || source.Playback != giftClipPlaybackAnimatedGIF || source.VisualWidth != 160 || source.VisualHeight != 90 || source.Duration != 110*time.Millisecond {
		t.Fatalf("source = %#v", source)
	}
}

func TestGiftClipSourceWritesNormalizedOwnedAnimationCopy(t *testing.T) {
	original := gifFixture(t, 16, 16, []int{2, 3}, -1)
	responseBody := bytes.Clone(original)
	store, receipt := giftClipSourceStore(t, &giftReceiptAnimation{GIF: "https://i0.hdslb.com/gift.gif"})
	media := newGiftReceiptAPI(store, giftClipMediaClient(map[string]giftClipMediaResponse{
		"/gift.gif": {contentType: "image/gif", body: responseBody},
	}))

	source, err := newGiftClipSourceResolver(store, media).Resolve(context.Background(), receipt.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(source.Path)
	if err != nil {
		t.Fatal(err)
	}
	if source.Kind != giftClipSourceGIF || source.Playback != giftClipPlaybackAnimatedGIF || !bytes.Equal(responseBody, original) || len(data) != len(original)+len(gifLoopExtension(0)) {
		t.Fatalf("source=%#v inputChanged=%t normalizedBytes=%d", source, !bytes.Equal(responseBody, original), len(data))
	}
}

func TestGiftClipSourceCancellationAfterFetchLeavesNoFiles(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store, receipt := giftClipSourceStore(t, &giftReceiptAnimation{GIF: "https://i0.hdslb.com/gift.gif"})
	media := newGiftReceiptAPI(store, giftMediaClientFunc(func(request *http.Request) (*http.Response, error) {
		cancel()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/gif"}},
			Body:       io.NopCloser(bytes.NewReader(gifFixture(t, 16, 16, []int{2, 3}, -1))),
			Request:    request,
		}, nil
	}))
	directory := t.TempDir()

	if _, err := newGiftClipSourceResolver(store, media).Resolve(ctx, receipt.ID, directory); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary files = %#v err=%v", entries, err)
	}
}

func TestGiftClipSourceUsesAnimationDurationBeforeDecodedCycle(t *testing.T) {
	gifData := twoFrameGIF(t, 160, 90, []int{4, 7})
	store, receipt := giftClipSourceStore(t, &giftReceiptAnimation{GIF: "https://i0.hdslb.com/gift.gif", DurationMS: 2400})
	media := newGiftReceiptAPI(store, giftClipMediaClient(map[string]giftClipMediaResponse{
		"/gift.gif": {contentType: "image/gif", body: gifData},
	}))

	source, err := newGiftClipSourceResolver(store, media).Resolve(context.Background(), receipt.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if source.Duration != 2400*time.Millisecond {
		t.Fatalf("duration = %s", source.Duration)
	}
}

func TestGiftClipSourceUsesDecodedCycleWhenReceiptDurationIsMissing(t *testing.T) {
	gifData := twoFrameGIF(t, 160, 90, []int{4, 7})
	store, receipt := giftClipSourceStore(t, &giftReceiptAnimation{GIF: "https://i0.hdslb.com/gift.gif"})
	media := newGiftReceiptAPI(store, giftClipMediaClient(map[string]giftClipMediaResponse{
		"/gift.gif": {contentType: "image/gif", body: gifData},
	}))

	source, err := newGiftClipSourceResolver(store, media).Resolve(context.Background(), receipt.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if source.Duration != 110*time.Millisecond {
		t.Fatalf("duration = %s", source.Duration)
	}
}

func TestGiftReceiptAnimationPreservesMissingDurationForDecoderTiming(t *testing.T) {
	animation := giftReceiptAnimationFromEvent(giftEvent{EffectID: 1, AnimationGIF: "https://i0.hdslb.com/gift.gif"})
	if animation == nil || animation.DurationMS != 0 {
		t.Fatalf("animation = %#v", animation)
	}
}

func TestGiftClipSourceRejectsUnsafeOrInvalidMediaWithoutFiles(t *testing.T) {
	tests := []struct {
		name      string
		animation *giftReceiptAnimation
		responses map[string]giftClipMediaResponse
	}{
		{name: "missing receipt", animation: nil},
		{name: "unsafe URL", animation: &giftReceiptAnimation{GIF: "https://example.com/gift.gif"}},
		{name: "oversized body", animation: &giftReceiptAnimation{GIF: "https://i0.hdslb.com/gift.gif"}, responses: map[string]giftClipMediaResponse{"/gift.gif": {contentType: "image/gif", body: []byte("x"), contentLength: maxGiftAnimationBytes + 1}}},
		{name: "invalid webp", animation: &giftReceiptAnimation{WebP: "https://i0.hdslb.com/gift.webp"}, responses: map[string]giftClipMediaResponse{"/gift.webp": {contentType: "image/webp", body: []byte("not a webp")}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, receipt := giftClipSourceStore(t, test.animation)
			media := newGiftReceiptAPI(store, giftClipMediaClient(test.responses))
			directory := t.TempDir()
			receiptID := receipt.ID
			if test.name == "missing receipt" {
				receiptID = "missing"
			}
			if _, err := newGiftClipSourceResolver(store, media).Resolve(context.Background(), receiptID, directory); err == nil {
				t.Fatal("expected resolution failure")
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 0 {
				t.Fatalf("temporary files = %#v err=%v", entries, err)
			}
		})
	}
}

func TestWriteGiftClipSourcePreservesAnExistingDestination(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "animation.gif")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeGiftClipSource(directory, "animation.gif", []byte("replacement")); err == nil {
		t.Fatal("expected existing destination to be rejected")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "existing" {
		t.Fatalf("destination data=%q err=%v", data, err)
	}
}

func TestWriteGiftClipSourceDoesNotReplaceDestinationCreatedBeforeInstallation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "animation.gif")
	if _, err := writeGiftClipSourceWithBeforeInstall(directory, "animation.gif", []byte("replacement"), func() {
		if err := os.WriteFile(path, []byte("concurrent"), 0o600); err != nil {
			t.Fatal(err)
		}
	}); err == nil {
		t.Fatal("expected destination created during installation to be rejected")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "concurrent" {
		t.Fatalf("destination data=%q err=%v", data, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "animation.gif" {
		t.Fatalf("directory entries=%#v err=%v", entries, err)
	}
}

func TestWriteGiftClipSourceKeepsCommittedFinalWhenStagingCleanupFails(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "animation.gif")
	returnedPath, err := writeGiftClipSourceWithHooks(directory, "animation.gif", []byte("installed"), giftClipSourceWriteHooks{
		removePartial: func(string) error {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("peer"), 0o600); err != nil {
				t.Fatal(err)
			}
			return errors.New("simulated staging cleanup failure")
		},
	})
	if err != nil || returnedPath != path {
		t.Fatalf("path=%q err=%v", returnedPath, err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "peer" {
		t.Fatalf("final data=%q err=%v", data, readErr)
	}
}

func twoFrameGIF(t *testing.T, width, height int, delays []int) []byte {
	return gifFixture(t, width, height, delays, 0)
}

func gifFixture(t *testing.T, width, height int, delays []int, loopCount int) []byte {
	t.Helper()
	palette := color.Palette{color.Black, color.White}
	frames := make([]*image.Paletted, len(delays))
	for index := range frames {
		frame := image.NewPaletted(image.Rect(0, 0, width, height), palette)
		frame.SetColorIndex(index%width, 0, 1)
		frames[index] = frame
	}
	var output bytes.Buffer
	if err := gif.EncodeAll(&output, &gif.GIF{Image: frames, Delay: delays, LoopCount: loopCount, Config: image.Config{Width: width, Height: height, ColorModel: palette}}); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func gifLoopExtension(loopCount int) []byte {
	return []byte{0x21, 0xff, 0x0b, 'N', 'E', 'T', 'S', 'C', 'A', 'P', 'E', '2', '.', '0', 0x03, 0x01, byte(loopCount), byte(loopCount >> 8), 0x00}
}

func validStaticLossyWebPFixture(t *testing.T) []byte {
	t.Helper()
	return decodeGiftClipFixture(t, "UklGRiQAAABXRUJQVlA4IBgAAAAwAQCdASoQABAAAgA0JaQAA3AA/vv9UAA=")
}

func validStaticLosslessWebPFixture(t *testing.T) []byte {
	t.Helper()
	return decodeGiftClipFixture(t, "UklGRhoAAABXRUJQVlA4TA4AAAAvD8ADAAcQEf0PRET/Aw==")
}

func validAnimatedWebPFixture(t *testing.T) []byte {
	t.Helper()
	return decodeGiftClipFixture(t, "UklGRsAAAABXRUJQVlA4WAoAAAACAAAADwAADwAAQU5JTQYAAAD/////AABBTk1GSAAAAAAAAAAAAA8AAA8AAMgAAAJWUDggMAAAANABAJ0BKhAAEAACADQloAJ0ugH4AAOwAP7w6Pf/ILlhdcjX/yA/4yp8ZU/48gAAAEFOTUZEAAAAAAAAAAAADwAADwAAyAAAAFZQOCAsAAAAlAEAnQEqEAAQAAAANCWgAnS6AAOYAP75k2//kB//kB//kB//ID/iF3sgMAA=")
}

func decodeGiftClipFixture(t *testing.T, encoded string) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func staticWebPWithVP8X(t *testing.T, static []byte, width, height int) []byte {
	t.Helper()
	if len(static) < 12 {
		t.Fatal("static WebP fixture is truncated")
	}
	vp8x := make([]byte, 10)
	putUint24(vp8x[4:7], width-1)
	putUint24(vp8x[7:10], height-1)
	return insertWebPChunk(t, static, 12, "VP8X", vp8x)
}

func insertWebPChunk(t *testing.T, data []byte, offset int, kind string, payload []byte) []byte {
	t.Helper()
	if offset < 12 || offset > len(data) || len(kind) != 4 {
		t.Fatal("invalid WebP fixture insertion")
	}
	var chunk bytes.Buffer
	writeWebPChunk(&chunk, kind, payload)
	result := make([]byte, 0, len(data)+chunk.Len())
	result = append(result, data[:offset]...)
	result = append(result, chunk.Bytes()...)
	result = append(result, data[offset:]...)
	binary.LittleEndian.PutUint32(result[4:8], uint32(len(result)-8))
	return result
}

func appendWebPChunk(t *testing.T, data []byte, kind string, payload []byte) []byte {
	t.Helper()
	return insertWebPChunk(t, data, len(data), kind, payload)
}

func insertFirstWebPFrameChunk(t *testing.T, data []byte, kind string, payload []byte) []byte {
	t.Helper()
	const firstANMFOffset = 44
	const firstANMFNestedOffset = 68
	if len(data) < firstANMFNestedOffset || string(data[firstANMFOffset:firstANMFOffset+4]) != "ANMF" {
		t.Fatal("animated WebP fixture layout changed")
	}
	result := insertWebPChunk(t, data, firstANMFNestedOffset, kind, payload)
	insertedBytes := len(result) - len(data)
	binary.LittleEndian.PutUint32(result[firstANMFOffset+4:firstANMFOffset+8], binary.LittleEndian.Uint32(data[firstANMFOffset+4:firstANMFOffset+8])+uint32(insertedBytes))
	return result
}

func animatedWebPHeader(width, height int, delays ...int) []byte {
	frames := make([][]byte, len(delays))
	for index, delay := range delays {
		frames[index] = animatedWebPFrameData(width, height, delay, webPChunkData("VP8 ", validWebPVP8FrameHeader()))
	}
	return animatedWebP(width, height, frames...)
}

func animatedWebPFrame(width, height, delay int, payload []byte) []byte {
	return animatedWebP(width, height, animatedWebPFrameData(width, height, delay, payload))
}

func animatedWebP(width, height int, frames ...[]byte) []byte {
	var payload bytes.Buffer
	payload.WriteString("WEBP")
	vp8x := make([]byte, 10)
	vp8x[0] = 0x02
	putUint24(vp8x[4:7], width-1)
	putUint24(vp8x[7:10], height-1)
	writeWebPChunk(&payload, "VP8X", vp8x)
	writeWebPChunk(&payload, "ANIM", make([]byte, 6))
	for _, frame := range frames {
		writeWebPChunk(&payload, "ANMF", frame)
	}
	result := make([]byte, 8+payload.Len())
	copy(result, "RIFF")
	binary.LittleEndian.PutUint32(result[4:8], uint32(payload.Len()))
	copy(result[8:], payload.Bytes())
	return result
}

func animatedWebPFrameData(width, height, delay int, payload []byte) []byte {
	frame := make([]byte, 16, 16+len(payload))
	putUint24(frame[6:9], width-1)
	putUint24(frame[9:12], height-1)
	putUint24(frame[12:15], delay)
	return append(frame, payload...)
}

func webPChunkData(kind string, data []byte) []byte {
	var output bytes.Buffer
	writeWebPChunk(&output, kind, data)
	return output.Bytes()
}

func rawUnpaddedWebPChunk(kind string, data []byte) []byte {
	chunk := make([]byte, 8, 8+len(data))
	copy(chunk, kind)
	binary.LittleEndian.PutUint32(chunk[4:8], uint32(len(data)))
	return append(chunk, data...)
}

func validWebPVP8FrameHeader() []byte {
	return []byte{0, 0, 0, 0x9d, 0x01, 0x2a, 0x40, 0x01, 0xb4, 0x00}
}

func writeWebPChunk(output *bytes.Buffer, kind string, data []byte) {
	output.WriteString(kind)
	_ = binary.Write(output, binary.LittleEndian, uint32(len(data)))
	output.Write(data)
	if len(data)%2 != 0 {
		output.WriteByte(0)
	}
}

func putUint24(target []byte, value int) {
	target[0] = byte(value)
	target[1] = byte(value >> 8)
	target[2] = byte(value >> 16)
}

type giftClipMediaResponse struct {
	contentType   string
	body          []byte
	contentLength int64
}

func giftClipMediaClient(responses map[string]giftClipMediaResponse) giftMediaClientFunc {
	return func(request *http.Request) (*http.Response, error) {
		response, ok := responses[request.URL.Path]
		if !ok {
			return mediaResponse(request, http.StatusNotFound, "text/plain", "missing"), nil
		}
		length := response.contentLength
		if length == 0 {
			length = int64(len(response.body))
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{response.contentType}}, Body: io.NopCloser(bytes.NewReader(response.body)), ContentLength: length, Request: request}, nil
	}
}

func giftClipSourceStore(t *testing.T, animation *giftReceiptAnimation) (*configStore, giftReceipt) {
	t.Helper()
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	receipt := giftReceipt{ID: "gift-clip", GiftID: 1, GiftName: "礼物", Effects: []giftReceiptEffect{}, Animation: animation}
	state := defaultAppState()
	state.GiftReceipts = []giftReceipt{receipt}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	return store, receipt
}

func validGiftClipMP4() []byte {
	return []byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2")
}
