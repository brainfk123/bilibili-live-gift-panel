package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	specialGiftGuardCaptain  = 1_900_000_001
	specialGiftGuardAdmiral  = 1_900_000_002
	specialGiftGuardGovernor = 1_900_000_003
	specialGiftSuperChat     = 1_900_000_004
)

type biliSenderUinfo struct {
	UID  biliUID `json:"uid"`
	Base struct {
		Name       string `json:"name"`
		Face       string `json:"face"`
		OriginInfo struct {
			Name string `json:"name"`
			Face string `json:"face"`
		} `json:"origin_info"`
	} `json:"base"`
}

func parseBiliPaidEvent(body []byte) (giftEvent, bool) {
	var envelope struct {
		Command string          `json:"cmd"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return giftEvent{}, false
	}
	command, _, _ := strings.Cut(strings.TrimSpace(envelope.Command), ":")
	switch command {
	case "GUARD_BUY":
		return parseGuardBuy(envelope.Data)
	case "SUPER_CHAT_MESSAGE", "SUPER_CHAT_MESSAGE_JPN":
		return parseSuperChat(envelope.Data)
	default:
		return giftEvent{}, false
	}
}

func parseGuardBuy(payload []byte) (giftEvent, bool) {
	var data struct {
		UID        biliUID         `json:"uid"`
		Uname      string          `json:"uname"`
		Username   string          `json:"username"`
		Face       string          `json:"face"`
		GuardLevel int             `json:"guard_level"`
		Num        int             `json:"num"`
		Price      float64         `json:"price"`
		TotalCoin  float64         `json:"total_coin"`
		Timestamp  int64           `json:"timestamp"`
		StartTime  int64           `json:"start_time"`
		ID         json.RawMessage `json:"id"`
		OrderID    json.RawMessage `json:"order_id"`
		Sender     biliSenderUinfo `json:"sender_uinfo"`
	}
	if json.Unmarshal(payload, &data) != nil {
		return giftEvent{}, false
	}
	giftID, giftName, iconLabel, iconColor, ok := guardGiftIdentity(data.GuardLevel)
	if !ok {
		return giftEvent{}, false
	}
	if data.Num < 1 {
		data.Num = 1
	}
	uid := int64(data.UID)
	if uid <= 0 {
		uid = int64(data.Sender.UID)
	}
	uname := bestBiliUsername(data.Username, data.Uname, data.Sender.Base.Name, data.Sender.Base.OriginInfo.Name)
	avatar := firstNonEmptyEventField(data.Sender.Base.Face, data.Sender.Base.OriginInfo.Face, data.Face)
	timestamp := firstPositiveInt64(data.Timestamp, data.StartTime)
	if timestamp <= 0 {
		timestamp = time.Now().Unix()
	}
	totalCoin := data.TotalCoin
	if totalCoin <= 0 {
		totalCoin = data.Price * float64(data.Num)
	}
	eventID := firstNonEmptyEventField(jsonScalarString(data.OrderID), jsonScalarString(data.ID))
	rnd := ""
	if eventID != "" {
		rnd = "guard:" + eventID
	}
	return giftEvent{
		GiftID: giftID, GiftName: giftName, Num: data.Num, Price: data.Price,
		CoinType: "gold", TotalCoin: totalCoin, Uname: uname, Avatar: avatar, UID: uid,
		Timestamp: timestamp, ImgBasic: specialEventIcon(iconLabel, iconColor), Rnd: rnd,
	}, true
}

func parseSuperChat(payload []byte) (giftEvent, bool) {
	var data struct {
		ID        json.RawMessage `json:"id"`
		UID       biliUID         `json:"uid"`
		PriceYuan float64         `json:"price"`
		Timestamp int64           `json:"ts"`
		StartTime int64           `json:"start_time"`
		UserInfo  struct {
			Uname string `json:"uname"`
			Face  string `json:"face"`
		} `json:"user_info"`
		Sender biliSenderUinfo `json:"sender_uinfo"`
	}
	if json.Unmarshal(payload, &data) != nil || data.PriceYuan <= 0 {
		return giftEvent{}, false
	}
	uid := int64(data.UID)
	if uid <= 0 {
		uid = int64(data.Sender.UID)
	}
	uname := bestBiliUsername(data.UserInfo.Uname, data.Sender.Base.Name, data.Sender.Base.OriginInfo.Name)
	avatar := firstNonEmptyEventField(data.UserInfo.Face, data.Sender.Base.Face, data.Sender.Base.OriginInfo.Face)
	timestamp := firstPositiveInt64(data.Timestamp, data.StartTime)
	if timestamp <= 0 {
		timestamp = time.Now().Unix()
	}
	price := data.PriceYuan * 1000
	eventID := jsonScalarString(data.ID)
	rnd := ""
	if eventID != "" {
		rnd = "super-chat:" + eventID
	}
	return giftEvent{
		GiftID: specialGiftSuperChat, GiftName: "Super Chat", Num: 1, Price: price,
		CoinType: "gold", TotalCoin: price, Uname: uname, Avatar: avatar, UID: uid,
		Timestamp: timestamp, ImgBasic: specialEventIcon("SC", "#ff5f91"), Rnd: rnd,
	}, true
}

func guardGiftIdentity(level int) (id int, name, label, color string, ok bool) {
	switch level {
	case 3:
		return specialGiftGuardCaptain, "大航海·舰长", "舰", "#3e9cff", true
	case 2:
		return specialGiftGuardAdmiral, "大航海·提督", "提", "#9f6cff", true
	case 1:
		return specialGiftGuardGovernor, "大航海·总督", "总", "#ff9d38", true
	default:
		return 0, "", "", "", false
	}
}

func bestBiliUsername(candidates ...string) string {
	result := ""
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if result == "" || (isMaskedUsername(result) && !isMaskedUsername(candidate)) {
			result = candidate
		}
	}
	return result
}

func firstNonEmptyEventField(candidates ...string) string {
	for _, candidate := range candidates {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return candidate
		}
	}
	return ""
}

func firstPositiveInt64(candidates ...int64) int64 {
	for _, candidate := range candidates {
		if candidate > 0 {
			return candidate
		}
	}
	return 0
}

func jsonScalarString(value json.RawMessage) string {
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	if strings.HasPrefix(trimmed, `"`) {
		var decoded string
		if json.Unmarshal(value, &decoded) == nil {
			return strings.TrimSpace(decoded)
		}
	}
	return strings.Trim(trimmed, `"`)
}

func specialEventIcon(label, color string) string {
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 96 96"><rect width="96" height="96" rx="24" fill="%s"/><rect x="7" y="7" width="82" height="82" rx="20" fill="none" stroke="#fff" stroke-opacity=".28" stroke-width="2"/><text x="48" y="59" text-anchor="middle" font-family="sans-serif" font-size="34" font-weight="800" fill="#fff">%s</text></svg>`, color, label)
	return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(svg))
}
