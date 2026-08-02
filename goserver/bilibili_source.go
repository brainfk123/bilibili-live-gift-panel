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

type bilibiliGiftSource struct{}

type biliPacket struct {
	protocolVersion uint16
	operation       uint32
	body            []byte
}

func (source *bilibiliGiftSource) Run(ctx context.Context, roomID string, callbacks runtimeCallbacks) error {
	callbacks.onState("connecting")
	info, err := getRoomInfo(roomID)
	if err != nil {
		return err
	}
	host := danmuHost{Host: "broadcastlv.chat.bilibili.com", WSSPort: 443}
	if len(info.HostList) > 0 {
		host = info.HostList[0]
	}
	url := fmt.Sprintf("wss://%s:%d/sub", host.Host, host.WSSPort)
	headers := http.Header{"User-Agent": []string{userAgent}, "Origin": []string{"https://live.bilibili.com"}}
	connection, _, err := websocket.DefaultDialer.DialContext(ctx, url, headers)
	if err != nil {
		return err
	}
	defer connection.Close()

	auth, _ := json.Marshal(map[string]any{
		"uid": 0, "roomid": info.RoomID, "protover": 2, "platform": "web",
		"buvid": info.Buvid, "type": 2, "key": info.Token,
	})
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
					callbacks.onGift(gift)
				}
			}
		}
	}
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
		GiftID    int             `json:"giftId"`
		GiftName  string          `json:"giftName"`
		Num       int             `json:"num"`
		Price     float64         `json:"price"`
		CoinType  string          `json:"coin_type"`
		TotalCoin float64         `json:"total_coin"`
		Uname     string          `json:"uname"`
		Face      string          `json:"face"`
		UID       int64           `json:"uid"`
		Timestamp int64           `json:"timestamp"`
		Rnd       json.RawMessage `json:"rnd"`
		GiftInfo  struct {
			ImgBasic string `json:"img_basic"`
		} `json:"gift_info"`
	}
	if json.Unmarshal(envelope.Data, &data) != nil {
		return giftEvent{}, false
	}
	rnd := strings.Trim(string(data.Rnd), `"`)
	if rnd == "" || rnd == "null" {
		rnd = fmt.Sprintf("%d-%d-%d", data.Timestamp, data.UID, data.GiftID)
	}
	if data.Num < 1 {
		data.Num = 1
	}
	return giftEvent{
		GiftID: data.GiftID, GiftName: data.GiftName, Num: data.Num, Price: data.Price,
		CoinType: data.CoinType, TotalCoin: data.TotalCoin, Uname: data.Uname, Avatar: data.Face, UID: data.UID,
		Timestamp: data.Timestamp, ImgBasic: data.GiftInfo.ImgBasic, Rnd: rnd,
	}, true
}
