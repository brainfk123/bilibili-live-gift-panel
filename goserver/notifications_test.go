package main

import (
	"testing"
	"time"
)

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

func TestUpdateSucceededNotificationConfirmsBackgroundRestart(t *testing.T) {
	notification := makeDesktopNotification(notificationUpdateSucceeded, "1.2.3")
	if notification.Title != "直播礼物面板已更新" {
		t.Fatalf("update title = %q", notification.Title)
	}
	if notification.Body != "已成功更新到 v1.2.3，后台服务已重新启动。" {
		t.Fatalf("update body = %q", notification.Body)
	}
}
