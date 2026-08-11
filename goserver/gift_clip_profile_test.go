package main

import (
	"testing"
	"time"
)

func TestNewGiftClipOutputProfileUsesEvenBoundsAndThirtyFPS(t *testing.T) {
	profile, err := newGiftClipOutputProfile(
		giftClipCrop{X: 101, Y: 53, Width: 960, Height: 540},
		1920, 1080, 2200*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Width != 960 || profile.Height != 540 || profile.FPS != 30 || profile.Frames != 66 || profile.Duration != 2200*time.Millisecond {
		t.Fatalf("profile = %#v", profile)
	}
}

func TestGiftClipBitrateScalesWithPixelArea(t *testing.T) {
	tests := []struct {
		width, height int
		average       int64
	}{
		{512, 360, 200_000}, {640, 360, 200_000}, {960, 540, 500_000},
		{1280, 720, 900_000}, {1920, 1080, 2_000_000},
		{2560, 1440, 3_550_000}, {3840, 2160, 8_000_000},
		{4096, 4096, 16_000_000},
	}
	for _, test := range tests {
		profile, err := newGiftClipOutputProfile(
			giftClipCrop{Width: test.width, Height: test.height},
			test.width, test.height, 3*time.Second,
		)
		if err != nil {
			t.Fatal(err)
		}
		if profile.AverageBitrate != test.average || profile.PeakBitrate != test.average*3/2 || profile.VBVBuffer != test.average*2 {
			t.Fatalf("%dx%d profile = %#v", test.width, test.height, profile)
		}
	}
}

func TestNewGiftClipOutputProfileRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name         string
		crop         giftClipCrop
		sourceWidth  int
		sourceHeight int
		duration     time.Duration
	}{
		{"width below minimum", giftClipCrop{Width: 63, Height: 64}, 64, 64, time.Second},
		{"height below minimum", giftClipCrop{Width: 64, Height: 63}, 64, 64, time.Second},
		{"width above maximum", giftClipCrop{Width: 4097, Height: 64}, 4097, 64, time.Second},
		{"height above maximum", giftClipCrop{Width: 64, Height: 4097}, 64, 4097, time.Second},
		{"odd width", giftClipCrop{Width: 65, Height: 64}, 65, 64, time.Second},
		{"odd height", giftClipCrop{Width: 64, Height: 65}, 64, 65, time.Second},
		{"negative x", giftClipCrop{X: -1, Width: 64, Height: 64}, 64, 64, time.Second},
		{"negative y", giftClipCrop{Y: -1, Width: 64, Height: 64}, 64, 64, time.Second},
		{"past source width", giftClipCrop{X: 1, Width: 64, Height: 64}, 64, 64, time.Second},
		{"past source height", giftClipCrop{Y: 1, Width: 64, Height: 64}, 64, 64, time.Second},
		{"duration too short", giftClipCrop{Width: 64, Height: 64}, 64, 64, time.Second - time.Nanosecond},
		{"duration too long", giftClipCrop{Width: 64, Height: 64}, 64, 64, 15*time.Second + time.Nanosecond},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newGiftClipOutputProfile(test.crop, test.sourceWidth, test.sourceHeight, test.duration); err == nil {
				t.Fatal("newGiftClipOutputProfile unexpectedly accepted invalid input")
			}
		})
	}
}
