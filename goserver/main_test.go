package main

import (
	"net"
	"testing"
	"time"
)

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
