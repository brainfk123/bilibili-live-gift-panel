package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64)"

var wbiKeyIndexTable = []int{
	46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35,
	27, 43, 5, 49, 33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13,
}

var wbiKeyCache struct {
	key      string
	expireAt time.Time
}

func fetchJSON(rawURL string, headers map[string]string) (map[string]any, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", "https://live.bilibili.com/")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func extractWbiKey(imgURL, subURL string) string {
	last := func(s string) string {
		parts := strings.Split(s, "/")
		return strings.Split(parts[len(parts)-1], ".")[0]
	}
	shuffled := last(imgURL) + last(subURL)
	var key strings.Builder
	for _, i := range wbiKeyIndexTable {
		if i < len(shuffled) {
			key.WriteByte(shuffled[i])
		}
	}
	return key.String()
}

func getWbiKey() (string, error) {
	now := time.Now()
	if wbiKeyCache.key != "" && now.Before(wbiKeyCache.expireAt) {
		return wbiKeyCache.key, nil
	}
	data, err := fetchJSON("https://api.bilibili.com/x/web-interface/nav", nil)
	if err != nil {
		return "", err
	}
	d, ok := data["data"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("获取 WBI 密钥失败")
	}
	img, ok := d["wbi_img"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("获取 WBI 密钥失败")
	}
	key := extractWbiKey(img["img_url"].(string), img["sub_url"].(string))
	wbiKeyCache.key = key
	wbiKeyCache.expireAt = now.Add(11 * time.Hour)
	return key, nil
}

func addWbiSign(params map[string]string, wbiKey string) map[string]string {
	wts := fmt.Sprintf("%d", time.Now().Unix())
	toSign := make(map[string]string, len(params)+1)
	replacer := strings.NewReplacer("!", "", "'", "", "(", "", ")", "", "*", "")
	for k, v := range params {
		toSign[k] = replacer.Replace(v)
	}
	toSign["wts"] = wts
	keys := make([]string, 0, len(toSign))
	for k := range toSign {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+url.QueryEscape(toSign[k]))
	}
	h := md5.Sum([]byte(strings.Join(parts, "&") + wbiKey))
	signed := make(map[string]string, len(params)+2)
	for k, v := range params {
		signed[k] = v
	}
	signed["wts"] = wts
	signed["w_rid"] = hex.EncodeToString(h[:])
	return signed
}

type roomInfo struct {
	RoomID   int64       `json:"roomId"`
	Buvid    string      `json:"buvid"`
	Token    string      `json:"token"`
	HostList []danmuHost `json:"hostList"`
}

type danmuHost struct {
	Host    string `json:"host"`
	WSSPort int    `json:"wss_port"`
}

func getRoomInfo(input string) (roomInfo, error) {
	infoData, err := fetchJSON("https://api.live.bilibili.com/room/v1/Room/get_info?room_id="+url.QueryEscape(input), nil)
	if err != nil {
		return roomInfo{}, err
	}
	code, _ := infoData["code"].(float64)
	if code != 0 {
		return roomInfo{}, fmt.Errorf("%v", infoData["message"])
	}
	roomID := int64(infoData["data"].(map[string]any)["room_id"].(float64))

	spiData, err := fetchJSON("https://api.bilibili.com/x/frontend/finger/spi", nil)
	if err != nil {
		return roomInfo{}, err
	}
	buvid := spiData["data"].(map[string]any)["b_3"].(string)

	wbiKey, err := getWbiKey()
	if err != nil {
		return roomInfo{}, err
	}
	signed := addWbiSign(map[string]string{"id": fmt.Sprintf("%d", roomID), "type": "0"}, wbiKey)
	qs := url.Values{}
	for k, v := range signed {
		qs.Set(k, v)
	}
	dmURL := "https://api.live.bilibili.com/xlive/web-room/v1/index/getDanmuInfo?" + qs.Encode()
	dmData, err := fetchJSON(dmURL, map[string]string{"Cookie": "buvid3=" + buvid})
	if err != nil {
		return roomInfo{}, err
	}
	code, _ = dmData["code"].(float64)
	if code != 0 {
		return roomInfo{}, fmt.Errorf("%v", dmData["message"])
	}
	dd := dmData["data"].(map[string]any)
	token := dd["token"].(string)
	hostBytes, err := json.Marshal(dd["host_list"])
	if err != nil {
		return roomInfo{}, err
	}
	var hosts []danmuHost
	if err := json.Unmarshal(hostBytes, &hosts); err != nil {
		return roomInfo{}, err
	}
	return roomInfo{RoomID: roomID, Buvid: buvid, Token: token, HostList: hosts}, nil
}
