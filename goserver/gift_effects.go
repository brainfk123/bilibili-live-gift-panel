package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// giftEffectResource hides Bilibili's packed-alpha video protocol from the
// receipt and UI layers. The MP4 contains RGB and grayscale alpha regions;
// MP4JSON describes their positions.
type giftEffectResource struct {
	ID      int
	MP4     string
	MP4JSON string
}

type giftEffectCatalog struct {
	ByGiftID map[int]giftEffectResource
	ByID     map[int]giftEffectResource
}

type giftEffectLayout struct {
	VideoWidth  int    `json:"videoWidth"`
	VideoHeight int    `json:"videoHeight"`
	RGBFrame    [4]int `json:"rgbFrame"`
	AlphaFrame  [4]int `json:"alphaFrame"`
	FPS         int    `json:"fps"`
	Frames      int    `json:"frames"`
}

func parseRoomGiftEffects(payload map[string]any) (map[int]giftEffectResource, error) {
	catalog, err := parseRoomGiftEffectCatalog(payload)
	if err != nil {
		return nil, err
	}
	return catalog.ByGiftID, nil
}

func parseRoomGiftEffectCatalog(payload map[string]any) (giftEffectCatalog, error) {
	var response struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			FullSCResource struct {
				ConfList []struct {
					ID          int    `json:"id"`
					Type        int    `json:"type"`
					WebMP4      string `json:"web_mp4"`
					WebMP4JSON  string `json:"web_mp4_json"`
					BindGiftIDs []int  `json:"bind_gift_ids"`
				} `json:"conf_list"`
			} `json:"full_sc_resource"`
		} `json:"data"`
	}
	if err := decodeBiliPayload(payload, &response); err != nil {
		return giftEffectCatalog{}, err
	}
	if response.Code != 0 {
		return giftEffectCatalog{}, fmt.Errorf("礼物特效配置无效：%s", response.Message)
	}
	result := giftEffectCatalog{
		ByGiftID: make(map[int]giftEffectResource),
		ByID:     make(map[int]giftEffectResource),
	}
	for _, entry := range response.Data.FullSCResource.ConfList {
		mp4 := strings.TrimSpace(entry.WebMP4)
		layout := strings.TrimSpace(entry.WebMP4JSON)
		if entry.ID <= 0 || mp4 == "" || layout == "" {
			continue
		}
		resource := giftEffectResource{ID: entry.ID, MP4: mp4, MP4JSON: layout}
		result.ByID[entry.ID] = resource
		if entry.Type != 1 {
			continue
		}
		for _, giftID := range entry.BindGiftIDs {
			if giftID > 0 {
				result.ByGiftID[giftID] = resource
			}
		}
	}
	return result, nil
}

func enrichRoomGiftEffects(gifts []roomGiftInfo, effects map[int]giftEffectResource) {
	for index := range gifts {
		effect, ok := effects[gifts[index].ID]
		if !ok || (gifts[index].EffectID > 0 && gifts[index].EffectID != effect.ID) {
			continue
		}
		gifts[index].EffectID = effect.ID
		gifts[index].EffectMP4 = effect.MP4
		gifts[index].EffectMP4JSON = effect.MP4JSON
	}
}

func parseGiftEffectLayout(data []byte) (giftEffectLayout, error) {
	var payload struct {
		Info struct {
			VideoWidth  int    `json:"videoW"`
			VideoHeight int    `json:"videoH"`
			RGBFrame    [4]int `json:"rgbFrame"`
			AlphaFrame  [4]int `json:"aFrame"`
			FPS         int    `json:"fps"`
			Frames      int    `json:"f"`
		} `json:"info"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return giftEffectLayout{}, errors.New("礼物特效坐标格式无效")
	}
	layout := giftEffectLayout{
		VideoWidth: payload.Info.VideoWidth, VideoHeight: payload.Info.VideoHeight,
		RGBFrame: payload.Info.RGBFrame, AlphaFrame: payload.Info.AlphaFrame,
		FPS: payload.Info.FPS, Frames: payload.Info.Frames,
	}
	if layout.VideoWidth < 1 || layout.VideoHeight < 1 || layout.VideoWidth > 8192 || layout.VideoHeight > 8192 {
		return giftEffectLayout{}, errors.New("礼物特效视频尺寸无效")
	}
	if !validGiftEffectFrame(layout.RGBFrame, layout.VideoWidth, layout.VideoHeight) ||
		!validGiftEffectFrame(layout.AlphaFrame, layout.VideoWidth, layout.VideoHeight) {
		return giftEffectLayout{}, errors.New("礼物特效画面坐标无效")
	}
	if layout.FPS < 1 || layout.FPS > 120 || layout.Frames < 1 || layout.Frames > 3600 {
		return giftEffectLayout{}, errors.New("礼物特效时长信息无效")
	}
	return layout, nil
}

func validGiftEffectFrame(frame [4]int, videoWidth, videoHeight int) bool {
	x, y, width, height := frame[0], frame[1], frame[2], frame[3]
	return x >= 0 && y >= 0 && width > 0 && height > 0 &&
		x <= videoWidth-width && y <= videoHeight-height
}
