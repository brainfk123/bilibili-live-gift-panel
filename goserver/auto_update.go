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
	updateGitHubReleaseURL = "https://github.com/brainfk123/bilibili-live-gift-panel/releases/latest/download/gift-panel-update.json"
	updateReleaseURL       = updateGitHubReleaseURL
	updateAssetName        = "gift-panel-windows-x64.exe"
	updateMaxBytes         = int64(256 << 20)
	updateCheckPeriod      = 6 * time.Hour
	updateSourceTimeout    = 20 * time.Second
	updateChecksumMaxBytes = int64(4096)
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
	SourceName string        `json:"-"`
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

type updateReleaseSource struct {
	Name   string
	URL    string
	GitHub bool
}

type updateReleaseCandidate struct {
	Source  updateReleaseSource
	Release githubRelease
	Version string
}

type autoUpdaterOptions struct {
	Store          *configStore
	Notifications  *notificationCenter
	Client         *http.Client
	CurrentVersion string
	ExecutablePath string
	UpdatesDir     string
	ReleaseURL     string
	ReleaseSources []updateReleaseSource
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
	releaseSources []updateReleaseSource
	assetName      string
	checkPeriod    time.Duration
	now            func() time.Time
	trigger        chan bool

	mu      sync.Mutex
	status  updateStatus
	pending *pendingUpdate
}

func defaultUpdateReleaseSources() []updateReleaseSource {
	return []updateReleaseSource{
		{Name: "GitHub", URL: updateGitHubReleaseURL, GitHub: true},
	}
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
		ReleaseSources: defaultUpdateReleaseSources(),
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
	releaseSources := append([]updateReleaseSource(nil), options.ReleaseSources...)
	if len(releaseSources) == 0 && strings.TrimSpace(options.ReleaseURL) != "" {
		releaseSources = []updateReleaseSource{{Name: "更新源", URL: options.ReleaseURL, GitHub: true}}
	}
	if len(releaseSources) == 0 {
		releaseSources = defaultUpdateReleaseSources()
	}
	updater := &autoUpdater{
		store:          options.Store,
		notifications:  options.Notifications,
		client:         client,
		currentVersion: strings.TrimPrefix(strings.TrimSpace(options.CurrentVersion), "v"),
		executablePath: options.ExecutablePath,
		updatesDir:     options.UpdatesDir,
		releaseSources: releaseSources,
		assetName:      options.AssetName,
		checkPeriod:    period,
		now:            now,
		trigger:        make(chan bool, 1),
	}
	if updater.currentVersion == "" {
		updater.currentVersion = "dev"
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
		updater.status.Message = "开发版本不会检查在线更新。"
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
	updater.setStatus("checking", updater.Status().LatestVersion, "正在检查最新版本…", 0, false)
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
	return isAutoUpdateSupported() && updater.currentVersion != "dev" && len(updater.releaseSources) > 0 && updater.executablePath != "" && updater.updatesDir != ""
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
	updater.setStatus("checking", updater.Status().LatestVersion, "正在检查最新版本…", 0, false)
	var sourceErrors []string
	foundCurrentRelease := false
	latestVersion := ""
	var candidates []updateReleaseCandidate
	for _, source := range updater.releaseSources {
		release, err := updater.fetchReleaseFromSource(ctx, source)
		if err != nil {
			sourceErrors = append(sourceErrors, fmt.Sprintf("%s：%v", source.Name, err))
			continue
		}
		sourceVersion := strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
		comparison, err := compareStableVersions(sourceVersion, updater.currentVersion)
		if err != nil {
			sourceErrors = append(sourceErrors, fmt.Sprintf("%s：Release 版本号无效：%v", source.Name, err))
			continue
		}
		if comparison <= 0 {
			foundCurrentRelease = true
			continue
		}
		if latestVersion == "" {
			latestVersion = sourceVersion
			candidates = []updateReleaseCandidate{{Source: source, Release: release, Version: sourceVersion}}
			continue
		}
		comparison, _ = compareStableVersions(sourceVersion, latestVersion)
		if comparison > 0 {
			latestVersion = sourceVersion
			candidates = []updateReleaseCandidate{{Source: source, Release: release, Version: sourceVersion}}
		} else if comparison == 0 {
			candidates = append(candidates, updateReleaseCandidate{Source: source, Release: release, Version: sourceVersion})
		}
	}
	if len(candidates) == 0 {
		updater.markChecked()
		if foundCurrentRelease {
			updater.setStatus("up-to-date", updater.currentVersion, "当前已经是最新版本。", 0, false)
			return
		}
		updater.setStatus("error", "", "检查更新失败："+strings.Join(sourceErrors, "；"), 0, false)
		return
	}
	for _, candidate := range candidates {
		asset, err := updater.resolveReleaseAsset(ctx, candidate.Release, updater.assetName)
		if err != nil {
			sourceErrors = append(sourceErrors, fmt.Sprintf("%s：%v", candidate.Source.Name, err))
			continue
		}
		if err := ensureUpdateTargetWritable(updater.executablePath); err != nil {
			updater.markChecked()
			updater.setStatus("error", candidate.Version, "程序所在目录不可写，无法静默更新："+err.Error(), 0, false)
			return
		}
		updater.setStatus("downloading", candidate.Version, fmt.Sprintf("正在通过 %s 静默下载 v%s…", candidate.Source.Name, candidate.Version), 0, false)
		pending, err = updater.downloadAsset(ctx, candidate.Version, asset)
		if err != nil {
			sourceErrors = append(sourceErrors, fmt.Sprintf("%s：下载更新失败：%v", candidate.Source.Name, err))
			updater.setStatus("checking", candidate.Version, "当前更新源下载失败，正在尝试备用源…", 0, false)
			continue
		}
		updater.markChecked()
		updater.mu.Lock()
		updater.pending = pending
		updater.mu.Unlock()
		updater.setStatus("ready", candidate.Version, fmt.Sprintf("v%s 已下载，退出后台程序后自动安装。", candidate.Version), 100, true)
		if updater.notifications != nil {
			updater.notifications.Publish(notificationUpdateReady, candidate.Version)
		}
		return
	}
	updater.markChecked()
	updater.setStatus("error", latestVersion, "检查更新失败："+strings.Join(sourceErrors, "；"), 0, false)
}

func (updater *autoUpdater) fetchReleaseFromSource(ctx context.Context, source updateReleaseSource) (githubRelease, error) {
	if strings.TrimSpace(source.URL) == "" {
		return githubRelease{}, errors.New("更新地址为空")
	}
	requestContext, cancel := context.WithTimeout(ctx, updateSourceTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, source.URL, nil)
	if err != nil {
		return githubRelease{}, err
	}
	request.Header.Set("Accept", "application/json")
	if source.GitHub {
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	}
	request.Header.Set("User-Agent", "bilibili-live-gift-panel/"+updater.currentVersion)
	response, err := updater.client.Do(request)
	if err != nil {
		return githubRelease{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return githubRelease{}, errors.New("尚未发布正式版本")
	}
	if response.StatusCode != http.StatusOK {
		return githubRelease{}, fmt.Errorf("返回 HTTP %d", response.StatusCode)
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&release); err != nil {
		return githubRelease{}, fmt.Errorf("解析 Release 失败：%w", err)
	}
	if release.Draft || release.Prerelease || strings.TrimSpace(release.TagName) == "" {
		return githubRelease{}, errors.New("最新正式 Release 无效")
	}
	release.SourceName = source.Name
	return release, nil
}

func findReleaseAsset(release githubRelease, assetName string) (githubAsset, error) {
	for _, asset := range release.Assets {
		if asset.Name != assetName {
			continue
		}
		if asset.Size < 0 || asset.Size > updateMaxBytes {
			return githubAsset{}, fmt.Errorf("Release 中的 %s 文件大小无效", assetName)
		}
		if strings.TrimSpace(asset.Digest) != "" {
			if _, err := normalizeSHA256(asset.Digest); err != nil {
				return githubAsset{}, fmt.Errorf("Release 中的 %s SHA-256 无效", assetName)
			}
		}
		if strings.TrimSpace(asset.DownloadURL) == "" {
			return githubAsset{}, fmt.Errorf("Release 中的 %s 缺少下载地址", assetName)
		}
		return asset, nil
	}
	return githubAsset{}, fmt.Errorf("Release 中没有找到 %s", assetName)
}

func (updater *autoUpdater) resolveReleaseAsset(ctx context.Context, release githubRelease, assetName string) (githubAsset, error) {
	asset, err := findReleaseAsset(release, assetName)
	if err != nil {
		return githubAsset{}, err
	}
	if strings.TrimSpace(asset.Digest) != "" {
		return asset, nil
	}
	checksumAsset, err := findReleaseAsset(release, assetName+".sha256")
	if err != nil {
		return githubAsset{}, fmt.Errorf("Release 中的 %s 缺少 SHA-256，且没有校验文件", assetName)
	}
	digest, err := updater.fetchChecksum(ctx, checksumAsset.DownloadURL)
	if err != nil {
		return githubAsset{}, fmt.Errorf("读取 %s 校验文件失败：%w", assetName, err)
	}
	asset.Digest = "sha256:" + digest
	return asset, nil
}

func (updater *autoUpdater) fetchChecksum(ctx context.Context, downloadURL string) (string, error) {
	requestContext, cancel := context.WithTimeout(ctx, updateSourceTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "bilibili-live-gift-panel/"+updater.currentVersion)
	response, err := updater.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载地址返回 HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, updateChecksumMaxBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > updateChecksumMaxBytes {
		return "", errors.New("校验文件过大")
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "", errors.New("校验文件为空")
	}
	digest, err := normalizeSHA256(fields[0])
	if err != nil {
		return "", err
	}
	return digest, nil
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
	expectedSize := asset.Size
	if expectedSize == 0 && response.ContentLength > 0 {
		expectedSize = response.ContentLength
	}
	if expectedSize > updateMaxBytes {
		return nil, fmt.Errorf("下载文件过大：%d 字节", expectedSize)
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
	if written > updateMaxBytes {
		return nil, fmt.Errorf("下载文件超过 %d 字节限制", updateMaxBytes)
	}
	if expectedSize > 0 && written != expectedSize {
		return nil, fmt.Errorf("下载大小不符：收到 %d 字节，预期 %d 字节", written, expectedSize)
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
