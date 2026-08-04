package main

import (
	"bytes"
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
	line.WriteString(sanitizeDiagnosticToken(event))
	for index := 0; index+1 < len(keyValues); index += 2 {
		key := sanitizeDiagnosticToken(fmt.Sprint(keyValues[index]))
		if key == "" {
			continue
		}
		value := keyValues[index+1]
		if isSensitiveDiagnosticKey(key) {
			value = "[REDACTED]"
		}
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
	for _, candidate := range []string{logger.path + ".1", logger.path} {
		data, err := os.ReadFile(candidate)
		if err != nil || len(data) == 0 {
			continue
		}
		output.WriteString("\n--- ")
		output.WriteString(filepath.Base(candidate))
		output.WriteString(" ---\n")
		output.Write(data)
		if data[len(data)-1] != '\n' {
			output.WriteByte('\n')
		}
	}
	if output.Len() == 0 {
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
	normalized := strings.ToLower(key)
	for _, candidate := range []string{"cookie", "token", "secret", "session", "authorization", "sessdata", "csrf"} {
		if strings.Contains(normalized, candidate) {
			return true
		}
	}
	return false
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
