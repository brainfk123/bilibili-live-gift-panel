package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
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
	sessionProvider   func(context.Context) (biliSession, bool)
	dial              biliDial
	heartbeatInterval time.Duration
	readTimeout       time.Duration
	diagnostics       *diagnosticLogger
	diagnosticMu      sync.Mutex
	diagnosticNow     func() time.Time
	ignoredAggregates map[string]biliDiagnosticAggregate
	decodedAggregates map[uint16]biliDiagnosticAggregate
}

type biliDiagnosticAggregate struct {
	count      int
	lastLogged time.Time
}

type biliSocket interface {
	ReadMessage() (int, []byte, error)
	WriteMessage(int, []byte) error
	SetReadDeadline(time.Time) error
	Close() error
}

type biliDial func(context.Context, string, http.Header) (biliSocket, error)

const (
	defaultBiliHeartbeatInterval = 30 * time.Second
	defaultBiliReadTimeout       = 45 * time.Second
	ignoredDiagnosticInterval    = time.Minute
)

var errBiliPacketBounds = errors.New("bilibili packet bounds")

func defaultBiliDial(ctx context.Context, url string, headers http.Header) (biliSocket, error) {
	connection, _, err := websocket.DefaultDialer.DialContext(ctx, url, headers)
	if err != nil {
		return nil, err
	}
	return connection, nil
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
	info, err := getRoomInfoWithSessionContext(ctx, roomID, session)
	if err != nil {
		return newConnectionFailure("source", err)
	}
	session = sessionForRoomInfo(info, session)
	catalogByID := map[int]roomGiftInfo{}
	effectsByID := map[int]giftEffectResource{}
	if resources, catalogErr := fetchCurrentRoomGiftResourcesContext(ctx, roomID, session); catalogErr == nil {
		for _, gift := range resources.Gifts {
			catalogByID[gift.ID] = gift
		}
		effectsByID = resources.EffectsByID
		if callbacks.onGiftCatalog != nil {
			callbacks.onGiftCatalog(resources.Gifts)
		}
	} else if callbacks.onGiftCatalogError != nil {
		callbacks.onGiftCatalogError(catalogErr)
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
	dial := source.dial
	if dial == nil {
		dial = defaultBiliDial
	}
	connection, err := dial(ctx, url, headers)
	if err != nil {
		return newConnectionFailure("dial", err)
	}
	defer connection.Close()
	return source.runSocket(ctx, connection, info, session, catalogByID, effectsByID, callbacks)
}

func (source *bilibiliGiftSource) runSocket(ctx context.Context, connection biliSocket, info roomInfo, session biliSession, catalogByID map[int]roomGiftInfo, effectsByID map[int]giftEffectResource, callbacks runtimeCallbacks) error {
	heartbeatInterval := source.heartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = defaultBiliHeartbeatInterval
	}
	readTimeout := source.readTimeout
	if readTimeout <= 0 {
		readTimeout = defaultBiliReadTimeout
	}

	auth := buildBiliAuthPayload(info, session)
	var writeMu sync.Mutex
	write := func(payload []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return connection.WriteMessage(websocket.BinaryMessage, payload)
	}
	if err := write(encodeBiliPacket(biliOpAuth, auth)); err != nil {
		return newConnectionFailure("write", err)
	}
	if err := connection.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		return newConnectionFailure("deadline", err)
	}

	done := make(chan struct{})
	defer close(done)
	heartbeatFailure := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = connection.Close()
				return
			case <-done:
				return
			case <-ticker.C:
				if err := write(encodeBiliPacket(biliOpHeartbeat, nil)); err != nil {
					select {
					case heartbeatFailure <- err:
					default:
					}
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
			select {
			case heartbeatErr := <-heartbeatFailure:
				return newConnectionFailure("heartbeat", heartbeatErr)
			default:
				return newConnectionFailure("read", err)
			}
		}
		packets, decodeErr := decodeBiliPacketsDetailed(payload)
		if decodeErr != nil {
			source.recordDiagnostic("bili_parse_failed", "reason", biliPacketDecodeReason(decodeErr))
		} else if len(packets) > 0 {
			source.recordDecodedPackets(packets)
		}
		if len(packets) == 0 {
			continue
		}
		if err := connection.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			return newConnectionFailure("deadline", err)
		}
		if callbacks.onFrame != nil {
			callbacks.onFrame()
		}
		for _, packet := range packets {
			if packet.operation == biliOpAuthReply {
				var reply struct {
					Code int `json:"code"`
				}
				if err := json.Unmarshal(packet.body, &reply); err != nil || reply.Code != 0 {
					return newConnectionFailure("auth", fmt.Errorf("弹幕服务器认证失败"))
				}
				if callbacks.onState != nil {
					callbacks.onState("connected")
				}
				continue
			}
			if packet.operation != biliOpMessage {
				continue
			}
			bodies := [][]byte{packet.body}
			if packet.protocolVersion == 2 {
				inflated, err := inflateBiliPacket(packet.body)
				if err != nil {
					source.recordDiagnostic("bili_parse_failed", "reason", "decompression_failure")
					continue
				}
				bodies = bodies[:0]
				nestedPackets, nestedErr := decodeBiliPacketsDetailed(inflated)
				if nestedErr != nil {
					source.recordDiagnostic("bili_parse_failed", "reason", biliPacketDecodeReason(nestedErr))
				}
				for _, nested := range nestedPackets {
					bodies = append(bodies, nested.body)
				}
			}
			for _, body := range bodies {
				gifts, reason, giftOK, parseDiagnostic := parseBiliGiftEventsWithDiagnostic(body)
				if giftOK {
					if callbacks.onGift != nil {
						for _, gift := range gifts {
							callbacks.onGift(enrichGiftAnimationFromRoomCatalog(gift, catalogByID))
						}
					}
					continue
				}
				if paidEvent, ok := parseBiliPaidEvent(body); ok {
					if callbacks.onGift != nil {
						callbacks.onGift(enrichGiftAnimationFromEffectCatalog(paidEvent, effectsByID))
					}
					continue
				}
				if reason == "ignored_command" {
					source.recordIgnoredMessage(ignoredBiliCommandCategory(body))
				} else {
					fields := []any{"reason", reason}
					fields = append(fields, parseDiagnostic.fields()...)
					source.recordDiagnostic("bili_parse_failed", fields...)
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
	packets, _ := decodeBiliPacketsDetailed(payload)
	return packets
}

func decodeBiliPacketsDetailed(payload []byte) ([]biliPacket, error) {
	packets := []biliPacket{}
	for offset := 0; offset < len(payload); {
		if offset+biliHeaderLength > len(payload) {
			return packets, errBiliPacketBounds
		}
		totalLength := int(binary.BigEndian.Uint32(payload[offset : offset+4]))
		headerLength := int(binary.BigEndian.Uint16(payload[offset+4 : offset+6]))
		if totalLength < headerLength || headerLength < biliHeaderLength || offset+totalLength > len(payload) {
			return packets, errBiliPacketBounds
		}
		packets = append(packets, biliPacket{
			protocolVersion: binary.BigEndian.Uint16(payload[offset+6 : offset+8]),
			operation:       binary.BigEndian.Uint32(payload[offset+8 : offset+12]),
			body:            append([]byte(nil), payload[offset+headerLength:offset+totalLength]...),
		})
		offset += totalLength
	}
	return packets, nil
}

func biliPacketDecodeReason(err error) string {
	if errors.Is(err, errBiliPacketBounds) {
		return "packet_bounds"
	}
	return "packet_bounds"
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
	gift, _, ok := parseBiliGiftDetailed(body)
	return gift, ok
}

func parseBiliGiftDetailed(body []byte) (giftEvent, string, bool) {
	gifts, reason, ok := parseBiliGiftEventsDetailed(body)
	if !ok || len(gifts) == 0 {
		return giftEvent{}, reason, false
	}
	return gifts[0], reason, true
}

func parseBiliGiftEventsDetailed(body []byte) ([]giftEvent, string, bool) {
	var envelope struct {
		Command string          `json:"cmd"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return nil, "malformed_envelope", false
	}
	switch envelope.Command {
	case "SEND_GIFT":
		gift, reason, ok := parseLegacyBiliGiftData(envelope.Data)
		if !ok {
			return nil, reason, false
		}
		return []giftEvent{gift}, reason, true
	case "SEND_GIFT_V2":
		return parseBiliGiftV2Data(envelope.Data)
	default:
		return nil, "ignored_command", false
	}
}

type biliGiftParseDiagnostic struct {
	GiftCommand            string
	ParseStage             string
	PayloadBytes           int
	DataKind               string
	DataFieldCount         int
	GiftListKind           string
	GiftListCount          int
	GiftIDKind             string
	GiftNameKind           string
	MissingGiftIDCount     int
	MissingGiftNameCount   int
	InspectedGiftItemCount int
	StructureTruncated     bool
	SchemaHash             string
}

func (diagnostic biliGiftParseDiagnostic) fields() []any {
	if diagnostic.ParseStage == "" {
		return nil
	}
	return []any{
		"gift_command", diagnostic.GiftCommand,
		"parse_stage", diagnostic.ParseStage,
		"payload_bytes", diagnostic.PayloadBytes,
		"data_kind", diagnostic.DataKind,
		"data_field_count", diagnostic.DataFieldCount,
		"gift_list_kind", diagnostic.GiftListKind,
		"gift_list_count", diagnostic.GiftListCount,
		"gift_id_kind", diagnostic.GiftIDKind,
		"gift_name_kind", diagnostic.GiftNameKind,
		"missing_gift_id_count", diagnostic.MissingGiftIDCount,
		"missing_gift_name_count", diagnostic.MissingGiftNameCount,
		"inspected_gift_item_count", diagnostic.InspectedGiftItemCount,
		"structure_truncated", diagnostic.StructureTruncated,
		"schema_hash", diagnostic.SchemaHash,
	}
}

func parseBiliGiftEventsWithDiagnostic(body []byte) ([]giftEvent, string, bool, biliGiftParseDiagnostic) {
	gifts, reason, ok := parseBiliGiftEventsDetailed(body)
	if ok || reason != "malformed_gift_data" {
		return gifts, reason, ok, biliGiftParseDiagnostic{}
	}
	if len(body) > biliDiagnosticMaxPayloadBytes {
		return gifts, reason, ok, biliGiftParseDiagnostic{
			ParseStage: "payload_too_large", PayloadBytes: len(body), DataKind: "invalid",
			GiftListKind: "missing", GiftIDKind: "missing", GiftNameKind: "missing",
			StructureTruncated: true, SchemaHash: diagnosticHash("payload_too_large"),
		}
	}
	var envelope struct {
		Command string          `json:"cmd"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return gifts, reason, ok, biliGiftParseDiagnostic{}
	}
	giftCommand := ""
	switch envelope.Command {
	case "SEND_GIFT":
		giftCommand = "send_gift"
	case "SEND_GIFT_V2":
		giftCommand = "send_gift_v2"
	default:
		return gifts, reason, ok, biliGiftParseDiagnostic{}
	}
	diagnostic := biliGiftParseDiagnostic{
		GiftCommand: giftCommand, PayloadBytes: len(body), GiftListKind: "missing",
		GiftIDKind: "missing", GiftNameKind: "missing",
	}
	var data any
	if json.Unmarshal(envelope.Data, &data) != nil {
		diagnostic.ParseStage = "data_decode"
		diagnostic.DataKind = "invalid"
		diagnostic.SchemaHash = diagnosticHash("invalid")
		return gifts, reason, ok, diagnostic
	}
	diagnostic.DataKind = biliJSONKind(data)
	shape, shapeTruncated := biliJSONStructuralShapeDetails(data)
	diagnostic.SchemaHash = diagnosticHash(shape)
	diagnostic.StructureTruncated = shapeTruncated
	dataObject, isObject := data.(map[string]any)
	if !isObject {
		diagnostic.ParseStage = "data_type"
		return gifts, reason, ok, diagnostic
	}
	diagnostic.DataFieldCount = len(dataObject)
	if envelope.Command == "SEND_GIFT" {
		diagnostic.ParseStage = "data_decode"
		diagnostic.GiftIDKind = biliJSONObjectFieldKind(dataObject, "giftId")
		diagnostic.GiftNameKind = biliJSONObjectFieldKind(dataObject, "giftName")
		return gifts, reason, ok, diagnostic
	}
	giftList, exists := dataObject["gift_list"]
	if !exists {
		diagnostic.ParseStage = "gift_list_missing"
		return gifts, reason, ok, diagnostic
	}
	diagnostic.GiftListKind = biliJSONKind(giftList)
	items, isArray := giftList.([]any)
	if !isArray {
		diagnostic.ParseStage = "gift_list_type"
		return gifts, reason, ok, diagnostic
	}
	diagnostic.GiftListCount = len(items)
	if len(items) == 0 {
		diagnostic.ParseStage = "gift_list_empty"
		return gifts, reason, ok, diagnostic
	}
	idKinds := make(map[string]bool)
	nameKinds := make(map[string]bool)
	itemTypeInvalid := false
	idTypeInvalid := false
	nameTypeInvalid := false
	inspectionLimit := minInt(len(items), biliDiagnosticMaxGiftItems)
	diagnostic.InspectedGiftItemCount = inspectionLimit
	if inspectionLimit < len(items) {
		diagnostic.StructureTruncated = true
	}
	for _, item := range items[:inspectionLimit] {
		object, itemIsObject := item.(map[string]any)
		if !itemIsObject {
			itemTypeInvalid = true
			continue
		}
		giftID, hasGiftID := object["gift_id"]
		if !hasGiftID {
			diagnostic.MissingGiftIDCount++
			idKinds["missing"] = true
		} else {
			kind := biliJSONKind(giftID)
			idKinds[kind] = true
			if kind != "number" {
				idTypeInvalid = true
			}
		}
		giftName, hasGiftName := object["gift_name"]
		if !hasGiftName {
			diagnostic.MissingGiftNameCount++
			nameKinds["missing"] = true
		} else {
			kind := biliJSONKind(giftName)
			nameKinds[kind] = true
			if kind != "string" {
				nameTypeInvalid = true
			} else if strings.TrimSpace(giftName.(string)) == "" {
				diagnostic.MissingGiftNameCount++
			}
		}
	}
	diagnostic.GiftIDKind = biliJSONKindSummary(idKinds)
	diagnostic.GiftNameKind = biliJSONKindSummary(nameKinds)
	switch {
	case itemTypeInvalid:
		diagnostic.ParseStage = "gift_item_type"
	case idTypeInvalid:
		diagnostic.ParseStage = "gift_item_id_type"
	case diagnostic.MissingGiftIDCount > 0:
		diagnostic.ParseStage = "gift_item_missing_id"
	case nameTypeInvalid:
		diagnostic.ParseStage = "gift_item_name_type"
	case diagnostic.MissingGiftNameCount > 0:
		diagnostic.ParseStage = "gift_item_missing_name"
	default:
		diagnostic.ParseStage = "data_decode"
	}
	return gifts, reason, ok, diagnostic
}

func biliJSONKind(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case float64, json.Number:
		return "number"
	case bool:
		return "boolean"
	default:
		return "invalid"
	}
}

func biliJSONKindSummary(kinds map[string]bool) string {
	if len(kinds) == 0 {
		return "missing"
	}
	if len(kinds) > 1 {
		return "mixed"
	}
	for kind := range kinds {
		return kind
	}
	return "missing"
}

func biliJSONObjectFieldKind(object map[string]any, key string) string {
	value, exists := object[key]
	if !exists {
		return "missing"
	}
	return biliJSONKind(value)
}

var biliDiagnosticShapeKeys = []string{
	"blind_gift", "blind_gift_id", "blind_gift_name", "blind_gift_price", "coin_type", "face",
	"giftId", "giftName", "gift_id", "gift_info", "gift_list", "gift_name", "gift_num", "gift_price",
	"guard_level", "medal_info", "num", "price", "sender_uinfo", "tid", "timestamp", "total_coin", "uid", "uname",
}

const (
	biliDiagnosticMaxPayloadBytes = 256 * 1024
	biliDiagnosticMaxGiftItems    = 32
	biliDiagnosticShapeMaxDepth   = 4
	biliDiagnosticShapeMaxItems   = 8
	biliDiagnosticShapeMaxNodes   = 128
)

func biliJSONStructuralShape(value any) string {
	shape, _ := biliJSONStructuralShapeDetails(value)
	return shape
}

type biliJSONShapeBudget struct {
	remaining int
	truncated bool
}

func biliJSONStructuralShapeDetails(value any) (string, bool) {
	budget := biliJSONShapeBudget{remaining: biliDiagnosticShapeMaxNodes}
	shape := biliJSONStructuralShapeAt(value, 0, &budget)
	return shape, budget.truncated
}

func biliJSONStructuralShapeAt(value any, depth int, budget *biliJSONShapeBudget) string {
	if budget.remaining <= 0 {
		budget.truncated = true
		return "limit"
	}
	budget.remaining--
	if depth >= biliDiagnosticShapeMaxDepth {
		if object, ok := value.(map[string]any); ok && len(object) > 0 {
			budget.truncated = true
		}
		if items, ok := value.([]any); ok && len(items) > 0 {
			budget.truncated = true
		}
		return biliJSONKind(value)
	}
	switch typed := value.(type) {
	case map[string]any:
		entries := make([]string, 0, len(biliDiagnosticShapeKeys)+1)
		knownCount := 0
		for _, key := range biliDiagnosticShapeKeys {
			child, exists := typed[key]
			if !exists {
				continue
			}
			knownCount++
			entries = append(entries, key+":"+biliJSONStructuralShapeAt(child, depth+1, budget))
		}
		if unknownCount := len(typed) - knownCount; unknownCount > 0 {
			entries = append(entries, fmt.Sprintf("?:*%d", unknownCount))
		}
		sort.Strings(entries)
		return "{" + strings.Join(entries, ",") + "}"
	case []any:
		limit := minInt(len(typed), biliDiagnosticShapeMaxItems)
		shapes := make([]string, 0, limit+1)
		for _, child := range typed[:limit] {
			shapes = append(shapes, biliJSONStructuralShapeAt(child, depth+1, budget))
		}
		if omitted := len(typed) - limit; omitted > 0 {
			budget.truncated = true
			shapes = append(shapes, fmt.Sprintf("+%d", omitted))
		}
		sort.Strings(shapes)
		return "[" + strconv.Itoa(len(typed)) + ":" + strings.Join(shapes, ",") + "]"
	default:
		return biliJSONKind(value)
	}
}

func parseLegacyBiliGiftData(raw json.RawMessage) (giftEvent, string, bool) {
	var data struct {
		GiftID            int     `json:"giftId"`
		BlindGiftID       biliUID `json:"blind_gift_id"`
		BlindGiftName     string  `json:"blind_gift_name"`
		BlindGiftPrice    float64 `json:"blind_gift_price"`
		OriginalGiftPrice float64 `json:"original_gift_price"`
		GiftName          string  `json:"giftName"`
		Num               int     `json:"num"`
		Price             float64 `json:"price"`
		CoinType          string  `json:"coin_type"`
		TotalCoin         float64 `json:"total_coin"`
		GuardLevel        int     `json:"guard_level"`
		MedalInfo         struct {
			MedalName  string `json:"medal_name"`
			MedalLevel int    `json:"medal_level"`
		} `json:"medal_info"`
		Uname     string          `json:"uname"`
		Face      string          `json:"face"`
		UID       biliUID         `json:"uid"`
		Timestamp int64           `json:"timestamp"`
		Rnd       json.RawMessage `json:"rnd"`
		BlindGift struct {
			GiftID            biliUID `json:"blind_gift_id"`
			GiftName          string  `json:"gift_name"`
			GiftPrice         float64 `json:"gift_price"`
			OriginalGiftPrice float64 `json:"original_gift_price"`
		} `json:"blind_gift"`
		GiftInfo struct {
			ImgBasic string `json:"img_basic"`
			GIF      string `json:"gif"`
			WebP     string `json:"webp"`
			EffectID int    `json:"effect_id"`
		} `json:"gift_info"`
		SenderUinfo biliSenderUinfo `json:"sender_uinfo"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return giftEvent{}, "malformed_gift_data", false
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
		Membership:   biliMembership(firstPositiveInt(data.GuardLevel, data.SenderUinfo.Guard.Level), firstNonEmptyEventField(data.MedalInfo.MedalName, data.SenderUinfo.Medal.Name)),
		AnimationGIF: strings.TrimSpace(data.GiftInfo.GIF), AnimationWebP: strings.TrimSpace(data.GiftInfo.WebP),
		EffectID: data.GiftInfo.EffectID,
		Rnd:      rnd,
	}, "SEND_GIFT", true
}

func parseBiliGiftV2Data(raw json.RawMessage) ([]giftEvent, string, bool) {
	var data struct {
		UID        biliUID `json:"uid"`
		Uname      string  `json:"uname"`
		Face       string  `json:"face"`
		Timestamp  int64   `json:"timestamp"`
		CoinType   string  `json:"coin_type"`
		GuardLevel int     `json:"guard_level"`
		MedalInfo  struct {
			MedalName  string `json:"medal_name"`
			MedalLevel int    `json:"medal_level"`
		} `json:"medal_info"`
		SenderUinfo biliSenderUinfo `json:"sender_uinfo"`
		BlindGift   struct {
			OriginalGiftID    biliUID `json:"original_gift_id"`
			OriginalGiftName  string  `json:"original_gift_name"`
			OriginalGiftPrice float64 `json:"original_gift_price"`
			GiftTipPrice      float64 `json:"gift_tip_price"`
		} `json:"blind_gift"`
		GiftList []struct {
			TID       json.RawMessage `json:"tid"`
			GiftID    int             `json:"gift_id"`
			GiftName  string          `json:"gift_name"`
			GiftPrice float64         `json:"gift_price"`
			GiftNum   int             `json:"gift_num"`
			GiftInfo  struct {
				ImgBasic string `json:"img_basic"`
				GIF      string `json:"gif"`
				WebP     string `json:"webp"`
				EffectID int    `json:"effect_id"`
			} `json:"gift_info"`
		} `json:"gift_list"`
	}
	if json.Unmarshal(raw, &data) != nil || len(data.GiftList) == 0 {
		return nil, "malformed_gift_data", false
	}
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
	parentPrice := firstPositiveFloat(data.BlindGift.OriginalGiftPrice, data.BlindGift.GiftTipPrice)
	membership := biliMembership(firstPositiveInt(data.GuardLevel, data.SenderUinfo.Guard.Level), firstNonEmptyEventField(data.MedalInfo.MedalName, data.SenderUinfo.Medal.Name))
	gifts := make([]giftEvent, 0, len(data.GiftList))
	for index, item := range data.GiftList {
		if item.GiftID <= 0 || strings.TrimSpace(item.GiftName) == "" {
			return nil, "malformed_gift_data", false
		}
		if item.GiftNum < 1 {
			item.GiftNum = 1
		}
		rnd := strings.Trim(strings.TrimSpace(string(item.TID)), `"`)
		if rnd == "" || rnd == "null" {
			rnd = fmt.Sprintf("%d-%d-%d-%d", data.Timestamp, uid, item.GiftID, index)
		}
		gifts = append(gifts, giftEvent{
			GiftID: item.GiftID, GiftName: item.GiftName, Num: item.GiftNum, Price: item.GiftPrice,
			BlindGiftID: int(data.BlindGift.OriginalGiftID), BlindGiftName: strings.TrimSpace(data.BlindGift.OriginalGiftName), BlindGiftPrice: parentPrice,
			CoinType: data.CoinType, TotalCoin: item.GiftPrice * float64(item.GiftNum), Uname: uname, Avatar: avatar, UID: uid,
			Timestamp: data.Timestamp, ImgBasic: item.GiftInfo.ImgBasic, Membership: membership,
			AnimationGIF: strings.TrimSpace(item.GiftInfo.GIF), AnimationWebP: strings.TrimSpace(item.GiftInfo.WebP), EffectID: item.GiftInfo.EffectID,
			Rnd: rnd,
		})
	}
	return gifts, "SEND_GIFT_V2", true
}

func (source *bilibiliGiftSource) recordDiagnostic(event string, keyValues ...any) {
	if source == nil || source.diagnostics == nil {
		return
	}
	source.diagnostics.Info(event, keyValues...)
}

func (source *bilibiliGiftSource) diagnosticTime() time.Time {
	if source != nil && source.diagnosticNow != nil {
		return source.diagnosticNow()
	}
	return time.Now()
}

func ignoredBiliCommandCategory(body []byte) string {
	var envelope struct {
		Command string `json:"cmd"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return "other"
	}
	switch envelope.Command {
	case "COMBO_SEND":
		return "combo_send"
	case "BATCH_COMBO_SEND":
		return "batch_combo_send"
	default:
		return "other"
	}
}

func normalizeIgnoredBiliCommandCategory(category string) string {
	switch category {
	case "combo_send", "batch_combo_send":
		return category
	default:
		return "other"
	}
}

func (source *bilibiliGiftSource) recordIgnoredMessage(category string) {
	if source == nil || source.diagnostics == nil {
		return
	}
	category = normalizeIgnoredBiliCommandCategory(category)
	now := source.diagnosticTime()
	source.diagnosticMu.Lock()
	if source.ignoredAggregates == nil {
		source.ignoredAggregates = make(map[string]biliDiagnosticAggregate)
	}
	aggregate := source.ignoredAggregates[category]
	aggregate.count++
	if !aggregate.lastLogged.IsZero() && now.Sub(aggregate.lastLogged) < ignoredDiagnosticInterval {
		source.ignoredAggregates[category] = aggregate
		source.diagnosticMu.Unlock()
		return
	}
	count := aggregate.count
	aggregate.count = 0
	aggregate.lastLogged = now
	source.ignoredAggregates[category] = aggregate
	source.diagnosticMu.Unlock()
	source.diagnostics.Info("bili_message_ignored", "reason", "ignored_command", "ignored_command_category", category, "count", count)
}

func (source *bilibiliGiftSource) recordDecodedPackets(packets []biliPacket) {
	if source == nil || source.diagnostics == nil || len(packets) == 0 {
		return
	}
	counts := make(map[uint16]int)
	for _, packet := range packets {
		counts[packet.protocolVersion]++
	}
	now := source.diagnosticTime()
	for protocolVersion, packetCount := range counts {
		source.diagnosticMu.Lock()
		if source.decodedAggregates == nil {
			source.decodedAggregates = make(map[uint16]biliDiagnosticAggregate)
		}
		aggregate := source.decodedAggregates[protocolVersion]
		aggregate.count += packetCount
		if !aggregate.lastLogged.IsZero() && now.Sub(aggregate.lastLogged) < ignoredDiagnosticInterval {
			source.decodedAggregates[protocolVersion] = aggregate
			source.diagnosticMu.Unlock()
			continue
		}
		decodedPacketCount := aggregate.count
		aggregate.count = 0
		aggregate.lastLogged = now
		source.decodedAggregates[protocolVersion] = aggregate
		source.diagnosticMu.Unlock()
		source.diagnostics.Info("bili_frame_decoded", "protocol_version", protocolVersion, "decoded_packet_count", decodedPacketCount)
	}
}

func enrichGiftAnimationFromEffectCatalog(gift giftEvent, effects map[int]giftEffectResource) giftEvent {
	if gift.EffectID <= 0 {
		return gift
	}
	effect, exists := effects[gift.EffectID]
	if !exists {
		return gift
	}
	if strings.TrimSpace(gift.EffectMP4) == "" {
		gift.EffectMP4 = effect.MP4
	}
	if strings.TrimSpace(gift.EffectMP4JSON) == "" {
		gift.EffectMP4JSON = effect.MP4JSON
	}
	return gift
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
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
	if gift.EffectID == 0 {
		gift.EffectID = metadata.EffectID
	}
	if metadata.EffectID == 0 || gift.EffectID == metadata.EffectID {
		gift.EffectMP4 = metadata.EffectMP4
		gift.EffectMP4JSON = metadata.EffectMP4JSON
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
