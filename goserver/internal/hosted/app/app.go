package app

import (
	"context"
	"encoding/json"
	"net/http"
)

type healthChecker interface {
	Health(context.Context) error
}

// Dependencies contains the hosted HTTP application's external services.
type Dependencies struct {
	DB            healthChecker
	Auth          http.Handler
	Admin         http.Handler
	Invitation    http.Handler
	Configuration http.Handler
	CSRFToken     string
}

// New builds the hosted HTTP handler.
func New(dependencies Dependencies) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		status := "ok"
		statusCode := http.StatusOK
		if dependencies.DB == nil || dependencies.DB.Health(request.Context()) != nil {
			status = "unavailable"
			statusCode = http.StatusServiceUnavailable
		}
		response.WriteHeader(statusCode)
		_ = json.NewEncoder(response).Encode(struct {
			Status string `json:"status"`
		}{Status: status})
	})
	mux.HandleFunc("/api/bootstrap", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Type", "application/json")
		if request.Method != http.MethodGet {
			response.Header().Set("Allow", http.MethodGet)
			response.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(response).Encode(struct {
				Error string `json:"error"`
			}{Error: "request_rejected"})
			return
		}
		if request.URL.RawQuery != "" {
			response.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(response).Encode(struct {
				Error string `json:"error"`
			}{Error: "invalid_request"})
			return
		}
		_ = json.NewEncoder(response).Encode(struct {
			CSRFToken string `json:"csrfToken"`
		}{CSRFToken: dependencies.CSRFToken})
	})
	// Keep these exact method-routes ahead of broad authentication and admin
	// prefixes so requests cannot fall through to a less specific handler.
	if dependencies.Configuration != nil {
		mux.Handle("GET /api/configuration", dependencies.Configuration)
		mux.Handle("PUT /api/configuration/definition", dependencies.Configuration)
		mux.Handle("PUT /api/configuration/state", dependencies.Configuration)
		mux.Handle("PUT /api/configuration/room-suggestion", dependencies.Configuration)
	}
	if dependencies.Auth != nil {
		mux.Handle("/api/auth/", dependencies.Auth)
		mux.Handle("/api/admin/accounts/", dependencies.Auth)
	}
	if dependencies.Admin != nil {
		mux.Handle("/api/admin/", dependencies.Admin)
	}
	if dependencies.Invitation != nil {
		mux.Handle("POST /api/auth/registration", dependencies.Invitation)
		mux.Handle("GET /api/invitations", dependencies.Invitation)
		mux.Handle("POST /api/invitations", dependencies.Invitation)
		mux.Handle("DELETE /api/invitations/{id}", dependencies.Invitation)
		mux.Handle("POST /api/admin/invitations", dependencies.Invitation)
		mux.Handle("POST /api/admin/accounts/{id}/invitation-quota", dependencies.Invitation)
	}
	return mux
}
