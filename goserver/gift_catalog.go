package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	roomGiftInfoEndpoint   = "https://api.live.bilibili.com/room/v1/Room/get_info"
	roomGiftConfigEndpoint = "https://api.live.bilibili.com/xlive/web-room/v1/giftPanel/giftConfig"
	roomGiftDataEndpoint   = "https://api.live.bilibili.com/xlive/web-room/v1/giftPanel/giftData"
)

type roomGiftContext struct {
	RoomID       int64
	AreaID       int
	ParentAreaID int
}

type giftPanelEntry struct {
	GiftID int `json:"gift_id"`
	ID     int `json:"id"`
}

type roomGiftInfo struct {
	ID                  int     `json:"id"`
	Name                string  `json:"name"`
	Price               float64 `json:"price"`
	CoinType            string  `json:"coinType"`
	ImgBasic            string  `json:"imgBasic"`
	Listed              bool    `json:"listed"`
	BlindBoxParent      bool    `json:"-"`
	BlindBoxParentID    int     `json:"blindBoxParentId,omitempty"`
	BlindBoxParentName  string  `json:"blindBoxParentName,omitempty"`
	BlindBoxParentPrice float64 `json:"blindBoxParentPrice,omitempty"`
}

func (entry giftPanelEntry) normalizedID() int {
	if entry.GiftID > 0 {
		return entry.GiftID
	}
	return entry.ID
}

func handleRoomGiftCatalog(login *loginManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
			return
		}
		roomID := strings.TrimSpace(r.URL.Query().Get("roomId"))
		if roomID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": "房间号不能为空"})
			return
		}

		session := biliSession{}
		if login != nil {
			if authenticated, ok := login.Session(r.Context()); ok {
				session = authenticated
			}
		}
		gifts, err := fetchCurrentRoomGiftCatalog(roomID, session)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"code": -1, "message": fmt.Sprintf("当前礼物目录读取失败：%v", err)})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "gifts": gifts})
	}
}

func fetchCurrentRoomGiftCatalog(roomID string, session biliSession) ([]roomGiftInfo, error) {
	headers := map[string]string{}
	if strings.TrimSpace(session.CookieHeader) != "" {
		headers["Cookie"] = session.CookieHeader
	}
	roomPayload, err := fetchJSON(roomGiftInfoEndpoint+"?room_id="+url.QueryEscape(roomID), headers)
	if err != nil {
		return nil, err
	}
	roomContext, err := parseRoomGiftContext(roomPayload)
	if err != nil {
		return nil, err
	}

	parameters := url.Values{
		"platform":       {"pc"},
		"room_id":        {strconv.FormatInt(roomContext.RoomID, 10)},
		"area_id":        {strconv.Itoa(roomContext.AreaID)},
		"area_parent_id": {strconv.Itoa(roomContext.ParentAreaID)},
		"biz_code":       {"live"},
	}
	configPayload, err := fetchJSON(roomGiftConfigEndpoint+"?"+parameters.Encode(), headers)
	if err != nil {
		return nil, err
	}
	panelPayload, err := fetchJSON(roomGiftDataEndpoint+"?"+parameters.Encode(), headers)
	if err != nil {
		return nil, err
	}
	gifts, err := buildCurrentRoomGiftCatalog(configPayload, panelPayload)
	if err != nil {
		return nil, err
	}
	markListedBlindBoxChildren(gifts, func(giftID int) (*blindBoxInfo, bool, error) {
		return fetchBlindBoxInfo(giftID, session)
	})
	return gifts, nil
}

func parseRoomGiftContext(payload map[string]any) (roomGiftContext, error) {
	var response struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			RoomID       int64 `json:"room_id"`
			AreaID       int   `json:"area_id"`
			ParentAreaID int   `json:"parent_area_id"`
		} `json:"data"`
	}
	if err := decodeBiliPayload(payload, &response); err != nil {
		return roomGiftContext{}, err
	}
	if response.Code != 0 || response.Data.RoomID <= 0 {
		return roomGiftContext{}, fmt.Errorf("房间信息无效：%s", response.Message)
	}
	return roomGiftContext{
		RoomID: response.Data.RoomID, AreaID: response.Data.AreaID, ParentAreaID: response.Data.ParentAreaID,
	}, nil
}

func buildCurrentRoomGiftCatalog(configPayload, panelPayload map[string]any) ([]roomGiftInfo, error) {
	var configResponse struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			List []struct {
				ID        int     `json:"id"`
				Name      string  `json:"name"`
				Price     float64 `json:"price"`
				CoinType  string  `json:"coin_type"`
				ImgBasic  string  `json:"img_basic"`
				GiftType  int     `json:"gift_type"`
				GiftAttrs []int   `json:"gift_attrs"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := decodeBiliPayload(configPayload, &configResponse); err != nil {
		return nil, err
	}
	if configResponse.Code != 0 {
		return nil, fmt.Errorf("礼物配置无效：%s", configResponse.Message)
	}

	var panelResponse struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			RoomGiftList struct {
				GoldList   []giftPanelEntry `json:"gold_list"`
				SilverList []giftPanelEntry `json:"silver_list"`
			} `json:"room_gift_list"`
			TabList []struct {
				List []giftPanelEntry `json:"list"`
			} `json:"tab_list"`
		} `json:"data"`
	}
	if err := decodeBiliPayload(panelPayload, &panelResponse); err != nil {
		return nil, err
	}
	if panelResponse.Code != 0 {
		return nil, fmt.Errorf("直播间礼物面板无效：%s", panelResponse.Message)
	}

	metadata := make(map[int]giftInfo, len(configResponse.Data.List))
	blindBoxParents := make(map[int]struct{})
	metadataOrder := make([]int, 0, len(configResponse.Data.List))
	for _, gift := range configResponse.Data.List {
		if gift.ID <= 0 || strings.TrimSpace(gift.Name) == "" {
			continue
		}
		coinType := gift.CoinType
		if coinType != "gold" {
			coinType = "silver"
		}
		metadata[gift.ID] = giftInfo{
			ID: gift.ID, Name: strings.TrimSpace(gift.Name), Price: gift.Price, CoinType: coinType, ImgBasic: strings.TrimSpace(gift.ImgBasic),
		}
		if gift.GiftType == 6 || containsGiftAttribute(gift.GiftAttrs, 6) {
			blindBoxParents[gift.ID] = struct{}{}
		}
		metadataOrder = append(metadataOrder, gift.ID)
	}

	entries := make([]giftPanelEntry, 0, len(panelResponse.Data.RoomGiftList.GoldList)+len(panelResponse.Data.RoomGiftList.SilverList))
	entries = append(entries, panelResponse.Data.RoomGiftList.GoldList...)
	entries = append(entries, panelResponse.Data.RoomGiftList.SilverList...)
	for _, tab := range panelResponse.Data.TabList {
		entries = append(entries, tab.List...)
	}
	seen := map[int]struct{}{}
	gifts := make([]roomGiftInfo, 0, len(metadata))
	for _, entry := range entries {
		giftID := entry.normalizedID()
		if giftID <= 0 {
			continue
		}
		if _, exists := seen[giftID]; exists {
			continue
		}
		gift, exists := metadata[giftID]
		if !exists {
			continue
		}
		seen[giftID] = struct{}{}
		_, blindBoxParent := blindBoxParents[gift.ID]
		gifts = append(gifts, roomGiftInfo{
			ID: gift.ID, Name: gift.Name, Price: gift.Price, CoinType: gift.CoinType, ImgBasic: gift.ImgBasic, Listed: true, BlindBoxParent: blindBoxParent,
		})
	}
	for _, giftID := range metadataOrder {
		if _, exists := seen[giftID]; exists {
			continue
		}
		gift := metadata[giftID]
		_, blindBoxParent := blindBoxParents[gift.ID]
		gifts = append(gifts, roomGiftInfo{
			ID: gift.ID, Name: gift.Name, Price: gift.Price, CoinType: gift.CoinType, ImgBasic: gift.ImgBasic, Listed: false, BlindBoxParent: blindBoxParent,
		})
	}
	return gifts, nil
}

func containsGiftAttribute(attributes []int, target int) bool {
	for _, attribute := range attributes {
		if attribute == target {
			return true
		}
	}
	return false
}

func markListedBlindBoxChildren(gifts []roomGiftInfo, lookup func(int) (*blindBoxInfo, bool, error)) {
	if lookup == nil {
		return
	}
	byID := make(map[int]int, len(gifts))
	for index := range gifts {
		byID[gifts[index].ID] = index
	}
	for _, gift := range gifts {
		if !gift.Listed || !gift.BlindBoxParent {
			continue
		}
		info, _, err := lookup(gift.ID)
		if err != nil || info == nil {
			continue
		}
		for _, child := range info.Gifts {
			if index, exists := byID[child.ID]; exists {
				gifts[index].Listed = true
				if gifts[index].BlindBoxParentID <= 0 {
					parentName := strings.TrimSpace(info.Name)
					if parentName == "" {
						parentName = gift.Name
					}
					gifts[index].BlindBoxParentID = gift.ID
					gifts[index].BlindBoxParentName = parentName
					gifts[index].BlindBoxParentPrice = gift.Price
				}
			}
		}
	}
}

func decodeBiliPayload(payload map[string]any, target any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}
