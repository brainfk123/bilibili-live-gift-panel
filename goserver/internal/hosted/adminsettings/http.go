package adminsettings

import (
	"bilibili-live-gift-panel/internal/hosted/identity"
	"encoding/json"
	"errors"
	"net/http"
)

type HTTPHandler struct {
	service *Service
	csrf    string
	origin  string
	mux     *http.ServeMux
}

func NewHTTPHandler(service *Service, origin, csrf string) (*HTTPHandler, error) {
	if service == nil || origin == "" || csrf == "" {
		return nil, ErrUnavailable
	}
	handler := &HTTPHandler{service: service, origin: origin, csrf: csrf, mux: http.NewServeMux()}
	handler.mux.HandleFunc("GET /api/admin/settings", handler.settings)
	handler.mux.HandleFunc("POST /api/admin/sessions/revoke-others", handler.revokeOthers)
	handler.mux.HandleFunc("GET /api/admin/events", handler.events)
	handler.mux.HandleFunc("GET /api/admin/diagnostics", handler.diagnostics)
	return handler, nil
}
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	h.mux.ServeHTTP(w, r)
}
func (h *HTTPHandler) token(w http.ResponseWriter, r *http.Request) (string, bool) {
	cookie, err := r.Cookie(identity.SiteSessionCookie)
	if err != nil || cookie.Value == "" {
		writeError(w, 401, "authentication_failed")
		return "", false
	}
	return cookie.Value, true
}
func (h *HTTPHandler) settings(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request")
		return
	}
	token, ok := h.token(w, r)
	if !ok {
		return
	}
	value, err := h.service.Settings(r.Context(), token)
	h.respond(w, value, err)
}
func (h *HTTPHandler) events(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request")
		return
	}
	token, ok := h.token(w, r)
	if !ok {
		return
	}
	value, err := h.service.Events(r.Context(), token)
	h.respond(w, struct {
		Events []Event `json:"events"`
	}{value}, err)
}
func (h *HTTPHandler) diagnostics(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request")
		return
	}
	token, ok := h.token(w, r)
	if !ok {
		return
	}
	value, err := h.service.Diagnostics(r.Context(), token)
	h.respond(w, value, err)
}
func (h *HTTPHandler) revokeOthers(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || r.Header.Get("X-CSRF-Token") != h.csrf || r.Header.Get("Origin") != h.origin || r.ContentLength != 0 {
		writeError(w, 403, "request_rejected")
		return
	}
	token, ok := h.token(w, r)
	if !ok {
		return
	}
	if err := h.service.RevokeOtherSessions(r.Context(), token); err != nil {
		h.respond(w, nil, err)
		return
	}
	w.WriteHeader(204)
}
func (h *HTTPHandler) respond(w http.ResponseWriter, value any, err error) {
	if errors.Is(err, ErrAuthentication) {
		writeError(w, 401, "authentication_failed")
		return
	}
	if err != nil {
		writeError(w, 503, "temporarily_unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}
