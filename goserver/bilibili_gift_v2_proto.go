package main

import (
	"encoding/base64"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
)

const (
	biliGiftV2ProtobufMaxBytes = 256 * 1024
	biliGiftV2ProtobufMaxItems = 256
)

type biliGiftV2ProtobufBatch struct {
	UID            int64
	Uname          string
	Face           string
	GuardLevel     int
	MedalName      string
	BlindGiftName  string
	BlindGiftPrice float64
	Items          []biliGiftV2ProtobufItem
}

type biliGiftV2ProtobufItem struct {
	GiftID    int
	GiftName  string
	Num       int
	Price     float64
	TotalCoin float64
	CoinType  string
	TID       string
	Timestamp int64
	Rnd       string
	ImgBasic  string
}

func parseBiliGiftV2Protobuf(encoded string) ([]giftEvent, string, bool) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" || len(encoded) > base64.StdEncoding.EncodedLen(biliGiftV2ProtobufMaxBytes) {
		return nil, "malformed_gift_data", false
	}
	message, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(message) == 0 || len(message) > biliGiftV2ProtobufMaxBytes {
		return nil, "malformed_gift_data", false
	}
	batch, ok := parseBiliGiftV2ProtobufBatch(message)
	if !ok || len(batch.Items) == 0 {
		return nil, "malformed_gift_data", false
	}

	membership := biliMembership(batch.GuardLevel, batch.MedalName)
	gifts := make([]giftEvent, 0, len(batch.Items))
	for index, item := range batch.Items {
		name := strings.TrimSpace(item.GiftName)
		if item.GiftID <= 0 || name == "" {
			return nil, "malformed_gift_data", false
		}
		if item.Num < 1 {
			item.Num = 1
		}
		rnd := strings.TrimSpace(item.Rnd)
		if rnd == "" {
			rnd = strings.TrimSpace(item.TID)
		}
		if rnd == "" {
			rnd = strings.TrimSpace(strings.Join([]string{
				formatInt64(item.Timestamp), formatInt64(batch.UID), formatInt(item.GiftID), formatInt(index),
			}, "-"))
		}
		totalCoin := item.TotalCoin
		if totalCoin <= 0 {
			totalCoin = item.Price * float64(item.Num)
		}
		gifts = append(gifts, giftEvent{
			GiftID: item.GiftID, GiftName: name, Num: item.Num, Price: item.Price,
			BlindGiftName: strings.TrimSpace(batch.BlindGiftName), BlindGiftPrice: batch.BlindGiftPrice,
			CoinType: strings.TrimSpace(item.CoinType), TotalCoin: totalCoin,
			Uname: strings.TrimSpace(batch.Uname), Avatar: strings.TrimSpace(batch.Face), UID: batch.UID,
			Timestamp: item.Timestamp, ImgBasic: strings.TrimSpace(item.ImgBasic), Membership: membership, Rnd: rnd,
		})
	}
	return gifts, "SEND_GIFT_V2", true
}

func parseBiliGiftV2ProtobufBatch(message []byte) (biliGiftV2ProtobufBatch, bool) {
	batch := biliGiftV2ProtobufBatch{Items: []biliGiftV2ProtobufItem{}}
	for len(message) > 0 {
		number, wireType, tagBytes := protowire.ConsumeTag(message)
		if tagBytes < 0 || !number.IsValid() {
			return biliGiftV2ProtobufBatch{}, false
		}
		message = message[tagBytes:]
		var valueBytes int
		switch number {
		case 1:
			value, consumed, ok := consumeBiliProtobufVarint(message, wireType)
			if !ok || value > math.MaxInt64 {
				return biliGiftV2ProtobufBatch{}, false
			}
			batch.UID, valueBytes = int64(value), consumed
		case 2:
			value, consumed, ok := consumeBiliProtobufString(message, wireType)
			if !ok {
				return biliGiftV2ProtobufBatch{}, false
			}
			batch.Uname, valueBytes = value, consumed
		case 3:
			value, consumed, ok := consumeBiliProtobufString(message, wireType)
			if !ok {
				return biliGiftV2ProtobufBatch{}, false
			}
			batch.Face, valueBytes = value, consumed
		case 5:
			value, consumed, ok := consumeBiliProtobufInt(message, wireType)
			if !ok {
				return biliGiftV2ProtobufBatch{}, false
			}
			batch.GuardLevel, valueBytes = value, consumed
		case 8:
			value, consumed, ok := consumeBiliProtobufBytes(message, wireType)
			if !ok {
				return biliGiftV2ProtobufBatch{}, false
			}
			medalName, ok := parseBiliGiftV2ProtobufMedal(value)
			if !ok {
				return biliGiftV2ProtobufBatch{}, false
			}
			batch.MedalName, valueBytes = medalName, consumed
		case 9:
			value, consumed, ok := consumeBiliProtobufBytes(message, wireType)
			if !ok {
				return biliGiftV2ProtobufBatch{}, false
			}
			name, price, ok := parseBiliGiftV2ProtobufBlindGift(value)
			if !ok {
				return biliGiftV2ProtobufBatch{}, false
			}
			batch.BlindGiftName, batch.BlindGiftPrice, valueBytes = name, price, consumed
		case 10:
			if len(batch.Items) >= biliGiftV2ProtobufMaxItems {
				return biliGiftV2ProtobufBatch{}, false
			}
			value, consumed, ok := consumeBiliProtobufBytes(message, wireType)
			if !ok {
				return biliGiftV2ProtobufBatch{}, false
			}
			item, ok := parseBiliGiftV2ProtobufItem(value)
			if !ok {
				return biliGiftV2ProtobufBatch{}, false
			}
			batch.Items = append(batch.Items, item)
			valueBytes = consumed
		default:
			valueBytes = protowire.ConsumeFieldValue(number, wireType, message)
			if valueBytes < 0 {
				return biliGiftV2ProtobufBatch{}, false
			}
		}
		message = message[valueBytes:]
	}
	return batch, true
}

func parseBiliGiftV2ProtobufItem(message []byte) (biliGiftV2ProtobufItem, bool) {
	item := biliGiftV2ProtobufItem{}
	for len(message) > 0 {
		number, wireType, tagBytes := protowire.ConsumeTag(message)
		if tagBytes < 0 || !number.IsValid() {
			return biliGiftV2ProtobufItem{}, false
		}
		message = message[tagBytes:]
		var valueBytes int
		switch number {
		case 1:
			value, consumed, ok := consumeBiliProtobufInt(message, wireType)
			if !ok {
				return biliGiftV2ProtobufItem{}, false
			}
			item.GiftID, valueBytes = value, consumed
		case 2:
			value, consumed, ok := consumeBiliProtobufString(message, wireType)
			if !ok {
				return biliGiftV2ProtobufItem{}, false
			}
			item.GiftName, valueBytes = value, consumed
		case 3:
			value, consumed, ok := consumeBiliProtobufInt(message, wireType)
			if !ok {
				return biliGiftV2ProtobufItem{}, false
			}
			item.Num, valueBytes = value, consumed
		case 5:
			value, consumed, ok := consumeBiliProtobufFloat(message, wireType)
			if !ok {
				return biliGiftV2ProtobufItem{}, false
			}
			item.Price, valueBytes = value, consumed
		case 7:
			value, consumed, ok := consumeBiliProtobufFloat(message, wireType)
			if !ok {
				return biliGiftV2ProtobufItem{}, false
			}
			item.TotalCoin, valueBytes = value, consumed
		case 8:
			value, consumed, ok := consumeBiliProtobufString(message, wireType)
			if !ok {
				return biliGiftV2ProtobufItem{}, false
			}
			item.CoinType, valueBytes = value, consumed
		case 9:
			value, consumed, ok := consumeBiliProtobufString(message, wireType)
			if !ok {
				return biliGiftV2ProtobufItem{}, false
			}
			item.TID, valueBytes = value, consumed
		case 10:
			value, consumed, ok := consumeBiliProtobufVarint(message, wireType)
			if !ok || value > math.MaxInt64 {
				return biliGiftV2ProtobufItem{}, false
			}
			item.Timestamp, valueBytes = int64(value), consumed
		case 12:
			value, consumed, ok := consumeBiliProtobufString(message, wireType)
			if !ok {
				return biliGiftV2ProtobufItem{}, false
			}
			item.Rnd, valueBytes = value, consumed
		case 35:
			value, consumed, ok := consumeBiliProtobufBytes(message, wireType)
			if !ok {
				return biliGiftV2ProtobufItem{}, false
			}
			imgBasic, ok := parseBiliGiftV2ProtobufGiftInfo(value)
			if !ok {
				return biliGiftV2ProtobufItem{}, false
			}
			item.ImgBasic, valueBytes = imgBasic, consumed
		default:
			valueBytes = protowire.ConsumeFieldValue(number, wireType, message)
			if valueBytes < 0 {
				return biliGiftV2ProtobufItem{}, false
			}
		}
		message = message[valueBytes:]
	}
	return item, true
}

func parseBiliGiftV2ProtobufMedal(message []byte) (string, bool) {
	name := ""
	for len(message) > 0 {
		number, wireType, tagBytes := protowire.ConsumeTag(message)
		if tagBytes < 0 || !number.IsValid() {
			return "", false
		}
		message = message[tagBytes:]
		valueBytes := 0
		if number == 6 {
			value, consumed, ok := consumeBiliProtobufString(message, wireType)
			if !ok {
				return "", false
			}
			name, valueBytes = value, consumed
		} else {
			valueBytes = protowire.ConsumeFieldValue(number, wireType, message)
			if valueBytes < 0 {
				return "", false
			}
		}
		message = message[valueBytes:]
	}
	return name, true
}

func parseBiliGiftV2ProtobufBlindGift(message []byte) (string, float64, bool) {
	name := ""
	price := float64(0)
	for len(message) > 0 {
		number, wireType, tagBytes := protowire.ConsumeTag(message)
		if tagBytes < 0 || !number.IsValid() {
			return "", 0, false
		}
		message = message[tagBytes:]
		var valueBytes int
		switch number {
		case 3:
			value, consumed, ok := consumeBiliProtobufString(message, wireType)
			if !ok {
				return "", 0, false
			}
			name, valueBytes = value, consumed
		case 6:
			value, consumed, ok := consumeBiliProtobufFloat(message, wireType)
			if !ok {
				return "", 0, false
			}
			price, valueBytes = value, consumed
		default:
			valueBytes = protowire.ConsumeFieldValue(number, wireType, message)
			if valueBytes < 0 {
				return "", 0, false
			}
		}
		message = message[valueBytes:]
	}
	return name, price, true
}

func parseBiliGiftV2ProtobufGiftInfo(message []byte) (string, bool) {
	imgBasic := ""
	for len(message) > 0 {
		number, wireType, tagBytes := protowire.ConsumeTag(message)
		if tagBytes < 0 || !number.IsValid() {
			return "", false
		}
		message = message[tagBytes:]
		valueBytes := 0
		if number == 1 {
			value, consumed, ok := consumeBiliProtobufString(message, wireType)
			if !ok {
				return "", false
			}
			imgBasic, valueBytes = value, consumed
		} else {
			valueBytes = protowire.ConsumeFieldValue(number, wireType, message)
			if valueBytes < 0 {
				return "", false
			}
		}
		message = message[valueBytes:]
	}
	return imgBasic, true
}

func consumeBiliProtobufVarint(message []byte, wireType protowire.Type) (uint64, int, bool) {
	if wireType != protowire.VarintType {
		return 0, 0, false
	}
	value, consumed := protowire.ConsumeVarint(message)
	return value, consumed, consumed >= 0
}

func consumeBiliProtobufInt(message []byte, wireType protowire.Type) (int, int, bool) {
	value, consumed, ok := consumeBiliProtobufVarint(message, wireType)
	if !ok || value > uint64(^uint(0)>>1) {
		return 0, 0, false
	}
	return int(value), consumed, true
}

func consumeBiliProtobufFloat(message []byte, wireType protowire.Type) (float64, int, bool) {
	value, consumed, ok := consumeBiliProtobufVarint(message, wireType)
	if !ok || value > math.MaxInt64 {
		return 0, 0, false
	}
	return float64(value), consumed, true
}

func consumeBiliProtobufBytes(message []byte, wireType protowire.Type) ([]byte, int, bool) {
	if wireType != protowire.BytesType {
		return nil, 0, false
	}
	value, consumed := protowire.ConsumeBytes(message)
	return value, consumed, consumed >= 0
}

func consumeBiliProtobufString(message []byte, wireType protowire.Type) (string, int, bool) {
	value, consumed, ok := consumeBiliProtobufBytes(message, wireType)
	if !ok || !utf8.Valid(value) {
		return "", 0, false
	}
	return string(value), consumed, true
}

func formatInt(value int) string {
	return strconv.Itoa(value)
}

func formatInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}
