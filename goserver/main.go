package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

//go:embed dist/index.html
var embeddedFS embed.FS

func writeJSON(w http.ResponseWriter, code int, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}

func handleRoomInfo(w http.ResponseWriter, r *http.Request) {
	roomID := r.URL.Query().Get("roomId")
	if roomID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": "缺少房间号"})
		return
	}
	info, err := getRoomInfo(roomID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"code": -1, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "roomId": info.RoomID, "buvid": info.Buvid, "token": info.Token, "hostList": info.HostList})
}

func listenWithFallback(startPort, attempts int) (net.Listener, int, error) {
	if attempts < 1 {
		return nil, 0, fmt.Errorf("没有可用的端口尝试次数")
	}
	var lastErr error
	for offset := 0; offset < attempts; offset++ {
		port := startPort + offset
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return listener, port, nil
		}
		lastErr = err
	}
	return nil, 0, fmt.Errorf("无法监听 %d-%d 端口：%w", startPort, startPort+attempts-1, lastErr)
}

func isExistingPanel(port int) bool {
	client := http.Client{Timeout: 300 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	return err == nil && resp.StatusCode == http.StatusOK && string(body) == "bilibili-live-gift-panel"
}

func existingPanelURL() string {
	for port := 12450; port < 12460; port++ {
		if isExistingPanel(port) {
			return fmt.Sprintf("http://localhost:%d/?mode=config", port)
		}
	}
	return ""
}

func handleFormulaPreview(store *configStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
			return
		}
		var request struct {
			Formula        string  `json:"formula"`
			AttributeName  string  `json:"attributeName"`
			AttributeValue float64 `json:"attributeValue"`
			Context        string  `json:"context"`
			GiftPrice      float64 `json:"giftPrice"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": "请求格式不正确"})
			return
		}
		state, err := store.readState()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"code": -1, "message": err.Error()})
			return
		}
		var result float64
		if request.Context == "timer" {
			result, err = timerFormulaPreview(state, request.Formula, request.AttributeName, request.AttributeValue)
		} else {
			price := request.GiftPrice
			if price <= 0 {
				price = 1000
			}
			result, err = formulaPreviewWithPrice(state, request.Formula, request.AttributeName, request.AttributeValue, price)
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "result": result})
	}
}

func announceStartup(notifications *notificationCenter, installedVersion string) bool {
	if installedVersion != "" {
		notifications.Publish(notificationUpdateSucceeded, installedVersion)
		return false
	}
	notifications.Publish(notificationServiceStarted, "")
	return true
}

func main() {
	if handled, updateErr := runUpdateHelper(os.Args[1:]); handled {
		if updateErr != nil {
			showStartupError(fmt.Sprintf("自动更新失败：%v", updateErr))
		}
		return
	}
	alreadyRunning, releaseInstance, err := acquireSingleInstance()
	if err != nil {
		showStartupError(fmt.Sprintf("单实例检查失败：%v", err))
		return
	}
	if alreadyRunning {
		for attempt := 0; attempt < 15; attempt++ {
			if url := existingPanelURL(); url != "" {
				openURL(url)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		showStartupError("直播礼物面板已在运行，但暂时无法打开配置页面。")
		return
	}
	defer releaseInstance()

	indexHTML, err := embeddedFS.ReadFile("dist/index.html")
	if err != nil {
		showStartupError(fmt.Sprintf("内嵌页面读取失败：%v", err))
		return
	}

	store, err := newDefaultConfigStore()
	if err != nil {
		showStartupError(err.Error())
		return
	}
	loginStore, err := newDefaultLoginCredentialStore()
	if err != nil {
		showStartupError(err.Error())
		return
	}
	login := newLoginManager(nil, loginStore, nil)
	notifications := newNotificationCenter()
	updater := newDefaultAutoUpdater(store)
	installedVersion := updater.ConsumeInstalledVersion()
	if installedVersion == "" && updater.HasPending() {
		if err := updater.InstallOnExit(true); err != nil {
			showStartupError(err.Error())
		}
		return
	}
	presence := newPagePresence(notifications)
	updateExit := make(chan struct{}, 1)
	requestIdleUpdate := func() {
		if !presence.IsIdle() {
			return
		}
		if updater.HasPending() {
			select {
			case updateExit <- struct{}{}:
			default:
			}
			return
		}
		updater.NotifyIdle()
	}
	updater.SetAutomaticAllowed(presence.IsIdle)
	updater.SetOnReady(func(_ string) { requestIdleUpdate() })
	presence.SetOnIdle(requestIdleUpdate)
	runtimeContext, stopRuntime := context.WithCancel(context.Background())
	background := newBackgroundRuntime(store, func() giftEventSource {
		return &bilibiliGiftSource{sessionProvider: login.Session}
	}, notifications)
	store.setOnChange(background.NotifyConfigChanged)
	store.setOnTimerChange(background.NotifyTimerConfigChanged)
	store.setOnUpdateChange(updater.NotifySettingsChanged)
	login.SetOnChange(background.NotifyConfigChanged)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})

	mux.HandleFunc("/api/room_info", handleRoomInfo)
	mux.HandleFunc("/api/config", store.handle)
	mux.HandleFunc("/api/formula/preview", handleFormulaPreview(store))
	mux.HandleFunc("/api/activities/transition", handleActivityTransition(store))
	mux.HandleFunc("/api/blind-box", handleBlindBoxInfo(login))
	mux.HandleFunc("/api/gifts", handleRoomGiftCatalog(login))
	mux.HandleFunc("/api/update", updater.handleStatus)
	mux.HandleFunc("/api/update/check", updater.handleCheck)
	mux.HandleFunc("/api/auth/", login.handle)
	mux.Handle("/api/pages/presence/", presence)
	mux.HandleFunc("/api/runtime", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "runtime": background.Status()})
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("bilibili-live-gift-panel"))
	})

	listener, port, err := listenWithFallback(12450, 10)
	if err != nil {
		showStartupError(err.Error())
		return
	}
	server := &http.Server{Handler: mux}
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			showStartupError(fmt.Sprintf("本地服务已停止：%v", serveErr))
		}
	}()
	openConfigOnStartup := announceStartup(notifications, installedVersion)
	go background.Run(runtimeContext)
	go updater.Run(runtimeContext)

	configURL := fmt.Sprintf("http://localhost:%d/?mode=config", port)
	if openConfigOnStartup {
		go openURL(configURL)
	}
	restartAfterUpdate, trayErr := runTrayApp(configURL, notifications, updateExit)
	if trayErr != nil {
		showStartupError(fmt.Sprintf("系统托盘启动失败：%v", trayErr))
	}
	stopRuntime()
	shutdownContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownContext)
	if err := updater.InstallOnExit(restartAfterUpdate); err != nil {
		showStartupError(err.Error())
	}
}
