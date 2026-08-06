package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPanelToOpenPrefersNewestRunningVersion(t *testing.T) {
	instances := []panelInstance{
		{Port: 12450, Version: "0.2.2"},
		{Port: 12451, Version: "0.2.4"},
		{Port: 12452, Version: "0.2.3"},
	}
	selected, found := panelToOpen("0.2.3", instances)
	if !found || selected.Port != 12451 {
		t.Fatalf("panelToOpen() = %#v, %v; want port 12451", selected, found)
	}
	if _, found := panelToOpen("0.2.5", instances); found {
		t.Fatal("newer launcher unexpectedly selected an older instance")
	}
}

func TestInstanceExitRequiresNewerLocalVersion(t *testing.T) {
	exit := make(chan struct{}, 1)
	handler := newInstanceExitHandler("0.2.3", exit)

	request := httptest.NewRequest(http.MethodPost, "/api/instance/exit", nil)
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("X-Bilibili-Panel-Takeover", "0.2.4")
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("newer takeover status = %d", response.Code)
	}
	select {
	case <-exit:
	default:
		t.Fatal("newer takeover did not request exit")
	}

	request = httptest.NewRequest(http.MethodPost, "/api/instance/exit", nil)
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("X-Bilibili-Panel-Takeover", "0.2.2")
	response = httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("older takeover status = %d, want %d", response.Code, http.StatusConflict)
	}
}
