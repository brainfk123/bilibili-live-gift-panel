package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestPagePresenceStreamTracksHTTPConnectionLifecycle(t *testing.T) {
	server := httptest.NewServer(newPagePresence(nil))
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/api/pages/presence/stream?mode=config&id=test-page")
	if err != nil {
		t.Fatal(err)
	}
	presence := server.Config.Handler.(*pagePresence)
	if config, display := presence.Counts(); config != 1 || display != 0 {
		t.Fatalf("open stream counts = config %d, display %d", config, display)
	}
	response.Body.Close()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if config, _ := presence.Counts(); config == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("closing the stream did not release the config page")
}

func TestPagePresenceNotifiesOnlyAfterTheLastPageCloses(t *testing.T) {
	center := newNotificationCenter()
	received := make(chan desktopNotification, 4)
	center.AttachSink(func(notification desktopNotification) {
		received <- notification
	})
	presence := newPagePresence(center)
	presence.closeGrace = 15 * time.Millisecond

	closeFirst := presence.register(pageModeConfig)
	closeLast := presence.register(pageModeConfig)
	closeFirst()
	select {
	case notification := <-received:
		t.Fatalf("closing one of two config pages notified: %#v", notification)
	case <-time.After(30 * time.Millisecond):
	}

	closeLast()
	select {
	case notification := <-received:
		if notification.Title != "配置页面已全部关闭" {
			t.Fatalf("config close title = %q", notification.Title)
		}
	case <-time.After(time.Second):
		t.Fatal("closing the last config page did not notify")
	}

	closeDisplay := presence.register(pageModeDisplay)
	closeDisplay()
	select {
	case notification := <-received:
		if notification.Title != "OBS 属性面板已全部关闭" {
			t.Fatalf("display close title = %q", notification.Title)
		}
	case <-time.After(time.Second):
		t.Fatal("closing the last OBS page did not notify")
	}
}

func TestPagePresenceSuppressesRefreshCloseNotification(t *testing.T) {
	center := newNotificationCenter()
	received := make(chan desktopNotification, 1)
	center.AttachSink(func(notification desktopNotification) {
		received <- notification
	})
	presence := newPagePresence(center)
	presence.closeGrace = 30 * time.Millisecond

	closeOldPage := presence.register(pageModeConfig)
	closeOldPage()
	time.Sleep(5 * time.Millisecond)
	closeRefreshedPage := presence.register(pageModeConfig)
	defer closeRefreshedPage()

	select {
	case notification := <-received:
		t.Fatalf("refresh produced a close notification: %#v", notification)
	case <-time.After(60 * time.Millisecond):
	}
}

func TestPagePresenceBecomesIdleOnlyAfterEveryPageCloses(t *testing.T) {
	presence := newPagePresence(nil)
	presence.closeGrace = 10 * time.Millisecond
	idle := make(chan struct{}, 1)
	presence.SetOnIdle(func() { idle <- struct{}{} })
	if presence.IsIdle() {
		t.Fatal("presence must not report startup as idle before any page connects")
	}

	closeConfig := presence.register(pageModeConfig)
	closeDisplay := presence.register(pageModeDisplay)
	closeConfig()
	select {
	case <-idle:
		t.Fatal("closing config while an OBS panel is open reported idle")
	case <-time.After(30 * time.Millisecond):
	}
	if presence.IsIdle() {
		t.Fatal("presence reported idle while an OBS panel remained open")
	}

	closeDisplay()
	select {
	case <-idle:
	case <-time.After(time.Second):
		t.Fatal("closing the final page did not report idle")
	}
	if !presence.IsIdle() {
		t.Fatal("presence did not stay idle after all pages closed")
	}
}
