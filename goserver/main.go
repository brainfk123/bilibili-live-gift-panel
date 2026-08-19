package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed all:dist
var embeddedFS embed.FS

func newEmbeddedPageHandler(pageFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(pageFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, segment := range strings.Split(r.URL.Path, "/") {
			if segment == ".." {
				http.NotFound(w, r)
				return
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}

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

func existingPanelURL() string {
	instances := findPanelInstances()
	if len(instances) > 0 {
		return panelConfigURL(instances[0])
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
			Condition      string  `json:"condition"`
			UserIdentity   *int    `json:"userIdentity"`
			ValidateOnly   bool    `json:"validateOnly"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&request); err != nil {
			if strings.Contains(err.Error(), "userIdentity") {
				writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": "用户身份必须是 0 到 4 的整数"})
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": "请求格式不正确"})
			return
		}
		identityLevel := giftIdentityOrdinary
		if request.UserIdentity != nil {
			identityLevel = *request.UserIdentity
			if identityLevel < giftIdentityOrdinary || identityLevel > giftIdentityGovernor {
				writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": "用户身份必须是 0 到 4 的整数"})
				return
			}
		}
		state, err := store.readState()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"code": -1, "message": err.Error()})
			return
		}
		if request.Context == "timer" {
			if request.ValidateOnly {
				if err := validateTimerFormula(state, request.Formula, request.AttributeName, request.AttributeValue); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": err.Error()})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"code": 0})
				return
			}
			result, err := timerFormulaPreview(state, request.Formula, request.AttributeName, request.AttributeValue)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"code": 0, "result": result})
			return
		} else {
			price := request.GiftPrice
			if price <= 0 {
				price = 1000
			}
			if request.ValidateOnly {
				if strings.TrimSpace(request.Condition) != "" {
					if err := validateGiftFormula(state, request.Condition, request.AttributeName, request.AttributeValue, price); err != nil {
						writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": err.Error()})
						return
					}
				}
				if err := validateGiftFormula(state, request.Formula, request.AttributeName, request.AttributeValue, price); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": err.Error()})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"code": 0})
				return
			}
			preview, err := previewGiftRule(state, request.Condition, request.Formula, request.AttributeName, request.AttributeValue, price, identityLevel)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1, "message": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"code": 0, "triggered": preview.Triggered, "result": preview.Result})
			return
		}
	}
}

func handleRuntimeStatus(background *backgroundRuntime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "runtime": background.Status()})
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

func newMainGiftClipJobs(store *configStore, media *giftReceiptAPI, diagnostics *diagnosticLogger, loadPayload func(string) (*giftClipPayload, error), newManager func(string, giftClipSourceResolver, giftClipEncoder, *diagnosticLogger) *giftClipJobManager) (*giftClipJobManager, error) {
	payload, err := loadPayload(defaultGiftClipCacheRoot())
	if err != nil {
		return nil, err
	}
	encoder := newGiftClipFFmpegEncoder(payload, newGiftClipProcessRunner(), diagnostics, giftClipFFmpegEncoderOptions{})
	return newManager(defaultGiftClipTaskRoot(), newGiftClipSourceResolver(store, media), encoder, diagnostics), nil
}

func newMainGiftClipCloser(closeGiftClips func()) func() {
	var once sync.Once
	return func() { once.Do(closeGiftClips) }
}

func runMainGiftClipShutdown(stopRuntime, closeGiftClips, closeServer, installUpdate func()) {
	stopRuntime()
	closeGiftClips()
	closeServer()
	installUpdate()
}

func runMainPendingGiftClipUpdate(closeGiftClips, installUpdate func()) {
	closeGiftClips()
	installUpdate()
}

func registerAttributeEditRoutes(mux *http.ServeMux, store *configStore, background *backgroundRuntime, leases *attributeEditLeaseCoordinator, service *attributeEditService) {
	background.setAttributeFreezeChecker(leases)
	mux.Handle("/api/attribute-edit-lease", newAttributeEditLeaseHandler(store, leases))
	handler := newAttributeEditHandler(service)
	mux.Handle("/api/attribute-edits/session", handler)
	mux.Handle("/api/attribute-edits", handler)
}

func updateReadyExitHandler(updateExit chan<- struct{}) func(string) {
	return func(_ string) {
		select {
		case updateExit <- struct{}{}:
		default:
		}
	}
}

func main() {
	if handled, updateErr := runUpdateHelper(os.Args[1:]); handled {
		if updateErr != nil {
			showStartupError(fmt.Sprintf("自动更新失败：%v", updateErr))
		}
		return
	}
	instances := findPanelInstances()
	if instance, found := panelToOpen(appVersion, instances); found {
		openURL(panelConfigURL(instance))
		return
	}
	for _, instance := range instances {
		if !requestPanelExit(instance, appVersion) {
			requestLegacyPanelExit()
		}
		if !waitForPanelExit(instance.Port) {
			showStartupError("旧版本仍在退出中，请稍后再次打开新版本。")
			return
		}
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

	pageFS, err := fs.Sub(embeddedFS, "dist")
	if err != nil {
		showStartupError(fmt.Sprintf("内嵌页面读取失败：%v", err))
		return
	}

	store, err := newDefaultConfigStore()
	if err != nil {
		showStartupError(err.Error())
		return
	}
	diagnostics, diagnosticErr := newDiagnosticLogger(filepath.Join(filepath.Dir(store.path), "runtime.log"))
	if diagnosticErr == nil {
		diagnostics.Info("service_start", "version", appVersion)
		defer diagnostics.Info("service_stop", "version", appVersion)
	}
	giftMedia := newGiftReceiptAPI(store, nil)
	giftClips, err := newMainGiftClipJobs(store, giftMedia, diagnostics, embeddedGiftClipPayload, newGiftClipJobManager)
	if err != nil {
		showStartupError(err.Error())
		return
	}
	closeGiftClips := newMainGiftClipCloser(giftClips.Close)
	defer closeGiftClips()
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
		runMainPendingGiftClipUpdate(closeGiftClips, func() {
			if err := updater.InstallOnExit(true); err != nil {
				showStartupError(err.Error())
			}
		})
		return
	}
	presence := newPagePresence(notifications)
	updateExit := make(chan struct{}, 1)
	instanceExit := make(chan struct{}, 1)
	updater.SetOnReady(updateReadyExitHandler(updateExit))
	runtimeContext, stopRuntime := context.WithCancel(context.Background())
	background := newBackgroundRuntime(store, func() giftEventSource {
		return &bilibiliGiftSource{sessionProvider: login.Session}
	}, notifications)
	attributeEditLeases := newDefaultAttributeEditLeaseCoordinator()
	attributeEdits := newAttributeEditService(store, attributeEditLeases, newAttributeEditID)
	background.setDiagnosticLogger(diagnostics)
	store.setOnChange(background.NotifyConfigChanged)
	store.setOnTimerChange(background.NotifyTimerConfigChanged)
	store.setOnUpdateChange(updater.NotifySettingsChanged)
	store.setResetCoordinator(background.ResetWithOutcome)
	login.SetOnChange(background.NotifyConfigChanged)

	mux := http.NewServeMux()
	mux.Handle("/", newEmbeddedPageHandler(pageFS))
	registerAttributeEditRoutes(mux, store, background, attributeEditLeases, attributeEdits)

	mux.HandleFunc("/api/room_info", handleRoomInfo)
	mux.HandleFunc("/api/room/anchor", newRoomAnchorHandler(login.roomOwnerUID, background.profileResolver))
	mux.HandleFunc("/api/config", store.handle)
	mux.HandleFunc("/api/contributions", handleContributionLedger(store))
	registerBlindBoxLeaderboardRoute(mux, store, diagnostics)
	mux.HandleFunc("/api/gift-targets/progress", handleGiftTargetProgress(store))
	mux.HandleFunc("/api/formula/preview", handleFormulaPreview(store))
	mux.HandleFunc("/api/activities/transition", handleActivityTransition(store))
	mux.HandleFunc("/api/blind-box", handleBlindBoxInfo(login))
	mux.HandleFunc("/api/gifts", handleRoomGiftCatalog(login))
	mux.HandleFunc("/api/gift-receipts", giftMedia.handleReceipts)
	mux.HandleFunc("/api/gift-receipts/media", giftMedia.handleMedia)
	giftClipAPI := newGiftClipHTTPHandler(giftClips)
	mux.Handle("/api/gift-clips", giftClipAPI)
	mux.Handle("/api/gift-clips/", giftClipAPI)
	mux.HandleFunc("/api/update", updater.handleStatus)
	mux.HandleFunc("/api/update/check", updater.handleCheck)
	mux.HandleFunc("/api/changelog", newHostedChangelogHandler(nil, defaultHostedChangelogSources()))
	mux.HandleFunc("/api/diagnostics/log", func(w http.ResponseWriter, r *http.Request) {
		if diagnostics == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": -1, "message": "运行日志暂时不可用"})
			return
		}
		diagnostics.handleExport(w, r)
	})
	mux.HandleFunc("/api/auth/", login.handle)
	mux.Handle("/api/pages/presence/", presence)
	mux.HandleFunc("/api/runtime", handleRuntimeStatus(background))
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"name": panelHealthMarker, "version": appVersion})
	})
	mux.HandleFunc("/api/instance/exit", newInstanceExitHandler(appVersion, instanceExit))

	listener, port, err := listenWithFallback(12450, 10)
	if err != nil {
		diagnostics.Error("http_listen_failed", "error", err)
		showStartupError(err.Error())
		return
	}
	diagnostics.Info("http_ready", "port", port)
	server := &http.Server{Handler: mux}
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			diagnostics.Error("http_server_stopped", "error", serveErr)
			showStartupError(fmt.Sprintf("本地服务已停止：%v", serveErr))
		}
	}()
	openConfigOnStartup := announceStartup(notifications, installedVersion)
	runtimeDone := make(chan struct{})
	go func() {
		defer close(runtimeDone)
		background.Run(runtimeContext)
	}()
	go updater.Run(runtimeContext)

	configURL := fmt.Sprintf("http://localhost:%d/?mode=config", port)
	if openConfigOnStartup {
		go openURL(configURL)
	}
	restartAfterUpdate, trayErr := runTrayApp(configURL, notifications, updateExit, instanceExit)
	if trayErr != nil {
		diagnostics.Error("tray_failed", "error", trayErr)
		showStartupError(fmt.Sprintf("系统托盘启动失败：%v", trayErr))
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runMainGiftClipShutdown(func() {
		stopRuntime()
		<-runtimeDone
	}, closeGiftClips, func() { _ = server.Shutdown(shutdownContext) }, func() {
		if err := updater.InstallOnExit(restartAfterUpdate); err != nil {
			diagnostics.Error("update_install_failed", "error", err)
			showStartupError(err.Error())
		}
	})
}
