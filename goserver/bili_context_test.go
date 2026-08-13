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

func TestGiftResourceContextCancelsStalledBlindBoxChildRequest(t *testing.T) {
	blindStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/room/v1/Room/get_info":
			_, _ = w.Write([]byte(`{"code":0,"data":{"room_id":1,"area_id":1,"parent_area_id":1}}`))
		case "/xlive/web-room/v1/giftPanel/giftConfig":
			_, _ = w.Write([]byte(`{"code":0,"data":{"list":[{"id":1,"name":"parent","price":1,"coin_type":"gold","gift_type":6},{"id":2,"name":"child","price":1,"coin_type":"gold"}]}}`))
		case "/xlive/web-room/v1/giftPanel/giftData":
			_, _ = w.Write([]byte(`{"code":0,"data":{"room_gift_list":{"gold_list":[{"gift_id":1},{"gift_id":2}]}}}`))
		case "/xlive/general-interface/v1/fullScSpecialEffect/GetEffectConfListV2":
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		case "/xlive/general-interface/v1/blindFirstWin/getInfo":
			close(blindStarted)
			<-request.Context().Done()
		}
	}))
	defer server.Close()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	previousClient := biliHTTPClient
	biliHTTPClient = &http.Client{Transport: localRewriteTransport{target: target, base: http.DefaultTransport}}
	defer func() { biliHTTPClient = previousClient }()
	blindBoxCache.Lock()
	blindBoxCache.entries = map[int]blindBoxCacheEntry{}
	blindBoxCache.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := fetchCurrentRoomGiftResourcesContext(ctx, "1", biliSession{})
		done <- err
	}()
	<-blindStarted
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("resource fetch error = %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stalled blind-box child request did not stop after cancellation")
	}
}

func TestBilibiliSourceCancellationJoinsStalledBlindBoxSetup(t *testing.T) {
	blindStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/room/v1/Room/get_info":
			_, _ = w.Write([]byte(`{"code":0,"data":{"room_id":1,"uid":1,"area_id":1,"parent_area_id":1}}`))
		case "/x/frontend/finger/spi":
			_, _ = w.Write([]byte(`{"code":0,"data":{"b_3":"buvid"}}`))
		case "/x/web-interface/nav":
			_, _ = w.Write([]byte(`{"code":0,"data":{"wbi_img":{"img_url":"https://example.test/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png","sub_url":"https://example.test/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.png"}}}`))
		case "/xlive/web-room/v1/index/getDanmuInfo":
			_, _ = w.Write([]byte(`{"code":0,"data":{"token":"token","host_list":[]}}`))
		case "/xlive/web-room/v1/giftPanel/giftConfig":
			_, _ = w.Write([]byte(`{"code":0,"data":{"list":[{"id":1,"name":"parent","price":1,"coin_type":"gold","gift_type":6},{"id":2,"name":"child","price":1,"coin_type":"gold"}]}}`))
		case "/xlive/web-room/v1/giftPanel/giftData":
			_, _ = w.Write([]byte(`{"code":0,"data":{"room_gift_list":{"gold_list":[{"gift_id":1},{"gift_id":2}]}}}`))
		case "/xlive/general-interface/v1/fullScSpecialEffect/GetEffectConfListV2":
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		case "/xlive/general-interface/v1/blindFirstWin/getInfo":
			close(blindStarted)
			<-request.Context().Done()
		}
	}))
	defer server.Close()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	previousClient := biliHTTPClient
	previousWBIKey := wbiKeyCache
	biliHTTPClient = &http.Client{Transport: localRewriteTransport{target: target, base: http.DefaultTransport}}
	wbiKeyCache = struct {
		key      string
		expireAt time.Time
	}{}
	defer func() {
		biliHTTPClient = previousClient
		wbiKeyCache = previousWBIKey
	}()
	blindBoxCache.Lock()
	blindBoxCache.entries = map[int]blindBoxCacheEntry{}
	blindBoxCache.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (&bilibiliGiftSource{dial: func(ctx context.Context, _ string, _ http.Header) (biliSocket, error) {
			return nil, ctx.Err()
		}}).Run(ctx, "1", runtimeCallbacks{onState: func(string) {}})
	}()
	<-blindStarted
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("source error = %v, want cancellation after blind-box fetch", err)
		}
	case <-time.After(time.Second):
		t.Fatal("source did not join after cancellation during blind-box setup")
	}
}
