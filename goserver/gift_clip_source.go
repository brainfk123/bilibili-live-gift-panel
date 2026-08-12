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

type giftClipPlaybackMode string

const (
	giftClipSourceGIF    giftClipSourceKind = "gif"
	giftClipSourceWebP   giftClipSourceKind = "webp"
	giftClipSourceEffect giftClipSourceKind = "effect"

	giftClipPlaybackSingleGIF    giftClipPlaybackMode = "single-gif"
	giftClipPlaybackAnimatedGIF  giftClipPlaybackMode = "animated-gif"
	giftClipPlaybackStaticWebP   giftClipPlaybackMode = "static-webp"
	giftClipPlaybackAnimatedWebP giftClipPlaybackMode = "animated-webp"
	giftClipPlaybackEffect       giftClipPlaybackMode = "effect"
)

type giftClipSource struct {
	Kind                      giftClipSourceKind
	Playback                  giftClipPlaybackMode
	Path                      string
	VisualWidth, VisualHeight int
	Duration                  time.Duration
	Layout                    *giftEffectLayout
}

type giftClipShortAnimation struct {
	Kind      giftClipSourceKind
	Playback  giftClipPlaybackMode
	Width     int
	Height    int
	Cycle     time.Duration
	Extension string
	Data      []byte
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
	path, err := writeGiftClipSourceContext(ctx, taskDir, "effect.mp4", video)
	if err != nil {
		return giftClipSource{}, err
	}
	duration := time.Duration(layout.Frames) * time.Second / time.Duration(layout.FPS)
	duration = time.Duration(normalizeGiftAnimationDuration(int(duration/time.Millisecond))) * time.Millisecond
	return giftClipSource{
		Kind: giftClipSourceEffect, Playback: giftClipPlaybackEffect, Path: path,
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
		animation, err := inspectGiftClipShortAnimation(mediaType, data)
		if err != nil {
			lastErr = err
			continue
		}
		if err := ctx.Err(); err != nil {
			return giftClipSource{}, err
		}
		path, err := writeGiftClipSourceContext(ctx, taskDir, "animation"+animation.Extension, animation.Data)
		if err != nil {
			return giftClipSource{}, err
		}
		duration := animation.Cycle
		if receipt.Animation.DurationMS > 0 {
			duration = time.Duration(normalizeGiftAnimationDuration(receipt.Animation.DurationMS)) * time.Millisecond
		}
		if duration <= 0 {
			duration = time.Duration(defaultGiftAnimationDurationMS) * time.Millisecond
		}
		return giftClipSource{Kind: animation.Kind, Playback: animation.Playback, Path: path, VisualWidth: animation.Width, VisualHeight: animation.Height, Duration: duration}, nil
	}
	if lastErr != nil {
		return giftClipSource{}, lastErr
	}
	return giftClipSource{}, errors.New("送礼记录没有可用短动画")
}

func giftClipShortAnimationInfo(mediaType string, data []byte) (giftClipSourceKind, int, int, time.Duration, string, error) {
	animation, err := inspectGiftClipShortAnimation(mediaType, data)
	return animation.Kind, animation.Width, animation.Height, animation.Cycle, animation.Extension, err
}

func inspectGiftClipShortAnimation(mediaType string, data []byte) (giftClipShortAnimation, error) {
	if len(data) > maxGiftAnimationBytes {
		return giftClipShortAnimation{}, errors.New("礼物动画素材超过大小限制")
	}
	switch mediaType {
	case "image/gif":
		return inspectGiftClipGIF(data)
	case "image/webp":
		return inspectGiftClipWebP(data)
	default:
		return giftClipShortAnimation{}, errors.New("礼物动画格式无效")
	}
}

func giftClipGIFInfo(data []byte) (int, int, time.Duration, error) {
	animation, err := inspectGiftClipGIF(data)
	if err != nil {
		return 0, 0, 0, err
	}
	return animation.Width, animation.Height, animation.Cycle, nil
}

func giftClipWebPInfo(data []byte) (int, int, time.Duration, error) {
	animation, err := inspectGiftClipWebP(data)
	if err != nil {
		return 0, 0, 0, err
	}
	return animation.Width, animation.Height, animation.Cycle, nil
}

var giftClipGIFLoopApplicationID = []byte("NETSCAPE2.0")

const (
	maxGiftClipGIFDecodedBytes = 32 << 20
	giftClipGIFFrameOverhead   = 8 << 10
)

func inspectGiftClipGIF(data []byte) (giftClipShortAnimation, error) {
	frameCount, trailerOffset, loopCountOffset, err := parseGiftClipGIFBlocks(data)
	if err != nil {
		return giftClipShortAnimation{}, err
	}
	decoded, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return giftClipShortAnimation{}, err
	}
	if decoded.Config.Width < 1 || decoded.Config.Height < 1 || decoded.Config.Width > maxGiftClipDimension || decoded.Config.Height > maxGiftClipDimension || len(decoded.Image) == 0 {
		return giftClipShortAnimation{}, errors.New("GIF 动画尺寸或帧无效")
	}
	if frameCount != len(decoded.Image) {
		return giftClipShortAnimation{}, errors.New("GIF 图像块数量不一致")
	}
	var cycle time.Duration
	for _, delay := range decoded.Delay {
		cycle += time.Duration(maxInt(1, delay)) * 10 * time.Millisecond
	}
	playback := giftClipPlaybackSingleGIF
	normalized := bytes.Clone(data)
	if frameCount > 1 {
		playback = giftClipPlaybackAnimatedGIF
		if loopCountOffset >= 0 {
			normalized[loopCountOffset], normalized[loopCountOffset+1] = 0, 0
		} else {
			loopExtension := giftClipGIFInfiniteLoopExtension()
			normalized = make([]byte, 0, len(data)+len(loopExtension))
			normalized = append(normalized, data[:trailerOffset]...)
			normalized = append(normalized, loopExtension...)
			normalized = append(normalized, data[trailerOffset:]...)
		}
	}
	return giftClipShortAnimation{
		Kind: giftClipSourceGIF, Playback: playback,
		Width: decoded.Config.Width, Height: decoded.Config.Height, Cycle: cycle,
		Extension: ".gif", Data: normalized,
	}, nil
}

func parseGiftClipGIFBlocks(data []byte) (int, int, int, error) {
	if len(data) < 13 || (string(data[:6]) != "GIF87a" && string(data[:6]) != "GIF89a") {
		return 0, 0, 0, errors.New("GIF 文件头无效")
	}
	if binary.LittleEndian.Uint16(data[6:8]) == 0 || binary.LittleEndian.Uint16(data[8:10]) == 0 {
		return 0, 0, 0, errors.New("GIF 画布尺寸无效")
	}
	offset := 13
	if data[10]&0x80 != 0 {
		colorTableBytes := 3 * (1 << ((data[10] & 0x07) + 1))
		if colorTableBytes > len(data)-offset {
			return 0, 0, 0, errors.New("GIF 全局调色板不完整")
		}
		offset += colorTableBytes
	}
	frameCount := 0
	loopCountOffset := -1
	decodedBytes := int64(0)
	var err error
	for offset < len(data) {
		markerOffset := offset
		marker := data[offset]
		offset++
		switch marker {
		case 0x3b:
			if offset != len(data) || frameCount == 0 {
				return 0, 0, 0, errors.New("GIF trailer 不唯一或位置无效")
			}
			return frameCount, markerOffset, loopCountOffset, nil
		case 0x2c:
			if len(data)-offset < 9 {
				return 0, 0, 0, errors.New("GIF 图像描述块不完整")
			}
			descriptor := data[offset : offset+9]
			width := int64(binary.LittleEndian.Uint16(descriptor[4:6]))
			height := int64(binary.LittleEndian.Uint16(descriptor[6:8]))
			if width == 0 || height == 0 || descriptor[8]&0x18 != 0 {
				return 0, 0, 0, errors.New("GIF 图像描述块无效")
			}
			frameBytes := width*height + giftClipGIFFrameOverhead
			if frameBytes > maxGiftClipGIFDecodedBytes-decodedBytes {
				return 0, 0, 0, errors.New("GIF 解码帧内存超过限制")
			}
			decodedBytes += frameBytes
			offset += 9
			if descriptor[8]&0x80 != 0 {
				colorTableBytes := 3 * (1 << ((descriptor[8] & 0x07) + 1))
				if colorTableBytes > len(data)-offset {
					return 0, 0, 0, errors.New("GIF 局部调色板不完整")
				}
				offset += colorTableBytes
			}
			if offset >= len(data) || data[offset] < 2 || data[offset] > 8 {
				return 0, 0, 0, errors.New("GIF LZW 信息无效")
			}
			offset++
			offset, err = skipGiftClipGIFSubBlocks(data, offset)
			if err != nil {
				return 0, 0, 0, err
			}
			frameCount++
		case 0x21:
			if offset >= len(data) {
				return 0, 0, 0, errors.New("GIF 扩展标签缺失")
			}
			label := data[offset]
			offset++
			switch label {
			case 0xf9:
				if len(data)-offset < 6 || data[offset] != 4 || data[offset+5] != 0 {
					return 0, 0, 0, errors.New("GIF 图形控制扩展无效")
				}
				offset += 6
			case 0xfe:
				offset, err = skipGiftClipGIFSubBlocks(data, offset)
				if err != nil {
					return 0, 0, 0, err
				}
			case 0x01:
				if len(data)-offset < 13 || data[offset] != 12 {
					return 0, 0, 0, errors.New("GIF 文本扩展无效")
				}
				offset += 13
				offset, err = skipGiftClipGIFSubBlocks(data, offset)
				if err != nil {
					return 0, 0, 0, err
				}
			case 0xff:
				if len(data)-offset < 12 || data[offset] != 11 {
					return 0, 0, 0, errors.New("GIF 应用扩展无效")
				}
				applicationID := data[offset+1 : offset+12]
				offset += 12
				if bytes.Equal(applicationID, giftClipGIFLoopApplicationID) {
					if loopCountOffset >= 0 || len(data)-offset < 5 || data[offset] != 3 || data[offset+1] != 1 || data[offset+4] != 0 {
						return 0, 0, 0, errors.New("GIF loop 扩展重复或无效")
					}
					loopCountOffset = offset + 2
					offset += 5
				} else {
					offset, err = skipGiftClipGIFSubBlocks(data, offset)
					if err != nil {
						return 0, 0, 0, err
					}
				}
			default:
				return 0, 0, 0, errors.New("GIF 扩展类型无效")
			}
		default:
			return 0, 0, 0, errors.New("GIF 块类型无效")
		}
	}
	return 0, 0, 0, errors.New("GIF trailer 缺失")
}

func skipGiftClipGIFSubBlocks(data []byte, offset int) (int, error) {
	for {
		if offset >= len(data) {
			return 0, errors.New("GIF 子块截断")
		}
		length := int(data[offset])
		offset++
		if length == 0 {
			return offset, nil
		}
		if length > len(data)-offset {
			return 0, errors.New("GIF 子块长度无效")
		}
		offset += length
	}
}

func giftClipGIFInfiniteLoopExtension() []byte {
	return []byte{0x21, 0xff, 0x0b, 'N', 'E', 'T', 'S', 'C', 'A', 'P', 'E', '2', '.', '0', 0x03, 0x01, 0x00, 0x00, 0x00}
}

func inspectGiftClipWebP(data []byte) (giftClipShortAnimation, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return giftClipShortAnimation{}, errors.New("WebP 文件头无效")
	}
	if int64(binary.LittleEndian.Uint32(data[4:8]))+8 != int64(len(data)) {
		return giftClipShortAnimation{}, errors.New("WebP RIFF 长度无效")
	}
	var canvasWidth, canvasHeight int
	var imageWidth, imageHeight int
	var hasVP8X, animationFlag bool
	var vp8xFlags byte
	var imageHasAlpha, animatedHasAlpha bool
	var animationCount, frameCount, imageCount, alphaCount int
	loopCountOffset := -1
	var cycle time.Duration
	for offset := 12; offset < len(data); {
		kind, payloadStart, payloadEnd, next, err := giftClipWebPChunk(data, offset)
		if err != nil {
			return giftClipShortAnimation{}, err
		}
		payload := data[payloadStart:payloadEnd]
		switch kind {
		case "VP8X":
			if hasVP8X || offset != 12 || len(payload) != 10 || payload[0]&^byte(0x12) != 0 || payload[1] != 0 || payload[2] != 0 || payload[3] != 0 {
				return giftClipShortAnimation{}, errors.New("WebP VP8X 信息无效")
			}
			hasVP8X = true
			vp8xFlags = payload[0]
			animationFlag = vp8xFlags&0x02 != 0
			canvasWidth = giftClipUint24(payload[4:7]) + 1
			canvasHeight = giftClipUint24(payload[7:10]) + 1
		case "ANIM":
			animationCount++
			if animationCount != 1 || len(payload) != 6 || frameCount != 0 {
				return giftClipShortAnimation{}, errors.New("WebP ANIM 分块重复或无效")
			}
			loopCountOffset = payloadStart + 4
		case "ANMF":
			if animationCount != 1 || !hasVP8X || !animationFlag {
				return giftClipShortAnimation{}, errors.New("WebP 动画分块顺序无效")
			}
			delay, hasAlpha, err := validateGiftClipWebPFrame(payload, canvasWidth, canvasHeight)
			if err != nil {
				return giftClipShortAnimation{}, err
			}
			animatedHasAlpha = animatedHasAlpha || hasAlpha
			cycle += time.Duration(maxInt(10, delay)) * time.Millisecond
			frameCount++
		case "ALPH":
			alphaCount++
			if len(payload) == 0 || !hasVP8X || vp8xFlags&0x10 == 0 || alphaCount != 1 || imageCount != 0 || animationCount != 0 || frameCount != 0 {
				return giftClipShortAnimation{}, errors.New("WebP alpha 分块无效")
			}
		case "VP8 ", "VP8L":
			imageCount++
			if imageCount != 1 || animationCount != 0 || frameCount != 0 || (kind == "VP8L" && alphaCount != 0) {
				return giftClipShortAnimation{}, errors.New("WebP 静态图像分块重复或无效")
			}
			imageWidth, imageHeight, imageHasAlpha, err = giftClipWebPImageDimensions(kind, payload)
			if err != nil {
				return giftClipShortAnimation{}, err
			}
		default:
			return giftClipShortAnimation{}, errors.New("WebP 分块类型无效")
		}
		offset = next
	}
	playback := giftClipPlaybackStaticWebP
	width, height := imageWidth, imageHeight
	normalized := bytes.Clone(data)
	if animationFlag || animationCount != 0 || frameCount != 0 {
		if !hasVP8X || !animationFlag || animationCount != 1 || frameCount < 1 || imageCount != 0 || alphaCount != 0 || loopCountOffset < 0 || (vp8xFlags&0x10 != 0) != animatedHasAlpha {
			return giftClipShortAnimation{}, errors.New("WebP 动画信息不完整")
		}
		playback = giftClipPlaybackAnimatedWebP
		width, height = canvasWidth, canvasHeight
		normalized[loopCountOffset], normalized[loopCountOffset+1] = 0, 0
	} else {
		if imageCount != 1 || (alphaCount == 1 && imageWidth == 0) {
			return giftClipShortAnimation{}, errors.New("WebP 静态图像信息不完整")
		}
		if hasVP8X {
			if canvasWidth != imageWidth || canvasHeight != imageHeight || (vp8xFlags&0x10 != 0) != (alphaCount == 1 || imageHasAlpha) {
				return giftClipShortAnimation{}, errors.New("WebP 静态 canvas 尺寸不一致")
			}
			width, height = canvasWidth, canvasHeight
		}
	}
	if width < 1 || height < 1 || width > maxGiftClipDimension || height > maxGiftClipDimension {
		return giftClipShortAnimation{}, errors.New("WebP 尺寸无效")
	}
	return giftClipShortAnimation{
		Kind: giftClipSourceWebP, Playback: playback,
		Width: width, Height: height, Cycle: cycle,
		Extension: ".webp", Data: normalized,
	}, nil
}

func validGiftClipWebPFramePayload(data []byte) bool {
	_, _, _, err := giftClipWebPFrameImageDimensions(data)
	return err == nil
}

func validateGiftClipWebPFrame(payload []byte, canvasWidth, canvasHeight int) (int, bool, error) {
	if len(payload) < 16 || payload[15]&0xfc != 0 {
		return 0, false, errors.New("WebP 动画帧头无效")
	}
	x := giftClipUint24(payload[0:3]) * 2
	y := giftClipUint24(payload[3:6]) * 2
	width := giftClipUint24(payload[6:9]) + 1
	height := giftClipUint24(payload[9:12]) + 1
	if width < 1 || height < 1 || x > canvasWidth-width || y > canvasHeight-height {
		return 0, false, errors.New("WebP 动画帧范围无效")
	}
	imageWidth, imageHeight, hasAlpha, err := giftClipWebPFrameImageDimensions(payload[16:])
	if err != nil || imageWidth != width || imageHeight != height {
		return 0, false, errors.New("WebP 动画帧内容无效")
	}
	return giftClipUint24(payload[12:15]), hasAlpha, nil
}

func giftClipWebPFrameImageDimensions(data []byte) (int, int, bool, error) {
	imageCount, alphaCount := 0, 0
	var width, height int
	var imageHasAlpha bool
	for offset := 0; offset < len(data); {
		kind, payloadStart, payloadEnd, next, err := giftClipWebPChunk(data, offset)
		if err != nil {
			return 0, 0, false, err
		}
		switch kind {
		case "ALPH":
			alphaCount++
			if alphaCount != 1 || imageCount != 0 || payloadEnd == payloadStart {
				return 0, 0, false, errors.New("WebP 帧 alpha 分块无效")
			}
		case "VP8 ", "VP8L":
			imageCount++
			if imageCount != 1 || (kind == "VP8L" && alphaCount != 0) {
				return 0, 0, false, errors.New("WebP 帧图像分块重复或无效")
			}
			width, height, imageHasAlpha, err = giftClipWebPImageDimensions(kind, data[payloadStart:payloadEnd])
			if err != nil {
				return 0, 0, false, err
			}
		default:
			return 0, 0, false, errors.New("WebP 帧分块类型无效")
		}
		offset = next
	}
	if imageCount != 1 {
		return 0, 0, false, errors.New("WebP 帧缺少图像分块")
	}
	return width, height, alphaCount == 1 || imageHasAlpha, nil
}

func giftClipWebPImageDimensions(kind string, payload []byte) (int, int, bool, error) {
	switch kind {
	case "VP8 ":
		if len(payload) < 10 || payload[0]&1 != 0 || !bytes.Equal(payload[3:6], []byte{0x9d, 0x01, 0x2a}) {
			return 0, 0, false, errors.New("WebP VP8 帧头无效")
		}
		width := int(binary.LittleEndian.Uint16(payload[6:8]) & 0x3fff)
		height := int(binary.LittleEndian.Uint16(payload[8:10]) & 0x3fff)
		if width < 1 || height < 1 {
			return 0, 0, false, errors.New("WebP VP8 尺寸无效")
		}
		return width, height, false, nil
	case "VP8L":
		if len(payload) < 5 || payload[0] != 0x2f {
			return 0, 0, false, errors.New("WebP VP8L 帧头无效")
		}
		packed := binary.LittleEndian.Uint32(payload[1:5])
		if packed>>29 != 0 {
			return 0, 0, false, errors.New("WebP VP8L 版本无效")
		}
		return int(packed&0x3fff) + 1, int((packed>>14)&0x3fff) + 1, packed&(1<<28) != 0, nil
	default:
		return 0, 0, false, errors.New("WebP 图像编码无效")
	}
}

func giftClipWebPChunk(data []byte, offset int) (string, int, int, int, error) {
	if offset < 0 || len(data)-offset < 8 {
		return "", 0, 0, 0, errors.New("WebP 分块不完整")
	}
	kind := string(data[offset : offset+4])
	length := int64(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
	payloadStart := offset + 8
	payloadEnd64 := int64(payloadStart) + length
	if payloadEnd64 > int64(len(data)) {
		return "", 0, 0, 0, errors.New("WebP 分块长度无效")
	}
	payloadEnd := int(payloadEnd64)
	next := payloadEnd
	if length%2 != 0 {
		if next >= len(data) || data[next] != 0 {
			return "", 0, 0, 0, errors.New("WebP 分块填充无效")
		}
		next++
	}
	return kind, payloadStart, payloadEnd, next, nil
}

func giftClipUint24(data []byte) int {
	return int(data[0]) | int(data[1])<<8 | int(data[2])<<16
}

func writeGiftClipSource(taskDir, name string, data []byte) (string, error) {
	return writeGiftClipSourceContextWithHooks(context.Background(), taskDir, name, data, giftClipSourceWriteHooks{})
}

func writeGiftClipSourceContext(ctx context.Context, taskDir, name string, data []byte) (string, error) {
	return writeGiftClipSourceContextWithHooks(ctx, taskDir, name, data, giftClipSourceWriteHooks{})
}

func writeGiftClipSourceWithBeforeInstall(taskDir, name string, data []byte, beforeInstall func()) (string, error) {
	return writeGiftClipSourceContextWithHooks(context.Background(), taskDir, name, data, giftClipSourceWriteHooks{beforeInstall: beforeInstall})
}

type giftClipSourceWriteHooks struct {
	beforeInstall func()
	removePartial func(string) error
}

func writeGiftClipSourceWithHooks(taskDir, name string, data []byte, hooks giftClipSourceWriteHooks) (string, error) {
	return writeGiftClipSourceContextWithHooks(context.Background(), taskDir, name, data, hooks)
}

func writeGiftClipSourceContextWithHooks(ctx context.Context, taskDir, name string, data []byte, hooks giftClipSourceWriteHooks) (string, error) {
	if strings.TrimSpace(taskDir) == "" {
		return "", errors.New("素材目录无效")
	}
	if ctx == nil {
		return "", errors.New("素材写入上下文无效")
	}
	if err := ctx.Err(); err != nil {
		return "", err
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
	file, err := os.CreateTemp(taskDir, "."+name+".partial-*")
	if err != nil {
		return "", err
	}
	partial := file.Name()
	removePartial := hooks.removePartial
	if removePartial == nil {
		removePartial = os.Remove
	}
	committed := false
	defer func() {
		if !committed {
			_ = removePartial(partial)
		}
	}()
	for offset := 0; offset < len(data); {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return "", err
		}
		end := minInt(offset+64*1024, len(data))
		written, err := file.Write(data[offset:end])
		if err != nil {
			_ = file.Close()
			return "", err
		}
		if written == 0 {
			_ = file.Close()
			return "", errors.New("素材写入未取得进展")
		}
		offset += written
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if hooks.beforeInstall != nil {
		hooks.beforeInstall()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Link(partial, path); err != nil {
		return "", err
	}
	committed = true
	_ = removePartial(partial)
	return path, nil
}
