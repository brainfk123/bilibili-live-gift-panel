package adminconsole

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"bilibili-live-gift-panel/internal/hosted/identity"
)

type sessionValidator interface {
	RequireSession(context.Context, string) error
}
type HTTPHandler struct {
	service  *Service
	sessions sessionValidator
	mux      *http.ServeMux
}

func NewHTTPHandler(service *Service, sessions sessionValidator) (*HTTPHandler, error) {
	if service == nil || sessions == nil {
		return nil, errors.New("service and sessions are required")
	}
	h := &HTTPHandler{service: service, sessions: sessions, mux: http.NewServeMux()}
	h.mux.HandleFunc("GET /api/admin/overview", h.overview)
	h.mux.HandleFunc("GET /api/admin/accounts", h.accounts)
	h.mux.HandleFunc("GET /api/admin/accounts/{id}", h.account)
	h.mux.HandleFunc("POST /api/admin/accounts/batch", h.batch)
	h.mux.HandleFunc("PUT /api/admin/accounts/{id}/room", h.updateRoom)
	return h, nil
}
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }
func (h *HTTPHandler) authorize(w http.ResponseWriter, r *http.Request) bool {
	_, ok := h.sessionToken(w, r)
	return ok
}
func (h *HTTPHandler) sessionToken(w http.ResponseWriter, r *http.Request) (string, bool) {
	cookie, err := r.Cookie(identity.SiteSessionCookie)
	if err != nil || cookie.Value == "" || h.sessions.RequireSession(r.Context(), cookie.Value) != nil {
		writeError(w, http.StatusUnauthorized, "authentication_required")
		return "", false
	}
	return cookie.Value, true
}
func (h *HTTPHandler) overview(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request")
		return
	}
	value, err := h.service.Overview(r.Context())
	if err != nil {
		writeError(w, 503, "temporarily_unavailable")
		return
	}
	writeJSON(w, value)
}
func (h *HTTPHandler) accounts(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	values := r.URL.Query()
	for key := range values {
		if key != "query" && key != "status" && key != "attention" && key != "cursor" && key != "limit" {
			writeError(w, 400, "invalid_request")
			return
		}
	}
	limit := 0
	var err error
	if values.Get("limit") != "" {
		limit, err = strconv.Atoi(values.Get("limit"))
		if err != nil {
			writeError(w, 400, "invalid_request")
			return
		}
	}
	value, err := h.service.Accounts(r.Context(), AccountQuery{Query: values.Get("query"), Status: AccountStatus(values.Get("status")), Attention: AttentionKind(values.Get("attention")), Cursor: values.Get("cursor"), Limit: limit})
	if errors.Is(err, ErrInvalidQuery) || errors.Is(err, ErrInvalidCursor) {
		writeError(w, 400, "invalid_request")
		return
	}
	if err != nil {
		writeError(w, 503, "temporarily_unavailable")
		return
	}
	writeJSON(w, value)
}
func (h *HTTPHandler) account(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request")
		return
	}
	id, err := parseAccountID(strings.TrimPrefix(r.URL.Path, "/api/admin/accounts/"))
	if err != nil {
		writeError(w, 400, "invalid_request")
		return
	}
	value, err := h.service.Account(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		writeError(w, 404, "account_not_found")
		return
	}
	if err != nil {
		writeError(w, 503, "temporarily_unavailable")
		return
	}
	writeJSON(w, value)
}

func (h *HTTPHandler) batch(w http.ResponseWriter, r *http.Request) {
	token, ok := h.sessionToken(w, r)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request")
		return
	}
	var request BatchRequest
	if !decodeBody(r, &request) {
		writeError(w, 400, "invalid_request")
		return
	}
	value, err := h.service.Batch(r.Context(), token, request)
	if errors.Is(err, ErrInvalidQuery) {
		writeError(w, 400, "invalid_request")
		return
	}
	if err != nil {
		writeError(w, 503, "temporarily_unavailable")
		return
	}
	writeJSON(w, value)
}
func (h *HTTPHandler) updateRoom(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request")
		return
	}
	suffix := strings.TrimPrefix(r.URL.Path, "/api/admin/accounts/")
	idText, ok := strings.CutSuffix(suffix, "/room")
	if !ok {
		writeError(w, 400, "invalid_request")
		return
	}
	id, err := parseAccountID(idText)
	if err != nil {
		writeError(w, 400, "invalid_request")
		return
	}
	var body struct {
		RoomID string `json:"roomId"`
	}
	if !decodeBody(r, &body) {
		writeError(w, 400, "invalid_request")
		return
	}
	value, err := h.service.UpdateRoom(r.Context(), id, body.RoomID)
	if errors.Is(err, ErrInvalidQuery) {
		writeError(w, 400, "invalid_request")
		return
	}
	if errors.Is(err, ErrNotFound) {
		writeError(w, 404, "account_not_found")
		return
	}
	if err != nil {
		writeError(w, 503, "temporarily_unavailable")
		return
	}
	writeJSON(w, value)
}
func decodeBody(r *http.Request, target any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		return false
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return false
	}
	var extra any
	return errors.Is(decoder.Decode(&extra), io.EOF)
}
func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}
