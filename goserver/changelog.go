package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	hostedChangelogURL      = "https://github.com/brainfk123/bilibili-live-gift-panel/releases/latest/download/gift-panel-changelog.json"
	hostedChangelogMaxBytes = int64(2 << 20)
	hostedChangelogCacheTTL = 30 * time.Minute
)

type hostedChangelogDocument struct {
	SchemaVersion int               `json:"schemaVersion"`
	Releases      []json.RawMessage `json:"releases"`
}

func newHostedChangelogHandler(client *http.Client, sourceURL string) http.HandlerFunc {
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	var mu sync.Mutex
	var cached hostedChangelogDocument
	var cachedAt time.Time

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
			return
		}

		mu.Lock()
		defer mu.Unlock()
		if len(cached.Releases) > 0 && time.Since(cachedAt) < hostedChangelogCacheTTL {
			writeJSON(w, http.StatusOK, map[string]any{"code": 0, "releases": cached.Releases})
			return
		}

		document, err := fetchHostedChangelog(r, client, sourceURL)
		if err != nil {
			if len(cached.Releases) > 0 {
				writeJSON(w, http.StatusOK, map[string]any{"code": 0, "releases": cached.Releases})
				return
			}
			writeJSON(w, http.StatusBadGateway, map[string]any{"code": -1, "message": "在线更新日志暂时不可用"})
			return
		}
		cached = document
		cachedAt = time.Now()
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "releases": document.Releases})
	}
}

func fetchHostedChangelog(r *http.Request, client *http.Client, sourceURL string) (hostedChangelogDocument, error) {
	if sourceURL == "" {
		return hostedChangelogDocument{}, errors.New("更新日志地址为空")
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, sourceURL, nil)
	if err != nil {
		return hostedChangelogDocument{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "bilibili-live-gift-panel")
	response, err := client.Do(request)
	if err != nil {
		return hostedChangelogDocument{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return hostedChangelogDocument{}, fmt.Errorf("更新日志返回 HTTP %d", response.StatusCode)
	}
	limited := io.LimitReader(response.Body, hostedChangelogMaxBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return hostedChangelogDocument{}, err
	}
	if int64(len(body)) > hostedChangelogMaxBytes {
		return hostedChangelogDocument{}, errors.New("更新日志文件过大")
	}
	var document hostedChangelogDocument
	if err := json.Unmarshal(body, &document); err != nil {
		return hostedChangelogDocument{}, err
	}
	if document.SchemaVersion != 1 || len(document.Releases) == 0 {
		return hostedChangelogDocument{}, errors.New("更新日志格式无效")
	}
	return document, nil
}
