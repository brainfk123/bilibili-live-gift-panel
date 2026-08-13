package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const blindBoxInfoEndpoint = "https://api.live.bilibili.com/xlive/general-interface/v1/blindFirstWin/getInfo"

type blindBoxGift struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Price    int    `json:"price"`
	ImgBasic string `json:"imgBasic"`
	Chance   string `json:"chance,omitempty"`
}

type blindBoxInfo struct {
	GiftID int            `json:"giftId"`
	Name   string         `json:"name"`
	Gifts  []blindBoxGift `json:"gifts"`
}

type blindBoxCacheEntry struct {
	info      *blindBoxInfo
	expiresAt time.Time
}

var blindBoxCache = struct {
	sync.Mutex
	entries map[int]blindBoxCacheEntry
}{entries: map[int]blindBoxCacheEntry{}}

func handleBlindBoxInfo(login *loginManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
			return
		}
		giftID, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("giftId")))
		if err != nil || giftID <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": "礼物 ID 无效"})
			return
		}

		session := biliSession{}
		if login != nil {
			if authenticated, ok := login.Session(r.Context()); ok {
				session = authenticated
			}
		}
		info, requiresLogin, err := fetchBlindBoxInfo(giftID, session)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"code": -1, "message": fmt.Sprintf("盲盒信息读取失败：%v", err)})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"code":          0,
			"blindBox":      info,
			"requiresLogin": requiresLogin,
		})
	}
}

func fetchBlindBoxInfo(giftID int, session biliSession) (*blindBoxInfo, bool, error) {
	return fetchBlindBoxInfoContext(context.Background(), giftID, session)
}

func fetchBlindBoxInfoContext(ctx context.Context, giftID int, session biliSession) (*blindBoxInfo, bool, error) {
	blindBoxCache.Lock()
	entry, cached := blindBoxCache.entries[giftID]
	blindBoxCache.Unlock()
	if cached && time.Now().Before(entry.expiresAt) {
		return entry.info, false, nil
	}

	endpoint := blindBoxInfoEndpoint + "?gift_id=" + url.QueryEscape(strconv.Itoa(giftID))
	headers := map[string]string{}
	if strings.TrimSpace(session.CookieHeader) != "" {
		headers["Cookie"] = session.CookieHeader
	}
	payload, err := fetchJSONContext(ctx, endpoint, headers)
	if err != nil {
		return nil, false, err
	}
	info, requiresLogin, err := parseBlindBoxResponse(payload, giftID)
	if err != nil || requiresLogin || info == nil {
		return info, requiresLogin, err
	}
	blindBoxCache.Lock()
	blindBoxCache.entries[giftID] = blindBoxCacheEntry{info: info, expiresAt: time.Now().Add(12 * time.Hour)}
	blindBoxCache.Unlock()
	return info, false, nil
}

func parseBlindBoxResponse(payload map[string]any, parentGiftID int) (*blindBoxInfo, bool, error) {
	code, ok := payload["code"].(float64)
	if !ok {
		return nil, false, fmt.Errorf("B 站盲盒响应缺少 code")
	}
	message := fmt.Sprint(payload["message"])
	if code == -101 || strings.Contains(message, "登录") {
		return nil, true, nil
	}
	if code != 0 {
		return nil, false, nil
	}
	rawData, ok := payload["data"]
	if !ok || rawData == nil {
		return nil, false, nil
	}
	encoded, err := json.Marshal(rawData)
	if err != nil {
		return nil, false, err
	}
	var data struct {
		BlindGiftName string `json:"blind_gift_name"`
		Gifts         []struct {
			GiftID   int    `json:"gift_id"`
			Price    int    `json:"price"`
			GiftName string `json:"gift_name"`
			GiftImg  string `json:"gift_img"`
			Chance   string `json:"chance"`
		} `json:"gifts"`
	}
	if err := json.Unmarshal(encoded, &data); err != nil {
		return nil, false, err
	}
	seen := map[int]struct{}{}
	gifts := make([]blindBoxGift, 0, len(data.Gifts))
	for _, gift := range data.Gifts {
		if gift.GiftID <= 0 || strings.TrimSpace(gift.GiftName) == "" {
			continue
		}
		if _, exists := seen[gift.GiftID]; exists {
			continue
		}
		seen[gift.GiftID] = struct{}{}
		gifts = append(gifts, blindBoxGift{
			ID: gift.GiftID, Name: gift.GiftName, Price: gift.Price, ImgBasic: gift.GiftImg, Chance: gift.Chance,
		})
	}
	if len(gifts) == 0 {
		return nil, false, nil
	}
	name := strings.TrimSpace(data.BlindGiftName)
	if name == "" {
		name = fmt.Sprintf("盲盒 %d", parentGiftID)
	}
	return &blindBoxInfo{GiftID: parentGiftID, Name: name, Gifts: gifts}, false, nil
}
