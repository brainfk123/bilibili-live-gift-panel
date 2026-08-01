package main

import (
	"net"
	"testing"
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
