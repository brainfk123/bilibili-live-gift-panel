package assistant

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAssistantMutationRejectsCrossSiteRequest(t *testing.T) {
	service := serviceWithActiveModel(t, &fakeEngine{}, time.Hour)
	mux := http.NewServeMux()
	service.Register(mux)
	request := httptest.NewRequest(http.MethodPost, "http://localhost:12450/api/assistant/chat", strings.NewReader(`{"question":"房间号在哪"}`))
	request.Host = "localhost:12450"
	request.Header.Set("Origin", "https://evil.example")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestAssistantMutationAllowsSameOriginAndLocalTools(t *testing.T) {
	for _, origin := range []string{"http://localhost:12450", ""} {
		request := httptest.NewRequest(http.MethodPost, "http://localhost:12450/api/assistant/model/check-update", nil)
		request.Host = "localhost:12450"
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		if !requireSameOrigin(response, request) {
			t.Fatalf("origin %q rejected: %s", origin, response.Body.String())
		}
	}
}
