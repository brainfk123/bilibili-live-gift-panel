package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const giftClipHTTPTestID = "AQIDBAUGBwgJCgsMDQ4PEBES"

type giftClipHTTPStore struct {
	created   giftClipJobSnapshot
	createErr error
	jobs      map[string]giftClipJobSnapshot
	videos    map[string]string
}

func (store *giftClipHTTPStore) Create(_ context.Context, receiptID string, crop giftClipCrop, background, overlay []byte) (giftClipJobSnapshot, error) {
	if store.createErr != nil {
		return giftClipJobSnapshot{}, store.createErr
	}
	if receiptID != "receipt-1" || crop != (giftClipCrop{X: 100, Y: 50, Width: 960, Height: 540}) || len(background) == 0 || len(overlay) == 0 {
		return giftClipJobSnapshot{}, errGiftClipHTTPBadFakeRequest
	}
	return store.created, nil
}

func (store *giftClipHTTPStore) Snapshot(id string) (giftClipJobSnapshot, bool) {
	snapshot, ok := store.jobs[id]
	return snapshot, ok
}

func (store *giftClipHTTPStore) VideoPath(id string) (string, bool) {
	path, ok := store.videos[id]
	return path, ok
}

func (store *giftClipHTTPStore) Cancel(id string) bool {
	_, ok := store.jobs[id]
	return ok
}

var errGiftClipHTTPBadFakeRequest = &giftClipHTTPTestError{}

type giftClipHTTPTestError struct{}

func (*giftClipHTTPTestError) Error() string { return "unexpected fake request" }

func TestGiftClipHTTPCreateAndStatus(t *testing.T) {
	store := &giftClipHTTPStore{
		created: giftClipJobSnapshot{ID: giftClipHTTPTestID, State: giftClipJobQueued, Width: 960, Height: 540, FPS: 30, Message: "等待导出。"},
		jobs:    map[string]giftClipJobSnapshot{giftClipHTTPTestID: {ID: giftClipHTTPTestID, State: giftClipJobEncoding, Progress: .5, Width: 960, Height: 540, FPS: 30}},
	}
	api := newGiftClipHTTPHandler(store)

	response := httptest.NewRecorder()
	api.ServeHTTP(response, newGiftClipMultipartRequest(t, `{"receiptId":"receipt-1","crop":{"x":100,"y":50,"width":960,"height":540},"version":1}`, validGiftClipHTTPPNG(t, 960, 540, false), validGiftClipHTTPPNG(t, 960, 540, true)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var created map[string]any
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created["id"] != giftClipHTTPTestID || created["state"] != string(giftClipJobQueued) || created["width"] != float64(960) || created["height"] != float64(540) || created["fps"] != float64(30) {
		t.Fatalf("create body=%#v", created)
	}

	status := httptest.NewRecorder()
	api.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/gift-clips/"+giftClipHTTPTestID, nil))
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"state":"encoding"`) || !strings.Contains(status.Body.String(), `"progress":0.5`) {
		t.Fatalf("status=%d body=%s", status.Code, status.Body.String())
	}
}

func TestGiftClipHTTPRejectsUnsafeCreateRequests(t *testing.T) {
	store := &giftClipHTTPStore{created: giftClipJobSnapshot{ID: giftClipHTTPTestID, State: giftClipJobQueued}, jobs: map[string]giftClipJobSnapshot{}}
	api := newGiftClipHTTPHandler(store)
	validMetadata := `{"receiptId":"receipt-1","crop":{"x":100,"y":50,"width":960,"height":540},"version":1}`
	background := validGiftClipHTTPPNG(t, 960, 540, false)
	overlay := validGiftClipHTTPPNG(t, 960, 540, true)

	for _, test := range []struct {
		name    string
		request *http.Request
		want    int
	}{
		{name: "wrong method", request: httptest.NewRequest(http.MethodPut, "/api/gift-clips", nil), want: http.StatusMethodNotAllowed},
		{name: "cross site fetch", request: giftClipHTTPCrossSiteRequest(t, validMetadata, background, overlay, "", "cross-site"), want: http.StatusForbidden},
		{name: "cross site origin", request: giftClipHTTPCrossSiteRequest(t, validMetadata, background, overlay, "https://attacker.invalid", ""), want: http.StatusForbidden},
		{name: "missing part", request: newGiftClipMultipartWithParts(t, map[string][]byte{"metadata": []byte(validMetadata), "background": background}), want: http.StatusBadRequest},
		{name: "duplicate part", request: newGiftClipMultipartWithPartList(t, []giftClipHTTPPart{{"metadata", []byte(validMetadata)}, {"background", background}, {"overlay", overlay}, {"overlay", overlay}}), want: http.StatusBadRequest},
		{name: "unknown part", request: newGiftClipMultipartWithPartList(t, []giftClipHTTPPart{{"metadata", []byte(validMetadata)}, {"background", background}, {"overlay", overlay}, {"extra", []byte("x")}}), want: http.StatusBadRequest},
		{name: "unknown metadata field", request: newGiftClipMultipartRequest(t, validMetadata[:len(validMetadata)-1]+`,"extra":true}`, background, overlay), want: http.StatusBadRequest},
		{name: "bad version", request: newGiftClipMultipartRequest(t, strings.Replace(validMetadata, `"version":1`, `"version":2`, 1), background, overlay), want: http.StatusBadRequest},
		{name: "odd dimensions", request: newGiftClipMultipartRequest(t, strings.Replace(validMetadata, `"width":960`, `"width":961`, 1), validGiftClipHTTPPNG(t, 961, 540, false), validGiftClipHTTPPNG(t, 961, 540, true)), want: http.StatusBadRequest},
		{name: "bad png", request: newGiftClipMultipartRequest(t, validMetadata, []byte("not a png"), overlay), want: http.StatusBadRequest},
		{name: "trailing metadata value", request: newGiftClipMultipartRequest(t, validMetadata+` {}`, background, overlay), want: http.StatusBadRequest},
		{name: "too large", request: newGiftClipMultipartRequest(t, validMetadata, background, append(overlay, bytes.Repeat([]byte("x"), 32<<20)...)), want: http.StatusRequestEntityTooLarge},
		{name: "too large unknown part", request: newGiftClipMultipartWithPartList(t, []giftClipHTTPPart{{"extra", bytes.Repeat([]byte("x"), 32<<20)}}), want: http.StatusRequestEntityTooLarge},
		{name: "unknown part does not mask oversized epilogue", request: newGiftClipMultipartPartListWithEpilogue(t, []giftClipHTTPPart{{"extra", []byte("x")}}, int(maxGiftClipRequestBytes)), want: http.StatusRequestEntityTooLarge},
		{name: "known length oversized epilogue", request: newGiftClipMultipartWithEpilogue(t, validMetadata, background, overlay, int(maxGiftClipRequestBytes), true), want: http.StatusRequestEntityTooLarge},
		{name: "unknown length oversized epilogue", request: newGiftClipMultipartWithEpilogue(t, validMetadata, background, overlay, int(maxGiftClipRequestBytes), false), want: http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			api.ServeHTTP(response, test.request)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "unexpected fake request") {
				t.Fatalf("leaked internal error: %s", response.Body.String())
			}
		})
	}
	exactLimit := newGiftClipMultipartWithEpilogue(t, validMetadata, background, overlay, 0, true)
	exactPadding := int(maxGiftClipRequestBytes - exactLimit.ContentLength)
	exactLimit = newGiftClipMultipartWithEpilogue(t, validMetadata, background, overlay, exactPadding, true)
	if exactLimit.ContentLength != maxGiftClipRequestBytes {
		t.Fatalf("exact request ContentLength=%d want=%d", exactLimit.ContentLength, maxGiftClipRequestBytes)
	}
	exactResponse := httptest.NewRecorder()
	api.ServeHTTP(exactResponse, exactLimit)
	if exactResponse.Code != http.StatusAccepted {
		t.Fatalf("exactly %d byte request status=%d body=%s", maxGiftClipRequestBytes, exactResponse.Code, exactResponse.Body.String())
	}
}

func TestGiftClipHTTPRejectsMalformedMIMEWithoutReadingBody(t *testing.T) {
	for _, contentType := range []string{"text/plain", "multipart/form-data; boundary=" + strings.Repeat("x", 71)} {
		body := &giftClipHTTPUnreadableBody{}
		request := httptest.NewRequest(http.MethodPost, "/api/gift-clips", body)
		request.Header.Set("Content-Type", contentType)
		response := httptest.NewRecorder()
		newGiftClipHTTPHandler(&giftClipHTTPStore{}).ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || body.reads != 0 {
			t.Fatalf("type=%q status=%d reads=%d", contentType, response.Code, body.reads)
		}
	}
}

func TestGiftClipHTTPCancelledBodyReturns(t *testing.T) {
	context, cancel := context.WithCancel(context.Background())
	cancel()
	body := &giftClipHTTPContextBody{context: context}
	request := httptest.NewRequest(http.MethodPost, "/api/gift-clips", body).WithContext(context)
	request.Header.Set("Content-Type", "multipart/form-data; boundary=valid-boundary")
	response := httptest.NewRecorder()
	newGiftClipHTTPHandler(&giftClipHTTPStore{}).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || body.reads != 1 {
		t.Fatalf("status=%d reads=%d", response.Code, body.reads)
	}
}

func TestGiftClipHTTPCreateErrorDoesNotLeakDetails(t *testing.T) {
	store := &giftClipHTTPStore{createErr: errors.New(`C:\private\secret.mp4: secret-value`), jobs: map[string]giftClipJobSnapshot{}}
	response := httptest.NewRecorder()
	newGiftClipHTTPHandler(store).ServeHTTP(response, newGiftClipMultipartRequest(t, `{"receiptId":"receipt-1","crop":{"x":100,"y":50,"width":960,"height":540},"version":1}`, validGiftClipHTTPPNG(t, 960, 540, false), validGiftClipHTTPPNG(t, 960, 540, true)))
	if response.Code != http.StatusInternalServerError || response.Header().Get("Content-Type") != "application/json; charset=utf-8" || strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Body.String(), "private") {
		t.Fatalf("create error status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestGiftClipHTTPVideoAndDelete(t *testing.T) {
	directory := t.TempDir()
	videoPath := filepath.Join(directory, "private-output.mp4")
	if err := os.WriteFile(videoPath, []byte("mp4-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &giftClipHTTPStore{jobs: map[string]giftClipJobSnapshot{giftClipHTTPTestID: {ID: giftClipHTTPTestID, State: giftClipJobEncoding}}, videos: map[string]string{}}
	api := newGiftClipHTTPHandler(store)

	for _, id := range []string{"short", strings.Repeat("a", len(giftClipHTTPTestID))} {
		response := httptest.NewRecorder()
		api.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/gift-clips/"+id, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("guessed id %q status=%d", id, response.Code)
		}
	}
	notReady := httptest.NewRecorder()
	api.ServeHTTP(notReady, httptest.NewRequest(http.MethodGet, "/api/gift-clips/"+giftClipHTTPTestID+"/video", nil))
	if notReady.Code != http.StatusNotFound {
		t.Fatalf("not-ready video status=%d body=%s", notReady.Code, notReady.Body.String())
	}
	store.jobs[giftClipHTTPTestID] = giftClipJobSnapshot{ID: giftClipHTTPTestID, State: giftClipJobReady}
	store.videos[giftClipHTTPTestID] = videoPath
	video := httptest.NewRecorder()
	api.ServeHTTP(video, httptest.NewRequest(http.MethodGet, "/api/gift-clips/"+giftClipHTTPTestID+"/video", nil))
	if video.Code != http.StatusOK || video.Header().Get("Content-Type") != "video/mp4" || video.Header().Get("Cache-Control") != "no-store" || video.Header().Get("Content-Disposition") != `attachment; filename="gift-clip.mp4"` || video.Body.String() != "mp4-data" {
		t.Fatalf("video status=%d headers=%v body=%q", video.Code, video.Header(), video.Body.String())
	}
	rangedRequest := httptest.NewRequest(http.MethodGet, "/api/gift-clips/"+giftClipHTTPTestID+"/video", nil)
	rangedRequest.Header.Set("Range", "bytes=0-3")
	ranged := httptest.NewRecorder()
	api.ServeHTTP(ranged, rangedRequest)
	if ranged.Code != http.StatusPartialContent || ranged.Body.String() != "mp4-" || ranged.Header().Get("Content-Range") != "bytes 0-3/8" || ranged.Header().Get("Accept-Ranges") != "bytes" || ranged.Header().Get("Content-Type") != "video/mp4" || ranged.Header().Get("Cache-Control") != "no-store" || ranged.Header().Get("Content-Disposition") != `attachment; filename="gift-clip.mp4"` {
		t.Fatalf("range status=%d headers=%v body=%q", ranged.Code, ranged.Header(), ranged.Body.String())
	}
	unsatisfiableRequest := httptest.NewRequest(http.MethodGet, "/api/gift-clips/"+giftClipHTTPTestID+"/video", nil)
	unsatisfiableRequest.Header.Set("Range", "bytes=100-101")
	unsatisfiable := httptest.NewRecorder()
	api.ServeHTTP(unsatisfiable, unsatisfiableRequest)
	if unsatisfiable.Code != http.StatusRequestedRangeNotSatisfiable || unsatisfiable.Header().Get("Content-Range") != "bytes */8" || unsatisfiable.Header().Get("Content-Type") != "video/mp4" || unsatisfiable.Header().Get("Cache-Control") != "no-store" || unsatisfiable.Header().Get("Content-Disposition") != `attachment; filename="gift-clip.mp4"` {
		t.Fatalf("unsatisfiable status=%d headers=%v", unsatisfiable.Code, unsatisfiable.Header())
	}
	nonregular := httptest.NewRecorder()
	store.videos[giftClipHTTPTestID] = directory
	api.ServeHTTP(nonregular, httptest.NewRequest(http.MethodGet, "/api/gift-clips/"+giftClipHTTPTestID+"/video", nil))
	if nonregular.Code != http.StatusNotFound {
		t.Fatalf("nonregular video status=%d body=%s", nonregular.Code, nonregular.Body.String())
	}
	store.videos[giftClipHTTPTestID] = videoPath

	store.videos = map[string]string{}
	unready := httptest.NewRecorder()
	api.ServeHTTP(unready, httptest.NewRequest(http.MethodGet, "/api/gift-clips/"+giftClipHTTPTestID+"/video", nil))
	if unready.Code != http.StatusNotFound {
		t.Fatalf("not ready status=%d", unready.Code)
	}
	for _, origin := range []string{"", "https://attacker.invalid"} {
		request := httptest.NewRequest(http.MethodDelete, "/api/gift-clips/"+giftClipHTTPTestID, nil)
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		want := http.StatusNoContent
		if origin != "" {
			want = http.StatusForbidden
		}
		if response.Code != want {
			t.Fatalf("delete origin=%q status=%d want=%d", origin, response.Code, want)
		}
	}
	unknown := httptest.NewRecorder()
	api.ServeHTTP(unknown, httptest.NewRequest(http.MethodDelete, "/api/gift-clips/"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 18)), nil))
	if unknown.Code != http.StatusNoContent {
		t.Fatalf("idempotent delete status=%d", unknown.Code)
	}
}

type giftClipHTTPPart struct {
	name string
	data []byte
}

type giftClipHTTPUnreadableBody struct{ reads int }

func (body *giftClipHTTPUnreadableBody) Read([]byte) (int, error) {
	body.reads++
	return 0, errors.New("malformed request body was read")
}

func (*giftClipHTTPUnreadableBody) Close() error { return nil }

type giftClipHTTPContextBody struct {
	context context.Context
	reads   int
}

func (body *giftClipHTTPContextBody) Read([]byte) (int, error) {
	body.reads++
	return 0, body.context.Err()
}

func (*giftClipHTTPContextBody) Close() error { return nil }

func newGiftClipMultipartRequest(t *testing.T, metadata string, background, overlay []byte) *http.Request {
	t.Helper()
	return newGiftClipMultipartWithParts(t, map[string][]byte{"metadata": []byte(metadata), "background": background, "overlay": overlay})
}

func newGiftClipMultipartWithParts(t *testing.T, parts map[string][]byte) *http.Request {
	t.Helper()
	ordered := []giftClipHTTPPart{}
	for _, name := range []string{"metadata", "background", "overlay"} {
		if data, ok := parts[name]; ok {
			ordered = append(ordered, giftClipHTTPPart{name, data})
		}
	}
	return newGiftClipMultipartWithPartList(t, ordered)
}

func newGiftClipMultipartWithPartList(t *testing.T, parts []giftClipHTTPPart) *http.Request {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for _, part := range parts {
		field, err := writer.CreateFormField(part.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := field.Write(part.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/gift-clips", body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func newGiftClipMultipartWithEpilogue(t *testing.T, metadata string, background, overlay []byte, epilogueBytes int, knownLength bool) *http.Request {
	t.Helper()
	return newGiftClipMultipartPartListWithEpilogue(t, []giftClipHTTPPart{{"metadata", []byte(metadata)}, {"background", background}, {"overlay", overlay}}, epilogueBytes, knownLength)
}

func newGiftClipMultipartPartListWithEpilogue(t *testing.T, parts []giftClipHTTPPart, epilogueBytes int, knownLength ...bool) *http.Request {
	t.Helper()
	request := newGiftClipMultipartWithPartList(t, parts)
	contentType := request.Header.Get("Content-Type")
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if epilogueBytes > 0 {
		body = append(body, bytes.Repeat([]byte("x"), epilogueBytes)...)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/gift-clips", bytes.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	if len(knownLength) > 0 && !knownLength[0] {
		request.ContentLength = -1
	}
	return request
}

func giftClipHTTPCrossSiteRequest(t *testing.T, metadata string, background, overlay []byte, origin, fetchSite string) *http.Request {
	t.Helper()
	request := newGiftClipMultipartRequest(t, metadata, background, overlay)
	request.Header.Set("Origin", origin)
	request.Header.Set("Sec-Fetch-Site", fetchSite)
	return request
}

func validGiftClipHTTPPNG(t *testing.T, width, height int, transparent bool) []byte {
	t.Helper()
	imageValue := image.NewNRGBA(image.Rect(0, 0, width, height))
	fill := color.NRGBA{R: 10, G: 20, B: 30, A: 255}
	if transparent {
		fill.A = 0
	}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			imageValue.SetNRGBA(x, y, fill)
		}
	}
	buffer := &bytes.Buffer{}
	if err := png.Encode(buffer, imageValue); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
