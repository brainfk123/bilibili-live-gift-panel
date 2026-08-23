package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

type healthChecker interface {
	Health(context.Context) error
}

// Dependencies contains the hosted HTTP application's external services.
type Dependencies struct {
	DB            healthChecker
	Auth          http.Handler
	Admin         http.Handler
	AdminConsole  http.Handler
	Invitation    http.Handler
	Configuration http.Handler
	Migration     http.Handler
	BiliService   http.Handler
	Runtime       http.Handler
	OBS           http.Handler
	Static        http.Handler
	CSRFToken     string
}

var obsLandingPublicIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

var obsLandingThemes = map[string]struct{}{
	"minimal": {},
	"glass":   {},
	"rpg":     {},
	"pixel":   {},
	"neon":    {},
	"kawaii":  {},
}

type manifestEntry struct {
	File   string   `json:"file"`
	CSS    []string `json:"css"`
	Assets []string `json:"assets"`
}

type staticContent struct {
	body        []byte
	contentType string
	cache       string
}

type staticHandler struct {
	hosted staticContent
	obs    staticContent
	assets map[string]staticContent
}

// NewStaticHandler validates and preloads the immutable hosted UI bundle. Only
// the two entry pages and files named by Vite's manifest can ever be served.
func NewStaticHandler(root string) (http.Handler, error) {
	if root == "" || !filepath.IsAbs(root) {
		return nil, errors.New("hosted UI root must be absolute")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("hosted UI root is unavailable")
	}
	read := func(name string) ([]byte, error) {
		fileName := filepath.Join(root, filepath.FromSlash(name))
		info, statErr := os.Lstat(fileName)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("hosted UI file %q is unavailable", name)
		}
		contents, readErr := os.ReadFile(fileName)
		if readErr != nil {
			return nil, fmt.Errorf("read hosted UI file %q", name)
		}
		return contents, nil
	}
	hostedPage, err := read("hosted.html")
	if err != nil {
		return nil, err
	}
	obsPage, err := read("obs.html")
	if err != nil {
		return nil, err
	}
	manifestBytes, err := read(".vite/manifest.json")
	if err != nil {
		return nil, err
	}
	var manifest map[string]manifestEntry
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil || len(manifest) == 0 {
		return nil, errors.New("hosted UI manifest is invalid")
	}
	if _, ok := manifest["hosted.html"]; !ok {
		return nil, errors.New("hosted UI manifest lacks hosted entry")
	}
	if _, ok := manifest["obs.html"]; !ok {
		return nil, errors.New("hosted UI manifest lacks OBS entry")
	}
	assets := make(map[string]staticContent)
	for _, entry := range manifest {
		files := append([]string{entry.File}, entry.CSS...)
		files = append(files, entry.Assets...)
		for _, name := range files {
			if name == "" {
				continue
			}
			if !strings.HasPrefix(name, "assets/") || path.Clean(name) != name || strings.Contains(name, `\`) {
				return nil, errors.New("hosted UI manifest contains an invalid asset path")
			}
			contents, readErr := read(name)
			if readErr != nil {
				return nil, readErr
			}
			contentType := mime.TypeByExtension(filepath.Ext(name))
			if contentType == "" {
				contentType = "application/octet-stream"
			}
			assets["/"+name] = staticContent{body: contents, contentType: contentType, cache: "public, max-age=31536000, immutable"}
		}
	}
	return &staticHandler{
		hosted: staticContent{body: hostedPage, contentType: "text/html; charset=utf-8", cache: "no-store"},
		obs:    staticContent{body: obsPage, contentType: "text/html; charset=utf-8", cache: "no-store"},
		assets: assets,
	}, nil
}

func (handler *staticHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if handler == nil || request == nil {
		http.NotFound(response, request)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var content staticContent
	var ok bool
	switch {
	case request.URL.Path == "/" || request.URL.Path == "/hosted.html":
		if request.URL.RawQuery == "" {
			content, ok = handler.hosted, true
		}
	case strings.HasPrefix(request.URL.Path, "/obs/") && obsLandingPublicIDPattern.MatchString(strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/obs/"), "/")):
		if validOBSThemeQuery(request.URL.RawQuery) {
			content, ok = handler.obs, true
		}
	default:
		if request.URL.RawQuery == "" {
			content, ok = handler.assets[request.URL.Path]
		}
	}
	if !ok {
		http.NotFound(response, request)
		return
	}
	response.Header().Set("Content-Type", content.contentType)
	response.Header().Set("Cache-Control", content.cache)
	response.Header().Set("X-Content-Type-Options", "nosniff")
	if request.Method == http.MethodHead {
		response.WriteHeader(http.StatusOK)
		return
	}
	_, _ = response.Write(content.body)
}

func validOBSThemeQuery(rawQuery string) bool {
	if rawQuery == "" {
		return true
	}
	for theme := range obsLandingThemes {
		if rawQuery == "theme="+theme {
			return true
		}
	}
	return false
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
	if dependencies.Migration != nil {
		mux.Handle("POST /api/migrations/preview", dependencies.Migration)
		mux.Handle("POST /api/migrations/{id}/apply", dependencies.Migration)
		mux.Handle("DELETE /api/migrations/{id}", dependencies.Migration)
		mux.Handle("POST /api/migrations/{id}/rollback", dependencies.Migration)
		mux.Handle("GET /api/migrations/{id}", dependencies.Migration)
	}
	if dependencies.BiliService != nil {
		// Own each complete path for every method. The Bili service handler
		// returns 405 for unsupported methods instead of letting them fall
		// through to the broader administrator router.
		mux.Handle("/api/admin/bili-service/challenge", dependencies.BiliService)
		mux.Handle("/api/admin/bili-service/replace", dependencies.BiliService)
		mux.Handle("/api/admin/bili-service/status", dependencies.BiliService)
	}
	if dependencies.Runtime != nil {
		// Own each complete runtime path so unsupported methods cannot fall
		// through to an unrelated broad handler. There is deliberately no
		// start or stop path.
		mux.Handle("/api/runtime/room", dependencies.Runtime)
		mux.Handle("/api/runtime/events", dependencies.Runtime)
		mux.Handle("/api/runtime/status", dependencies.Runtime)
	}
	if dependencies.OBS != nil {
		// OBS owns every method on its credential, exchange, and event paths so
		// mutations cannot fall through to broader account/admin handlers.
		mux.Handle("/api/admin/accounts/{id}/obs-credential", dependencies.OBS)
		mux.Handle("/obs/{publicID}/exchange", dependencies.OBS)
		mux.Handle("/obs/{publicID}/events", dependencies.OBS)
	}
	if dependencies.AdminConsole != nil {
		// Administrator projections own these exact read paths before the
		// legacy broad account mutation handler.
		mux.Handle("GET /api/admin/overview", dependencies.AdminConsole)
		mux.Handle("GET /api/admin/accounts", dependencies.AdminConsole)
		mux.Handle("GET /api/admin/accounts/{id}", dependencies.AdminConsole)
		mux.Handle("POST /api/admin/accounts/batch", dependencies.AdminConsole)
		mux.Handle("PUT /api/admin/accounts/{id}/room", dependencies.AdminConsole)
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
		mux.Handle("GET /api/admin/invitations", dependencies.Invitation)
		mux.Handle("DELETE /api/admin/invitations/{id}", dependencies.Invitation)
		mux.Handle("POST /api/admin/accounts/{id}/invitation-quota", dependencies.Invitation)
	}
	if dependencies.Static != nil {
		mux.Handle("GET /{$}", dependencies.Static)
		mux.Handle("GET /hosted.html", dependencies.Static)
		mux.Handle("GET /obs/{publicID}", dependencies.Static)
		mux.Handle("GET /obs/{publicID}/{$}", dependencies.Static)
		mux.Handle("GET /assets/", dependencies.Static)
	}
	return mux
}
