package assistant

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

func (service *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/assistant/status", service.handleStatus)
	mux.HandleFunc("/api/assistant/model/install", service.handleInstall)
	mux.HandleFunc("/api/assistant/model/check-update", service.handleCheckUpdate)
	mux.HandleFunc("/api/assistant/model/update", service.handleUpdate)
	mux.HandleFunc("/api/assistant/model", service.handleDelete)
	mux.HandleFunc("/api/assistant/chat", service.handleChat)
}

func (service *Service) handleStatus(w http.ResponseWriter, request *http.Request) {
	if !requireMethod(w, request, http.MethodGet) {
		return
	}
	writeResponse(w, http.StatusOK, service.Status())
}

func (service *Service) handleInstall(w http.ResponseWriter, request *http.Request) {
	if !requireMethod(w, request, http.MethodPost) {
		return
	}
	if !requireSameOrigin(w, request) {
		return
	}
	if err := service.StartInstall(); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeResponse(w, http.StatusAccepted, service.Status())
}

func (service *Service) handleCheckUpdate(w http.ResponseWriter, request *http.Request) {
	if !requireMethod(w, request, http.MethodPost) {
		return
	}
	if !requireSameOrigin(w, request) {
		return
	}
	status, err := service.CheckUpdate(request.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeResponse(w, http.StatusOK, status)
}

func (service *Service) handleUpdate(w http.ResponseWriter, request *http.Request) {
	if !requireMethod(w, request, http.MethodPost) {
		return
	}
	if !requireSameOrigin(w, request) {
		return
	}
	if err := service.StartUpdate(); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeResponse(w, http.StatusAccepted, service.Status())
}

func (service *Service) handleDelete(w http.ResponseWriter, request *http.Request) {
	if !requireMethod(w, request, http.MethodDelete) {
		return
	}
	if !requireSameOrigin(w, request) {
		return
	}
	if err := service.DeleteModel(); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeResponse(w, http.StatusOK, service.Status())
}

func (service *Service) handleChat(w http.ResponseWriter, request *http.Request) {
	if !requireMethod(w, request, http.MethodPost) {
		return
	}
	if !requireSameOrigin(w, request) {
		return
	}
	var body ChatRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, request.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("请求格式不正确"))
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("当前服务不支持流式回答"))
		return
	}
	w.WriteHeader(http.StatusOK)
	encoder := json.NewEncoder(w)
	emit := func(event StreamEvent) error {
		if err := encoder.Encode(event); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	if err := service.Chat(request.Context(), body, emit); err != nil && !errors.Is(err, request.Context().Err()) {
		_ = emit(StreamEvent{Type: "error", Message: err.Error()})
	}
}

func requireSameOrigin(w http.ResponseWriter, request *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(request.Header.Get("Sec-Fetch-Site")), "cross-site") {
		writeError(w, http.StatusForbidden, errors.New("拒绝跨站请求"))
		return false
	}
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Host, request.Host) {
		writeError(w, http.StatusForbidden, errors.New("请求来源无效"))
		return false
	}
	return true
}

func requireMethod(w http.ResponseWriter, request *http.Request, method string) bool {
	if request.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeError(w, http.StatusMethodNotAllowed, errors.New("不支持的请求方法"))
	return false
}

func writeResponse(w http.ResponseWriter, statusCode int, status AssistantStatus) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "assistant": status})
}

func writeError(w http.ResponseWriter, statusCode int, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": -1, "message": err.Error()})
}
