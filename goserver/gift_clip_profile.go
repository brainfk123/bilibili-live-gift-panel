package main

import (
	"fmt"
	"time"
)

const (
	giftClipFPS          = 30
	minGiftClipDimension = 64
	maxGiftClipDimension = 4096
	minGiftClipBitrate   = int64(150_000)
	maxGiftClipBitrate   = int64(16_000_000)
)

type giftClipCrop struct {
	X, Y, Width, Height int
}

type giftClipOutputProfile struct {
	Width, Height, FPS, Frames             int
	Duration                               time.Duration
	AverageBitrate, PeakBitrate, VBVBuffer int64
}

func newGiftClipOutputProfile(crop giftClipCrop, sourceWidth, sourceHeight int, duration time.Duration) (giftClipOutputProfile, error) {
	if err := validateGiftClipCrop(crop, sourceWidth, sourceHeight); err != nil {
		return giftClipOutputProfile{}, err
	}
	if duration < time.Second || duration > 15*time.Second {
		return giftClipOutputProfile{}, fmt.Errorf("gift clip duration %s is outside the supported range", duration)
	}

	averageBitrate := giftClipAverageBitrate(crop.Width, crop.Height)
	return giftClipOutputProfile{
		Width:          crop.Width,
		Height:         crop.Height,
		FPS:            giftClipFPS,
		Frames:         giftClipFrameCount(duration),
		Duration:       duration,
		AverageBitrate: averageBitrate,
		PeakBitrate:    averageBitrate * 3 / 2,
		VBVBuffer:      averageBitrate * 2,
	}, nil
}

func validateGiftClipCrop(crop giftClipCrop, sourceWidth, sourceHeight int) error {
	if crop.Width < minGiftClipDimension || crop.Width > maxGiftClipDimension || crop.Width%2 != 0 || crop.Height < minGiftClipDimension || crop.Height > maxGiftClipDimension || crop.Height%2 != 0 {
		return fmt.Errorf("gift clip crop dimensions %dx%d must be even and between %d and %d", crop.Width, crop.Height, minGiftClipDimension, maxGiftClipDimension)
	}
	if crop.X < 0 || crop.Y < 0 {
		return fmt.Errorf("gift clip crop origin %d,%d must not be negative", crop.X, crop.Y)
	}
	if sourceWidth < crop.Width || sourceHeight < crop.Height || crop.X > sourceWidth-crop.Width || crop.Y > sourceHeight-crop.Height {
		return fmt.Errorf("gift clip crop %d,%d %dx%d exceeds source bounds %dx%d", crop.X, crop.Y, crop.Width, crop.Height, sourceWidth, sourceHeight)
	}
	return nil
}

func giftClipAverageBitrate(width, height int) int64 {
	baselinePixels := int64(1920 * 1080)
	numerator := int64(2_000_000) * int64(width) * int64(height)
	rounded := ((numerator + 25_000*baselinePixels) / (50_000 * baselinePixels)) * 50_000
	return minInt64(maxGiftClipBitrate, maxInt64(minGiftClipBitrate, rounded))
}

func giftClipFrameCount(duration time.Duration) int {
	return int((duration.Nanoseconds()*giftClipFPS + int64(time.Second) - 1) / int64(time.Second))
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}
