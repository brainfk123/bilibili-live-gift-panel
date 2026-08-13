package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxDiagnosticLogBytes int64 = 2 << 20

const diagnosticHashLength = 12

type diagnosticLogger struct {
	path string
	mu   sync.Mutex
	now  func() time.Time
}

func newDiagnosticLogger(path string) (*diagnosticLogger, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("运行日志路径不能为空")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("创建运行日志目录失败：%w", err)
	}
	return &diagnosticLogger{path: path, now: time.Now}, nil
}

func (logger *diagnosticLogger) Info(event string, keyValues ...any) {
	logger.write("INFO", event, keyValues...)
}

func (logger *diagnosticLogger) Error(event string, keyValues ...any) {
	logger.write("ERROR", event, keyValues...)
}

func (logger *diagnosticLogger) write(level, event string, keyValues ...any) {
	if logger == nil {
		return
	}
	line := strings.Builder{}
	line.WriteString(logger.now().Format(time.RFC3339Nano))
	line.WriteByte(' ')
	line.WriteString(sanitizeDiagnosticLevel(level))
	line.WriteByte(' ')
	line.WriteString(sanitizeDiagnosticEvent(event))
	for _, field := range validatedDiagnosticFields(keyValues) {
		line.WriteByte(' ')
		line.WriteString(field.key)
		line.WriteByte('=')
		line.WriteString(formatDiagnosticFieldValue(field.key, field.value))
	}
	line.WriteByte('\n')

	logger.mu.Lock()
	defer logger.mu.Unlock()
	_ = logger.rotateIfNeeded(int64(line.Len()))
	file, err := os.OpenFile(logger.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = file.WriteString(line.String())
	_ = file.Close()
}

// diagnosticHash creates a stable local correlation token without retaining
// the source value in diagnostics. It is deliberately short because it is for
// one export, not a security boundary.
func diagnosticHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:diagnosticHashLength]
}

func (logger *diagnosticLogger) rotateIfNeeded(incomingBytes int64) error {
	info, err := os.Stat(logger.path)
	if os.IsNotExist(err) || (err == nil && info.Size()+incomingBytes <= maxDiagnosticLogBytes) {
		return nil
	}
	if err != nil {
		return err
	}
	backup := logger.path + ".1"
	_ = os.Remove(backup)
	return os.Rename(logger.path, backup)
}

func (logger *diagnosticLogger) exportBytes() []byte {
	if logger == nil {
		return []byte("运行日志不可用。\n")
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()

	var output bytes.Buffer
	output.WriteString("# 哔哩哔哩直播礼物面板运行日志\n")
	output.WriteString("# 导出时间：")
	output.WriteString(logger.now().Format(time.RFC3339))
	output.WriteString("\n# 日志不包含 Cookie、访问令牌或登录凭据。\n")
	hadEntries := false
	for _, candidate := range []string{logger.path + ".1", logger.path} {
		data, err := os.ReadFile(candidate)
		if err != nil || len(data) == 0 {
			continue
		}
		output.WriteString("\n--- ")
		output.WriteString(filepath.Base(candidate))
		output.WriteString(" ---\n")
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			output.WriteString(sanitizeLegacyDiagnosticLine(line))
			output.WriteByte('\n')
			hadEntries = true
		}
	}
	if !hadEntries {
		output.WriteString("暂无运行日志。\n")
	}
	return output.Bytes()
}

func (logger *diagnosticLogger) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
		return
	}
	filename := "gift-panel-runtime-" + logger.now().Format("20060102-150405") + ".log"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(logger.exportBytes())
}

func sanitizeDiagnosticToken(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(character rune) rune {
		if character == '_' || character == '-' || character == '.' || character >= '0' && character <= '9' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' {
			return character
		}
		return '_'
	}, value)
	return strings.Trim(value, "_")
}

type diagnosticField struct {
	key   string
	value any
}

type diagnosticFieldSpec func(any) (any, bool)

var diagnosticFieldOrder = []string{
	"gift_id", "blind_parent_id", "count", "timestamp", "rnd_hash", "reason", "state", "room_id", "error_kind", "source_duplicate", "task_id", "phase", "exit_class", "mode",
	"accept_write_ms", "inbox_depth", "oldest_pending_age_ms", "attempts", "duration_ms", "blind_source", "blind_cost", "blind_value", "blind_priced",
	"mapped_children", "port", "version",
	"protocol_version", "decoded_packet_count", "ignored_command_category",
}

var diagnosticFieldSpecs = map[string]diagnosticFieldSpec{
	"gift_id":                  validateDiagnosticNonNegativeInteger,
	"blind_parent_id":          validateDiagnosticNonNegativeInteger,
	"count":                    validateDiagnosticNonNegativeInteger,
	"timestamp":                validateDiagnosticNonNegativeInteger,
	"rnd_hash":                 validateDiagnosticHash,
	"reason":                   validateDiagnosticReason,
	"state":                    validateDiagnosticState,
	"room_id":                  validateDiagnosticRoomID,
	"error_kind":               validateDiagnosticErrorKind,
	"source_duplicate":         validateDiagnosticBoolean,
	"accept_write_ms":          validateDiagnosticNonNegativeInteger,
	"inbox_depth":              validateDiagnosticNonNegativeInteger,
	"oldest_pending_age_ms":    validateDiagnosticNonNegativeInteger,
	"attempts":                 validateDiagnosticNonNegativeInteger,
	"duration_ms":              validateDiagnosticNonNegativeInteger,
	"blind_source":             validateDiagnosticBlindSource,
	"blind_cost":               validateDiagnosticAmount,
	"blind_value":              validateDiagnosticAmount,
	"blind_priced":             validateDiagnosticBoolean,
	"mapped_children":          validateDiagnosticNonNegativeInteger,
	"port":                     validateDiagnosticPort,
	"version":                  validateDiagnosticVersion,
	"task_id":                  validateDiagnosticTaskID,
	"phase":                    validateDiagnosticPhase,
	"exit_class":               validateDiagnosticExitClass,
	"mode":                     validateDiagnosticMode,
	"protocol_version":         validateDiagnosticProtocolVersion,
	"decoded_packet_count":     validateDiagnosticNonNegativeInteger,
	"ignored_command_category": validateDiagnosticIgnoredCommandCategory,
}

var diagnosticVersionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?(?:\+[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)

func validatedDiagnosticFields(keyValues []any) []diagnosticField {
	fields := make([]diagnosticField, 0, len(keyValues)/2)
	for index := 0; index+1 < len(keyValues); index += 2 {
		key, ok := keyValues[index].(string)
		if !ok {
			continue
		}
		value, ok := validateDiagnosticField(key, keyValues[index+1])
		if !ok {
			continue
		}
		fields = append(fields, diagnosticField{key: key, value: value})
	}
	return fields
}

func validateDiagnosticField(key string, value any) (any, bool) {
	spec, exists := diagnosticFieldSpecs[key]
	if !exists {
		return nil, false
	}
	return spec(value)
}

func validateDiagnosticNonNegativeInteger(value any) (any, bool) {
	integer, ok := diagnosticInteger(value)
	if !ok || integer < 0 || integer > 1_000_000_000_000_000 {
		return nil, false
	}
	return integer, true
}

func validateDiagnosticPort(value any) (any, bool) {
	integer, ok := diagnosticInteger(value)
	if !ok || integer < 1 || integer > 65535 {
		return nil, false
	}
	return integer, true
}

func validateDiagnosticProtocolVersion(value any) (any, bool) {
	integer, ok := diagnosticInteger(value)
	if !ok || integer < 0 || integer > 65535 {
		return nil, false
	}
	return integer, true
}

func validateDiagnosticIgnoredCommandCategory(value any) (any, bool) {
	text, ok := value.(string)
	if !ok {
		return nil, false
	}
	switch text {
	case "combo_send", "batch_combo_send", "other":
		return text, true
	default:
		return nil, false
	}
}

func diagnosticInteger(value any) (int64, bool) {
	switch value := value.(type) {
	case int:
		return int64(value), true
	case int8:
		return int64(value), true
	case int16:
		return int64(value), true
	case int32:
		return int64(value), true
	case int64:
		return value, true
	case uint:
		if uint64(value) <= math.MaxInt64 {
			return int64(value), true
		}
	case uint8:
		return int64(value), true
	case uint16:
		return int64(value), true
	case uint32:
		return int64(value), true
	case uint64:
		if value <= math.MaxInt64 {
			return int64(value), true
		}
	case json.Number:
		integer, err := value.Int64()
		if err == nil {
			return integer, true
		}
	}
	return 0, false
}

func validateDiagnosticBoolean(value any) (any, bool) {
	boolean, ok := value.(bool)
	return boolean, ok
}

func validateDiagnosticAmount(value any) (any, bool) {
	var number float64
	switch value := value.(type) {
	case float64:
		number = value
	case float32:
		number = float64(value)
	case json.Number:
		parsed, err := value.Float64()
		if err != nil {
			return nil, false
		}
		number = parsed
	default:
		return nil, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || number > 1_000_000_000_000_000 {
		return nil, false
	}
	return number, true
}

func validateDiagnosticHash(value any) (any, bool) {
	text, ok := value.(string)
	if !ok || !isDiagnosticHash(text) {
		return nil, false
	}
	return text, true
}

func validateDiagnosticReason(value any) (any, bool) {
	text, ok := value.(string)
	if !ok || !diagnosticReasonValues[text] {
		return nil, false
	}
	return text, true
}

func validateDiagnosticState(value any) (any, bool) {
	text, ok := value.(string)
	if !ok || !diagnosticStateValues[text] {
		return nil, false
	}
	return text, true
}

func validateDiagnosticRoomID(value any) (any, bool) {
	text, ok := value.(string)
	if !ok || len(text) == 0 || len(text) > 20 || text[0] == '0' {
		return nil, false
	}
	for _, character := range text {
		if character < '0' || character > '9' {
			return nil, false
		}
	}
	if _, err := strconv.ParseUint(text, 10, 64); err != nil {
		return nil, false
	}
	return text, true
}

func validateDiagnosticErrorKind(value any) (any, bool) {
	text, ok := value.(string)
	if !ok || !diagnosticErrorKindValues[text] {
		return nil, false
	}
	return text, true
}

func validateDiagnosticBlindSource(value any) (any, bool) {
	text, ok := value.(string)
	if !ok || !diagnosticBlindSourceValues[text] {
		return nil, false
	}
	return text, true
}

func validateDiagnosticVersion(value any) (any, bool) {
	text, ok := value.(string)
	if !ok || (text != "dev" && !diagnosticVersionPattern.MatchString(text)) {
		return nil, false
	}
	return text, true
}

var diagnosticReasonValues = map[string]bool{"accept": true, "auth": true, "catalog_fetch_failed": true, "connection": true, "consumer": true, "deadline": true, "decompression_failure": true, "dial": true, "duplicate": true, "empty_legacy_line": true, "heartbeat": true, "ignored_command": true, "malformed_envelope": true, "malformed_gift_data": true, "malformed_legacy_line": true, "packet_bounds": true, "read": true, "room_mismatch": true, "source": true, "state_save_failed": true, "write": true}
var diagnosticStateValues = map[string]bool{"idle": true, "connecting": true, "connected": true, "reconnecting": true, "error": true}
var diagnosticErrorKindValues = map[string]bool{"auth": true, "connection": true, "deadline": true, "dial": true, "heartbeat": true, "inbox_capacity": true, "inbox_durability": true, "inbox_open": true, "inbox_persist": true, "inbox_recovery": true, "read": true, "reset_failure": true, "source": true, "transaction": true, "transaction_recovery": true, "write": true}
var diagnosticBlindSourceValues = map[string]bool{"catalog": true, "event": true, "none": true}
var diagnosticPhaseValues = map[string]bool{"resolve": true, "profile": true, "encode": true, "cleanup": true}
var diagnosticExitClassValues = map[string]bool{"source_error": true, "invalid_profile": true, "disk_full": true, "payload_integrity": true, "encoder_error": true, "filesystem_error": true}
var diagnosticModeValues = map[string]bool{"hardware": true, "software": true, "none": true}

func isSafeDiagnosticEvent(value string) bool {
	if value == "bili_frame_decoded" {
		return true
	}
	switch value {
	case "bili_message_ignored", "bili_parse_failed", "blind_box_catalog_failed", "blind_box_catalog_ready", "blind_box_catalog_save_failed", "blind_box_leaderboard_read_failed", "connection_gap", "connection_state", "diagnostic_event_omitted", "gift_accepted", "gift_ignored", "gift_ingestion_failed", "gift_received", "gift_transaction_complete", "gift_transaction_prepare", "gift_transaction_recovery", "gift_clip_job_failed", "gift_clip_job_cleanup_failed", "gift_clip_ffmpeg_failed", "http_listen_failed", "http_ready", "http_server_stopped", "service_start", "service_stop", "tray_failed", "update_install_failed":
		return true
	default:
		return false
	}
}

func validateDiagnosticTaskID(value any) (any, bool) {
	text, ok := value.(string)
	if !ok || len(text) != 24 {
		return nil, false
	}
	for _, character := range text {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' {
			continue
		}
		return nil, false
	}
	return text, true
}

func validateDiagnosticPhase(value any) (any, bool) {
	text, ok := value.(string)
	if !ok || !diagnosticPhaseValues[text] {
		return nil, false
	}
	return text, true
}

func validateDiagnosticExitClass(value any) (any, bool) {
	text, ok := value.(string)
	if !ok || !diagnosticExitClassValues[text] {
		return nil, false
	}
	return text, true
}

func validateDiagnosticMode(value any) (any, bool) {
	text, ok := value.(string)
	if !ok || !diagnosticModeValues[text] {
		return nil, false
	}
	return text, true
}

func sanitizeDiagnosticEvent(value string) string {
	value = sanitizeDiagnosticToken(value)
	if isSafeDiagnosticEvent(value) {
		return value
	}
	return "diagnostic_event_omitted"
}

func sanitizeDiagnosticLevel(value string) string {
	if strings.ToUpper(strings.TrimSpace(value)) == "ERROR" {
		return "ERROR"
	}
	return "INFO"
}

func isDiagnosticHash(value string) bool {
	if len(value) != diagnosticHashLength {
		return false
	}
	for _, character := range value {
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' {
			continue
		}
		return false
	}
	return true
}

func formatDiagnosticValue(value any) string {
	text := strings.TrimSpace(fmt.Sprint(value))
	text = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(text)
	if len(text) > 512 {
		text = text[:512] + "…"
	}
	if text == "" {
		return `""`
	}
	if _, err := strconv.ParseFloat(text, 64); err == nil || text == "true" || text == "false" {
		return text
	}
	return strconv.Quote(text)
}

func formatDiagnosticFieldValue(_ string, value any) string {
	if text, ok := value.(string); ok {
		return strconv.Quote(text)
	}
	return formatDiagnosticValue(value)
}

func sanitizeLegacyDiagnosticLine(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return "legacy_diagnostic_omitted reason=\"empty_legacy_line\""
	}
	var entry map[string]any
	decoder := json.NewDecoder(strings.NewReader(line))
	decoder.UseNumber()
	if decoder.Decode(&entry) == nil && decoder.Decode(&struct{}{}) == io.EOF {
		return formatSanitizedLegacyEntry(entry)
	}
	fields := strings.Fields(line)
	if len(fields) < 3 || !isDiagnosticTimestamp(fields[0]) {
		return "legacy_diagnostic_omitted reason=\"malformed_legacy_line\""
	}
	level := strings.ToUpper(fields[1])
	if level != "INFO" && level != "ERROR" {
		return "legacy_diagnostic_omitted reason=\"malformed_legacy_line\""
	}
	entry = map[string]any{"__log_timestamp": fields[0], "level": level, "event": fields[2]}
	for _, field := range fields[3:] {
		key, value, found := strings.Cut(field, "=")
		if !found {
			continue
		}
		if decoded, err := strconv.Unquote(value); err == nil {
			value = decoded
		}
		entry[key] = parseLegacyDiagnosticValue(key, value)
	}
	return formatSanitizedLegacyEntry(entry)
}

func formatSanitizedLegacyEntry(entry map[string]any) string {
	timestamp, _ := entry["__log_timestamp"].(string)
	if timestamp == "" {
		timestamp, _ = entry["timestamp"].(string)
	}
	level, _ := entry["level"].(string)
	event, _ := entry["event"].(string)
	if !isDiagnosticTimestamp(timestamp) || (strings.ToUpper(level) != "INFO" && strings.ToUpper(level) != "ERROR") || !isSafeDiagnosticEvent(event) {
		return "legacy_diagnostic_omitted reason=\"malformed_legacy_line\""
	}
	line := strings.Builder{}
	line.WriteString(timestamp)
	line.WriteByte(' ')
	line.WriteString(strings.ToUpper(level))
	line.WriteByte(' ')
	line.WriteString(sanitizeDiagnosticToken(event))
	for _, key := range diagnosticFieldOrder {
		value, exists := entry[key]
		if !exists || (key == "timestamp" && entry["__log_timestamp"] == nil) {
			continue
		}
		validated, ok := validateDiagnosticField(key, value)
		if !ok {
			continue
		}
		line.WriteByte(' ')
		line.WriteString(key)
		line.WriteByte('=')
		line.WriteString(formatDiagnosticFieldValue(key, validated))
	}
	return line.String()
}

func parseLegacyDiagnosticValue(key, value string) any {
	if key == "room_id" {
		return value
	}
	if key == "blind_cost" || key == "blind_value" {
		return json.Number(value)
	}
	if value == "true" || value == "false" {
		return value == "true"
	}
	if integer, err := strconv.ParseInt(value, 10, 64); err == nil {
		return integer
	}
	if number, err := strconv.ParseFloat(value, 64); err == nil {
		return number
	}
	return value
}

func isDiagnosticTimestamp(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}
