package main

import (
	"bytes"
	"context"
	"encoding/binary"
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
	width, height, cycle, err := giftClipWebPInfo(animatedWebPHeader(320, 180, 40, 70))
	if err != nil {
		t.Fatal(err)
	}
	if width != 320 || height != 180 || cycle != 110*time.Millisecond {
		t.Fatalf("info = %dx%d %s", width, height, cycle)
	}
}

func TestGiftClipWebPInfoClampsShortFrameDelay(t *testing.T) {
	_, _, cycle, err := giftClipWebPInfo(animatedWebPHeader(1, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	if cycle != 10*time.Millisecond {
		t.Fatalf("cycle = %s", cycle)
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
	if source.Kind != giftClipSourceEffect || source.Layout == nil || source.VisualWidth != 720 || source.VisualHeight != 1280 || source.Duration != 2500*time.Millisecond {
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
	if source.Kind != giftClipSourceGIF || source.VisualWidth != 160 || source.VisualHeight != 90 || source.Duration != 110*time.Millisecond {
		t.Fatalf("source = %#v", source)
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

func twoFrameGIF(t *testing.T, width, height int, delays []int) []byte {
	t.Helper()
	palette := color.Palette{color.Black, color.White}
	frames := make([]*image.Paletted, len(delays))
	for index := range frames {
		frame := image.NewPaletted(image.Rect(0, 0, width, height), palette)
		frame.SetColorIndex(index%width, 0, 1)
		frames[index] = frame
	}
	var output bytes.Buffer
	if err := gif.EncodeAll(&output, &gif.GIF{Image: frames, Delay: delays, Config: image.Config{Width: width, Height: height, ColorModel: palette}}); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func animatedWebPHeader(width, height int, delays ...int) []byte {
	var payload bytes.Buffer
	payload.WriteString("WEBP")
	vp8x := make([]byte, 10)
	putUint24(vp8x[4:7], width-1)
	putUint24(vp8x[7:10], height-1)
	writeWebPChunk(&payload, "VP8X", vp8x)
	writeWebPChunk(&payload, "ANIM", make([]byte, 6))
	for _, delay := range delays {
		frame := make([]byte, 16)
		putUint24(frame[6:9], width-1)
		putUint24(frame[9:12], height-1)
		putUint24(frame[12:15], delay)
		writeWebPChunk(&payload, "ANMF", frame)
	}
	result := make([]byte, 8+payload.Len())
	copy(result, "RIFF")
	binary.LittleEndian.PutUint32(result[4:8], uint32(payload.Len()))
	copy(result[8:], payload.Bytes())
	return result
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
