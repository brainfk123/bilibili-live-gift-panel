package roomsource

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"strconv"
	"strings"

	"bilibili-live-gift-panel/internal/hosted/biligateway"
)

// Paid events are rejected above these limits before gameplay so untrusted
// room payloads cannot amplify the engine's per-occurrence loop or overflow
// aggregate price arithmetic.
const maxPaidEventCount = 1000
const maxPaidEventUnitPrice = 1_000_000_000_000
const maxPaidEventTotalPrice = maxPaidEventCount * maxPaidEventUnitPrice

// Viewer is process-local identity attached to a room event. Ephemeral is set
// on every fanout copy so downstream code cannot mistake viewer data for
// durable gameplay state.
type Viewer struct {
	UID       int64
	Uname     string
	Avatar    string
	Ephemeral bool
	Metadata  map[string]string
}

// PaidEvent is the identity-free semantic form of a supported paid room
// event. It contains no viewer fields or mutable reference values. Consumers
// must treat the pointed-to value as immutable; cloneEvent detaches it for
// every fanout copy.
type PaidEvent struct {
	GiftID           int     `json:"giftId,omitempty"`
	BlindGiftID      int     `json:"blindGiftId,omitempty"`
	Count            int     `json:"count"`
	UnitPrice        float64 `json:"unitPrice"`
	GuardLevel       int     `json:"guardLevel,omitempty"`
	HasFanMedal      bool    `json:"hasFanMedal,omitempty"`
	OccurredAtMillis int64   `json:"occurredAtMillis,omitempty"`
}

// Event is an immutable-by-contract room event. Manager gives every
// subscriber a deep clone of all mutable fields.
type Event struct {
	ID       string
	RoomID   string
	Type     string
	Data     []byte
	Viewer   Viewer
	Metadata map[string]string
	Paid     *PaidEvent
}

func eventFromGateway(roomID string, input biligateway.Event) Event {
	event := Event{RoomID: roomID, Type: input.Type, Data: append([]byte(nil), input.Data...), Viewer: Viewer{Ephemeral: true}}
	var envelope struct {
		Command string          `json:"cmd"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(input.Data, &envelope) != nil || strings.TrimSpace(envelope.Command) == "" {
		return event
	}
	event.Type = normalizeCommand(envelope.Command)
	var data struct {
		UID         numericID       `json:"uid"`
		Uname       string          `json:"uname"`
		Username    string          `json:"username"`
		Face        string          `json:"face"`
		Rnd         json.RawMessage `json:"rnd"`
		ID          json.RawMessage `json:"id"`
		OrderID     json.RawMessage `json:"order_id"`
		GiftID      numericID       `json:"giftId"`
		GiftIDAlt   numericID       `json:"gift_id"`
		BlindGiftID numericID       `json:"blind_gift_id"`
		Num         int             `json:"num"`
		Price       float64         `json:"price"`
		Timestamp   int64           `json:"timestamp"`
		StartTime   int64           `json:"start_time"`
		TS          int64           `json:"ts"`
		GuardLevel  int             `json:"guard_level"`
		MedalInfo   struct {
			MedalName string `json:"medal_name"`
		} `json:"medal_info"`
		BlindGift struct {
			GiftID numericID `json:"blind_gift_id"`
		} `json:"blind_gift"`
		UserInfo struct {
			Uname      string `json:"uname"`
			Face       string `json:"face"`
			GuardLevel int    `json:"guard_level"`
			MedalInfo  struct {
				MedalName string `json:"medal_name"`
			} `json:"medal_info"`
		} `json:"user_info"`
		SenderUinfo struct {
			UID  numericID `json:"uid"`
			Base struct {
				Name       string `json:"name"`
				Face       string `json:"face"`
				OriginInfo struct {
					Name string `json:"name"`
					Face string `json:"face"`
				} `json:"origin_info"`
			} `json:"base"`
			Guard struct {
				Level int `json:"level"`
			} `json:"guard"`
			Medal struct {
				Name string `json:"name"`
			} `json:"medal"`
		} `json:"sender_uinfo"`
	}
	if json.Unmarshal(envelope.Data, &data) != nil {
		return event
	}
	event.Viewer.UID = int64(data.UID)
	if event.Viewer.UID <= 0 {
		event.Viewer.UID = int64(data.SenderUinfo.UID)
	}
	event.Viewer.Uname = firstNonEmpty(data.SenderUinfo.Base.Name, data.SenderUinfo.Base.OriginInfo.Name, data.Uname, data.Username, data.UserInfo.Uname)
	event.Viewer.Avatar = firstNonEmpty(data.SenderUinfo.Base.Face, data.SenderUinfo.Base.OriginInfo.Face, data.Face, data.UserInfo.Face)
	event.Paid = normalizePaidEvent(event.Type, data.GiftID, data.GiftIDAlt, data.BlindGiftID, data.BlindGift.GiftID, data.Num, data.Price, data.Timestamp, data.StartTime, data.TS, data.GuardLevel, data.SenderUinfo.Guard.Level, data.UserInfo.GuardLevel, data.MedalInfo.MedalName, data.SenderUinfo.Medal.Name, data.UserInfo.MedalInfo.MedalName)
	switch event.Type {
	case "SEND_GIFT":
		if stable := stableJSONText(data.Rnd); stable != "" {
			event.ID = "send-gift:" + stable
		}
	case "GUARD_BUY":
		if stable := firstNonEmpty(stableJSONText(data.OrderID), stableJSONText(data.ID)); stable != "" {
			event.ID = "guard:" + stable
		}
	case "SUPER_CHAT_MESSAGE", "SUPER_CHAT_MESSAGE_JPN":
		if stable := stableJSONText(data.ID); stable != "" {
			event.ID = "super-chat:" + stable
		}
	}
	return event
}

func cloneEvent(event Event) Event {
	event.Data = append([]byte(nil), event.Data...)
	event.Metadata = cloneStringMap(event.Metadata)
	event.Viewer.Metadata = cloneStringMap(event.Viewer.Metadata)
	event.Viewer.Ephemeral = true
	if event.Paid != nil {
		paid := *event.Paid
		event.Paid = &paid
	}
	return event
}

func normalizePaidEvent(eventType string, giftID, giftIDAlt, blindGiftID, nestedBlindGiftID numericID, count int, unitPrice float64, timestamp, startTime, ts int64, guardLevel, senderGuardLevel, userGuardLevel int, medalName, senderMedalName, userMedalName string) *PaidEvent {
	if math.IsNaN(unitPrice) || math.IsInf(unitPrice, 0) || unitPrice < 0 {
		return nil
	}
	if guardLevel == 0 {
		guardLevel = senderGuardLevel
	}
	if guardLevel == 0 {
		guardLevel = userGuardLevel
	}
	if guardLevel < 0 || guardLevel > 3 {
		return nil
	}
	hasFanMedal := firstNonEmpty(medalName, senderMedalName, userMedalName) != ""
	if timestamp <= 0 {
		timestamp = startTime
	}
	if timestamp <= 0 {
		timestamp = ts
	}
	occurredAtMillis := secondsToMillis(timestamp)

	switch eventType {
	case "SEND_GIFT":
		if giftID == 0 {
			giftID = giftIDAlt
		}
		if blindGiftID == 0 {
			blindGiftID = nestedBlindGiftID
		}
		if giftID <= 0 || blindGiftID < 0 || int64(int(giftID)) != int64(giftID) || int64(int(blindGiftID)) != int64(blindGiftID) || !validPaidEventAmount(count, unitPrice) {
			return nil
		}
		return &PaidEvent{GiftID: int(giftID), BlindGiftID: int(blindGiftID), Count: count, UnitPrice: unitPrice, GuardLevel: guardLevel, HasFanMedal: hasFanMedal, OccurredAtMillis: occurredAtMillis}
	case "GUARD_BUY":
		if guardLevel < 1 || count < 0 {
			return nil
		}
		if count == 0 {
			count = 1
		}
		if !validPaidEventAmount(count, unitPrice) {
			return nil
		}
		return &PaidEvent{Count: count, UnitPrice: unitPrice, GuardLevel: guardLevel, HasFanMedal: hasFanMedal, OccurredAtMillis: occurredAtMillis}
	case "SUPER_CHAT_MESSAGE", "SUPER_CHAT_MESSAGE_JPN":
		if unitPrice <= 0 || unitPrice > maxPaidEventUnitPrice/1000 {
			return nil
		}
		normalizedPrice := unitPrice * 1000
		if !validPaidEventAmount(1, normalizedPrice) {
			return nil
		}
		return &PaidEvent{Count: 1, UnitPrice: normalizedPrice, GuardLevel: guardLevel, HasFanMedal: hasFanMedal, OccurredAtMillis: occurredAtMillis}
	default:
		return nil
	}
}

func validPaidEventAmount(count int, unitPrice float64) bool {
	if count <= 0 || count > maxPaidEventCount || unitPrice < 0 || unitPrice > maxPaidEventUnitPrice || math.IsNaN(unitPrice) || math.IsInf(unitPrice, 0) {
		return false
	}
	total := float64(count) * unitPrice
	return !math.IsNaN(total) && !math.IsInf(total, 0) && total <= maxPaidEventTotalPrice
}

func secondsToMillis(seconds int64) int64 {
	if seconds <= 0 || seconds > int64(^uint64(0)>>1)/1000 {
		return 0
	}
	return seconds * 1000
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

type numericID int64

func (value *numericID) UnmarshalJSON(input []byte) error {
	text := strings.Trim(strings.TrimSpace(string(input)), `"`)
	if text == "" || text == "null" {
		*value = 0
		return nil
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return err
	}
	*value = numericID(parsed)
	return nil
}

func normalizeCommand(command string) string {
	command = strings.TrimSpace(command)
	if separator := strings.IndexByte(command, ':'); separator >= 0 {
		command = command[:separator]
	}
	return command
}

func stableJSONText(input json.RawMessage) string {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return ""
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ""
	}
	switch scalar := value.(type) {
	case string:
		return boundedStableText(scalar)
	case json.Number:
		return boundedStableText(scalar.String())
	default:
		return ""
	}
}

func boundedStableText(input string) string {
	input = strings.TrimSpace(input)
	if input == "" || input == "null" || len(input) > 512 {
		return ""
	}
	return input
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
