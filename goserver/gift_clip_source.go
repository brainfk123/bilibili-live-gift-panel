package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image/gif"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type giftClipSourceKind string

const (
	giftClipSourceGIF    giftClipSourceKind = "gif"
	giftClipSourceWebP   giftClipSourceKind = "webp"
	giftClipSourceEffect giftClipSourceKind = "effect"
)

type giftClipSource struct {
	Kind                      giftClipSourceKind
	Path                      string
	VisualWidth, VisualHeight int
	Duration                  time.Duration
	Layout                    *giftEffectLayout
}

type giftClipSourceResolver interface {
	Resolve(context.Context, string, string) (giftClipSource, error)
}

type receiptGiftClipSourceResolver struct {
	store *configStore
	media *giftReceiptAPI
}

func newGiftClipSourceResolver(store *configStore, media *giftReceiptAPI) giftClipSourceResolver {
	return &receiptGiftClipSourceResolver{store: store, media: media}
}

func (resolver *receiptGiftClipSourceResolver) Resolve(ctx context.Context, receiptID, taskDir string) (giftClipSource, error) {
	receipt, err := resolver.findReceipt(receiptID)
	if err != nil {
		return giftClipSource{}, err
	}
	if receipt.Animation == nil {
		return giftClipSource{}, errors.New("送礼记录没有可用动画")
	}
	if receipt.Animation.MP4 != "" && receipt.Animation.MP4JSON != "" {
		if source, effectErr := resolver.resolveEffect(ctx, receipt, taskDir); effectErr == nil {
			return source, nil
		}
	}
	return resolver.resolveShortAnimation(ctx, receipt, taskDir)
}

func (resolver *receiptGiftClipSourceResolver) findReceipt(receiptID string) (giftReceipt, error) {
	if resolver == nil || resolver.store == nil || resolver.media == nil {
		return giftReceipt{}, errors.New("礼物素材解析器未初始化")
	}
	receiptID = strings.TrimSpace(receiptID)
	if receiptID == "" {
		return giftReceipt{}, errors.New("送礼记录不存在")
	}
	state, err := resolver.store.readState()
	if err != nil {
		return giftReceipt{}, err
	}
	for _, receipt := range state.GiftReceipts {
		if receipt.ID == receiptID {
			return receipt, nil
		}
	}
	return giftReceipt{}, errors.New("送礼记录不存在")
}

func (resolver *receiptGiftClipSourceResolver) resolveEffect(ctx context.Context, receipt giftReceipt, taskDir string) (giftClipSource, error) {
	video, _, err := resolver.media.fetchMedia(ctx, receipt.Animation.MP4, maxGiftEffectVideoBytes, map[string]struct{}{"video/mp4": {}})
	if err != nil {
		return giftClipSource{}, err
	}
	if !isMP4Media(video) {
		return giftClipSource{}, errors.New("礼物特效视频格式无效")
	}
	layoutData, _, err := resolver.media.fetchMedia(ctx, receipt.Animation.MP4JSON, maxGiftEffectLayoutBytes, map[string]struct{}{"application/json": {}, "text/json": {}})
	if err != nil {
		return giftClipSource{}, err
	}
	layout, err := parseGiftEffectLayout(layoutData)
	if err != nil {
		return giftClipSource{}, err
	}
	path, err := writeGiftClipSource(taskDir, "effect.mp4", video)
	if err != nil {
		return giftClipSource{}, err
	}
	duration := time.Duration(layout.Frames) * time.Second / time.Duration(layout.FPS)
	duration = time.Duration(normalizeGiftAnimationDuration(int(duration/time.Millisecond))) * time.Millisecond
	return giftClipSource{
		Kind: giftClipSourceEffect, Path: path,
		VisualWidth: layout.RGBFrame[2], VisualHeight: layout.RGBFrame[3],
		Duration: duration, Layout: &layout,
	}, nil
}

func (resolver *receiptGiftClipSourceResolver) resolveShortAnimation(ctx context.Context, receipt giftReceipt, taskDir string) (giftClipSource, error) {
	candidates, maxBytes, allowedTypes := giftReceiptMediaCandidates(receipt, "animation")
	var lastErr error
	for _, candidate := range candidates {
		data, mediaType, err := resolver.media.fetchMedia(ctx, candidate, maxBytes, allowedTypes)
		if err != nil {
			lastErr = err
			continue
		}
		kind, width, height, cycle, extension, err := giftClipShortAnimationInfo(mediaType, data)
		if err != nil {
			lastErr = err
			continue
		}
		path, err := writeGiftClipSource(taskDir, "animation"+extension, data)
		if err != nil {
			return giftClipSource{}, err
		}
		duration := cycle
		if receipt.Animation.DurationMS > 0 {
			duration = time.Duration(normalizeGiftAnimationDuration(receipt.Animation.DurationMS)) * time.Millisecond
		}
		if duration <= 0 {
			duration = time.Duration(defaultGiftAnimationDurationMS) * time.Millisecond
		}
		return giftClipSource{Kind: kind, Path: path, VisualWidth: width, VisualHeight: height, Duration: duration}, nil
	}
	if lastErr != nil {
		return giftClipSource{}, lastErr
	}
	return giftClipSource{}, errors.New("送礼记录没有可用短动画")
}

func giftClipShortAnimationInfo(mediaType string, data []byte) (giftClipSourceKind, int, int, time.Duration, string, error) {
	switch mediaType {
	case "image/gif":
		width, height, cycle, err := giftClipGIFInfo(data)
		return giftClipSourceGIF, width, height, cycle, ".gif", err
	case "image/webp":
		width, height, cycle, err := giftClipWebPInfo(data)
		return giftClipSourceWebP, width, height, cycle, ".webp", err
	default:
		return "", 0, 0, 0, "", errors.New("礼物动画格式无效")
	}
}

func giftClipGIFInfo(data []byte) (int, int, time.Duration, error) {
	animation, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return 0, 0, 0, err
	}
	if animation.Config.Width < 1 || animation.Config.Height < 1 || len(animation.Image) == 0 {
		return 0, 0, 0, errors.New("GIF 动画尺寸或帧无效")
	}
	var cycle time.Duration
	for _, delay := range animation.Delay {
		cycle += time.Duration(maxInt(1, delay)) * 10 * time.Millisecond
	}
	return animation.Config.Width, animation.Config.Height, cycle, nil
}

func giftClipWebPInfo(data []byte) (int, int, time.Duration, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0, 0, errors.New("WebP 文件头无效")
	}
	if int64(binary.LittleEndian.Uint32(data[4:8]))+8 != int64(len(data)) {
		return 0, 0, 0, errors.New("WebP RIFF 长度无效")
	}
	var width, height int
	var hasCanvas, hasAnimation, hasFrame bool
	var cycle time.Duration
	for offset := 12; offset < len(data); {
		if len(data)-offset < 8 {
			return 0, 0, 0, errors.New("WebP 分块不完整")
		}
		kind := string(data[offset : offset+4])
		length := int64(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		payloadStart := offset + 8
		payloadEnd := int64(payloadStart) + length
		if length < 0 || payloadEnd > int64(len(data)) {
			return 0, 0, 0, errors.New("WebP 分块长度无效")
		}
		if kind == "VP8X" {
			if length < 10 {
				return 0, 0, 0, errors.New("WebP canvas 信息无效")
			}
			width = giftClipUint24(data[payloadStart+4:]) + 1
			height = giftClipUint24(data[payloadStart+7:]) + 1
			hasCanvas = true
		}
		if kind == "ANIM" {
			hasAnimation = true
		}
		if kind == "ANMF" {
			if length < 16 {
				return 0, 0, 0, errors.New("WebP 动画帧无效")
			}
			delay := giftClipUint24(data[payloadStart+12:])
			cycle += time.Duration(maxInt(10, delay)) * time.Millisecond
			hasFrame = true
		}
		next := payloadEnd
		if length%2 != 0 {
			next++
		}
		if next > int64(len(data)) {
			return 0, 0, 0, errors.New("WebP 分块填充无效")
		}
		offset = int(next)
	}
	if !hasCanvas || !hasAnimation || !hasFrame || width < 1 || height < 1 {
		return 0, 0, 0, errors.New("WebP 动画信息不完整")
	}
	return width, height, cycle, nil
}

func giftClipUint24(data []byte) int {
	return int(data[0]) | int(data[1])<<8 | int(data[2])<<16
}

func writeGiftClipSource(taskDir, name string, data []byte) (string, error) {
	if strings.TrimSpace(taskDir) == "" {
		return "", errors.New("素材目录无效")
	}
	if err := os.MkdirAll(taskDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(taskDir, name)
	if _, err := os.Lstat(path); err == nil {
		return "", errors.New("素材目标已存在")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	partial := path + ".partial"
	file, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	clean := true
	defer func() {
		if clean {
			_ = os.Remove(partial)
		}
	}()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(partial, path); err != nil {
		return "", err
	}
	clean = false
	return path, nil
}
