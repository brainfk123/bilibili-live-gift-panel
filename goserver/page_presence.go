package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type pageMode string

const (
	pageModeConfig  pageMode = "config"
	pageModeDisplay pageMode = "display"
)

type pagePresence struct {
	mu            sync.Mutex
	counts        map[pageMode]int
	generations   map[pageMode]uint64
	closeGrace    time.Duration
	notifications *notificationCenter
	seenPage      bool
	onIdle        func()
}

func newPagePresence(notifications *notificationCenter) *pagePresence {
	return &pagePresence{
		counts:        map[pageMode]int{pageModeConfig: 0, pageModeDisplay: 0},
		generations:   map[pageMode]uint64{pageModeConfig: 0, pageModeDisplay: 0},
		closeGrace:    1200 * time.Millisecond,
		notifications: notifications,
	}
}

func (presence *pagePresence) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/pages/presence/status":
		presence.handleStatus(w, r)
	case "/api/pages/presence/stream":
		presence.handleStream(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (presence *pagePresence) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
		return
	}
	configCount, displayCount := presence.Counts()
	writeJSON(w, http.StatusOK, map[string]any{
		"code":  0,
		"pages": map[string]int{"config": configCount, "display": displayCount},
	})
}

func (presence *pagePresence) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
		return
	}
	mode := pageMode(r.URL.Query().Get("mode"))
	if mode != pageModeConfig && mode != pageModeDisplay {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": "页面类型无效"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": -1, "message": "当前连接不支持页面状态流"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	closePage := presence.register(mode)
	defer closePage()
	_, _ = fmt.Fprint(w, pagePresenceReadyEvent(appVersion))
	flusher.Flush()

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func pagePresenceReadyEvent(version string) string {
	payload, _ := json.Marshal(map[string]string{"version": version})
	return fmt.Sprintf("retry: 500\nevent: ready\ndata: %s\n\n", payload)
}

func (presence *pagePresence) register(mode pageMode) func() {
	presence.mu.Lock()
	presence.seenPage = true
	presence.counts[mode]++
	presence.generations[mode]++
	presence.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			presence.mu.Lock()
			if presence.counts[mode] > 0 {
				presence.counts[mode]--
			}
			presence.generations[mode]++
			generation := presence.generations[mode]
			becameEmpty := presence.counts[mode] == 0
			grace := presence.closeGrace
			presence.mu.Unlock()
			if becameEmpty {
				time.AfterFunc(grace, func() {
					presence.notifyIfStillEmpty(mode, generation)
				})
			}
		})
	}
}

func (presence *pagePresence) notifyIfStillEmpty(mode pageMode, generation uint64) {
	presence.mu.Lock()
	stillEmpty := presence.counts[mode] == 0 && presence.generations[mode] == generation
	allPagesClosed := stillEmpty && presence.seenPage && presence.counts[pageModeConfig] == 0 && presence.counts[pageModeDisplay] == 0
	onIdle := presence.onIdle
	presence.mu.Unlock()
	if !stillEmpty {
		return
	}
	if mode == pageModeConfig {
		presence.notifications.Publish(notificationConfigPagesClosed, "")
	} else {
		presence.notifications.Publish(notificationDisplayPagesClosed, "")
	}
	if allPagesClosed && onIdle != nil {
		onIdle()
	}
}

func (presence *pagePresence) Counts() (config, display int) {
	presence.mu.Lock()
	defer presence.mu.Unlock()
	return presence.counts[pageModeConfig], presence.counts[pageModeDisplay]
}

func (presence *pagePresence) IsIdle() bool {
	presence.mu.Lock()
	defer presence.mu.Unlock()
	return presence.seenPage && presence.counts[pageModeConfig] == 0 && presence.counts[pageModeDisplay] == 0
}

func (presence *pagePresence) SetOnIdle(onIdle func()) {
	presence.mu.Lock()
	presence.onIdle = onIdle
	presence.mu.Unlock()
}
