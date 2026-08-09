package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultGiftAnimationDurationMS = 3000
	minGiftAnimationDurationMS     = 1000
	maxGiftAnimationDurationMS     = 15000
	maxGiftAnimationBytes          = 16 << 20
	maxGiftEffectVideoBytes        = 64 << 20
	maxGiftEffectLayoutBytes       = 64 << 10
	maxGiftAvatarBytes             = 5 << 20
	giftMediaTimeout               = 10 * time.Second
)

type giftMediaHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type giftReceiptAPI struct {
	store  *configStore
	client giftMediaHTTPClient
}

func normalizeGiftAnimationDuration(durationMS int) int {
	if durationMS <= 0 {
		return defaultGiftAnimationDurationMS
	}
	if durationMS < minGiftAnimationDurationMS {
		return minGiftAnimationDurationMS
	}
	if durationMS > maxGiftAnimationDurationMS {
		return maxGiftAnimationDurationMS
	}
	return durationMS
}

func giftReceiptID(roomID string, gift giftEvent) string {
	key := strings.TrimSpace(gift.Rnd)
	if key == "" {
		key = fmt.Sprintf("%d|%d|%d|%d", gift.Timestamp, gift.UID, gift.GiftID, maxInt(1, gift.Num))
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(roomID) + "|" + key))
	return base64.RawURLEncoding.EncodeToString(sum[:18])
}

func appendGiftReceipt(state *appState, gift giftEvent, changes []logEntry) {
	receipt := giftReceipt{
		ID:        giftReceiptID(state.RoomID, gift),
		Time:      gift.Timestamp,
		GiftID:    gift.GiftID,
		GiftName:  strings.TrimSpace(gift.GiftName),
		Num:       maxInt(1, gift.Num),
		Price:     gift.Price,
		TotalCoin: gift.TotalCoin,
		CoinType:  gift.CoinType,
		Uname:     strings.TrimSpace(gift.Uname),
		Avatar:    strings.TrimSpace(gift.Avatar),
		SenderUID: gift.UID,
		ImgBasic:  strings.TrimSpace(gift.ImgBasic),
		Effects:   make([]giftReceiptEffect, 0, len(changes)),
	}
	gifURL := strings.TrimSpace(gift.AnimationGIF)
	webpURL := strings.TrimSpace(gift.AnimationWebP)
	effectMP4 := strings.TrimSpace(gift.EffectMP4)
	effectMP4JSON := strings.TrimSpace(gift.EffectMP4JSON)
	if gifURL != "" || webpURL != "" || (effectMP4 != "" && effectMP4JSON != "") {
		receipt.Animation = &giftReceiptAnimation{
			GIF: gifURL, WebP: webpURL, DurationMS: normalizeGiftAnimationDuration(gift.AnimationDurationMS),
			EffectID: gift.EffectID, MP4: effectMP4, MP4JSON: effectMP4JSON,
		}
	}
	for _, change := range changes {
		receipt.Effects = append(receipt.Effects, giftReceiptEffect{
			AttributeName: change.AttributeName,
			Delta:         change.Delta,
			ValueAfter:    change.ValueAfter,
			RuleID:        change.RuleID,
			TriggerName:   change.TriggerName,
		})
	}
	for _, existing := range state.GiftReceipts {
		if existing.ID == receipt.ID {
			return
		}
	}
	state.GiftReceipts = append([]giftReceipt{receipt}, state.GiftReceipts...)
	if len(state.GiftReceipts) > maxGiftReceiptEntries {
		state.GiftReceipts = state.GiftReceipts[:maxGiftReceiptEntries]
	}
}

func migrateGiftReceiptsFromLog(entries []logEntry) []giftReceipt {
	receipts := make([]giftReceipt, 0, minInt(len(entries), maxGiftReceiptEntries))
	indexes := make(map[string]int)
	for _, entry := range entries {
		if entry.Source == "timer" || entry.GiftID <= 0 {
			continue
		}
		key := legacyGiftReceiptKey(entry)
		index, exists := indexes[key]
		if !exists {
			sum := sha256.Sum256([]byte("legacy|" + key))
			receipts = append(receipts, giftReceipt{
				ID:        base64.RawURLEncoding.EncodeToString(sum[:18]),
				Time:      entry.Time,
				GiftID:    entry.GiftID,
				GiftName:  entry.GiftName,
				Num:       maxInt(1, entry.Num),
				Uname:     entry.Uname,
				Avatar:    entry.Avatar,
				SenderUID: entry.SenderUID,
				Effects:   []giftReceiptEffect{},
			})
			index = len(receipts) - 1
			indexes[key] = index
		}
		effect := giftReceiptEffect{
			AttributeName: entry.AttributeName,
			Delta:         entry.Delta, ValueAfter: entry.ValueAfter,
			RuleID: entry.RuleID, TriggerName: entry.TriggerName,
		}
		receipts[index].Effects = append(receipts[index].Effects, effect)
		if len(receipts) >= maxGiftReceiptEntries {
			break
		}
	}
	return receipts
}

func legacyGiftReceiptKey(entry logEntry) string {
	eventID := strings.TrimSpace(entry.EventID)
	suffix := ":" + entry.AttributeName
	if eventID != "" && strings.HasSuffix(eventID, suffix) {
		eventID = strings.TrimSuffix(eventID, suffix)
	}
	if eventID != "" {
		return "event|" + eventID
	}
	return fmt.Sprintf("fields|%d|%d|%d|%d|%s", entry.Time, entry.GiftID, entry.SenderUID, maxInt(1, entry.Num), entry.Uname)
}

func newGiftReceiptAPI(store *configStore, client giftMediaHTTPClient) *giftReceiptAPI {
	if client == nil {
		client = &http.Client{
			Timeout: giftMediaTimeout,
			CheckRedirect: func(request *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("媒体重定向次数过多")
				}
				return validateBilibiliMediaURL(request.URL)
			},
		}
	}
	return &giftReceiptAPI{store: store, client: client}
}

func (api *giftReceiptAPI) handleReceipts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
		return
	}
	if !isSameOriginGiftReceiptRequest(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"code": -1, "message": "拒绝跨站请求"})
		return
	}
	state, err := api.store.updateState(func(state *appState) error {
		state.GiftReceipts = []giftReceipt{}
		return nil
	})
	if err != nil {
		writeConfigStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "giftReceipts": state.GiftReceipts})
}

func (api *giftReceiptAPI) handleMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "不支持的请求方法", http.StatusMethodNotAllowed)
		return
	}
	receiptID := strings.TrimSpace(r.URL.Query().Get("id"))
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if receiptID == "" || !isGiftReceiptMediaKind(kind) {
		http.Error(w, "媒体参数无效", http.StatusBadRequest)
		return
	}
	state, err := api.store.readState()
	if err != nil {
		http.Error(w, "送礼记录读取失败", http.StatusInternalServerError)
		return
	}
	var receipt *giftReceipt
	for index := range state.GiftReceipts {
		if state.GiftReceipts[index].ID == receiptID {
			receipt = &state.GiftReceipts[index]
			break
		}
	}
	if receipt == nil {
		http.Error(w, "送礼记录不存在", http.StatusNotFound)
		return
	}
	candidates, maxBytes, allowedTypes := giftReceiptMediaCandidates(*receipt, kind)
	if len(candidates) == 0 {
		http.Error(w, "该记录没有可用媒体", http.StatusNotFound)
		return
	}
	var media []byte
	var mediaType string
	for _, candidate := range candidates {
		media, mediaType, err = api.fetchMedia(r.Context(), candidate, maxBytes, allowedTypes)
		if err == nil {
			break
		}
	}
	if err != nil {
		http.Error(w, "媒体暂时无法读取，请稍后重试", http.StatusBadGateway)
		return
	}
	if kind == "effect-layout" {
		layout, parseErr := parseGiftEffectLayout(media)
		if parseErr != nil {
			http.Error(w, "礼物特效坐标暂时无法读取，请稍后重试", http.StatusBadGateway)
			return
		}
		media, err = json.Marshal(layout)
		if err != nil {
			http.Error(w, "礼物特效坐标暂时无法读取，请稍后重试", http.StatusInternalServerError)
			return
		}
		mediaType = "application/json"
	}
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Length", strconv.Itoa(len(media)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(media)
}

func isGiftReceiptMediaKind(kind string) bool {
	switch kind {
	case "animation", "avatar", "effect-video", "effect-layout":
		return true
	default:
		return false
	}
}

func giftReceiptMediaCandidates(receipt giftReceipt, kind string) ([]string, int64, map[string]struct{}) {
	if kind == "avatar" {
		return compactMediaURLs(receipt.Avatar), maxGiftAvatarBytes, map[string]struct{}{
			"image/gif": {}, "image/jpeg": {}, "image/png": {}, "image/webp": {},
		}
	}
	if receipt.Animation == nil {
		return nil, maxGiftAnimationBytes, nil
	}
	if kind == "effect-video" {
		return compactMediaURLs(receipt.Animation.MP4), maxGiftEffectVideoBytes, map[string]struct{}{
			"video/mp4": {},
		}
	}
	if kind == "effect-layout" {
		return compactMediaURLs(receipt.Animation.MP4JSON), maxGiftEffectLayoutBytes, map[string]struct{}{
			"application/json": {}, "text/json": {},
		}
	}
	return compactMediaURLs(receipt.Animation.GIF, receipt.Animation.WebP), maxGiftAnimationBytes, map[string]struct{}{
		"image/gif": {}, "image/webp": {},
	}
}

func compactMediaURLs(values ...string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (api *giftReceiptAPI) fetchMedia(parent context.Context, rawURL string, maxBytes int64, allowedTypes map[string]struct{}) ([]byte, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", err
	}
	if err := validateBilibiliMediaURL(parsed); err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(parent, giftMediaTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Accept", giftReceiptMediaAccept(allowedTypes))
	response, err := api.client.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.Request != nil && response.Request.URL != nil {
		if err := validateBilibiliMediaURL(response.Request.URL); err != nil {
			return nil, "", err
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, "", fmt.Errorf("媒体 HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maxBytes {
		return nil, "", errors.New("媒体文件过大")
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil {
		return nil, "", errors.New("媒体类型无效")
	}
	mediaType = strings.ToLower(mediaType)
	if _, ok := allowedTypes[mediaType]; !ok {
		return nil, "", errors.New("媒体类型不受支持")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > maxBytes {
		return nil, "", errors.New("媒体文件过大")
	}
	return data, mediaType, nil
}

func giftReceiptMediaAccept(allowedTypes map[string]struct{}) string {
	if _, ok := allowedTypes["video/mp4"]; ok {
		return "video/mp4"
	}
	if _, ok := allowedTypes["application/json"]; ok {
		return "application/json"
	}
	return "image/avif,image/webp,image/png,image/jpeg,image/gif"
}

func validateBilibiliMediaURL(candidate *url.URL) error {
	if candidate == nil || !strings.EqualFold(candidate.Scheme, "https") {
		return errors.New("只允许 HTTPS 媒体")
	}
	host := strings.ToLower(strings.TrimSuffix(candidate.Hostname(), "."))
	if host != "hdslb.com" && !strings.HasSuffix(host, ".hdslb.com") {
		return errors.New("媒体域名不在允许列表")
	}
	if candidate.User != nil {
		return errors.New("媒体地址不能包含身份信息")
	}
	return nil
}

func isSameOriginGiftReceiptRequest(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}
