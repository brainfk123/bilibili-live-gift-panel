package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAttributeEditHTTPRejectsReleasedAndExpiredTokensWhileCurrentTokenCanSave(t *testing.T) {
	now := time.Unix(100, 0)
	tokens := []string{strings.Repeat("A", 24), strings.Repeat("B", 24)}
	store := attributeEditFixtureStore(t)
	leases := newAttributeEditLeaseCoordinator(attributeEditLeaseTTL, func() time.Time { return now }, func() (string, error) {
		token := tokens[0]
		tokens = tokens[1:]
		return token, nil
	})
	handler := newAttributeEditHandler(newAttributeEditService(store, leases, fixedAttributeID))

	first := attributeEditHTTPSession(t, handler, "attribute-a")
	second := attributeEditHTTPSession(t, handler, "attribute-a")
	if !leases.Release("attribute-a", first.Token) {
		t.Fatal("first token was not released")
	}
	stale := existingAttributeEdit("attribute-a", "过期草稿", 10)
	stale.Target.LeaseToken = first.Token
	if response := attributeEditHTTPSubmit(t, handler, stale); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"lease_lost"`) {
		t.Fatalf("released stale token response=%d body=%s", response.Code, response.Body.String())
	}
	current := existingAttributeEdit("attribute-a", "当前草稿", 10)
	current.Target.LeaseToken = second.Token
	if response := attributeEditHTTPSubmit(t, handler, current); response.Code != http.StatusOK {
		t.Fatalf("current token response=%d body=%s", response.Code, response.Body.String())
	}
	now = now.Add(attributeEditLeaseTTL)
	expired := existingAttributeEdit("attribute-a", "超时草稿", 20)
	expired.Target.LeaseToken = second.Token
	if response := attributeEditHTTPSubmit(t, handler, expired); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"lease_lost"`) {
		t.Fatalf("expired token response=%d body=%s", response.Code, response.Body.String())
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.findAttribute("当前草稿") == nil || state.findAttribute("超时草稿") != nil {
		t.Fatalf("stale or expired save changed state: %#v", state.Attributes)
	}
}

type attributeEditHTTPTestSession struct {
	Token string `json:"token"`
}

func attributeEditHTTPSession(t *testing.T, handler http.Handler, attributeID string) attributeEditHTTPTestSession {
	t.Helper()
	response := attributeEditHTTPCall(handler, "/api/attribute-edits/session", http.MethodPost, `{"attributeId":"`+attributeID+`"}`, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("session response=%d body=%s", response.Code, response.Body.String())
	}
	var session attributeEditHTTPTestSession
	if err := json.NewDecoder(response.Body).Decode(&session); err != nil || session.Token == "" {
		t.Fatalf("session=%#v err=%v", session, err)
	}
	return session
}

func attributeEditHTTPSubmit(t *testing.T, handler http.Handler, command attributeEditCommand) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	return attributeEditHTTPCall(handler, "/api/attribute-edits", http.MethodPost, string(body), nil)
}
