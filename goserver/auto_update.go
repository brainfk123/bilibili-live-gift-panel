package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	appVersion = "dev"
	appCommit  = ""
)

const (
	updateReleaseURL  = "https://api.github.com/repos/brainfk123/bilibili-live-gift-panel/releases/latest"
	updateAssetName   = "gift-panel-windows-x64.exe"
	updateMaxBytes    = int64(256 << 20)
	updateCheckPeriod = 6 * time.Hour
)

type updateStatus struct {
	State           string `json:"state"`
	CurrentVersion  string `json:"currentVersion"`
	LatestVersion   string `json:"latestVersion,omitempty"`
	Message         string `json:"message"`
	Progress        int    `json:"progress,omitempty"`
	LastCheckedAt   int64  `json:"lastCheckedAt,omitempty"`
	AutoUpdate      bool   `json:"autoUpdate"`
	RestartRequired bool   `json:"restartRequired"`
}

type githubRelease struct {
	TagName    string        `json:"tag_name"`
	Draft      bool          `json:"draft"`
	Prerelease bool          `json:"prerelease"`
	Assets     []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
	Digest      string `json:"digest"`
}

type pendingUpdate struct {
	Version     string `json:"version"`
	SHA256      string `json:"sha256"`
	PendingPath string `json:"pendingPath"`
	TargetPath  string `json:"targetPath"`
}

type autoUpdaterOptions struct {
	Store          *configStore
	Notifications  *notificationCenter
	Client         *http.Client
	CurrentVersion string
	ExecutablePath string
	UpdatesDir     string
	ReleaseURL     string
	AssetName      string
	CheckPeriod    time.Duration
	Now            func() time.Time
}

type autoUpdater struct {
	store          *configStore
	notifications  *notificationCenter
	client         *http.Client
	currentVersion string
	executablePath string
	updatesDir     string
	releaseURL     string
	assetName      string
	checkPeriod    time.Duration
	now            func() time.Time
	trigger        chan bool

	mu      sync.Mutex
	status  updateStatus
	pending *pendingUpdate
	etag    string
}

func newDefaultAutoUpdater(store *configStore, notifications *notificationCenter) *autoUpdater {
	root, rootErr := os.UserConfigDir()
	executablePath, executableErr := os.Executable()
	updater := newAutoUpdater(autoUpdaterOptions{
		Store:          store,
		Notifications:  notifications,
		Client:         &http.Client{Timeout: 10 * time.Minute},
		CurrentVersion: appVersion,
		ExecutablePath: executablePath,
		UpdatesDir:     filepath.Join(root, "BilibiliLiveGiftPanel", "updates"),
		ReleaseURL:     updateReleaseURL,
		AssetName:      updateAssetName,
		CheckPeriod:    updateCheckPeriod,
		Now:            time.Now,
	})
	if rootErr != nil || executableErr != nil {
		updater.setStatus("error", "", "无法确定自动更新目录或程序路径。", 0, false)
	}
	return updater
}

func newAutoUpdater(options autoUpdaterOptions) *autoUpdater {
	client := options.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	period := options.CheckPeriod
	if period <= 0 {
		period = updateCheckPeriod
	}
	updater := &autoUpdater{
		store:          options.Store,
		notifications:  options.Notifications,
		client:         client,
		currentVersion: strings.TrimPrefix(strings.TrimSpace(options.CurrentVersion), "v"),
		executablePath: options.ExecutablePath,
		updatesDir:     options.UpdatesDir,
		releaseURL:     options.ReleaseURL,
		assetName:      options.AssetName,
		checkPeriod:    period,
		now:            now,
		trigger:        make(chan bool, 1),
	}
	if updater.currentVersion == "" {
		updater.currentVersion = "dev"
	}
	if updater.releaseURL == "" {
		updater.releaseURL = updateReleaseURL
	}
	if updater.assetName == "" {
		updater.assetName = updateAssetName
	}
	updater.status = updateStatus{
		State:          "idle",
		CurrentVersion: updater.currentVersion,
		Message:        "尚未检查更新。",
	}
	if !isAutoUpdateSupported() {
		updater.status.State = "unsupported"
		updater.status.Message = "当前系统暂不支持自动更新。"
	} else if updater.currentVersion == "dev" {
		updater.status.State = "development"
		updater.status.Message = "开发版本不会检查 GitHub 更新。"
	} else {
		updater.restorePendingUpdate()
	}
	return updater
}

func (updater *autoUpdater) Run(ctx context.Context) {
	if updater == nil {
		return
	}
	if updater.canCheck() && updater.autoUpdateEnabled() && updater.Status().State != "ready" {
		updater.checkAndDownload(ctx, false)
	}
	ticker := time.NewTicker(updater.checkPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case manual := <-updater.trigger:
			if manual || updater.autoUpdateEnabled() {
				updater.checkAndDownload(ctx, manual)
			}
		case <-ticker.C:
			if updater.autoUpdateEnabled() {
				updater.checkAndDownload(ctx, false)
			}
		}
	}
}

func (updater *autoUpdater) NotifySettingsChanged() {
	if updater == nil || !updater.autoUpdateEnabled() {
		return
	}
	select {
	case updater.trigger <- true:
	default:
	}
}

func (updater *autoUpdater) CheckNow() updateStatus {
	if updater == nil {
		return updateStatus{State: "error", CurrentVersion: appVersion, Message: "更新模块未初始化。"}
	}
	if !updater.canCheck() {
		return updater.Status()
	}
	updater.setStatus("checking", updater.Status().LatestVersion, "正在检查 GitHub 最新版本…", 0, false)
	select {
	case updater.trigger <- true:
	default:
	}
	return updater.Status()
}

func (updater *autoUpdater) Status() updateStatus {
	if updater == nil {
		return updateStatus{State: "error", CurrentVersion: appVersion, Message: "更新模块未初始化。"}
	}
	updater.mu.Lock()
	status := updater.status
	updater.mu.Unlock()
	status.AutoUpdate = updater.autoUpdateEnabled()
	if !status.AutoUpdate && status.State != "ready" && status.State != "downloading" && status.State != "checking" && updater.canCheck() {
		status.State = "disabled"
		status.Message = "自动更新已关闭，仍可手动检查。"
	}
	return status
}

func (updater *autoUpdater) InstallOnExit() error {
	if updater == nil || !isAutoUpdateSupported() {
		return nil
	}
	updater.mu.Lock()
	pending := updater.pending
	updater.mu.Unlock()
	if pending == nil {
		return nil
	}
	if err := verifyFileSHA256(pending.PendingPath, pending.SHA256); err != nil {
		return fmt.Errorf("退出更新校验失败：%w", err)
	}
	metadataPath := updater.metadataPath()
	if err := launchUpdateInstaller(metadataPath, os.Getpid()); err != nil {
		return fmt.Errorf("启动更新替换器失败：%w", err)
	}
	return nil
}

func (updater *autoUpdater) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "update": updater.Status()})
}

func (updater *autoUpdater) handleCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"code": 0, "update": updater.CheckNow()})
}

func (updater *autoUpdater) canCheck() bool {
	return isAutoUpdateSupported() && updater.currentVersion != "dev" && updater.releaseURL != "" && updater.executablePath != "" && updater.updatesDir != ""
}

func (updater *autoUpdater) autoUpdateEnabled() bool {
	if updater.store == nil {
		return true
	}
	state, err := updater.store.readState()
	return err == nil && autoUpdateEnabled(state)
}

func (updater *autoUpdater) setStatus(state, latestVersion, message string, progress int, restartRequired bool) {
	updater.mu.Lock()
	defer updater.mu.Unlock()
	updater.status.State = state
	updater.status.CurrentVersion = updater.currentVersion
	updater.status.LatestVersion = strings.TrimPrefix(latestVersion, "v")
	updater.status.Message = message
	updater.status.Progress = progress
	updater.status.RestartRequired = restartRequired
}

func (updater *autoUpdater) markChecked() {
	updater.mu.Lock()
	updater.status.LastCheckedAt = updater.now().Unix()
	updater.mu.Unlock()
}

func (updater *autoUpdater) checkAndDownload(ctx context.Context, manual bool) {
	if !updater.canCheck() || (!manual && !updater.autoUpdateEnabled()) {
		return
	}
	updater.mu.Lock()
	pending := updater.pending
	updater.mu.Unlock()
	if pending != nil {
		updater.setStatus("ready", pending.Version, fmt.Sprintf("v%s 已下载，退出后台程序后自动安装。", pending.Version), 100, true)
		return
	}
	if updater.Status().State == "downloading" {
		return
	}
	updater.setStatus("checking", updater.Status().LatestVersion, "正在检查 GitHub 最新版本…", 0, false)
	release, notModified, err := updater.fetchLatestRelease(ctx)
	if err != nil {
		updater.markChecked()
		updater.setStatus("error", "", "检查更新失败："+err.Error(), 0, false)
		return
	}
	if notModified {
		updater.markChecked()
		updater.setStatus("up-to-date", updater.Status().LatestVersion, "当前已经是最新版本。", 0, false)
		return
	}
	latestVersion := strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
	comparison, err := compareStableVersions(latestVersion, updater.currentVersion)
	if err != nil {
		updater.markChecked()
		updater.setStatus("error", latestVersion, "Release 版本号无效："+err.Error(), 0, false)
		return
	}
	if comparison <= 0 {
		updater.markChecked()
		updater.setStatus("up-to-date", latestVersion, "当前已经是最新版本。", 0, false)
		return
	}
	asset, err := findReleaseAsset(release, updater.assetName)
	if err != nil {
		updater.markChecked()
		updater.setStatus("error", latestVersion, err.Error(), 0, false)
		return
	}
	if err := ensureUpdateTargetWritable(updater.executablePath); err != nil {
		updater.markChecked()
		updater.setStatus("error", latestVersion, "程序所在目录不可写，无法静默更新："+err.Error(), 0, false)
		return
	}
	updater.setStatus("downloading", latestVersion, fmt.Sprintf("正在静默下载 v%s…", latestVersion), 0, false)
	pending, err = updater.downloadAsset(ctx, latestVersion, asset)
	updater.markChecked()
	if err != nil {
		updater.setStatus("error", latestVersion, "下载更新失败："+err.Error(), 0, false)
		return
	}
	updater.mu.Lock()
	updater.pending = pending
	updater.mu.Unlock()
	updater.setStatus("ready", latestVersion, fmt.Sprintf("v%s 已下载，退出后台程序后自动安装。", latestVersion), 100, true)
	if updater.notifications != nil {
		updater.notifications.Publish(notificationUpdateReady, latestVersion)
	}
}

func (updater *autoUpdater) fetchLatestRelease(ctx context.Context) (githubRelease, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, updater.releaseURL, nil)
	if err != nil {
		return githubRelease{}, false, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	request.Header.Set("User-Agent", "bilibili-live-gift-panel/"+updater.currentVersion)
	updater.mu.Lock()
	etag := updater.etag
	updater.mu.Unlock()
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	response, err := updater.client.Do(request)
	if err != nil {
		return githubRelease{}, false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		return githubRelease{}, true, nil
	}
	if response.StatusCode == http.StatusNotFound {
		return githubRelease{TagName: "v" + updater.currentVersion}, false, nil
	}
	if response.StatusCode != http.StatusOK {
		return githubRelease{}, false, fmt.Errorf("GitHub 返回 HTTP %d", response.StatusCode)
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&release); err != nil {
		return githubRelease{}, false, fmt.Errorf("解析 Release 失败：%w", err)
	}
	if release.Draft || release.Prerelease || strings.TrimSpace(release.TagName) == "" {
		return githubRelease{}, false, errors.New("GitHub 最新正式 Release 无效")
	}
	updater.mu.Lock()
	updater.etag = response.Header.Get("ETag")
	updater.mu.Unlock()
	return release, false, nil
}

func findReleaseAsset(release githubRelease, assetName string) (githubAsset, error) {
	for _, asset := range release.Assets {
		if asset.Name != assetName {
			continue
		}
		if asset.Size <= 0 || asset.Size > updateMaxBytes {
			return githubAsset{}, fmt.Errorf("Release 中的 %s 文件大小无效", assetName)
		}
		if _, err := normalizeSHA256(asset.Digest); err != nil {
			return githubAsset{}, fmt.Errorf("Release 中的 %s 缺少有效 SHA-256", assetName)
		}
		if strings.TrimSpace(asset.DownloadURL) == "" {
			return githubAsset{}, fmt.Errorf("Release 中的 %s 缺少下载地址", assetName)
		}
		return asset, nil
	}
	return githubAsset{}, fmt.Errorf("Release 中没有找到 %s", assetName)
}

func (updater *autoUpdater) downloadAsset(ctx context.Context, version string, asset githubAsset) (*pendingUpdate, error) {
	if err := os.MkdirAll(updater.updatesDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建更新目录失败：%w", err)
	}
	expectedSHA, _ := normalizeSHA256(asset.Digest)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.DownloadURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "bilibili-live-gift-panel/"+updater.currentVersion)
	response, err := updater.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载地址返回 HTTP %d", response.StatusCode)
	}
	temporary, err := os.CreateTemp(updater.updatesDir, "gift-panel-*.download")
	if err != nil {
		return nil, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hasher), io.LimitReader(response.Body, updateMaxBytes+1))
	closeErr := temporary.Close()
	if copyErr != nil {
		return nil, copyErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if written > updateMaxBytes || written != asset.Size {
		return nil, fmt.Errorf("下载大小不符：收到 %d 字节，预期 %d 字节", written, asset.Size)
	}
	actualSHA := hex.EncodeToString(hasher.Sum(nil))
	if actualSHA != expectedSHA {
		return nil, errors.New("SHA-256 校验不通过，已丢弃下载文件")
	}
	pendingPath := filepath.Join(updater.updatesDir, "gift-panel-pending.exe")
	_ = os.Remove(pendingPath)
	if err := os.Rename(temporaryPath, pendingPath); err != nil {
		return nil, fmt.Errorf("保存待安装更新失败：%w", err)
	}
	pending := &pendingUpdate{
		Version:     version,
		SHA256:      actualSHA,
		PendingPath: pendingPath,
		TargetPath:  updater.executablePath,
	}
	if err := updater.writePendingMetadata(*pending); err != nil {
		_ = os.Remove(pendingPath)
		return nil, err
	}
	return pending, nil
}

func (updater *autoUpdater) metadataPath() string {
	return filepath.Join(updater.updatesDir, "pending-update.json")
}

func (updater *autoUpdater) writePendingMetadata(pending pendingUpdate) error {
	data, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := writeFileAtomically(updater.metadataPath(), data); err != nil {
		return fmt.Errorf("保存更新状态失败：%w", err)
	}
	return nil
}

func (updater *autoUpdater) restorePendingUpdate() {
	data, err := os.ReadFile(updater.metadataPath())
	if err != nil {
		return
	}
	var pending pendingUpdate
	if json.Unmarshal(data, &pending) != nil || pending.PendingPath != filepath.Join(updater.updatesDir, "gift-panel-pending.exe") || pending.TargetPath != updater.executablePath {
		updater.cleanupPendingUpdate(pending)
		return
	}
	comparison, versionErr := compareStableVersions(pending.Version, updater.currentVersion)
	if versionErr != nil || comparison <= 0 {
		updater.cleanupPendingUpdate(pending)
		return
	}
	if verifyFileSHA256(pending.PendingPath, pending.SHA256) != nil {
		updater.cleanupPendingUpdate(pending)
		return
	}
	updater.pending = &pending
	updater.status = updateStatus{
		State:           "ready",
		CurrentVersion:  updater.currentVersion,
		LatestVersion:   pending.Version,
		Message:         fmt.Sprintf("v%s 已下载，退出后台程序后自动安装。", pending.Version),
		Progress:        100,
		RestartRequired: true,
	}
}

func (updater *autoUpdater) cleanupPendingUpdate(pending pendingUpdate) {
	knownPendingPath := filepath.Join(updater.updatesDir, "gift-panel-pending.exe")
	if pending.PendingPath == "" || filepath.Clean(pending.PendingPath) == filepath.Clean(knownPendingPath) {
		_ = os.Remove(knownPendingPath)
	} else if filepath.Dir(pending.PendingPath) == filepath.Clean(updater.updatesDir) {
		_ = os.Remove(pending.PendingPath)
	}
	_ = os.Remove(updater.metadataPath())
	if updater.executablePath != "" {
		_ = os.Remove(updater.executablePath + ".old")
		_ = os.Remove(updater.executablePath + ".new")
	}
}

func normalizeSHA256(value string) (string, error) {
	normalized := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(value), "sha256:"))
	if len(normalized) != sha256.Size*2 {
		return "", errors.New("SHA-256 长度无效")
	}
	if _, err := hex.DecodeString(normalized); err != nil {
		return "", errors.New("SHA-256 格式无效")
	}
	return normalized, nil
}

func verifyFileSHA256(path, expected string) error {
	normalized, err := normalizeSHA256(expected)
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, io.LimitReader(file, updateMaxBytes+1)); err != nil {
		return err
	}
	if hex.EncodeToString(hasher.Sum(nil)) != normalized {
		return errors.New("SHA-256 不匹配")
	}
	return nil
}

func ensureUpdateTargetWritable(executablePath string) error {
	directory := filepath.Dir(executablePath)
	probe, err := os.CreateTemp(directory, ".gift-panel-update-*.tmp")
	if err != nil {
		return err
	}
	probePath := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(probePath)
		return err
	}
	return os.Remove(probePath)
}

func compareStableVersions(left, right string) (int, error) {
	leftParts, err := parseStableVersion(left)
	if err != nil {
		return 0, err
	}
	rightParts, err := parseStableVersion(right)
	if err != nil {
		return 0, err
	}
	for index := range leftParts {
		if leftParts[index] > rightParts[index] {
			return 1, nil
		}
		if leftParts[index] < rightParts[index] {
			return -1, nil
		}
	}
	return 0, nil
}

func parseStableVersion(value string) ([3]int, error) {
	normalized := strings.TrimPrefix(strings.TrimSpace(value), "v")
	if normalized == "" || strings.Contains(normalized, "-") {
		return [3]int{}, fmt.Errorf("%q 不是正式版本号", value)
	}
	if metadata := strings.IndexByte(normalized, '+'); metadata >= 0 {
		normalized = normalized[:metadata]
	}
	parts := strings.Split(normalized, ".")
	if len(parts) != 3 {
		return [3]int{}, fmt.Errorf("%q 必须使用 major.minor.patch", value)
	}
	var result [3]int
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return [3]int{}, fmt.Errorf("%q 含有无效数字", value)
		}
		result[index] = number
	}
	return result, nil
}

func runUpdateHelper(args []string) (bool, error) {
	if len(args) == 0 || args[0] != "--apply-update" {
		return false, nil
	}
	if len(args) != 4 || args[1] != "--state" || args[3] == "" {
		return true, errors.New("更新替换参数无效")
	}
	waitPID, err := strconv.Atoi(args[3])
	if err != nil || waitPID <= 0 {
		return true, errors.New("更新等待进程无效")
	}
	data, err := os.ReadFile(args[2])
	if err != nil {
		return true, err
	}
	var pending pendingUpdate
	if err := json.Unmarshal(data, &pending); err != nil {
		return true, err
	}
	return true, applyDownloadedUpdate(pending, waitPID)
}

func startDetachedExecutable(path string, args ...string) error {
	command := exec.Command(path, args...)
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
