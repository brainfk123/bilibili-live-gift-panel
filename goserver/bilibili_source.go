package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	biliHeaderLength = 16
	biliOpHeartbeat  = 2
	biliOpMessage    = 5
	biliOpAuth       = 7
	biliOpAuthReply  = 8
)

type bilibiliGiftSource struct {
	sessionProvider func(context.Context) (biliSession, bool)
}

type biliPacket struct {
	protocolVersion uint16
	operation       uint32
	body            []byte
}

type biliUID int64

func (uid *biliUID) UnmarshalJSON(value []byte) error {
	text := strings.Trim(strings.TrimSpace(string(value)), `"`)
	if text == "" || text == "null" {
		*uid = 0
		return nil
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return err
	}
	*uid = biliUID(parsed)
	return nil
}

func (source *bilibiliGiftSource) Run(ctx context.Context, roomID string, callbacks runtimeCallbacks) error {
	callbacks.onState("connecting")
	session := biliSession{}
	if source.sessionProvider != nil {
		if authenticated, ok := source.sessionProvider(ctx); ok {
			session = authenticated
		}
	}
	info, err := getRoomInfoWithSession(roomID, session)
	if err != nil {
		return err
	}
	session = sessionForRoomInfo(info, session)
	catalogByID := map[int]roomGiftInfo{}
	if callbacks.onGiftCatalog != nil {
		if gifts, catalogErr := fetchCurrentRoomGiftCatalog(roomID, session); catalogErr == nil {
			for _, gift := range gifts {
				catalogByID[gift.ID] = gift
			}
			callbacks.onGiftCatalog(gifts)
		} else if callbacks.onGiftCatalogError != nil {
			callbacks.onGiftCatalogError(catalogErr)
		}
	}
	host := danmuHost{Host: "broadcastlv.chat.bilibili.com", WSSPort: 443}
	if len(info.HostList) > 0 {
		host = info.HostList[0]
	}
	url := fmt.Sprintf("wss://%s:%d/sub", host.Host, host.WSSPort)
	headers := http.Header{"User-Agent": []string{userAgent}, "Origin": []string{"https://live.bilibili.com"}}
	if session.CookieHeader != "" {
		headers.Set("Cookie", session.CookieHeader)
	}
	connection, _, err := websocket.DefaultDialer.DialContext(ctx, url, headers)
	if err != nil {
		return err
	}
	defer connection.Close()

	auth := buildBiliAuthPayload(info, session)
	var writeMu sync.Mutex
	write := func(payload []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return connection.WriteMessage(websocket.BinaryMessage, payload)
	}
	if err := write(encodeBiliPacket(biliOpAuth, auth)); err != nil {
		return err
	}
	_ = connection.SetReadDeadline(time.Now().Add(90 * time.Second))

	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = connection.Close()
				return
			case <-done:
				return
			case <-ticker.C:
				if write(encodeBiliPacket(biliOpHeartbeat, nil)) != nil {
					_ = connection.Close()
					return
				}
			}
		}
	}()

	for {
		_, payload, err := connection.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		_ = connection.SetReadDeadline(time.Now().Add(90 * time.Second))
		for _, packet := range decodeBiliPackets(payload) {
			if packet.operation == biliOpAuthReply {
				var reply struct {
					Code int `json:"code"`
				}
				if err := json.Unmarshal(packet.body, &reply); err != nil || reply.Code != 0 {
					return fmt.Errorf("弹幕服务器认证失败")
				}
				callbacks.onState("connected")
				continue
			}
			if packet.operation != biliOpMessage {
				continue
			}
			bodies := [][]byte{packet.body}
			if packet.protocolVersion == 2 {
				inflated, err := inflateBiliPacket(packet.body)
				if err != nil {
					continue
				}
				bodies = bodies[:0]
				for _, nested := range decodeBiliPackets(inflated) {
					bodies = append(bodies, nested.body)
				}
			}
			for _, body := range bodies {
				if gift, ok := parseBiliGift(body); ok {
					callbacks.onGift(enrichGiftAnimationFromRoomCatalog(gift, catalogByID))
					continue
				}
				if paidEvent, ok := parseBiliPaidEvent(body); ok {
					callbacks.onGift(paidEvent)
				}
			}
		}
	}
}

func sessionForRoomInfo(_ roomInfo, session biliSession) biliSession {
	if session.UID <= 0 || strings.TrimSpace(session.CookieHeader) == "" {
		return biliSession{}
	}
	return session
}

func buildBiliAuthPayload(info roomInfo, session biliSession) []byte {
	uid := session.UID
	if uid < 0 {
		uid = 0
	}
	buvid := strings.TrimSpace(session.Buvid)
	if buvid == "" {
		buvid = info.Buvid
	}
	payload, _ := json.Marshal(map[string]any{
		"uid": uid, "roomid": info.RoomID, "protover": 2, "platform": "web",
		"buvid": buvid, "type": 2, "key": info.Token,
	})
	return payload
}

func encodeBiliPacket(operation uint32, body []byte) []byte {
	payload := make([]byte, biliHeaderLength+len(body))
	binary.BigEndian.PutUint32(payload[0:4], uint32(len(payload)))
	binary.BigEndian.PutUint16(payload[4:6], biliHeaderLength)
	binary.BigEndian.PutUint16(payload[6:8], 0)
	binary.BigEndian.PutUint32(payload[8:12], operation)
	binary.BigEndian.PutUint32(payload[12:16], 1)
	copy(payload[biliHeaderLength:], body)
	return payload
}

func decodeBiliPackets(payload []byte) []biliPacket {
	packets := []biliPacket{}
	for offset := 0; offset+biliHeaderLength <= len(payload); {
		totalLength := int(binary.BigEndian.Uint32(payload[offset : offset+4]))
		headerLength := int(binary.BigEndian.Uint16(payload[offset+4 : offset+6]))
		if totalLength < headerLength || headerLength < biliHeaderLength || offset+totalLength > len(payload) {
			break
		}
		packets = append(packets, biliPacket{
			protocolVersion: binary.BigEndian.Uint16(payload[offset+6 : offset+8]),
			operation:       binary.BigEndian.Uint32(payload[offset+8 : offset+12]),
			body:            append([]byte(nil), payload[offset+headerLength:offset+totalLength]...),
		})
		offset += totalLength
	}
	return packets
}

func inflateBiliPacket(body []byte) ([]byte, error) {
	reader, err := zlib.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func parseBiliGift(body []byte) (giftEvent, bool) {
	var envelope struct {
		Command string          `json:"cmd"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Command != "SEND_GIFT" {
		return giftEvent{}, false
	}
	var data struct {
		GiftID            int             `json:"giftId"`
		BlindGiftID       biliUID         `json:"blind_gift_id"`
		BlindGiftName     string          `json:"blind_gift_name"`
		BlindGiftPrice    float64         `json:"blind_gift_price"`
		OriginalGiftPrice float64         `json:"original_gift_price"`
		GiftName          string          `json:"giftName"`
		Num               int             `json:"num"`
		Price             float64         `json:"price"`
		CoinType          string          `json:"coin_type"`
		TotalCoin         float64         `json:"total_coin"`
		Uname             string          `json:"uname"`
		Face              string          `json:"face"`
		UID               biliUID         `json:"uid"`
		Timestamp         int64           `json:"timestamp"`
		Rnd               json.RawMessage `json:"rnd"`
		BlindGift         struct {
			GiftID            biliUID `json:"blind_gift_id"`
			GiftName          string  `json:"gift_name"`
			GiftPrice         float64 `json:"gift_price"`
			OriginalGiftPrice float64 `json:"original_gift_price"`
		} `json:"blind_gift"`
		GiftInfo struct {
			ImgBasic string `json:"img_basic"`
			GIF      string `json:"gif"`
			WebP     string `json:"webp"`
		} `json:"gift_info"`
		SenderUinfo struct {
			UID  biliUID `json:"uid"`
			Base struct {
				Name       string `json:"name"`
				Face       string `json:"face"`
				OriginInfo struct {
					Name string `json:"name"`
					Face string `json:"face"`
				} `json:"origin_info"`
			} `json:"base"`
		} `json:"sender_uinfo"`
	}
	if json.Unmarshal(envelope.Data, &data) != nil {
		return giftEvent{}, false
	}
	if data.BlindGiftID <= 0 {
		data.BlindGiftID = data.BlindGift.GiftID
	}
	blindGiftName := strings.TrimSpace(data.BlindGiftName)
	if blindGiftName == "" {
		blindGiftName = strings.TrimSpace(data.BlindGift.GiftName)
	}
	blindGiftPrice := firstPositiveFloat(
		data.BlindGiftPrice,
		data.OriginalGiftPrice,
		data.BlindGift.OriginalGiftPrice,
		data.BlindGift.GiftPrice,
	)
	uid := int64(data.UID)
	if uid <= 0 {
		uid = int64(data.SenderUinfo.UID)
	}
	uname := strings.TrimSpace(data.Uname)
	for _, candidate := range []string{data.SenderUinfo.Base.Name, data.SenderUinfo.Base.OriginInfo.Name} {
		if isMaskedUsername(uname) && !isMaskedUsername(candidate) {
			uname = strings.TrimSpace(candidate)
		}
	}
	avatar := strings.TrimSpace(data.SenderUinfo.Base.Face)
	if avatar == "" {
		avatar = strings.TrimSpace(data.SenderUinfo.Base.OriginInfo.Face)
	}
	if avatar == "" {
		avatar = strings.TrimSpace(data.Face)
	}
	rnd := strings.Trim(string(data.Rnd), `"`)
	if rnd == "" || rnd == "null" {
		rnd = fmt.Sprintf("%d-%d-%d", data.Timestamp, uid, data.GiftID)
	}
	if data.Num < 1 {
		data.Num = 1
	}
	return giftEvent{
		GiftID: data.GiftID, BlindGiftID: int(data.BlindGiftID), BlindGiftName: blindGiftName, BlindGiftPrice: blindGiftPrice,
		GiftName: data.GiftName, Num: data.Num, Price: data.Price,
		CoinType: data.CoinType, TotalCoin: data.TotalCoin, Uname: uname, Avatar: avatar, UID: uid,
		Timestamp: data.Timestamp, ImgBasic: data.GiftInfo.ImgBasic,
		AnimationGIF: strings.TrimSpace(data.GiftInfo.GIF), AnimationWebP: strings.TrimSpace(data.GiftInfo.WebP),
		Rnd: rnd,
	}, true
}

func enrichGiftAnimationFromRoomCatalog(gift giftEvent, catalog map[int]roomGiftInfo) giftEvent {
	metadata, exists := catalog[gift.GiftID]
	if !exists {
		return gift
	}
	if strings.TrimSpace(gift.ImgBasic) == "" {
		gift.ImgBasic = metadata.ImgBasic
	}
	if strings.TrimSpace(gift.AnimationGIF) == "" {
		gift.AnimationGIF = metadata.AnimationGIF
	}
	if strings.TrimSpace(gift.AnimationWebP) == "" {
		gift.AnimationWebP = metadata.AnimationWebP
	}
	if gift.AnimationDurationMS <= 0 {
		gift.AnimationDurationMS = metadata.AnimationDurationMS
	}
	return gift
}

func firstPositiveFloat(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
