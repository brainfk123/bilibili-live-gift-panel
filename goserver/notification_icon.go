package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
)

const notificationIconSize = 64
const notificationIconDownloadLimit = 2 << 20

func buildNotificationIcon(ctx context.Context, client *http.Client, iconURL string) ([]byte, error) {
	parsed, err := url.Parse(iconURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("通知头像地址无效")
	}
	if client == nil {
		client = http.DefaultClient
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("通知头像请求返回 HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, notificationIconDownloadLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > notificationIconDownloadLimit {
		return nil, fmt.Errorf("通知头像超过大小限制")
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("通知头像格式无效：%w", err)
	}
	resized := resizeNotificationIcon(source)
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, resized); err != nil {
		return nil, err
	}
	return encodePNGIcon(pngData.Bytes()), nil
}

func resizeNotificationIcon(source image.Image) *image.NRGBA {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	result := image.NewNRGBA(image.Rect(0, 0, notificationIconSize, notificationIconSize))
	if width <= 0 || height <= 0 {
		return result
	}
	square := width
	if height < square {
		square = height
	}
	xOffset := bounds.Min.X + (width-square)/2
	yOffset := bounds.Min.Y + (height-square)/2
	for y := 0; y < notificationIconSize; y++ {
		sourceY := yOffset + y*square/notificationIconSize
		for x := 0; x < notificationIconSize; x++ {
			sourceX := xOffset + x*square/notificationIconSize
			result.SetNRGBA(x, y, color.NRGBAModel.Convert(source.At(sourceX, sourceY)).(color.NRGBA))
		}
	}
	return result
}

func encodePNGIcon(pngData []byte) []byte {
	result := make([]byte, 6+16+len(pngData))
	binary.LittleEndian.PutUint16(result[2:4], 1)
	binary.LittleEndian.PutUint16(result[4:6], 1)
	result[6] = notificationIconSize
	result[7] = notificationIconSize
	result[8] = 0
	result[9] = 0
	binary.LittleEndian.PutUint16(result[10:12], 1)
	binary.LittleEndian.PutUint16(result[12:14], 32)
	binary.LittleEndian.PutUint32(result[14:18], uint32(len(pngData)))
	binary.LittleEndian.PutUint32(result[18:22], 22)
	copy(result[22:], pngData)
	return result
}
