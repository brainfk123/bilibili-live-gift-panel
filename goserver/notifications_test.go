package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBuildNotificationIconDownloadsAndResizesAvatar(t *testing.T) {
	avatar := image.NewRGBA(image.Rect(0, 0, 2, 1))
	avatar.Set(0, 0, color.RGBA{R: 255, A: 255})
	avatar.Set(1, 0, color.RGBA{B: 255, A: 255})
	var source bytes.Buffer
	if err := png.Encode(&source, avatar); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(source.Bytes())
	}))
	defer server.Close()

	icon, err := buildNotificationIcon(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(icon) <= 22 || !bytes.Equal(icon[:6], []byte{0, 0, 1, 0, 1, 0}) {
		t.Fatalf("ICO header/length invalid: %x (%d bytes)", icon[:min(len(icon), 22)], len(icon))
	}
	if icon[6] != 64 || icon[7] != 64 {
		t.Fatalf("ICO dimensions = %dx%d, want 64x64", icon[6], icon[7])
	}
	offset := binary.LittleEndian.Uint32(icon[18:22])
	decoded, err := png.Decode(bytes.NewReader(icon[offset:]))
	if err != nil {
		t.Fatalf("decode embedded PNG: %v", err)
	}
	if decoded.Bounds().Dx() != 64 || decoded.Bounds().Dy() != 64 {
		t.Fatalf("embedded PNG = %v, want 64x64", decoded.Bounds())
	}
}

func TestNotificationCenterBuffersUntilTraySinkIsReady(t *testing.T) {
	center := newNotificationCenter()
	center.Publish(notificationServiceStarted, "")

	received := make(chan desktopNotification, 1)
	center.AttachSink(func(notification desktopNotification) {
		received <- notification
	})

	select {
	case notification := <-received:
		if notification.Title != "直播礼物面板已启动" {
			t.Fatalf("startup title = %q", notification.Title)
		}
	case <-time.After(time.Second):
		t.Fatal("buffered startup notification was not delivered")
	}
}

func TestRuntimePublishesOnlyRealConnectionTransitions(t *testing.T) {
	center := newNotificationCenter()
	received := make(chan desktopNotification, 4)
	center.AttachSink(func(notification desktopNotification) {
		received <- notification
	})
	runtime := newBackgroundRuntime(nil, nil, center)

	runtime.setStatus("connecting", "31567150", nil)
	runtime.setStatus("connected", "31567150", nil)
	runtime.setStatus("connected", "31567150", nil)
	runtime.setStatus("reconnecting", "31567150", nil)

	connected := <-received
	disconnected := <-received
	if connected.Title != "直播间连接成功" || disconnected.Title != "直播间连接已断开" {
		t.Fatalf("connection notifications = %#v, %#v", connected, disconnected)
	}
	select {
	case extra := <-received:
		t.Fatalf("duplicate connection notification = %#v", extra)
	default:
	}
}

func TestRuntimeCoalescesRoomChangeIntoBroadcasterNotification(t *testing.T) {
	center := newNotificationCenter()
	received := make(chan desktopNotification, 4)
	center.AttachSink(func(notification desktopNotification) { received <- notification })
	runtime := newBackgroundRuntime(nil, nil, center)
	runtime.roomNotificationResolver = func(_ context.Context, roomID string) (roomNotificationProfile, error) {
		if roomID == "room-a" {
			return roomNotificationProfile{Name: "旧主播", AvatarURL: "https://example.test/old.png"}, nil
		}
		return roomNotificationProfile{Name: "新主播", AvatarURL: "https://example.test/new.png"}, nil
	}

	runtime.setStatus("connecting", "room-a", nil)
	runtime.setStatus("connected", "room-a", nil)
	select {
	case notification := <-received:
		if notification.Body != "已连接「旧主播」，后台正在接收礼物消息。" || notification.IconURL != "https://example.test/old.png" {
			t.Fatalf("initial broadcaster notification = %#v", notification)
		}
	case <-time.After(time.Second):
		t.Fatal("initial room did not publish a connection notification")
	}

	runtime.setStatus("connecting", "room-b", nil)
	runtime.setStatus("connected", "room-b", nil)
	select {
	case notification := <-received:
		if notification.Title != "直播间已切换" {
			t.Fatalf("switch title = %q", notification.Title)
		}
		if notification.Body != "已从「旧主播」切换至「新主播」，后台正在接收礼物消息。" {
			t.Fatalf("switch body = %q", notification.Body)
		}
		if notification.IconURL != "https://example.test/new.png" {
			t.Fatalf("switch icon = %q", notification.IconURL)
		}
		if strings.Contains(notification.Body, "room-a") || strings.Contains(notification.Body, "room-b") {
			t.Fatalf("switch notification leaked room IDs: %#v", notification)
		}
	case <-time.After(time.Second):
		t.Fatal("room switch did not publish one combined notification")
	}
	select {
	case extra := <-received:
		t.Fatalf("room switch published an extra notification: %#v", extra)
	default:
	}
}

func TestRuntimeKeepsSameRoomDisconnectAndReconnectNotifications(t *testing.T) {
	center := newNotificationCenter()
	received := make(chan desktopNotification, 4)
	center.AttachSink(func(notification desktopNotification) { received <- notification })
	runtime := newBackgroundRuntime(nil, nil, center)
	runtime.roomNotificationResolver = func(context.Context, string) (roomNotificationProfile, error) {
		return roomNotificationProfile{Name: "测试主播", AvatarURL: "https://example.test/avatar.png"}, nil
	}
	runtime.setStatus("connecting", "room-a", nil)
	runtime.setStatus("connected", "room-a", nil)
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("initial connection notification missing")
	}

	runtime.setStatus("reconnecting", "room-a", context.DeadlineExceeded)
	runtime.setStatus("connected", "room-a", nil)
	first := <-received
	second := <-received
	if first.Title != "直播间连接已断开" || second.Title != "直播间连接成功" {
		t.Fatalf("same-room notifications = %#v, %#v", first, second)
	}
	for _, notification := range []desktopNotification{first, second} {
		if !strings.Contains(notification.Body, "测试主播") || strings.Contains(notification.Body, "room-a") {
			t.Fatalf("same-room notification did not use broadcaster identity: %#v", notification)
		}
		if notification.IconURL != "https://example.test/avatar.png" {
			t.Fatalf("same-room icon = %q", notification.IconURL)
		}
	}
}

func TestRuntimeRoomNotificationFallsBackWithoutLeakingRoomID(t *testing.T) {
	center := newNotificationCenter()
	received := make(chan desktopNotification, 1)
	center.AttachSink(func(notification desktopNotification) { received <- notification })
	runtime := newBackgroundRuntime(nil, nil, center)
	runtime.roomNotificationResolver = func(context.Context, string) (roomNotificationProfile, error) {
		return roomNotificationProfile{}, context.DeadlineExceeded
	}
	runtime.setStatus("connecting", "private-room-id", nil)
	runtime.setStatus("connected", "private-room-id", nil)
	select {
	case notification := <-received:
		if strings.Contains(notification.Body, "private-room-id") || !strings.Contains(notification.Body, "直播间主播") {
			t.Fatalf("fallback notification = %#v", notification)
		}
		if notification.IconURL != "" {
			t.Fatalf("fallback icon = %q, want app icon fallback", notification.IconURL)
		}
	case <-time.After(time.Second):
		t.Fatal("fallback notification missing")
	}
}

func TestUpdateSucceededNotificationConfirmsBackgroundRestart(t *testing.T) {
	notification := makeDesktopNotification(notificationUpdateSucceeded, "1.2.3")
	if notification.Title != "直播礼物面板已更新" {
		t.Fatalf("update title = %q", notification.Title)
	}
	if notification.Body != "已成功更新到 v1.2.3，后台服务已重新启动。" {
		t.Fatalf("update body = %q", notification.Body)
	}
}
