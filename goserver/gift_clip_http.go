package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
)

const (
	maxGiftClipRequestBytes int64 = 32 << 20
	giftClipJobIDBytes            = 18
	giftClipJobIDLength           = 24
)

type giftClipCreateMetadata struct {
	ReceiptID string       `json:"receiptId"`
	Crop      giftClipCrop `json:"crop"`
	Version   int          `json:"version"`
}

type giftClipHTTPJobs interface {
	Create(context.Context, string, giftClipCrop, []byte, []byte) (giftClipJobSnapshot, error)
	Snapshot(string) (giftClipJobSnapshot, bool)
	VideoPath(string) (string, bool)
	Cancel(string) bool
}

type giftClipHTTPHandler struct {
	jobs giftClipHTTPJobs
}

func newGiftClipHTTPHandler(jobs giftClipHTTPJobs) http.Handler {
	return &giftClipHTTPHandler{jobs: jobs}
}

func (handler *giftClipHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/gift-clips" {
		handler.handleCollection(w, r)
		return
	}
	const prefix = "/api/gift-clips/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	remainder := strings.TrimPrefix(r.URL.Path, prefix)
	if strings.Contains(remainder, "/") {
		id, suffix, ok := strings.Cut(remainder, "/")
		if !ok || suffix != "video" || !validGiftClipHTTPID(id) {
			giftClipHTTPNotFound(w)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			giftClipHTTPError(w, http.StatusMethodNotAllowed, "不支持的请求方法")
			return
		}
		handler.handleVideo(w, r, id)
		return
	}
	if !validGiftClipHTTPID(remainder) {
		giftClipHTTPNotFound(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		handler.handleStatus(w, remainder)
	case http.MethodDelete:
		if !isSameOriginGiftReceiptRequest(r) {
			giftClipHTTPError(w, http.StatusForbidden, "拒绝跨站请求")
			return
		}
		handler.handleDelete(w, remainder)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodDelete)
		giftClipHTTPError(w, http.StatusMethodNotAllowed, "不支持的请求方法")
	}
}

func (handler *giftClipHTTPHandler) handleCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		giftClipHTTPError(w, http.StatusMethodNotAllowed, "不支持的请求方法")
		return
	}
	if !isSameOriginGiftReceiptRequest(r) {
		giftClipHTTPError(w, http.StatusForbidden, "拒绝跨站请求")
		return
	}
	metadata, background, overlay, err := parseGiftClipCreateRequest(w, r)
	if err != nil {
		if errors.Is(err, errGiftClipHTTPRequestTooLarge) {
			giftClipHTTPError(w, http.StatusRequestEntityTooLarge, "请求内容过大")
			return
		}
		giftClipHTTPError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}
	if handler.jobs == nil {
		giftClipHTTPError(w, http.StatusServiceUnavailable, "视频导出任务暂时不可用")
		return
	}
	snapshot, err := handler.jobs.Create(r.Context(), metadata.ReceiptID, metadata.Crop, background, overlay)
	if err != nil {
		giftClipHTTPError(w, http.StatusInternalServerError, "视频生成失败，请重试。")
		return
	}
	writeJSON(w, http.StatusAccepted, giftClipHTTPSnapshot(snapshot))
}

func (handler *giftClipHTTPHandler) handleStatus(w http.ResponseWriter, id string) {
	if handler.jobs == nil {
		giftClipHTTPNotFound(w)
		return
	}
	snapshot, ok := handler.jobs.Snapshot(id)
	if !ok {
		giftClipHTTPNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, giftClipHTTPSnapshot(snapshot))
}

func (handler *giftClipHTTPHandler) handleVideo(w http.ResponseWriter, r *http.Request, id string) {
	if handler.jobs == nil {
		giftClipHTTPNotFound(w)
		return
	}
	path, ok := handler.jobs.VideoPath(id)
	if !ok {
		giftClipHTTPNotFound(w)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		giftClipHTTPNotFound(w)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		giftClipHTTPNotFound(w)
		return
	}
	http.ServeContent(&giftClipVideoResponseWriter{ResponseWriter: w}, r, "gift-clip.mp4", info.ModTime(), file)
}

type giftClipVideoResponseWriter struct{ http.ResponseWriter }

func (writer *giftClipVideoResponseWriter) WriteHeader(status int) {
	writer.setSafeVideoHeaders()
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *giftClipVideoResponseWriter) Write(data []byte) (int, error) {
	writer.setSafeVideoHeaders()
	return writer.ResponseWriter.Write(data)
}

func (writer *giftClipVideoResponseWriter) setSafeVideoHeaders() {
	header := writer.Header()
	mediaType, _, _ := mime.ParseMediaType(header.Get("Content-Type"))
	if !strings.EqualFold(mediaType, "multipart/byteranges") {
		header.Set("Content-Type", "video/mp4")
	}
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Disposition", `attachment; filename="gift-clip.mp4"`)
}

func (handler *giftClipHTTPHandler) handleDelete(w http.ResponseWriter, id string) {
	if handler.jobs != nil {
		handler.jobs.Cancel(id)
	}
	w.WriteHeader(http.StatusNoContent)
}

func parseGiftClipCreateRequest(w http.ResponseWriter, r *http.Request) (giftClipCreateMetadata, []byte, []byte, error) {
	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	boundary := parameters["boundary"]
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") || !validGiftClipHTTPBoundary(boundary) {
		return giftClipCreateMetadata{}, nil, nil, errors.New("invalid multipart content type")
	}
	if r.ContentLength > maxGiftClipRequestBytes {
		return giftClipCreateMetadata{}, nil, nil, errGiftClipHTTPRequestTooLarge
	}
	limited := http.MaxBytesReader(w, r.Body, maxGiftClipRequestBytes)
	reader := multipart.NewReader(limited, boundary)
	parts := make(map[string][]byte, 3)
	var parseErr error
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			parseErr = err
			break
		}
		if parseErr != nil {
			if _, err := io.Copy(io.Discard, part); err != nil {
				parseErr = err
				break
			}
			continue
		}
		if err := readGiftClipHTTPPart(parts, part); err != nil {
			parseErr = err
		}
	}
	_, drainErr := io.Copy(io.Discard, limited)
	if isGiftClipHTTPTooLarge(drainErr) || isGiftClipHTTPTooLarge(parseErr) {
		return giftClipCreateMetadata{}, nil, nil, errGiftClipHTTPRequestTooLarge
	}
	if parseErr == nil && drainErr != nil {
		parseErr = drainErr
	}
	if parseErr != nil {
		return giftClipCreateMetadata{}, nil, nil, parseErr
	}
	metadataData, metadataOK := parts["metadata"]
	background, backgroundOK := parts["background"]
	overlay, overlayOK := parts["overlay"]
	if len(parts) != 3 || !metadataOK || !backgroundOK || !overlayOK {
		return giftClipCreateMetadata{}, nil, nil, errors.New("invalid parts")
	}
	metadata, err := decodeGiftClipCreateMetadata(metadataData)
	if err != nil {
		return giftClipCreateMetadata{}, nil, nil, err
	}
	if err := validateGiftClipHTTPMetadata(metadata); err != nil {
		return giftClipCreateMetadata{}, nil, nil, err
	}
	if err := validateGiftClipJobLayer(background, metadata.Crop, true); err != nil {
		return giftClipCreateMetadata{}, nil, nil, err
	}
	if err := validateGiftClipJobLayer(overlay, metadata.Crop, false); err != nil {
		return giftClipCreateMetadata{}, nil, nil, err
	}
	return metadata, background, overlay, nil
}

func validGiftClipHTTPBoundary(boundary string) bool {
	writer := multipart.NewWriter(io.Discard)
	return writer.SetBoundary(boundary) == nil
}

func readGiftClipHTTPPart(parts map[string][]byte, part *multipart.Part) error {
	data, err := io.ReadAll(part)
	if err != nil {
		if isGiftClipHTTPTooLarge(err) {
			return errGiftClipHTTPRequestTooLarge
		}
		return err
	}
	name := part.FormName()
	if name != "metadata" && name != "background" && name != "overlay" {
		return errors.New("unknown part")
	}
	if _, exists := parts[name]; exists {
		return errors.New("duplicate part")
	}
	parts[name] = data
	return nil
}

func decodeGiftClipCreateMetadata(data []byte) (giftClipCreateMetadata, error) {
	var metadata giftClipCreateMetadata
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return giftClipCreateMetadata{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return giftClipCreateMetadata{}, errors.New("trailing metadata")
	}
	return metadata, nil
}

func validateGiftClipHTTPMetadata(metadata giftClipCreateMetadata) error {
	if metadata.Version != 1 || strings.TrimSpace(metadata.ReceiptID) == "" {
		return errors.New("invalid metadata")
	}
	crop := metadata.Crop
	if crop.X < 0 || crop.Y < 0 || crop.X > int(^uint(0)>>1)-crop.Width || crop.Y > int(^uint(0)>>1)-crop.Height {
		return errors.New("invalid crop")
	}
	return validateGiftClipCrop(crop, crop.X+crop.Width, crop.Y+crop.Height)
}

func giftClipHTTPSnapshot(snapshot giftClipJobSnapshot) map[string]any {
	return map[string]any{
		"id": snapshot.ID, "state": snapshot.State, "progress": snapshot.Progress,
		"message": snapshot.Message, "width": snapshot.Width, "height": snapshot.Height, "fps": snapshot.FPS,
	}
}

func validGiftClipHTTPID(id string) bool {
	if len(id) != giftClipJobIDLength {
		return false
	}
	for index := 0; index < len(id); index++ {
		character := id[index]
		if !((character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '_') {
			return false
		}
	}
	decoded, err := base64.RawURLEncoding.DecodeString(id)
	return err == nil && len(decoded) == giftClipJobIDBytes
}

var errGiftClipHTTPRequestTooLarge = errors.New("gift clip request too large")

func isGiftClipHTTPTooLarge(err error) bool {
	var maxBytesError *http.MaxBytesError
	return errors.As(err, &maxBytesError)
}

func giftClipHTTPError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]any{"code": -1, "message": message})
}

func giftClipHTTPNotFound(w http.ResponseWriter) {
	giftClipHTTPError(w, http.StatusNotFound, "导出任务不存在")
}
