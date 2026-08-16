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
	DB    healthChecker
	Auth  http.Handler
	Admin http.Handler
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
	if dependencies.Auth != nil {
		mux.Handle("/api/auth/", dependencies.Auth)
	}
	if dependencies.Admin != nil {
		mux.Handle("/api/admin/", dependencies.Admin)
	}
	return mux
}
