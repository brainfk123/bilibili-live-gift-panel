package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAttributeEditLeasesKeepAttributeFrozenUntilLastSessionEnds(t *testing.T) {
	now := time.Unix(100, 0)
	tokens := []string{strings.Repeat("a", 24), strings.Repeat("b", 24)}
	leases := newAttributeEditLeaseCoordinator(15*time.Second, func() time.Time { return now }, func() (string, error) {
		token := tokens[0]
		tokens = tokens[1:]
		return token, nil
	})

	first, _, err := leases.Create("attribute-1")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := leases.Create("attribute-1")
	if err != nil {
		t.Fatal(err)
	}
	if !leases.IsFrozen("attribute-1") || leases.IsFrozen("attribute-2") {
		t.Fatal("unexpected freeze set")
	}
	if !leases.Release("attribute-1", first) || !leases.IsFrozen("attribute-1") {
		t.Fatal("first release thawed live peer")
	}
	if !leases.Release("attribute-1", second) || leases.IsFrozen("attribute-1") {
		t.Fatal("last release did not thaw")
	}
}

func TestAttributeEditLeaseExpiresAndRenewExtendsIt(t *testing.T) {
	now := time.Unix(100, 0)
	leases := newAttributeEditLeaseCoordinator(15*time.Second, func() time.Time { return now }, func() (string, error) {
		return strings.Repeat("c", 24), nil
	})
	token, _, err := leases.Create("attribute-1")
	if err != nil {
		t.Fatal(err)
	}
	now = time.Unix(110, 0)
	if _, ok := leases.Renew("attribute-1", token); !ok {
		t.Fatal("renew failed")
	}
	now = time.Unix(124, 0)
	if !leases.IsFrozen("attribute-1") {
		t.Fatal("renewed lease expired early")
	}
	now = time.Unix(126, 0)
	if leases.IsFrozen("attribute-1") {
		t.Fatal("expired lease remained frozen")
	}
}

func TestAttributeEditLeaseRenewAndReleaseRequireMatchingAttributeAndToken(t *testing.T) {
	now := time.Unix(100, 0)
	leases := newAttributeEditLeaseCoordinator(15*time.Second, func() time.Time { return now }, func() (string, error) {
		return strings.Repeat("d", 24), nil
	})
	token, _, err := leases.Create("attribute-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := leases.Renew("attribute-2", token); ok {
		t.Fatal("mismatched attribute renewed lease")
	}
	if leases.Release("attribute-2", token) {
		t.Fatal("mismatched attribute released lease")
	}
	if !leases.IsFrozen("attribute-1") {
		t.Fatal("mismatched operations thawed lease")
	}
}

func TestAttributeEditLeaseHTTPRejectsUnsafeAndMalformedRequests(t *testing.T) {
	store := attributeEditLeaseTestStore(t, "attribute-1")
	handler := newAttributeEditLeaseHandler(store, newAttributeEditLeaseCoordinator(15*time.Second, time.Now, func() (string, error) {
		return strings.Repeat("A", 24), nil
	}))
	overseized := `{"attributeId":"` + strings.Repeat("a", 4096) + `"}`

	for _, test := range []struct {
		name   string
		method string
		body   string
		setup  func(*http.Request)
		want   int
	}{
		{name: "cross site fetch", method: http.MethodPost, body: `{"attributeId":"attribute-1"}`, setup: func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, want: http.StatusForbidden},
		{name: "cross site origin", method: http.MethodPost, body: `{"attributeId":"attribute-1"}`, setup: func(r *http.Request) { r.Header.Set("Origin", "https://attacker.invalid") }, want: http.StatusForbidden},
		{name: "unknown method", method: http.MethodGet, want: http.StatusMethodNotAllowed},
		{name: "unknown JSON field", method: http.MethodPost, body: `{"attributeId":"attribute-1","extra":true}`, want: http.StatusBadRequest},
		{name: "trailing JSON", method: http.MethodPost, body: `{"attributeId":"attribute-1"} {}`, want: http.StatusBadRequest},
		{name: "oversized body", method: http.MethodPost, body: overseized, want: http.StatusRequestEntityTooLarge},
		{name: "empty attribute ID", method: http.MethodPost, body: `{"attributeId":" "}`, want: http.StatusBadRequest},
		{name: "unknown attribute ID", method: http.MethodPost, body: `{"attributeId":"missing"}`, want: http.StatusNotFound},
		{name: "malformed token", method: http.MethodPut, body: `{"attributeId":"attribute-1","token":"========================"}`, want: http.StatusBadRequest},
		{name: "token surrounding whitespace", method: http.MethodPut, body: `{"attributeId":"attribute-1","token":" AAAAAAAAAAAAAAAAAAAAAAAA "}`, want: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "http://panel.local/api/attribute-edit-lease", strings.NewReader(test.body))
			if test.setup != nil {
				test.setup(request)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control=%q", response.Header().Get("Cache-Control"))
			}
			if test.method == http.MethodGet && response.Header().Get("Allow") != "POST, PUT, DELETE" {
				t.Fatalf("Allow=%q", response.Header().Get("Allow"))
			}
		})
	}
}

func TestAttributeEditLeaseHTTPRejectsAmbiguousAttributeID(t *testing.T) {
	store := attributeEditLeaseTestStore(t, "attribute-1")
	if _, err := store.updateState(func(state *appState) error {
		state.Attributes = append(state.Attributes, state.Attributes[0])
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	handler := newAttributeEditLeaseHandler(store, newDefaultAttributeEditLeaseCoordinator())
	response := attributeEditLeaseHTTPCall(handler, http.MethodPost, `{"attributeId":"attribute-1"}`)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d body=%s", response.Code, http.StatusNotFound, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control=%q", response.Header().Get("Cache-Control"))
	}
}

func TestAttributeEditLeaseHTTPLifecycleAndIdempotentRelease(t *testing.T) {
	store := attributeEditLeaseTestStore(t, "attribute-1")
	leases := newAttributeEditLeaseCoordinator(15*time.Second, time.Now, func() (string, error) {
		return strings.Repeat("A", 24), nil
	})
	handler := newAttributeEditLeaseHandler(store, leases)

	post := attributeEditLeaseHTTPCall(handler, http.MethodPost, `{"attributeId":" attribute-1 "}`)
	if post.Code != http.StatusOK {
		t.Fatalf("POST status=%d body=%s", post.Code, post.Body.String())
	}
	var created struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := json.NewDecoder(post.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if len(created.Token) != 24 {
		t.Fatalf("token length=%d", len(created.Token))
	}
	decoded, err := base64.RawURLEncoding.DecodeString(created.Token)
	if err != nil || len(decoded) != 18 {
		t.Fatalf("token=%q decoded=%d err=%v", created.Token, len(decoded), err)
	}
	if _, err := time.Parse(time.RFC3339Nano, created.ExpiresAt); err != nil {
		t.Fatalf("expiresAt=%q err=%v", created.ExpiresAt, err)
	}

	renew := attributeEditLeaseHTTPCall(handler, http.MethodPut, `{"attributeId":"attribute-1","token":"`+created.Token+`"}`)
	if renew.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", renew.Code, renew.Body.String())
	}
	mismatch := attributeEditLeaseHTTPCall(handler, http.MethodDelete, `{"attributeId":"attribute-2","token":"`+created.Token+`"}`)
	if mismatch.Code != http.StatusOK || !leases.IsFrozen("attribute-1") {
		t.Fatalf("mismatch status=%d frozen=%t body=%s", mismatch.Code, leases.IsFrozen("attribute-1"), mismatch.Body.String())
	}
	firstDelete := attributeEditLeaseHTTPCall(handler, http.MethodDelete, `{"attributeId":"attribute-1","token":"`+created.Token+`"}`)
	secondDelete := attributeEditLeaseHTTPCall(handler, http.MethodDelete, `{"attributeId":"attribute-1","token":"`+created.Token+`"}`)
	if firstDelete.Code != http.StatusOK || secondDelete.Code != http.StatusOK || leases.IsFrozen("attribute-1") {
		t.Fatalf("delete statuses=%d,%d frozen=%t", firstDelete.Code, secondDelete.Code, leases.IsFrozen("attribute-1"))
	}
	for _, response := range []*httptest.ResponseRecorder{post, renew, mismatch, firstDelete, secondDelete} {
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("Cache-Control=%q", response.Header().Get("Cache-Control"))
		}
	}
}

func TestRegisterAttributeEditLeaseRouteSharesCoordinatorWithRuntime(t *testing.T) {
	store := attributeEditLeaseTestStore(t, "attribute-1")
	if _, err := store.updateState(func(state *appState) error {
		state.RoomID = "room-1"
		state.Rules = []giftRule{{ID: "rule-1", GiftID: 1, AttributeName: "积分", Formula: "积分+1"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	background := newBackgroundRuntime(store, nil)
	leases := newAttributeEditLeaseCoordinator(15*time.Second, time.Now, func() (string, error) {
		return strings.Repeat("A", 24), nil
	})
	mux := http.NewServeMux()
	registerAttributeEditLeaseRoute(mux, store, background, leases)

	created := attributeEditLeaseHTTPCall(mux, http.MethodPost, `{"attributeId":"attribute-1"}`)
	if created.Code != http.StatusOK {
		t.Fatalf("POST status=%d body=%s", created.Code, created.Body.String())
	}
	var lease struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(created.Body).Decode(&lease); err != nil {
		t.Fatal(err)
	}

	if err := background.processInboxRecord(context.Background(), giftInboxRecord{
		IngestionID: "frozen-gift", RoomID: "room-1",
		Gift: giftEvent{GiftID: 1, GiftName: "test", Num: 1, Timestamp: 1700000000, Rnd: "frozen-gift"},
	}); err != nil {
		t.Fatal(err)
	}
	frozen, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if got := frozen.findAttribute("积分").Value; got != 0 {
		t.Fatalf("frozen attribute value=%v want=0", got)
	}

	released := attributeEditLeaseHTTPCall(mux, http.MethodDelete, `{"attributeId":"attribute-1","token":"`+lease.Token+`"}`)
	if released.Code != http.StatusOK {
		t.Fatalf("DELETE status=%d body=%s", released.Code, released.Body.String())
	}
	if err := background.processInboxRecord(context.Background(), giftInboxRecord{
		IngestionID: "thawed-gift", RoomID: "room-1",
		Gift: giftEvent{GiftID: 1, GiftName: "test", Num: 1, Timestamp: 1700000001, Rnd: "thawed-gift"},
	}); err != nil {
		t.Fatal(err)
	}
	thawed, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if got := thawed.findAttribute("积分").Value; got != 1 {
		t.Fatalf("thawed attribute value=%v want=1", got)
	}
}

func attributeEditLeaseTestStore(t *testing.T, ids ...string) *configStore {
	t.Helper()
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	attributes := make([]string, 0, len(ids))
	for _, id := range ids {
		attributes = append(attributes, `{"id":"`+id+`","name":"积分","value":0,"unit":"none","format":"number"}`)
	}
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"attributes":[`+strings.Join(attributes, ",")+`],"rules":[]}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("setup status=%d body=%s", response.Code, response.Body.String())
	}
	return store
}

func attributeEditLeaseHTTPCall(handler http.Handler, method, body string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, "http://panel.local/api/attribute-edit-lease", strings.NewReader(body)))
	return response
}
