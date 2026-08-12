package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
	line.WriteString(strings.ToUpper(strings.TrimSpace(level)))
	line.WriteByte(' ')
	line.WriteString(sanitizeDiagnosticEvent(event))
	for index := 0; index+1 < len(keyValues); index += 2 {
		key := sanitizeDiagnosticToken(fmt.Sprint(keyValues[index]))
		if key == "" {
			continue
		}
		value := sanitizeDiagnosticValue(key, keyValues[index+1])
		line.WriteByte(' ')
		line.WriteString(key)
		line.WriteByte('=')
		line.WriteString(formatDiagnosticValue(value))
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

func isSensitiveDiagnosticKey(key string) bool {
	normalized := normalizeDiagnosticKey(key)
	if normalized == "rndhash" || normalized == "errorkind" {
		return false
	}
	for _, candidate := range []string{"cookie", "token", "secret", "session", "authorization", "sessdata", "csrf", "password", "credential"} {
		if strings.Contains(normalized, candidate) {
			return true
		}
	}
	for _, candidate := range []string{"rnd", "uid", "user", "viewer", "uname", "avatar", "face", "url", "payload", "frame", "json", "error", "message"} {
		if strings.Contains(normalized, candidate) {
			return true
		}
	}
	return false
}

func normalizeDiagnosticKey(key string) string {
	return strings.Map(func(character rune) rune {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			return character
		}
		return -1
	}, strings.ToLower(key))
}

func sanitizeDiagnosticValue(key string, value any) any {
	if isSensitiveDiagnosticKey(key) {
		return "[REDACTED]"
	}
	switch value := value.(type) {
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		return value
	case string:
		if normalizeDiagnosticKey(key) == "rndhash" && isDiagnosticHash(value) {
			return value
		}
		if isSafeDiagnosticString(key, value) {
			return value
		}
	}
	return "[REDACTED]"
}

func isSafeDiagnosticString(key, value string) bool {
	if !isSafeDiagnosticToken(value) {
		return false
	}
	switch normalizeDiagnosticKey(key) {
	case "reason":
		switch value {
		case "accept", "auth", "catalog_fetch_failed", "connection", "consumer", "deadline", "decompression_failure", "dial", "duplicate", "empty_legacy_line", "heartbeat", "ignored_command", "malformed_envelope", "malformed_gift_data", "malformed_legacy_line", "packet_bounds", "read", "room_mismatch", "source", "state_save_failed", "write":
			return true
		}
	case "state":
		switch value {
		case "idle", "connecting", "connected", "reconnecting", "error":
			return true
		}
	case "errorkind":
		switch value {
		case "auth", "connection", "deadline", "dial", "heartbeat", "read", "source", "write":
			return true
		}
	case "blindsource":
		switch value {
		case "catalog", "event", "none":
			return true
		}
	case "version":
		return strings.HasPrefix(value, "v") || value[0] >= '0' && value[0] <= '9'
	}
	return false
}

func isSafeDiagnosticEvent(value string) bool {
	switch value {
	case "bili_message_ignored", "bili_parse_failed", "blind_box_catalog_failed", "blind_box_catalog_ready", "blind_box_catalog_save_failed", "connection_gap", "connection_state", "diagnostic_event_omitted", "gift_accepted", "gift_ignored", "gift_ingestion_failed", "gift_received", "gift_transaction_complete", "gift_transaction_prepare", "gift_transaction_recovery", "http_listen_failed", "http_ready", "http_server_stopped", "service_start", "service_stop", "tray_failed", "update_install_failed":
		return true
	default:
		return false
	}
}

func sanitizeDiagnosticEvent(value string) string {
	value = sanitizeDiagnosticToken(value)
	if isSafeDiagnosticEvent(value) {
		return value
	}
	return "diagnostic_event_omitted"
}

func isSafeDiagnosticToken(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 96 {
		return false
	}
	for _, character := range value {
		if character == '_' || character == '-' || character == '.' || character >= '0' && character <= '9' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' {
			continue
		}
		return false
	}
	return true
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

func sanitizeLegacyDiagnosticLine(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return "legacy_diagnostic_omitted reason=\"empty_legacy_line\""
	}
	var entry map[string]any
	if json.Unmarshal([]byte(line), &entry) == nil {
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
		entry[key] = parseLegacyDiagnosticValue(value)
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
	for _, key := range []string{"gift_id", "blind_parent_id", "count", "timestamp", "rnd_hash", "reason", "state", "error_kind", "source_duplicate", "accept_write_ms", "inbox_depth", "oldest_pending_age_ms", "attempts", "duration_ms"} {
		value, exists := entry[key]
		if !exists || (key == "timestamp" && entry["__log_timestamp"] == nil) {
			continue
		}
		sanitized := sanitizeDiagnosticValue(key, value)
		line.WriteByte(' ')
		line.WriteString(key)
		line.WriteByte('=')
		line.WriteString(formatDiagnosticValue(sanitized))
	}
	return line.String()
}

func parseLegacyDiagnosticValue(value string) any {
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
