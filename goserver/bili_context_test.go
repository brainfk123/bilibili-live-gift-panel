package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

type localRewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (transport localRewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.URL.Scheme = transport.target.Scheme
	copy.URL.Host = transport.target.Host
	return transport.base.RoundTrip(copy)
}

func TestFetchJSONContextCancelsStalledLocalServer(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		select {}
	}))
	defer server.CloseClientConnections()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := fetchJSONContext(ctx, server.URL, nil)
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("fetch error = %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stalled HTTP fetch did not stop after cancellation")
	}
}

func TestBilibiliSourceCancelsStalledRoomSetupRequest(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	previousClient := biliHTTPClient
	biliHTTPClient = &http.Client{Transport: localRewriteTransport{target: target, base: http.DefaultTransport}}
	defer func() { biliHTTPClient = previousClient }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (&bilibiliGiftSource{}).Run(ctx, "room-a", runtimeCallbacks{onState: func(string) {}})
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || connectionFailureKind(err) != "source" {
			t.Fatalf("source setup error = %v, want categorized cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stalled room setup did not stop after cancellation")
	}
}
