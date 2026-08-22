//go:build windows

package main

import (
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoadNotificationBalloonIconCreatesWindowsIcon(t *testing.T) {
	avatar := image.NewRGBA(image.Rect(0, 0, 2, 2))
	avatar.Set(0, 0, color.RGBA{R: 255, A: 255})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		if err := png.Encode(w, avatar); err != nil {
			t.Errorf("encode avatar: %v", err)
		}
	}))
	defer server.Close()

	icon, release := loadNotificationBalloonIcon(server.URL)
	if icon == 0 {
		t.Fatal("LoadImageW did not create an HICON from the downloaded avatar")
	}
	release()
}
