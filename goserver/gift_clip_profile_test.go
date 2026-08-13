package main

import (
	"math"
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

func TestNewGiftClipOutputProfileCountsFractionalFramesAndAllowsDurationEndpoints(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		frames   int
	}{
		{"minimum duration", time.Second, 30},
		{"fractional frame", time.Second + time.Nanosecond, 31},
		{"maximum duration", 15 * time.Second, 450},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile, err := newGiftClipOutputProfile(giftClipCrop{Width: 64, Height: 64}, 64, 64, test.duration)
			if err != nil {
				t.Fatal(err)
			}
			if profile.Frames != test.frames || profile.Duration != test.duration {
				t.Fatalf("profile = %#v", profile)
			}
		})
	}
}

func TestGiftClipBitrateScalesWithPixelArea(t *testing.T) {
	tests := []struct {
		width, height int
		average       int64
	}{
		{64, 64, 450_000}, {648, 360, 750_000},
		{512, 360, 600_000}, {640, 360, 600_000}, {960, 540, 1_500_000},
		{1280, 720, 2_700_000}, {1920, 1080, 6_000_000},
		{2560, 1440, 10_650_000}, {3840, 2160, 24_000_000},
		{4096, 4096, 48_000_000},
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
		{"maximum x does not wrap", giftClipCrop{X: math.MaxInt, Width: 64, Height: 64}, math.MaxInt, 64, time.Second},
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
