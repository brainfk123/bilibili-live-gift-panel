package main

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

func TestMainGiftClipShutdownOrdersRuntimeJobsServer(t *testing.T) {
	order := []string{}
	runMainGiftClipShutdown(func() { order = append(order, "runtime") }, func() { order = append(order, "jobs") }, func() { order = append(order, "server") })
	if got := strings.Join(order, ","); got != "runtime,jobs,server" {
		t.Fatalf("shutdown order=%q", got)
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
