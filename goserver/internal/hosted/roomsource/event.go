package roomsource

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"bilibili-live-gift-panel/internal/hosted/biligateway"
)

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

// Event is an immutable-by-contract room event. Manager gives every
// subscriber a deep clone of all mutable fields.
type Event struct {
	ID       string
	RoomID   string
	Type     string
	Data     []byte
	Viewer   Viewer
	Metadata map[string]string
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
		UID      numericID       `json:"uid"`
		Uname    string          `json:"uname"`
		Username string          `json:"username"`
		Face     string          `json:"face"`
		Rnd      json.RawMessage `json:"rnd"`
		ID       json.RawMessage `json:"id"`
		OrderID  json.RawMessage `json:"order_id"`
		UserInfo struct {
			Uname string `json:"uname"`
			Face  string `json:"face"`
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
	return event
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
