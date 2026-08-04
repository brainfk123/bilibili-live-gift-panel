package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestHostedChangelogHandlerProxiesAndCachesReleaseHistory(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schemaVersion":1,"releases":[{"version":"0.2.0"},{"version":"0.1.0"}]}`))
	}))
	defer upstream.Close()
	handler := newHostedChangelogHandler(upstream.Client(), upstream.URL)

	for range 2 {
		request := httptest.NewRequest(http.MethodGet, "/api/changelog", nil)
		response := httptest.NewRecorder()
		handler(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
		}
		var payload struct {
			Code     int               `json:"code"`
			Releases []json.RawMessage `json:"releases"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Code != 0 || len(payload.Releases) != 2 {
			t.Fatalf("payload = %+v, want two releases", payload)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("upstream requests = %d, want 1 cached request", requests.Load())
	}
}

func TestHostedChangelogHandlerRejectsInvalidPayload(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"schemaVersion":1,"releases":[]}`))
	}))
	defer upstream.Close()
	response := httptest.NewRecorder()
	newHostedChangelogHandler(upstream.Client(), upstream.URL)(response, httptest.NewRequest(http.MethodGet, "/api/changelog", nil))
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadGateway)
	}
}
