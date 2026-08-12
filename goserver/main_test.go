package main

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestEmbeddedPageHandlerServesNestedUIAssets(t *testing.T) {
	pageFS := fstest.MapFS{
		"index.html":                 &fstest.MapFile{Data: []byte("<!doctype html>")},
		"chunks/config-entry-abc.js": &fstest.MapFile{Data: []byte("export const config = true;")},
		"assets/app.css":             &fstest.MapFile{Data: []byte(".app { color: red; }")},
	}
	handler := newEmbeddedPageHandler(pageFS)

	tests := []struct {
		name string
		path string
		want int
		body string
	}{
		{name: "index", path: "/", want: http.StatusOK, body: "<!doctype html>"},
		{name: "nested chunk", path: "/chunks/config-entry-abc.js", want: http.StatusOK, body: "export const config = true;"},
		{name: "nested asset", path: "/assets/app.css", want: http.StatusOK, body: ".app { color: red; }"},
		{name: "missing asset", path: "/chunks/missing.js", want: http.StatusNotFound},
		{name: "traversal", path: "/chunks/../index.html", want: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
			if test.body != "" && response.Body.String() != test.body {
				t.Fatalf("body = %q, want %q", response.Body.String(), test.body)
			}
		})
	}
}

func TestNewMainGiftClipJobsStopsOnPayloadFailure(t *testing.T) {
	want := errors.New("payload unavailable")
	called := false
	jobs, err := newMainGiftClipJobs(nil, nil, nil,
		func(string) (*giftClipPayload, error) { return nil, want },
		func(string, giftClipSourceResolver, giftClipEncoder, *diagnosticLogger) *giftClipJobManager {
			called = true
			return nil
		},
	)
	if !errors.Is(err, want) || jobs != nil || called {
		t.Fatalf("jobs=%v err=%v managerCalled=%v", jobs, err, called)
	}
}

func TestNewMainGiftClipJobsSharesGiftMediaWithoutStartingEncoder(t *testing.T) {
	media := &giftReceiptAPI{}
	payload := &giftClipPayload{}
	called := false
	jobs, err := newMainGiftClipJobs(&configStore{}, media, nil,
		func(string) (*giftClipPayload, error) { return payload, nil },
		func(_ string, resolver giftClipSourceResolver, encoder giftClipEncoder, _ *diagnosticLogger) *giftClipJobManager {
			called = true
			resolved, ok := resolver.(*receiptGiftClipSourceResolver)
			if !ok || resolved.media != media {
				t.Fatalf("resolver=%T media=%p want=%p", resolver, resolved.media, media)
			}
			ffmpeg, ok := encoder.(*giftClipFFmpegEncoder)
			if !ok || ffmpeg.payload != payload {
				t.Fatalf("encoder=%T payload=%p want=%p", encoder, ffmpeg.payload, payload)
			}
			return nil
		},
	)
	if err != nil || jobs != nil || !called {
		t.Fatalf("jobs=%v err=%v managerCalled=%v", jobs, err, called)
	}
}

func TestMainGiftClipShutdownOrdersRuntimeJobsServerInstallOnce(t *testing.T) {
	order := []string{}
	closeCount := 0
	closeJobs := newMainGiftClipCloser(func() {
		closeCount++
		order = append(order, "jobs")
	})
	runMainGiftClipShutdown(func() { order = append(order, "runtime") }, closeJobs, func() { order = append(order, "server") }, func() { order = append(order, "install") })
	closeJobs() // mirrors the deferred close after normal shutdown.
	if got := strings.Join(order, ","); got != "runtime,jobs,server,install" || closeCount != 1 {
		t.Fatalf("shutdown order=%q closeCount=%d", got, closeCount)
	}
}

func TestMainPendingGiftClipUpdateClosesOnceBeforeInstall(t *testing.T) {
	order := []string{}
	closeCount := 0
	closeJobs := newMainGiftClipCloser(func() {
		closeCount++
		order = append(order, "jobs")
	})
	runMainPendingGiftClipUpdate(closeJobs, func() { order = append(order, "install") })
	closeJobs() // mirrors the deferred close on the pending-update return path.
	if got := strings.Join(order, ","); got != "jobs,install" || closeCount != 1 {
		t.Fatalf("pending update order=%q closeCount=%d", got, closeCount)
	}
}

func TestMainGiftClipCloserRunsOnlyOnce(t *testing.T) {
	count := 0
	closeJobs := newMainGiftClipCloser(func() { count++ })
	closeJobs()
	closeJobs()
	if count != 1 {
		t.Fatalf("close count=%d", count)
	}
}

func TestFormulaPreviewUsesSelectedGiftPrice(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	request := httptest.NewRequest(http.MethodPost, "/api/formula/preview", strings.NewReader(`{
		"formula":"加班时间+price/1000*60",
		"attributeName":"加班时间",
		"attributeValue":0,
		"context":"gift",
		"giftPrice":5200
	}`))
	response := httptest.NewRecorder()

	handleFormulaPreview(store)(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"result":312`) {
		t.Fatalf("preview status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestListenWithFallbackSkipsOccupiedPort(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()

	port := occupied.Addr().(*net.TCPAddr).Port
	listener, selected, err := listenWithFallback(port, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	if selected == port {
		t.Fatalf("selected occupied port %d", selected)
	}
}

func TestUpdatedStartupNotifiesWithoutOpeningConfig(t *testing.T) {
	center := newNotificationCenter()
	received := make(chan desktopNotification, 1)
	center.AttachSink(func(notification desktopNotification) { received <- notification })
	if openConfig := announceStartup(center, "1.2.3"); openConfig {
		t.Fatal("updated startup requested the configuration page")
	}
	select {
	case notification := <-received:
		if notification.Title != "直播礼物面板已更新" {
			t.Fatalf("startup notification = %#v", notification)
		}
	case <-time.After(time.Second):
		t.Fatal("updated startup did not notify")
	}
}

func TestNormalStartupStillOpensConfig(t *testing.T) {
	if openConfig := announceStartup(newNotificationCenter(), ""); !openConfig {
		t.Fatal("normal startup no longer requests the configuration page")
	}
}

func TestListenWithFallbackUsesRequestedPortWhenAvailable(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	listener, selected, err := listenWithFallback(port, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	if selected != port {
		t.Fatalf("selected port %d, want %d", selected, port)
	}
}
