package main

import (
	"context"
	"crypto/tls"
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

func TestAttributeEditLeaseHasRequiresExactLiveOwnership(t *testing.T) {
	now := time.Unix(100, 0)
	leases := newAttributeEditLeaseCoordinator(15*time.Second, func() time.Time { return now }, func() (string, error) {
		return strings.Repeat("e", 24), nil
	})
	token, _, err := leases.Create("attribute-1")
	if err != nil {
		t.Fatal(err)
	}
	if !leases.Has("attribute-1", token) || leases.Has("attribute-2", token) || leases.Has("attribute-1", strings.Repeat("f", 24)) {
		t.Fatal("Has did not require exact ownership")
	}
	now = now.Add(15 * time.Second)
	if leases.Has("attribute-1", token) || leases.IsFrozen("attribute-1") {
		t.Fatal("Has did not clean expired ownership")
	}
}

func TestAttributeEditLeaseClaimAvoidsReverseLockDeadlockAndDefersRelease(t *testing.T) {
	leases := newAttributeEditLeaseCoordinator(15*time.Second, time.Now, func() (string, error) { return strings.Repeat("f", 24), nil })
	token, _, err := leases.Create("attribute-1")
	if err != nil {
		t.Fatal(err)
	}
	claim, ok := leases.Begin("attribute-1", token)
	if !ok || !claim.Live() {
		t.Fatal("could not establish live claim")
	}
	if _, ok := leases.Renew("attribute-1", token); !ok || !claim.Live() {
		t.Fatal("Renew did not preserve the active claim")
	}
	released := make(chan bool, 1)
	go func() { released <- leases.Release("attribute-1", token) }()
	select {
	case <-released:
		t.Fatal("Release returned while the claim was active")
	case <-time.After(30 * time.Millisecond):
	}
	claim.Finish()
	if !<-released || leases.Has("attribute-1", token) {
		t.Fatal("Release did not complete after claim finish")
	}
}

func TestAttributeEditLeaseFinishDeletesExpiredClaimAfterClockRollsBack(t *testing.T) {
	now := time.Unix(100, 0)
	leases := newAttributeEditLeaseCoordinator(15*time.Second, func() time.Time { return now }, func() (string, error) {
		return strings.Repeat("g", 24), nil
	})
	token, _, err := leases.Create("attribute-1")
	if err != nil {
		t.Fatal(err)
	}
	claim, ok := leases.Begin("attribute-1", token)
	if !ok {
		t.Fatal("claim did not begin")
	}
	now = time.Unix(115, 0)
	if claim.Live() {
		t.Fatal("claim did not become permanently expired")
	}
	now = time.Unix(100, 0)
	claim.Finish()

	leases.mu.Lock()
	_, retained := leases.sessions[token]
	leases.mu.Unlock()
	if retained {
		t.Fatal("zero-claim expired record survived clock rollback")
	}
	if leases.Has("attribute-1", token) || leases.Release("attribute-1", token) {
		t.Fatal("removed expired claim regained authorization or release ownership")
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

func TestAttributeEditHTTPStrictSessionAndSubmitAdapters(t *testing.T) {
	store := attributeEditFixtureStore(t)
	leases := newAttributeEditLeaseCoordinator(15*time.Second, time.Now, func() (string, error) { return strings.Repeat("A", 24), nil })
	handler := newAttributeEditHandler(newAttributeEditService(store, leases, fixedAttributeID))
	overseized := `{"legacyName":"` + strings.Repeat("a", maxConfigBytes) + `"}`

	for _, test := range []struct {
		name   string
		path   string
		method string
		body   string
		setup  func(*http.Request)
		want   int
	}{
		{name: "cross site fetch", path: "/api/attribute-edits/session", method: http.MethodPost, body: `{"attributeId":"attribute-a"}`, setup: func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, want: http.StatusForbidden},
		{name: "cross site origin", path: "/api/attribute-edits/session", method: http.MethodPost, body: `{"attributeId":"attribute-a"}`, setup: func(r *http.Request) { r.Header.Set("Origin", "https://attacker.invalid") }, want: http.StatusForbidden},
		{name: "https origin on http session", path: "/api/attribute-edits/session", method: http.MethodPost, body: `{"attributeId":"attribute-a"}`, setup: func(r *http.Request) { r.Header.Set("Origin", "https://panel.local") }, want: http.StatusForbidden},
		{name: "https origin on http submit", path: "/api/attribute-edits", method: http.MethodPost, body: `{"target":{"kind":"invalid"}}`, setup: func(r *http.Request) { r.Header.Set("Origin", "https://panel.local") }, want: http.StatusForbidden},
		{name: "unknown path", path: "/api/attribute-edits/missing", method: http.MethodPost, body: `{}`, want: http.StatusNotFound},
		{name: "wrong method", path: "/api/attribute-edits", method: http.MethodGet, body: `{}`, want: http.StatusMethodNotAllowed},
		{name: "missing content type", path: "/api/attribute-edits/session", method: http.MethodPost, body: `{"attributeId":"attribute-a"}`, setup: func(r *http.Request) { r.Header.Del("Content-Type") }, want: http.StatusBadRequest},
		{name: "unknown field", path: "/api/attribute-edits/session", method: http.MethodPost, body: `{"attributeId":"attribute-a","extra":true}`, want: http.StatusBadRequest},
		{name: "trailing JSON", path: "/api/attribute-edits/session", method: http.MethodPost, body: `{"attributeId":"attribute-a"} {}`, want: http.StatusBadRequest},
		{name: "invalid target discriminant", path: "/api/attribute-edits", method: http.MethodPost, body: `{"target":{"kind":"invalid"}}`, want: http.StatusBadRequest},
		{name: "invalid token", path: "/api/attribute-edits", method: http.MethodPost, body: `{"target":{"kind":"existing","attributeId":"attribute-a","leaseToken":"invalid"}}`, want: http.StatusBadRequest},
		{name: "oversized body", path: "/api/attribute-edits/session", method: http.MethodPost, body: overseized, want: http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := attributeEditHTTPCall(handler, test.path, test.method, test.body, test.setup)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control=%q", response.Header().Get("Cache-Control"))
			}
			if test.method == http.MethodGet && response.Header().Get("Allow") != http.MethodPost {
				t.Fatalf("Allow=%q", response.Header().Get("Allow"))
			}
		})
	}
}

func TestAttributeEditHTTPSessionAndSubmitLeaseSemantics(t *testing.T) {
	now := time.Unix(100, 0)
	tokens := []string{strings.Repeat("A", 24), strings.Repeat("B", 24)}
	store := attributeEditFixtureStore(t)
	leases := newAttributeEditLeaseCoordinator(15*time.Second, func() time.Time { return now }, func() (string, error) {
		token := tokens[0]
		tokens = tokens[1:]
		return token, nil
	})
	handler := newAttributeEditHandler(newAttributeEditService(store, leases, fixedAttributeID))

	first := attributeEditHTTPCall(handler, "/api/attribute-edits/session", http.MethodPost, `{"attributeId":"attribute-a"}`, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("session status=%d body=%s", first.Code, first.Body.String())
	}
	var session struct {
		Code        int      `json:"code"`
		AttributeID string   `json:"attributeId"`
		Token       string   `json:"token"`
		State       appState `json:"state"`
	}
	if err := json.NewDecoder(first.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	if session.Code != 0 || session.AttributeID != "attribute-a" || session.Token != strings.Repeat("A", 24) || session.State.Attributes[0].ID != "attribute-a" {
		t.Fatalf("session=%#v", session)
	}

	second := attributeEditHTTPCall(handler, "/api/attribute-edits/session", http.MethodPost, `{"attributeId":"attribute-a"}`, nil)
	var peer struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(second.Body).Decode(&peer); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", strings.Repeat("C", 24)} {
		command := existingAttributeEdit("attribute-a", "能量", 10)
		command.Target.LeaseToken = token
		body, err := json.Marshal(command)
		if err != nil {
			t.Fatal(err)
		}
		response := attributeEditHTTPCall(handler, "/api/attribute-edits", http.MethodPost, string(body), nil)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"lease_lost"`) {
			t.Fatalf("token=%q status=%d body=%s", token, response.Code, response.Body.String())
		}
	}
	now = now.Add(15 * time.Second)
	command := existingAttributeEdit("attribute-a", "能量", 10)
	command.Target.LeaseToken = peer.Token
	body, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	expired := attributeEditHTTPCall(handler, "/api/attribute-edits", http.MethodPost, string(body), nil)
	if expired.Code != http.StatusConflict || !strings.Contains(expired.Body.String(), `"lease_lost"`) {
		t.Fatalf("expired status=%d body=%s", expired.Code, expired.Body.String())
	}

	// Two live leases for one attribute intentionally permit last-write-wins.
	store = attributeEditFixtureStore(t)
	liveTokens := []string{strings.Repeat("D", 24), strings.Repeat("E", 24)}
	leases = newAttributeEditLeaseCoordinator(15*time.Second, time.Now, func() (string, error) {
		token := liveTokens[0]
		liveTokens = liveTokens[1:]
		return token, nil
	})
	handler = newAttributeEditHandler(newAttributeEditService(store, leases, fixedAttributeID))
	first = attributeEditHTTPCall(handler, "/api/attribute-edits/session", http.MethodPost, `{"attributeId":"attribute-a"}`, nil)
	second = attributeEditHTTPCall(handler, "/api/attribute-edits/session", http.MethodPost, `{"attributeId":"attribute-a"}`, nil)
	var firstLive, secondLive struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(first.Body).Decode(&firstLive); err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(second.Body).Decode(&secondLive); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []struct {
		token string
		name  string
		value float64
	}{{firstLive.Token, "能量", 10}, {secondLive.Token, "热度", 20}} {
		command := existingAttributeEdit("attribute-a", edit.name, edit.value)
		command.Target.LeaseToken = edit.token
		body, err := json.Marshal(command)
		if err != nil {
			t.Fatal(err)
		}
		response := attributeEditHTTPCall(handler, "/api/attribute-edits", http.MethodPost, string(body), nil)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"created":false`) {
			t.Fatalf("submit status=%d body=%s", response.Code, response.Body.String())
		}
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.findAttribute("热度").Value != 20 {
		t.Fatalf("last write did not win: %#v", state.Attributes)
	}
}

func TestAttributeEditHTTPRejectsHTTPOriginOnHTTPSRequest(t *testing.T) {
	store := attributeEditFixtureStore(t)
	handler := newAttributeEditHandler(newAttributeEditService(store, newDefaultAttributeEditLeaseCoordinator(), fixedAttributeID))
	for _, path := range []string{"/api/attribute-edits/session", "/api/attribute-edits"} {
		request := httptest.NewRequest(http.MethodPost, "https://panel.local"+path, strings.NewReader(`{}`))
		request.TLS = &tls.ConnectionState{}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "http://panel.local")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("path=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func attributeEditHTTPCall(handler http.Handler, path, method, body string, setup func(*http.Request)) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://panel.local"+path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if setup != nil {
		setup(request)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
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

func TestRegisterAttributeEditRoutesSharesCoordinatorWithRuntime(t *testing.T) {
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
	service := newAttributeEditService(store, leases, fixedAttributeID)
	registerAttributeEditRoutes(mux, store, background, leases, service)

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
