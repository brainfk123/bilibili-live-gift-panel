package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFrontendDisplayOnlyHarnessServer(t *testing.T) {
	portFile := strings.TrimSpace(os.Getenv("FRONTEND_DISPLAY_E2E_PORT_FILE"))
	if portFile == "" {
		t.Skip("set FRONTEND_DISPLAY_E2E_PORT_FILE to run the browser harness")
	}
	stopFile := strings.TrimSpace(os.Getenv("FRONTEND_DISPLAY_E2E_STOP_FILE"))
	if stopFile == "" {
		t.Fatal("FRONTEND_DISPLAY_E2E_STOP_FILE is required with FRONTEND_DISPLAY_E2E_PORT_FILE")
	}

	server, listener := newFrontendDisplayOnlyHarness(t)
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Errorf("shutdown frontend display harness: %v", err)
		}
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close frontend display listener: %v", err)
		}
		if err := <-serveResult; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve frontend display harness: %v", err)
		}
	}()

	writeFrontendDisplayHarnessPortFile(t, portFile, listener)
	for {
		if _, err := os.Stat(stopFile); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("inspect frontend display stop file: %v", err)
		}
		select {
		case <-t.Context().Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func newFrontendDisplayOnlyHarness(t *testing.T) (*http.Server, net.Listener) {
	t.Helper()
	store, err := newConfigStoreAtPath(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("create frontend display config store: %v", err)
	}
	if _, err := store.replaceClientState(frontendDisplayFixture()); err != nil {
		t.Fatalf("persist frontend display fixture: %v", err)
	}
	listener := listenFrontendDisplayHarness(t)
	uiDirectory := filepath.Join("dist")
	if info, err := os.Stat(filepath.Join(uiDirectory, "ui-assets.json")); err != nil || !info.Mode().IsRegular() {
		_ = listener.Close()
		t.Fatalf("packaged UI manifest missing from %s: %v", uiDirectory, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", store.handle)
	mux.HandleFunc("/api/contributions", handleContributionLedger(store))
	mux.HandleFunc("/api/blind-box/leaderboard", handleBlindBoxLeaderboard(store, nil))
	mux.HandleFunc("/api/runtime", func(w http.ResponseWriter, r *http.Request) {
		writeFrontendDisplayHarnessJSON(w, r, map[string]any{"code": 0, "runtime": map[string]any{"state": "idle", "roomId": ""}})
	})
	mux.HandleFunc("/api/auth/status", func(w http.ResponseWriter, r *http.Request) {
		writeFrontendDisplayHarnessJSON(w, r, map[string]any{"code": 0, "auth": map[string]any{"state": "anonymous"}})
	})
	mux.HandleFunc("/api/room/anchor", func(w http.ResponseWriter, r *http.Request) {
		writeFrontendDisplayHarnessJSON(w, r, map[string]any{"code": 0, "roomId": "fixture-room", "anchor": map[string]any{"uid": 1, "uname": "Fixture"}})
	})
	mux.HandleFunc("/api/gifts", func(w http.ResponseWriter, r *http.Request) {
		writeFrontendDisplayHarnessJSON(w, r, map[string]any{"code": 0, "gifts": []any{}})
	})
	mux.HandleFunc("/api/changelog", func(w http.ResponseWriter, r *http.Request) {
		writeFrontendDisplayHarnessJSON(w, r, map[string]any{"code": 0, "releases": []any{}})
	})
	mux.HandleFunc("/api/update", func(w http.ResponseWriter, r *http.Request) {
		writeFrontendDisplayHarnessJSON(w, r, map[string]any{"code": 0, "update": map[string]any{
			"state": "development", "currentVersion": "dev", "message": "E2E harness", "autoUpdate": false, "restartRequired": false,
		}})
	})
	mux.HandleFunc("/api/pages/presence/stream", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		if flusher, ok := w.(http.Flusher); ok {
			_, _ = io.WriteString(w, ": connected\n\n")
			flusher.Flush()
		}
		<-r.Context().Done()
	})
	mux.Handle("/", newEmbeddedPageHandler(os.DirFS(uiDirectory)))
	return &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}, listener
}

func frontendDisplayFixture() appState {
	state := defaultAppState()
	state.RoomID = "fixture-room"
	state.Settings.ConfigExperience = "advanced"
	state.Attributes = []attributeState{{ID: "score", Name: "积分", Value: 12, Unit: "none", Format: "number", Decimals: 0, Suffix: "分"}}
	state.GiftCatalog = []giftInfo{
		{ID: 35800, Name: "宝藏盲盒", Price: 1000, CoinType: "gold", ImgBasic: "data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw=="},
		{ID: 35801, Name: "星光盲盒", Price: 500, CoinType: "gold", ImgBasic: "data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw=="},
	}
	state.GiftKPIPanels = []giftKPIPanelState{{
		ID: "target", Name: "盲盒目标", Layout: "row", Items: []giftKPIItemState{{GiftID: 35800, GiftName: "宝藏盲盒", Target: 10, BarStyle: "progress"}},
		Appearance: displayAppearanceState{ThemeID: "glass", FontSize: 36, AccentColor: "#fb7299", ShowConnection: true, Align: "center", PanelOpacity: 55},
	}}
	state.Activities = []activitySessionState{{ID: "activity", Name: "活动", AttributeNames: []string{"积分"}, Status: "draft", ResultMode: "manual", InitialValues: map[string]float64{"积分": 12}, Milestones: []activityMilestoneState{}}}
	state.TimerRules = []timerRule{{ID: "timer", AttributeName: "积分", FormulaName: "每分钟", IntervalSeconds: 60, Formula: "value + 1", Enabled: true}}
	state.Contributions = contributionLedgerState{UpdatedAt: 1_700_000_000_000, Viewers: []viewerContribution{
		{Key: "uid:1", UID: 1, Uname: "甲观众", GiftCount: 3, GoldValue: 3000, AttributeDeltas: map[string]float64{"积分": 3}, BlindBoxCount: 2, BlindBoxCost: 2000, BlindBoxValue: 3600, BlindBoxProfit: 1600, BlindBoxes: []blindBoxContribution{{GiftID: 35800, GiftName: "宝藏盲盒", Count: 2, Cost: 2000, Value: 3600, Profit: 1600, LastGiftAt: 1_700_000_000_000}}, LastGiftAt: 1_700_000_000_000},
		{Key: "uid:2", UID: 2, Uname: "乙观众", GiftCount: 2, GoldValue: 1500, AttributeDeltas: map[string]float64{"积分": 2}, BlindBoxCount: 2, BlindBoxCost: 1000, BlindBoxValue: 600, BlindBoxProfit: -400, BlindBoxes: []blindBoxContribution{{GiftID: 35800, GiftName: "宝藏盲盒", Count: 1, Cost: 1000, Value: 200, Profit: -800, LastGiftAt: 1_700_000_000_100}, {GiftID: 35801, GiftName: "星光盲盒", Count: 1, Cost: 0, Value: 400, Profit: 400, UnpricedCount: 1, LastGiftAt: 1_700_000_000_200}}, LastGiftAt: 1_700_000_000_200},
	}}
	return state
}

func listenFrontendDisplayHarness(t *testing.T) net.Listener {
	t.Helper()
	for attempt := 0; attempt < 32; attempt++ {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen frontend display harness: %v", err)
		}
		address, ok := listener.Addr().(*net.TCPAddr)
		if !ok || !address.IP.IsLoopback() || address.Port == 0 {
			_ = listener.Close()
			t.Fatal("frontend display harness did not receive a dynamic loopback address")
		}
		if address.Port < 12450 || address.Port > 12459 {
			return listener
		}
		_ = listener.Close()
	}
	t.Fatal("frontend display harness could not reserve a dynamic port outside 12450-12459")
	return nil
}

func writeFrontendDisplayHarnessJSON(w http.ResponseWriter, r *http.Request, payload map[string]any) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func writeFrontendDisplayHarnessPortFile(t *testing.T, path string, listener net.Listener) {
	t.Helper()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("frontend display harness listener is not TCP")
	}
	payload, err := json.Marshal(struct {
		URL string `json:"url"`
		PID int    `json:"pid"`
	}{URL: fmt.Sprintf("http://127.0.0.1:%d", address.Port), PID: os.Getpid()})
	if err != nil {
		t.Fatalf("encode frontend display harness port file: %v", err)
	}
	if err := os.WriteFile(path, append(payload, '\n'), 0o600); err != nil {
		t.Fatalf("write frontend display harness port file: %v", err)
	}
}
