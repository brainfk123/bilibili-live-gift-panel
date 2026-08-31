package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var (
	appVersion                 = "dev"
	appCommit                  = ""
	updateAPIBaseURLHex        = ""
	updateExpectedPublisherHex = ""
)

type updateResultError string

func (err updateResultError) Error() string { return string(err) }

func boundedUpdateResult(err error, fallback string) error {
	if err == nil {
		return nil
	}
	var resultErr updateResultError
	if errors.As(err, &resultErr) {
		return resultErr
	}
	var policyErr *updateTrustPolicyError
	if errors.As(err, &policyErr) {
		return policyErr
	}
	return updateResultError(fallback)
}

func pendingFailureRequiresRedownload(err error) bool {
	if err == nil {
		return false
	}
	switch boundedUpdateResult(err, "").Error() {
	case "pending_policy_context_changed", "pending_verification_invalid", "pending_metadata_invalid", "pending_metadata_unavailable", "pending_enrollment_floor_invalid", "artifact_verification_failed":
		return true
	default:
		return false
	}
}

func logUpdateResult(err error) {
	if err == nil {
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "update_result=%s\n", boundedUpdateResult(err, "update_failed"))
}

func pendingUsesSignedPolicy(pending pendingUpdate) bool {
	return pending.SchemaVersion == pendingUpdateSchemaVersion && pending.Verification.Provenance == pendingVerificationSignedPolicy
}

func logPendingUpdateDiagnostic(pending pendingUpdate, legacyPrefix string, err error, enrollmentCode string) {
	if pending.legacyDiagnosticsApproved && pending.SchemaVersion == pendingUpdateSchemaVersion &&
		(pending.Verification.Provenance == pendingVerificationLegacyMigrated || pending.Verification.Provenance == pendingVerificationLegacyCompatibility) {
		_, _ = fmt.Fprintf(os.Stderr, "%s：%v\n", legacyPrefix, err)
		return
	}
	logUpdateResult(boundedUpdateResult(err, enrollmentCode))
}

const (
	updateGitHubReleaseURL               = "https://github.com/brainfk123/bilibili-live-gift-panel/releases/latest/download/gift-panel-update.json"
	updateGitHubTrustURL                 = "https://raw.githubusercontent.com/brainfk123/bilibili-live-gift-panel/publisher-trust/gift-panel-publisher-policy.json"
	updateReleaseURL                     = updateGitHubReleaseURL
	updateAssetName                      = "gift-panel-windows-x64.exe"
	updateMaxBytes                       = int64(256 << 20)
	updateCheckPeriod                    = 6 * time.Hour
	updateSourceTimeout                  = 20 * time.Second
	updateVerifyNoticeWait               = 300 * time.Millisecond
	updateInstallCountdown               = 3 * time.Second
	updateChecksumMaxBytes               = int64(4096)
	updateInstalledMarker                = "installed-update.json"
	updateCleanupAttempts                = 3
	updateCleanupRetryWait               = 10 * time.Millisecond
	pendingUpdateSchemaVersion           = 2
	pendingUpdateEnrollmentFloorFilename = "pending-update-enrollment-floor.json"
)

var pendingUpdateEnrollmentFloorBytes = []byte("{\"schemaVersion\":2,\"enrollmentRequired\":true}\n")

const (
	pendingVerificationLegacyMigrated      = "legacy-migrated"
	pendingVerificationLegacyCompatibility = "legacy-compatibility"
	pendingVerificationSignedPolicy        = "signed-policy"
)

var (
	errUpdateArtifactCleanup              = errors.New("更新文件清理失败")
	errPendingExecutableCleanup           = errors.New("待安装更新可执行文件清理失败")
	startUpdatedTargetExecutable          = startDetachedExecutable
	applyPendingUpdate                    = applyDownloadedUpdate
	pendingUpdateVerifierForBuild         = defaultPendingUpdateVerifier
	writePendingEnrollmentFloorAtomically = writeFileAtomically
	writePendingMetadataAtomically        = writeFileAtomically
	removeUpdateHelperArtifact            = os.Remove
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
	InstallAt       int64  `json:"installAt,omitempty"`
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
	SchemaVersion             int                       `json:"schemaVersion"`
	Version                   string                    `json:"version"`
	Tag                       string                    `json:"tag,omitempty"`
	Channel                   updateChannel             `json:"channel,omitempty"`
	Size                      int64                     `json:"size"`
	SHA256                    string                    `json:"sha256"`
	PendingPath               string                    `json:"pendingPath"`
	TargetPath                string                    `json:"targetPath"`
	Verification              pendingUpdateVerification `json:"verification"`
	legacyDiagnosticsApproved bool
}

type pendingUpdateVerification struct {
	Provenance      string                `json:"provenance"`
	SourceName      string                `json:"sourceName,omitempty"`
	SourceURLSHA256 string                `json:"sourceUrlSha256,omitempty"`
	SourceGitHub    *bool                 `json:"sourceGitHub,omitempty"`
	Tag             string                `json:"tag,omitempty"`
	Channel         updateChannel         `json:"channel,omitempty"`
	ArtifactSHA256  string                `json:"artifactSha256,omitempty"`
	PolicyEpoch     uint64                `json:"policyEpoch,omitempty"`
	PolicySHA256    string                `json:"policySha256,omitempty"`
	PolicyMode      updateTrustPolicyMode `json:"policyMode,omitempty"`
}

type installedUpdate struct {
	Version string `json:"version"`
}

type updateReleaseSource struct {
	Name           string
	URL            string
	GitHub         bool
	DefaultChannel updateChannel
}

type updateReleaseCandidate struct {
	Source  updateReleaseSource
	Release githubRelease
	Version string
	Channel updateChannel
	Policy  resolvedUpdateTrustPolicy
}

type autoUpdaterOptions struct {
	Store                   *configStore
	Client                  *http.Client
	CurrentVersion          string
	ExecutablePath          string
	UpdatesDir              string
	ReleaseURL              string
	ReleaseSources          []updateReleaseSource
	TrustSources            []updateTrustSource
	TrustStore              *updateTrustStore
	AssetName               string
	CheckPeriod             time.Duration
	Now                     func() time.Time
	VerifyExecutable        func(string) error
	InspectAuthenticode     func(string) (inspectedUpdateCertificate, error)
	LaunchInstaller         func(string, int, bool) error
	RemoveFile              func(string) error
	VerificationNoticeDelay time.Duration
}

type autoUpdater struct {
	store                   *configStore
	client                  *http.Client
	currentVersion          string
	executablePath          string
	updatesDir              string
	releaseSources          []updateReleaseSource
	trustSources            []updateTrustSource
	trustStore              *updateTrustStore
	assetName               string
	checkPeriod             time.Duration
	now                     func() time.Time
	trigger                 chan bool
	onReady                 func(string)
	onInstallNow            func()
	verifyExecutable        func(string) error
	inspectAuthenticode     func(string) (inspectedUpdateCertificate, error)
	launchInstaller         func(string, int, bool) error
	removeFile              func(string) error
	verificationNoticeDelay time.Duration

	mu      sync.Mutex
	status  updateStatus
	pending *pendingUpdate
}

func defaultUpdateReleaseSources() []updateReleaseSource {
	sources := make([]updateReleaseSource, 0, 2)
	if domesticURL := domesticUpdateReleaseURL(); domesticURL != "" {
		sources = append(sources, updateReleaseSource{Name: "国内镜像", URL: domesticURL, DefaultChannel: updateChannelStable})
	}
	return append(sources, updateReleaseSource{Name: "GitHub", URL: updateGitHubReleaseURL, GitHub: true, DefaultChannel: updateChannelStable})
}

func domesticUpdateReleaseURL() string {
	encoded := strings.TrimSpace(updateAPIBaseURLHex)
	if encoded == "" {
		return ""
	}
	decoded, err := hex.DecodeString(encoded)
	if err != nil || !utf8.Valid(decoded) {
		return ""
	}
	rawBaseURL := string(decoded)
	baseURL, err := url.Parse(rawBaseURL)
	if err != nil || baseURL.Scheme != "https" || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.ForceQuery || baseURL.Fragment != "" {
		return ""
	}
	if baseURL.Path != "" && baseURL.Path != "/" {
		return ""
	}
	hostname := baseURL.Hostname()
	if hostname == "" || updateAPIHostnameIsIPLiteral(hostname) {
		return ""
	}
	for _, character := range hostname {
		if character > 127 {
			return ""
		}
	}
	canonicalHostname := strings.ToLower(hostname)
	canonicalHost := canonicalHostname
	if strings.Contains(canonicalHostname, ":") {
		canonicalHost = "[" + canonicalHostname + "]"
	}
	if portText := baseURL.Port(); portText != "" {
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 || port == 443 {
			return ""
		}
		canonicalHost = canonicalHostname + ":" + strconv.Itoa(port)
		if strings.Contains(canonicalHostname, ":") {
			canonicalHost = "[" + canonicalHostname + "]:" + strconv.Itoa(port)
		}
	}
	canonicalOrigin := "https://" + canonicalHost
	if rawBaseURL != canonicalOrigin && rawBaseURL != canonicalOrigin+"/" {
		return ""
	}
	return canonicalOrigin + "/api/v1/releases/latest"
}

func updateAPIHostnameIsIPLiteral(hostname string) bool {
	if strings.Contains(hostname, ":") || net.ParseIP(hostname) != nil {
		return true
	}
	lastLabel := strings.TrimSuffix(hostname, ".")
	if separator := strings.LastIndexByte(lastLabel, '.'); separator >= 0 {
		lastLabel = lastLabel[separator+1:]
	}
	if strings.HasPrefix(strings.ToLower(lastLabel), "0x") {
		lastLabel = lastLabel[2:]
		if lastLabel == "" {
			return false
		}
		for _, character := range lastLabel {
			if !strings.ContainsRune("0123456789abcdefABCDEF", character) {
				return false
			}
		}
		return true
	}
	if lastLabel == "" {
		return false
	}
	for _, character := range lastLabel {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func newDefaultAutoUpdater(store *configStore) *autoUpdater {
	root, rootErr := os.UserConfigDir()
	executablePath, executableErr := os.Executable()
	updatesDir := filepath.Join(root, "BilibiliLiveGiftPanel", "updates")
	trustStore, trustSources, trustErr := defaultEmbeddedUpdateTrust(filepath.Join(updatesDir, "update-trust"), time.Now)
	if strings.TrimSpace(updateAPIBaseURLHex) != "" && domesticUpdateReleaseURL() == "" {
		_, _ = fmt.Fprintln(os.Stderr, "自动更新国内镜像配置无效，已使用 GitHub 回退。")
	}
	updater := newAutoUpdater(autoUpdaterOptions{
		Store:          store,
		Client:         newUpdateHTTPClient(10 * time.Minute),
		CurrentVersion: appVersion,
		ExecutablePath: executablePath,
		UpdatesDir:     updatesDir,
		ReleaseSources: defaultUpdateReleaseSources(),
		TrustSources:   trustSources,
		TrustStore:     trustStore,
		AssetName:      updateAssetName,
		CheckPeriod:    updateCheckPeriod,
		Now:            time.Now,
	})
	if rootErr != nil || executableErr != nil {
		updater.setStatus("error", "", "无法确定自动更新目录或程序路径。", 0, false)
	} else if trustErr != nil {
		updater.setStatus("error", "", "更新信任配置无效，已停止自动更新。", 0, false)
		logUpdateResult(updateResultError("policy_embedded_invalid"))
	}
	return updater
}

func defaultEmbeddedUpdateTrust(cacheDir string, now func() time.Time) (*updateTrustStore, []updateTrustSource, error) {
	if now == nil {
		now = time.Now
	}
	hasRoot := strings.TrimSpace(updateTrustRootSPKIBase64) != ""
	hasPolicy := strings.TrimSpace(updateTrustBootstrapPolicyBase64) != ""
	if !hasRoot && !hasPolicy {
		return nil, nil, nil
	}
	sources := defaultUpdateTrustSources()
	root, policy, err := embeddedUpdateTrust()
	if err != nil {
		return &updateTrustStore{CacheDir: cacheDir, Now: now}, sources, policyError("policy_embedded_invalid")
	}
	if _, err := verifyUpdateTrustPolicyAtAnyExpiry(policy, root); err != nil {
		return &updateTrustStore{Root: root, EmbeddedPolicy: policy, CacheDir: cacheDir, Now: now}, sources, policyError("policy_embedded_invalid")
	}
	return &updateTrustStore{Root: root, EmbeddedPolicy: policy, CacheDir: cacheDir, Now: now}, sources, nil
}

func embeddedUpdateTrustConfigured() bool {
	return strings.TrimSpace(updateTrustRootSPKIBase64) != "" || strings.TrimSpace(updateTrustBootstrapPolicyBase64) != ""
}

func defaultUpdateTrustSources() []updateTrustSource {
	sources := make([]updateTrustSource, 0, 2)
	if domesticURL := domesticUpdateReleaseURL(); domesticURL != "" {
		origin := strings.TrimSuffix(domesticURL, "/api/v1/releases/latest")
		sources = append(sources, updateTrustSource{Name: "国内镜像", URL: origin + "/api/v1/trust/publisher-policy"})
	}
	return append(sources, updateTrustSource{Name: "GitHub", URL: updateGitHubTrustURL})
}

func newAutoUpdater(options autoUpdaterOptions) *autoUpdater {
	client := options.Client
	if client == nil {
		client = newUpdateHTTPClient(10 * time.Minute)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	period := options.CheckPeriod
	if period <= 0 {
		period = updateCheckPeriod
	}
	verifyExecutable := options.VerifyExecutable
	if verifyExecutable == nil {
		verifyExecutable = defaultVerifyUpdateExecutable
	}
	inspectCertificate := options.InspectAuthenticode
	if inspectCertificate == nil {
		inspectCertificate = inspectAuthenticode
	}
	launchInstaller := options.LaunchInstaller
	if launchInstaller == nil {
		launchInstaller = launchUpdateInstaller
	}
	removeFile := options.RemoveFile
	if removeFile == nil {
		removeFile = os.Remove
	}
	verificationNoticeDelay := options.VerificationNoticeDelay
	if verificationNoticeDelay <= 0 {
		verificationNoticeDelay = updateVerifyNoticeWait
	}
	releaseSources := append([]updateReleaseSource(nil), options.ReleaseSources...)
	if len(releaseSources) == 0 && strings.TrimSpace(options.ReleaseURL) != "" {
		releaseSources = []updateReleaseSource{{Name: "更新源", URL: options.ReleaseURL, GitHub: true, DefaultChannel: updateChannelStable}}
	}
	if len(releaseSources) == 0 {
		releaseSources = defaultUpdateReleaseSources()
	}
	trustSources := append([]updateTrustSource(nil), options.TrustSources...)
	trustStore := options.TrustStore
	if trustStore != nil && trustStore.Now == nil {
		trustStore.Now = now
	}
	updater := &autoUpdater{
		store:                   options.Store,
		client:                  client,
		currentVersion:          strings.TrimPrefix(strings.TrimSpace(options.CurrentVersion), "v"),
		executablePath:          options.ExecutablePath,
		updatesDir:              options.UpdatesDir,
		releaseSources:          releaseSources,
		trustSources:            trustSources,
		trustStore:              trustStore,
		assetName:               options.AssetName,
		checkPeriod:             period,
		now:                     now,
		trigger:                 make(chan bool, 1),
		verifyExecutable:        verifyExecutable,
		inspectAuthenticode:     inspectCertificate,
		launchInstaller:         launchInstaller,
		removeFile:              removeFile,
		verificationNoticeDelay: verificationNoticeDelay,
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

type updateUserAgentTransport struct {
	base      http.RoundTripper
	userAgent string
}

func (transport updateUserAgentTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	cloned.Header.Set("User-Agent", transport.userAgent)
	return transport.base.RoundTrip(cloned)
}

func (updater *autoUpdater) resolveUpdateTrustPolicy(ctx context.Context) (resolvedUpdateTrustPolicy, error) {
	return updater.resolveUpdateTrustPolicyFrom(ctx, updater.trustSources)
}

func (updater *autoUpdater) resolveUpdateTrustPolicyFrom(ctx context.Context, sources []updateTrustSource) (resolvedUpdateTrustPolicy, error) {
	if updater.trustStore == nil {
		return resolvedUpdateTrustPolicy{}, policyError("policy_unavailable")
	}
	store := *updater.trustStore
	client := store.Client
	if client == nil {
		client = updater.client
	}
	if client == nil {
		client = newUpdateHTTPClient(maxUpdateTrustSourceWait)
	}
	clientCopy := *client
	base := clientCopy.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clientCopy.Transport = updateUserAgentTransport{base: base, userAgent: "bilibili-live-gift-panel/" + updater.currentVersion}
	store.Client = &clientCopy
	return store.Resolve(ctx, sources...)
}

func decodeExpectedUpdatePublisher(version, encoded string) (string, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		if version == "dev" {
			return "", nil
		}
		return "", errors.New("发布构建缺少预期 Authenticode 发布者")
	}
	decoded, err := hex.DecodeString(encoded)
	if err != nil {
		return "", errors.New("预期 Authenticode 发布者编码无效")
	}
	if !utf8.Valid(decoded) || len(decoded) == 0 {
		return "", errors.New("预期 Authenticode 发布者不是有效 UTF-8")
	}
	return string(decoded), nil
}

func defaultVerifyUpdateExecutable(path string) error {
	expectedPublisher, err := decodeExpectedUpdatePublisher(appVersion, updateExpectedPublisherHex)
	if err != nil {
		return err
	}
	if expectedPublisher == "" {
		return nil
	}
	return verifyAuthenticodePublisher(path, expectedPublisher)
}

func (updater *autoUpdater) Run(ctx context.Context) {
	if updater == nil {
		return
	}
	if updater.canCheck() {
		status := updater.Status()
		if status.State == "ready" && updater.HasPending() {
			updater.notifyReady(status.LatestVersion)
		} else if updater.autoUpdateEnabled() && status.State != "ready" {
			updater.checkAndDownload(ctx, false)
		}
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
	case updater.trigger <- false:
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
	status := updater.Status()
	if status.State == "checking" || status.State == "downloading" || status.State == "verifying" || status.State == "ready" || status.State == "installing" {
		return status
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
	if !status.AutoUpdate && status.State != "ready" && status.State != "downloading" && status.State != "verifying" && status.State != "installing" && status.State != "checking" && updater.canCheck() {
		status.State = "disabled"
		status.Message = "自动更新已关闭，仍可手动检查。"
	}
	return status
}

func (updater *autoUpdater) SetOnReady(onReady func(string)) {
	if updater == nil {
		return
	}
	updater.onReady = onReady
}

func (updater *autoUpdater) SetOnInstallNow(onInstallNow func()) {
	if updater == nil {
		return
	}
	updater.onInstallNow = onInstallNow
}

func (updater *autoUpdater) HasPending() bool {
	if updater == nil {
		return false
	}
	updater.mu.Lock()
	defer updater.mu.Unlock()
	return updater.pending != nil
}

func (updater *autoUpdater) InstallOnExit(restart bool) error {
	if updater == nil || !isAutoUpdateSupported() {
		return nil
	}
	updater.mu.Lock()
	pending := updater.pending
	updater.mu.Unlock()
	if pending == nil {
		return nil
	}
	normalized, migrated, metadataErr := normalizePendingUpdateMetadata(*pending)
	if metadataErr == nil && migrated {
		metadataErr = updater.writePendingMetadata(normalized)
	}
	if metadataErr != nil {
		resultErr := boundedUpdateResult(metadataErr, "pending_verification_invalid")
		logUpdateResult(resultErr)
		if cleanupErr := updater.cleanupPendingUpdate(*pending); cleanupErr != nil {
			logUpdateResult(updateResultError("artifact_cleanup_failed"))
			return updateResultError("artifact_cleanup_failed")
		}
		updater.mu.Lock()
		updater.pending = nil
		updater.mu.Unlock()
		return resultErr
	}
	if migrated {
		pending = &normalized
		updater.mu.Lock()
		updater.pending = pending
		updater.mu.Unlock()
	}
	if pending.Verification.Provenance == pendingVerificationLegacyMigrated || pending.Verification.Provenance == pendingVerificationLegacyCompatibility {
		if updater.trustStore != nil {
			if err := ensurePendingUpdateEnrollmentFloor(updater.metadataPath()); err != nil {
				logUpdateResult(err)
				return errors.New("待安装更新验证上下文无效")
			}
		}
		pending.legacyDiagnosticsApproved = true
	}
	// This revalidation narrows accidental replacement and ordinary tampering windows.
	// A malicious process running as the same user can still race path-based checks;
	// defending that boundary requires a handle-based installer protocol and is outside
	// the updater's current threat model.
	verifyExecutable, verificationErr := updater.pendingUpdateVerifier(context.Background(), *pending, updater.trustSources)
	if verificationErr == nil {
		verificationErr = verifyPendingExecutable(*pending, verifyExecutable)
	}
	if verificationErr != nil {
		logPendingUpdateDiagnostic(*pending, "待安装更新执行前安全校验失败", verificationErr, "artifact_verification_failed")
		cleanupErr := updater.cleanupPendingUpdate(*pending)
		if !errors.Is(cleanupErr, errPendingExecutableCleanup) {
			updater.mu.Lock()
			updater.pending = nil
			updater.mu.Unlock()
		}
		if cleanupErr != nil {
			logPendingUpdateDiagnostic(*pending, "待安装更新拒绝后清理失败", cleanupErr, "artifact_cleanup_failed")
			updater.setStatus("error", pending.Version, "待安装更新清理失败，已拒绝执行。", 0, false)
			return errors.New("待安装更新清理失败，已拒绝执行")
		}
		updater.setStatus("error", pending.Version, "待安装更新安全校验失败，已拒绝执行。", 0, false)
		return errors.New("待安装更新安全校验失败，已拒绝执行")
	}
	if err := updater.preparePendingInstall(); err != nil {
		logPendingUpdateDiagnostic(*pending, "启动更新替换器前残留文件清理失败", err, "artifact_cleanup_failed")
		updater.mu.Lock()
		updater.pending = nil
		updater.mu.Unlock()
		updater.setStatus("error", pending.Version, "待安装更新清理失败，已拒绝执行。", 0, false)
		return errors.New("待安装更新清理失败，已拒绝执行")
	}
	metadataPath := updater.metadataPath()
	if err := updater.launchInstaller(metadataPath, os.Getpid(), restart); err != nil {
		resultErr := boundedUpdateResult(err, "installer_launch_failed")
		if pendingFailureRequiresRedownload(resultErr) {
			cleanupErr := updater.cleanupPendingUpdate(*pending)
			if cleanupErr != nil {
				logPendingUpdateDiagnostic(*pending, "更新验证上下文变化后的清理诊断", cleanupErr, "artifact_cleanup_failed")
				updater.setStatus("error", pending.Version, "更新验证上下文已变化，待安装文件清理失败。", 0, false)
				return updateResultError("artifact_cleanup_failed")
			}
			updater.mu.Lock()
			updater.pending = nil
			updater.mu.Unlock()
			updater.setStatus("error", pending.Version, "更新验证上下文已变化，需要重新下载。", 0, false)
			logUpdateResult(resultErr)
			return resultErr
		}
		logPendingUpdateDiagnostic(*pending, "启动更新替换器诊断", err, "installer_launch_failed")
		return errors.New("启动更新替换器失败")
	}
	return nil
}

func (updater *autoUpdater) preparePendingInstall() error {
	if updater.executablePath == "" {
		return errors.New("更新目标路径为空")
	}
	var cleanupErr error
	for _, path := range []string{updater.executablePath + ".old", updater.executablePath + ".new"} {
		cleanupErr = errors.Join(cleanupErr, updater.removeUpdateArtifact(path))
	}
	return cleanupErr
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

func (updater *autoUpdater) handleInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
		return
	}
	updater.mu.Lock()
	pending := updater.pending != nil
	install := updater.onInstallNow
	updater.mu.Unlock()
	if !pending || install == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"code": -1, "message": "当前没有等待安装的更新"})
		return
	}
	updater.setStatus("installing", updater.Status().LatestVersion, "正在安装更新，本地服务将短暂重启…", 100, true)
	install()
	writeJSON(w, http.StatusAccepted, map[string]any{"code": 0})
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

func (updater *autoUpdater) automaticCheckDue() bool {
	status := updater.Status()
	return status.LastCheckedAt == 0 || updater.now().Sub(time.Unix(status.LastCheckedAt, 0)) >= updater.checkPeriod
}

func (updater *autoUpdater) notifyReady(version string) {
	if updater.onReady != nil {
		updater.onReady(version)
	}
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
	updater.status.InstallAt = 0
}

func (updater *autoUpdater) setReadyStatus(version string) {
	updater.setStatus("ready", version, "校验完成，3 秒后自动安装。", 100, true)
	updater.mu.Lock()
	updater.status.InstallAt = updater.now().Add(updateInstallCountdown).UnixMilli()
	updater.mu.Unlock()
}

func (updater *autoUpdater) showVerificationIfDownloading() {
	updater.mu.Lock()
	defer updater.mu.Unlock()
	if updater.status.State == "downloading" {
		updater.status.State = "verifying"
		updater.status.Message = "正在校验更新文件…"
		updater.status.Progress = 99
	}
}

func (updater *autoUpdater) setDownloadProgress(received, total int64) {
	if total <= 0 || received <= 0 {
		return
	}
	progress := int(received * 100 / total)
	if progress > 99 {
		progress = 99
	}
	updater.mu.Lock()
	defer updater.mu.Unlock()
	if updater.status.State == "downloading" && progress > updater.status.Progress {
		updater.status.Progress = progress
	}
}

type updateProgressReader struct {
	reader   io.Reader
	total    int64
	received int64
	report   func(int64, int64)
}

func (reader *updateProgressReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	if count > 0 {
		reader.received += int64(count)
		reader.report(reader.received, reader.total)
	}
	return count, err
}

func (updater *autoUpdater) markChecked() {
	updater.mu.Lock()
	updater.status.LastCheckedAt = updater.now().Unix()
	updater.mu.Unlock()
}

func (updater *autoUpdater) checkAndDownload(ctx context.Context, manual bool) error {
	if !updater.canCheck() {
		return nil
	}
	if !manual && (!updater.autoUpdateEnabled() || !updater.automaticCheckDue()) {
		return nil
	}
	updater.mu.Lock()
	pending := updater.pending
	updater.mu.Unlock()
	if pending != nil {
		updater.setReadyStatus(pending.Version)
		updater.notifyReady(pending.Version)
		return nil
	}
	if updater.Status().State == "downloading" {
		return nil
	}
	updater.setStatus("checking", updater.Status().LatestVersion, "正在检查最新版本…", 0, false)
	var sourceErrors []string
	foundCurrentRelease := false
	latestVersion := ""
	var candidates []updateReleaseCandidate
	var lastResultErr error
	for _, source := range updater.releaseSources {
		release, channel, err := updater.fetchReleaseCandidateFromSource(ctx, source)
		if err != nil {
			resultErr := boundedUpdateResult(err, "release_unavailable")
			lastResultErr = resultErr
			sourceErrors = append(sourceErrors, fmt.Sprintf("%s：%s", source.Name, resultErr))
			continue
		}
		sourceVersion := strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
		comparison, err := compareStableVersions(sourceVersion, updater.currentVersion)
		if err != nil {
			sourceErrors = append(sourceErrors, fmt.Sprintf("%s：Release 版本号无效：%v", source.Name, err))
			lastResultErr = updateResultError("release_version_invalid")
			continue
		}
		if comparison <= 0 {
			foundCurrentRelease = true
			continue
		}
		if latestVersion == "" {
			latestVersion = sourceVersion
			candidates = []updateReleaseCandidate{{Source: source, Release: release, Version: sourceVersion, Channel: channel}}
			continue
		}
		comparison, _ = compareStableVersions(sourceVersion, latestVersion)
		if comparison > 0 {
			latestVersion = sourceVersion
			candidates = []updateReleaseCandidate{{Source: source, Release: release, Version: sourceVersion, Channel: channel}}
		} else if comparison == 0 {
			candidates = append(candidates, updateReleaseCandidate{Source: source, Release: release, Version: sourceVersion, Channel: channel})
		}
	}
	if len(candidates) == 0 {
		updater.markChecked()
		if foundCurrentRelease {
			updater.setStatus("up-to-date", updater.currentVersion, "当前已经是最新版本。", 0, false)
			return nil
		}
		updater.setStatus("error", "", "检查更新失败："+strings.Join(sourceErrors, "；"), 0, false)
		if lastResultErr != nil {
			return lastResultErr
		}
		return updateResultError("release_unavailable")
	}
	var resolvedPolicy resolvedUpdateTrustPolicy
	if updater.trustStore != nil {
		var err error
		resolvedPolicy, err = updater.resolveUpdateTrustPolicy(ctx)
		if err != nil {
			resultErr := boundedUpdateResult(err, "policy_unavailable")
			updater.markChecked()
			updater.setStatus("error", latestVersion, "更新信任策略不可用（"+resultErr.Error()+"）。", 0, false)
			logUpdateResult(resultErr)
			return resultErr
		}
		for index := range candidates {
			candidates[index].Policy = resolvedPolicy
		}
	}
	for _, candidate := range candidates {
		asset, err := updater.resolveReleaseAsset(ctx, candidate.Release, updater.assetName)
		if err != nil {
			sourceErrors = append(sourceErrors, fmt.Sprintf("%s：%v", candidate.Source.Name, err))
			lastResultErr = boundedUpdateResult(err, "asset_metadata_invalid")
			continue
		}
		if err := ensureUpdateTargetWritable(updater.executablePath); err != nil {
			updater.markChecked()
			resultErr := updateResultError("update_target_unavailable")
			updater.setStatus("error", candidate.Version, "程序所在目录不可写，无法静默更新。", 0, false)
			logUpdateResult(resultErr)
			return resultErr
		}
		updater.setStatus("downloading", candidate.Version, fmt.Sprintf("正在通过 %s 静默下载 v%s…", candidate.Source.Name, candidate.Version), 0, false)
		if updater.trustStore == nil {
			pending, err = updater.downloadAsset(ctx, candidate.Version, asset)
		} else {
			pending, err = updater.downloadCandidate(ctx, candidate, asset)
		}
		if err != nil {
			resultErr := boundedUpdateResult(err, "download_failed")
			lastResultErr = resultErr
			if errors.Is(err, errUpdateArtifactCleanup) {
				updater.markChecked()
				updater.setStatus("error", candidate.Version, "更新文件清理失败，已停止安装。", 0, false)
				logUpdateResult(updateResultError("artifact_cleanup_failed"))
				return updateResultError("artifact_cleanup_failed")
			}
			failureMessage := "下载更新失败"
			if resultErr.Error() == "artifact_verification_failed" || resultErr.Error() == "authenticode_invalid" || resultErr.Error() == "authenticode_unavailable" || resultErr.Error() == "publisher_not_authorized" {
				failureMessage = "更新文件安全校验失败"
			}
			sourceErrors = append(sourceErrors, fmt.Sprintf("%s：%s（%s）", candidate.Source.Name, failureMessage, resultErr))
			logUpdateResult(resultErr)
			updater.setStatus("checking", candidate.Version, "当前更新源下载失败，正在尝试备用源…", 0, false)
			continue
		}
		updater.markChecked()
		updater.mu.Lock()
		updater.pending = pending
		updater.mu.Unlock()
		updater.setReadyStatus(candidate.Version)
		updater.notifyReady(candidate.Version)
		logUpdateResult(updateResultError("ready"))
		return nil
	}
	updater.markChecked()
	updater.setStatus("error", latestVersion, "检查更新失败："+strings.Join(sourceErrors, "；"), 0, false)
	if lastResultErr != nil {
		return lastResultErr
	}
	return updateResultError("update_failed")
}

func (updater *autoUpdater) fetchReleaseFromSource(ctx context.Context, source updateReleaseSource) (githubRelease, error) {
	release, _, err := updater.fetchReleaseCandidateFromSource(ctx, source)
	return release, err
}

func (updater *autoUpdater) fetchReleaseCandidateFromSource(ctx context.Context, source updateReleaseSource) (githubRelease, updateChannel, error) {
	if strings.TrimSpace(source.URL) == "" {
		return githubRelease{}, "", errors.New("更新地址为空")
	}
	requestContext, cancel := context.WithTimeout(ctx, updateSourceTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, source.URL, nil)
	if err != nil {
		return githubRelease{}, "", errors.New("更新地址无效")
	}
	request.Header.Set("Accept", "application/json")
	if source.GitHub {
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	}
	request.Header.Set("User-Agent", "bilibili-live-gift-panel/"+updater.currentVersion)
	response, err := updater.client.Do(request)
	if err != nil {
		return githubRelease{}, "", safeUpdateNetworkError(err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return githubRelease{}, "", errors.New("尚未发布正式版本")
	}
	if response.StatusCode != http.StatusOK {
		return githubRelease{}, "", fmt.Errorf("返回 HTTP %d", response.StatusCode)
	}
	channel, err := releaseChannelFromResponse(source, response)
	if err != nil {
		return githubRelease{}, "", err
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&release); err != nil {
		return githubRelease{}, "", fmt.Errorf("解析 Release 失败：%w", err)
	}
	if release.Draft || release.Prerelease || strings.TrimSpace(release.TagName) == "" {
		return githubRelease{}, "", errors.New("最新正式 Release 无效")
	}
	release.SourceName = source.Name
	return release, channel, nil
}

func releaseChannelFromResponse(source updateReleaseSource, response *http.Response) (updateChannel, error) {
	if source.GitHub {
		return updateChannelStable, nil
	}
	values := response.Header.Values("X-Gift-Panel-Update-Channel")
	if len(values) != 1 {
		return "", updateResultError("update_channel_invalid")
	}
	channel := updateChannel(values[0])
	if channel != updateChannelStable && channel != updateChannelLegacyRushRush {
		return "", updateResultError("update_channel_invalid")
	}
	return channel, nil
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
	if asset.Size <= 0 {
		return githubAsset{}, fmt.Errorf("Release 中的 %s 文件大小无效", assetName)
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
		return "", errors.New("校验地址无效")
	}
	request.Header.Set("User-Agent", "bilibili-live-gift-panel/"+updater.currentVersion)
	response, err := updater.client.Do(request)
	if err != nil {
		return "", safeUpdateNetworkError(err)
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

type updateArtifactVerifier func(string, string) error

func (updater *autoUpdater) downloadAsset(ctx context.Context, version string, asset githubAsset) (*pendingUpdate, error) {
	return updater.downloadAssetVerified(ctx, version, asset, pendingUpdateVerification{Provenance: pendingVerificationLegacyCompatibility}, func(path, _ string) error {
		return updater.verifyExecutable(path)
	})
}

func (updater *autoUpdater) downloadCandidate(ctx context.Context, candidate updateReleaseCandidate, asset githubAsset) (*pendingUpdate, error) {
	verification, err := pendingVerificationForCandidate(candidate, strings.TrimPrefix(strings.ToLower(asset.Digest), "sha256:"), candidate.Policy)
	if err != nil {
		return nil, err
	}
	return updater.downloadAssetVerified(ctx, candidate.Version, asset, verification, func(path, sha256Hex string) error {
		return verifyUpdateArtifactWithInspector(path, candidate, sha256Hex, candidate.Policy, updater.inspectAuthenticode)
	})
}

func verifyUpdateArtifact(path string, candidate updateReleaseCandidate, sha256Hex string, policy resolvedUpdateTrustPolicy) error {
	return verifyUpdateArtifactWithInspector(path, candidate, sha256Hex, policy, inspectAuthenticode)
}

func verifyUpdateArtifactWithInspector(path string, candidate updateReleaseCandidate, sha256Hex string, policy resolvedUpdateTrustPolicy, inspect func(string) (inspectedUpdateCertificate, error)) error {
	if inspect == nil {
		return updateResultError("authenticode_unavailable")
	}
	certificate, err := inspect(path)
	if err != nil {
		return updateResultError("authenticode_invalid")
	}
	return policy.Authorize(updateArtifactIdentity{
		Tag: candidate.Release.TagName, Channel: candidate.Channel,
		SHA256: sha256Hex, Certificate: certificate.LegalIdentity,
	})
}

func pendingVerificationForCandidate(candidate updateReleaseCandidate, artifactSHA string, policy resolvedUpdateTrustPolicy) (pendingUpdateVerification, error) {
	artifactSHA, err := normalizeSHA256(artifactSHA)
	if err != nil || policy.Policy.Epoch == 0 || len(policy.Policy.SignedRaw) == 0 ||
		(policy.Mode != updateTrustModeCurrent && policy.Mode != updateTrustModeExpiredIdentityFallback) ||
		strings.TrimSpace(candidate.Source.Name) == "" || strings.TrimSpace(candidate.Source.URL) == "" {
		return pendingUpdateVerification{}, updateResultError("pending_verification_invalid")
	}
	policyDigest := sha256.Sum256(policy.Policy.SignedRaw)
	sourceDigest := sha256.Sum256([]byte(candidate.Source.URL))
	github := candidate.Source.GitHub
	verification := pendingUpdateVerification{
		Provenance: pendingVerificationSignedPolicy,
		SourceName: candidate.Source.Name, SourceURLSHA256: hex.EncodeToString(sourceDigest[:]), SourceGitHub: &github,
		Tag: candidate.Release.TagName, Channel: candidate.Channel, ArtifactSHA256: artifactSHA,
		PolicyEpoch: policy.Policy.Epoch, PolicySHA256: hex.EncodeToString(policyDigest[:]), PolicyMode: policy.Mode,
	}
	pending := pendingUpdate{Version: candidate.Version, SHA256: artifactSHA, Verification: verification}
	if err := validatePendingUpdateVerification(pending); err != nil {
		return pendingUpdateVerification{}, err
	}
	return verification, nil
}

func verifyPendingResolvedPolicyContext(verification pendingUpdateVerification, policy resolvedUpdateTrustPolicy) error {
	if verification.Provenance != pendingVerificationSignedPolicy || policy.Policy.Epoch == 0 || len(policy.Policy.SignedRaw) == 0 {
		return updateResultError("pending_policy_context_changed")
	}
	digest := sha256.Sum256(policy.Policy.SignedRaw)
	if verification.PolicyEpoch != policy.Policy.Epoch || verification.PolicySHA256 != hex.EncodeToString(digest[:]) || verification.PolicyMode != policy.Mode {
		return updateResultError("pending_policy_context_changed")
	}
	return nil
}

func (updater *autoUpdater) downloadAssetVerified(ctx context.Context, version string, asset githubAsset, verification pendingUpdateVerification, verify updateArtifactVerifier) (_ *pendingUpdate, resultErr error) {
	if err := os.MkdirAll(updater.updatesDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建更新目录失败：%w", err)
	}
	if asset.Size <= 0 {
		return nil, errors.New("更新文件缺少有效大小")
	}
	expectedSHA, err := normalizeSHA256(asset.Digest)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.DownloadURL, nil)
	if err != nil {
		return nil, errors.New("下载地址无效")
	}
	request.Header.Set("User-Agent", "bilibili-live-gift-panel/"+updater.currentVersion)
	response, err := updater.client.Do(request)
	if err != nil {
		return nil, safeUpdateNetworkError(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载地址返回 HTTP %d", response.StatusCode)
	}
	expectedSize := asset.Size
	if expectedSize > updateMaxBytes {
		return nil, fmt.Errorf("下载文件过大：%d 字节", expectedSize)
	}
	temporary, err := os.CreateTemp(updater.updatesDir, "gift-panel-*.download")
	if err != nil {
		return nil, err
	}
	temporaryPath := temporary.Name()
	temporaryNeedsCleanup := true
	defer func() {
		if !temporaryNeedsCleanup {
			return
		}
		if err := updater.removeUpdateArtifact(temporaryPath); err != nil {
			logUpdateResult(updateResultError("artifact_cleanup_failed"))
			resultErr = errors.Join(resultErr, err)
		}
	}()
	limited := io.LimitReader(response.Body, updateMaxBytes+1)
	progressReader := &updateProgressReader{reader: limited, total: expectedSize, report: updater.setDownloadProgress}
	written, copyErr := io.Copy(temporary, progressReader)
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
	if written != expectedSize {
		return nil, fmt.Errorf("下载大小不符：收到 %d 字节，预期 %d 字节", written, expectedSize)
	}
	verificationTimer := time.AfterFunc(updater.verificationNoticeDelay, updater.showVerificationIfDownloading)
	defer verificationTimer.Stop()
	computedSHA, err := verifiedFileSHA256(temporaryPath, expectedSHA)
	if err != nil {
		return nil, fmt.Errorf("SHA-256 校验不通过，已丢弃下载文件：%w", err)
	}
	if verify == nil {
		return nil, updateResultError("artifact_verifier_unavailable")
	}
	if err := verify(temporaryPath, computedSHA); err != nil {
		return nil, boundedUpdateResult(err, "artifact_verification_failed")
	}
	pendingPath := filepath.Join(updater.updatesDir, "gift-panel-pending.exe")
	if err := updater.removeUpdateArtifact(pendingPath); err != nil {
		return nil, err
	}
	if err := os.Rename(temporaryPath, pendingPath); err != nil {
		return nil, fmt.Errorf("保存待安装更新失败：%w", err)
	}
	temporaryNeedsCleanup = false
	pending := &pendingUpdate{
		SchemaVersion: pendingUpdateSchemaVersion,
		Version:       version,
		Size:          expectedSize,
		SHA256:        expectedSHA,
		PendingPath:   pendingPath,
		TargetPath:    updater.executablePath,
		Verification:  verification,
	}
	if err := verifyPendingExecutable(*pending, func(path string) error { return verify(path, computedSHA) }); err != nil {
		verificationErr := boundedUpdateResult(err, "artifact_verification_failed")
		if cleanupErr := updater.removeUpdateArtifact(pendingPath); cleanupErr != nil {
			return nil, errors.Join(verificationErr, cleanupErr)
		}
		return nil, verificationErr
	}
	if err := updater.writePendingMetadata(*pending); err != nil {
		if cleanupErr := updater.removeUpdateArtifact(pendingPath); cleanupErr != nil {
			return nil, errors.Join(err, cleanupErr)
		}
		return nil, err
	}
	return pending, nil
}

func (updater *autoUpdater) removeUpdateArtifact(path string) error {
	return removeUpdateArtifactWith(updater.removeFile, path)
}

func removeUpdateArtifactWith(removeFile func(string) error, path string) error {
	if path == "" {
		return nil
	}
	var lastErr error
	for attempt := 0; attempt < updateCleanupAttempts; attempt++ {
		err := removeFile(path)
		if err == nil || os.IsNotExist(err) {
			return nil
		}
		lastErr = err
		if attempt+1 < updateCleanupAttempts {
			time.Sleep(updateCleanupRetryWait)
		}
	}
	return fmt.Errorf("%w：%v", errUpdateArtifactCleanup, lastErr)
}

func verifyPendingExecutable(pending pendingUpdate, verifyExecutable func(string) error) error {
	if pending.Size <= 0 {
		return errors.New("待安装更新缺少有效大小")
	}
	info, err := os.Stat(pending.PendingPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("待安装更新不是普通文件")
	}
	if info.Size() != pending.Size {
		return fmt.Errorf("待安装更新大小不符：收到 %d 字节，预期 %d 字节", info.Size(), pending.Size)
	}
	if err := verifyFileSHA256(pending.PendingPath, pending.SHA256); err != nil {
		return err
	}
	if verifyExecutable == nil {
		return errors.New("待安装更新缺少签名验证器")
	}
	return verifyExecutable(pending.PendingPath)
}

func (updater *autoUpdater) pendingUpdateVerifier(ctx context.Context, pending pendingUpdate, sources []updateTrustSource) (func(string) error, error) {
	if pending.SchemaVersion != pendingUpdateSchemaVersion {
		return nil, updateResultError("pending_verification_invalid")
	}
	if err := validatePendingUpdateVerification(pending); err != nil {
		return nil, err
	}
	if pending.Verification.Provenance == pendingVerificationLegacyMigrated || pending.Verification.Provenance == pendingVerificationLegacyCompatibility {
		return updater.verifyExecutable, nil
	}
	verification := pending.Verification
	if updater.trustStore == nil || !updater.pendingSourceMatches(verification) {
		return nil, updateResultError("pending_policy_context_changed")
	}
	policy, err := updater.resolveUpdateTrustPolicyFrom(ctx, sources)
	if err != nil {
		return nil, err
	}
	if err := verifyPendingResolvedPolicyContext(verification, policy); err != nil {
		return nil, err
	}
	candidate := updateReleaseCandidate{
		Source:  updateReleaseSource{Name: verification.SourceName, GitHub: *verification.SourceGitHub},
		Release: githubRelease{TagName: verification.Tag}, Version: strings.TrimPrefix(pending.Version, "v"), Channel: verification.Channel,
	}
	return func(path string) error {
		return verifyUpdateArtifactWithInspector(path, candidate, verification.ArtifactSHA256, policy, updater.inspectAuthenticode)
	}, nil
}

func (updater *autoUpdater) pendingSourceMatches(verification pendingUpdateVerification) bool {
	if len(updater.releaseSources) == 0 {
		return true
	}
	for _, source := range updater.releaseSources {
		digest := sha256.Sum256([]byte(source.URL))
		if source.Name == verification.SourceName && source.GitHub == *verification.SourceGitHub && hex.EncodeToString(digest[:]) == verification.SourceURLSHA256 {
			return true
		}
	}
	return false
}

func (updater *autoUpdater) metadataPath() string {
	return filepath.Join(updater.updatesDir, "pending-update.json")
}

func (updater *autoUpdater) installedMarkerPath() string {
	return filepath.Join(updater.updatesDir, updateInstalledMarker)
}

func (updater *autoUpdater) ConsumeInstalledVersion() string {
	if updater == nil || updater.updatesDir == "" {
		return ""
	}
	data, err := os.ReadFile(updater.installedMarkerPath())
	if err != nil {
		return ""
	}
	_ = os.Remove(updater.installedMarkerPath())
	var installed installedUpdate
	if json.Unmarshal(data, &installed) != nil {
		return ""
	}
	version := strings.TrimPrefix(strings.TrimSpace(installed.Version), "v")
	comparison, err := compareStableVersions(version, updater.currentVersion)
	if err != nil || comparison != 0 {
		return ""
	}
	updater.cleanupInstalledUpdateArtifacts()
	return version
}

func (updater *autoUpdater) cleanupInstalledUpdateArtifacts() {
	paths := []string{
		filepath.Join(updater.updatesDir, "gift-panel-pending.exe"),
		updater.metadataPath(),
	}
	if updater.executablePath != "" {
		paths = append(paths, updater.executablePath+".old", updater.executablePath+".new")
	}
	go func() {
		for attempt := 0; attempt < 20; attempt++ {
			remaining := false
			for _, path := range paths {
				if path == "" {
					continue
				}
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					remaining = true
				}
			}
			if !remaining {
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
	}()
}

func writeInstalledUpdateMarker(metadataPath, version string) error {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if _, err := parseStableVersion(version); err != nil {
		return err
	}
	data, err := json.MarshalIndent(installedUpdate{Version: version}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	markerPath := filepath.Join(filepath.Dir(metadataPath), updateInstalledMarker)
	if err := writeFileAtomically(markerPath, data); err != nil {
		return fmt.Errorf("保存更新完成状态失败：%w", err)
	}
	return nil
}

func (updater *autoUpdater) writePendingMetadata(pending pendingUpdate) error {
	var err error
	pending, migrated, err := normalizePendingUpdateMetadata(pending)
	if err != nil {
		return err
	}
	requiresFloor := pendingUsesSignedPolicy(pending) || updater.trustStore != nil && (migrated || pending.Verification.Provenance == pendingVerificationLegacyMigrated || pending.Verification.Provenance == pendingVerificationLegacyCompatibility)
	if requiresFloor {
		if err := ensurePendingUpdateEnrollmentFloor(updater.metadataPath()); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := writePendingMetadataAtomically(updater.metadataPath(), data); err != nil {
		return updateResultError("pending_metadata_write_failed")
	}
	return nil
}

func decodePendingUpdateMetadata(data []byte) (pendingUpdate, bool, error) {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var pending pendingUpdate
	if err := decoder.Decode(&pending); err != nil {
		return pendingUpdate{}, false, updateResultError("pending_metadata_invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return pendingUpdate{}, false, updateResultError("pending_metadata_invalid")
	}
	return normalizePendingUpdateMetadata(pending)
}

func pendingUpdateEnrollmentFloorPath(metadataPath string) string {
	return filepath.Join(filepath.Dir(metadataPath), pendingUpdateEnrollmentFloorFilename)
}

func readPendingUpdateEnrollmentFloor(metadataPath string) (bool, error) {
	data, err := os.ReadFile(pendingUpdateEnrollmentFloorPath(metadataPath))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || string(data) != string(pendingUpdateEnrollmentFloorBytes) {
		return false, updateResultError("pending_enrollment_floor_invalid")
	}
	return true, nil
}

func ensurePendingUpdateEnrollmentFloor(metadataPath string) error {
	exists, err := readPendingUpdateEnrollmentFloor(metadataPath)
	if err != nil || exists {
		return err
	}
	if err := writePendingEnrollmentFloorAtomically(pendingUpdateEnrollmentFloorPath(metadataPath), pendingUpdateEnrollmentFloorBytes); err != nil {
		return updateResultError("pending_enrollment_floor_write_failed")
	}
	exists, err = readPendingUpdateEnrollmentFloor(metadataPath)
	if err != nil || !exists {
		return updateResultError("pending_enrollment_floor_write_failed")
	}
	return nil
}

func readPendingUpdateMetadata(path string, enrollmentRequired bool) (pendingUpdate, error) {
	floorExists, err := readPendingUpdateEnrollmentFloor(path)
	if err != nil {
		return pendingUpdate{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return pendingUpdate{}, updateResultError("pending_metadata_unavailable")
	}
	var schema struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		return pendingUpdate{}, updateResultError("pending_metadata_invalid")
	}
	if schema.SchemaVersion == 0 && floorExists {
		return pendingUpdate{}, updateResultError("pending_verification_invalid")
	}
	pending, migrated, err := decodePendingUpdateMetadata(data)
	if err != nil {
		return pendingUpdate{}, err
	}
	requiresFloor := enrollmentRequired || pendingUsesSignedPolicy(pending)
	if requiresFloor && !floorExists {
		if err := ensurePendingUpdateEnrollmentFloor(path); err != nil {
			return pendingUpdate{}, err
		}
		floorExists = true
	}
	if migrated {
		encoded, err := json.MarshalIndent(pending, "", "  ")
		if err != nil {
			return pendingUpdate{}, updateResultError("pending_metadata_invalid")
		}
		encoded = append(encoded, '\n')
		if enrollmentRequired && !floorExists {
			return pendingUpdate{}, updateResultError("pending_enrollment_floor_write_failed")
		}
		if err := writePendingMetadataAtomically(path, encoded); err != nil {
			return pendingUpdate{}, updateResultError("pending_metadata_migration_failed")
		}
	}
	if pending.Verification.Provenance == pendingVerificationLegacyMigrated || pending.Verification.Provenance == pendingVerificationLegacyCompatibility {
		pending.legacyDiagnosticsApproved = true
	}
	return pending, nil
}

func normalizePendingUpdateMetadata(pending pendingUpdate) (pendingUpdate, bool, error) {
	if pending.SchemaVersion == 0 {
		if pending.Tag != "" || pending.Channel != "" || pending.Verification.Provenance != "" {
			return pendingUpdate{}, false, updateResultError("pending_verification_invalid")
		}
		pending.SchemaVersion = pendingUpdateSchemaVersion
		pending.Verification = pendingUpdateVerification{Provenance: pendingVerificationLegacyMigrated}
		return pending, true, nil
	}
	if pending.SchemaVersion != pendingUpdateSchemaVersion {
		return pendingUpdate{}, false, updateResultError("pending_verification_invalid")
	}
	if err := validatePendingUpdateVerification(pending); err != nil {
		return pendingUpdate{}, false, err
	}
	return pending, false, nil
}

func validatePendingUpdateVerification(pending pendingUpdate) error {
	if pending.Tag != "" || pending.Channel != "" {
		return updateResultError("pending_verification_invalid")
	}
	verification := pending.Verification
	switch verification.Provenance {
	case pendingVerificationLegacyMigrated, pendingVerificationLegacyCompatibility:
		if verification.SourceName != "" || verification.SourceURLSHA256 != "" || verification.SourceGitHub != nil || verification.Tag != "" || verification.Channel != "" ||
			verification.ArtifactSHA256 != "" || verification.PolicyEpoch != 0 || verification.PolicySHA256 != "" || verification.PolicyMode != "" {
			return updateResultError("pending_verification_invalid")
		}
	case pendingVerificationSignedPolicy:
		if strings.TrimSpace(verification.SourceName) == "" || verification.SourceName != strings.TrimSpace(verification.SourceName) ||
			!sha256Hex.MatchString(verification.SourceURLSHA256) || verification.SourceGitHub == nil || !canonicalPolicyTag.MatchString(verification.Tag) ||
			(verification.Channel != updateChannelStable && verification.Channel != updateChannelLegacyRushRush) ||
			!sha256Hex.MatchString(verification.ArtifactSHA256) || verification.ArtifactSHA256 != strings.ToLower(strings.TrimSpace(pending.SHA256)) ||
			verification.PolicyEpoch == 0 || !sha256Hex.MatchString(verification.PolicySHA256) ||
			(verification.PolicyMode != updateTrustModeCurrent && verification.PolicyMode != updateTrustModeExpiredIdentityFallback) ||
			strings.TrimPrefix(verification.Tag, "v") != strings.TrimPrefix(pending.Version, "v") {
			return updateResultError("pending_verification_invalid")
		}
	default:
		return updateResultError("pending_verification_invalid")
	}
	return nil
}

func (updater *autoUpdater) restorePendingUpdate() {
	if _, err := os.Stat(updater.metadataPath()); errors.Is(err, os.ErrNotExist) {
		return
	}
	pending, decodeErr := readPendingUpdateMetadata(updater.metadataPath(), updater.trustStore != nil)
	if decodeErr != nil || pending.PendingPath != filepath.Join(updater.updatesDir, "gift-panel-pending.exe") || pending.TargetPath != updater.executablePath {
		logPendingUpdateDiagnostic(pending, "恢复待安装更新元数据诊断", decodeErr, "pending_metadata_invalid")
		updater.cleanupRestoredPending(pending)
		return
	}
	comparison, versionErr := compareStableVersions(pending.Version, updater.currentVersion)
	if versionErr != nil || comparison <= 0 {
		updater.cleanupRestoredPending(pending)
		return
	}
	verifier, verificationErr := updater.pendingUpdateVerifier(context.Background(), pending, nil)
	if verificationErr == nil {
		verificationErr = verifyPendingExecutable(pending, verifier)
	}
	if verificationErr != nil {
		logPendingUpdateDiagnostic(pending, "恢复待安装更新安全校验诊断", verificationErr, "artifact_verification_failed")
		if cleanupErr := updater.cleanupPendingUpdate(pending); cleanupErr != nil {
			logUpdateResult(updateResultError("artifact_cleanup_failed"))
			updater.setStatus("error", pending.Version, "待安装更新清理失败，已拒绝执行。", 0, false)
		} else {
			updater.setStatus("error", pending.Version, "待安装更新安全校验失败，已拒绝执行。", 0, false)
		}
		return
	}
	updater.pending = &pending
	updater.status = updateStatus{
		State:           "ready",
		CurrentVersion:  updater.currentVersion,
		LatestVersion:   strings.TrimPrefix(pending.Version, "v"),
		Message:         "校验完成，3 秒后自动安装。",
		Progress:        100,
		RestartRequired: true,
		InstallAt:       updater.now().Add(updateInstallCountdown).UnixMilli(),
	}
}

func (updater *autoUpdater) cleanupRestoredPending(pending pendingUpdate) {
	if err := updater.cleanupPendingUpdate(pending); err != nil {
		logPendingUpdateDiagnostic(pending, "恢复待安装更新时清理失败", err, "artifact_cleanup_failed")
		updater.setStatus("error", pending.Version, "待安装更新清理失败，已拒绝执行。", 0, false)
	}
}

func (updater *autoUpdater) cleanupPendingUpdate(pending pendingUpdate) error {
	knownPendingPath := filepath.Join(updater.updatesDir, "gift-panel-pending.exe")
	pendingPath := ""
	if pending.PendingPath == "" || filepath.Clean(pending.PendingPath) == filepath.Clean(knownPendingPath) {
		pendingPath = knownPendingPath
	} else if filepath.Dir(pending.PendingPath) == filepath.Clean(updater.updatesDir) {
		pendingPath = pending.PendingPath
	}
	if pendingPath != "" {
		if err := updater.removeUpdateArtifact(pendingPath); err != nil {
			return errors.Join(errPendingExecutableCleanup, err)
		}
	}
	paths := []string{updater.metadataPath()}
	if updater.executablePath != "" {
		paths = append(paths, updater.executablePath+".old", updater.executablePath+".new")
	}
	var cleanupErr error
	for _, path := range paths {
		cleanupErr = errors.Join(cleanupErr, updater.removeUpdateArtifact(path))
	}
	return cleanupErr
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
	_, err := verifiedFileSHA256(path, expected)
	return err
}

func verifiedFileSHA256(path, expected string) (string, error) {
	normalized, err := normalizeSHA256(expected)
	if err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, io.LimitReader(file, updateMaxBytes+1)); err != nil {
		return "", err
	}
	computed := hex.EncodeToString(hasher.Sum(nil))
	if computed != normalized {
		return "", errors.New("SHA-256 不匹配")
	}
	return computed, nil
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
	if (len(args) != 4 && len(args) != 5) || args[1] != "--state" || args[3] == "" {
		return true, errors.New("更新替换参数无效")
	}
	restart := len(args) == 5 && args[4] == "--restart"
	if len(args) == 5 && !restart {
		return true, errors.New("更新重启参数无效")
	}
	waitPID, err := strconv.Atoi(args[3])
	if err != nil || waitPID <= 0 {
		return true, errors.New("更新等待进程无效")
	}
	pending, err := readPendingUpdateMetadata(args[2], embeddedUpdateTrustConfigured())
	if err != nil {
		logUpdateResult(boundedUpdateResult(err, "pending_metadata_invalid"))
		if err.Error() == "pending_metadata_unavailable" {
			return true, errors.New("读取更新状态失败")
		}
		return true, errors.New("解析更新状态失败")
	}
	if err := applyPendingUpdate(pending, waitPID); err != nil {
		resultErr := boundedUpdateResult(err, "update_apply_failed")
		if pendingFailureRequiresRedownload(resultErr) {
			cleanupErr := cleanupDefinitiveUpdateHelper(args[2], pending)
			logPendingUpdateDiagnostic(pending, "应用待安装更新诊断", resultErr, resultErr.Error())
			if cleanupErr != nil {
				logPendingUpdateDiagnostic(pending, "确定性更新失败后的清理诊断", cleanupErr, "artifact_cleanup_failed")
			}
			return true, resultErr
		}
		logPendingUpdateDiagnostic(pending, "应用待安装更新诊断", err, "update_apply_failed")
		return true, errors.New("应用待安装更新失败")
	}
	if err := writeInstalledUpdateMarker(args[2], pending.Version); err != nil {
		logPendingUpdateDiagnostic(pending, "记录已安装更新诊断", err, "installed_marker_failed")
		return true, errors.New("记录已安装更新失败")
	}
	if err := os.Remove(args[2]); err != nil && !errors.Is(err, os.ErrNotExist) {
		logPendingUpdateDiagnostic(pending, "清理更新状态诊断", err, "pending_metadata_cleanup_failed")
	}
	if restart {
		if err := startVerifiedUpdatedExecutable(pending); err != nil {
			logPendingUpdateDiagnostic(pending, "重新启动更新后的程序诊断", err, "restart_failed")
			return true, errors.New("重新启动更新后的程序失败")
		}
	}
	return true, nil
}

func cleanupDefinitiveUpdateHelper(metadataPath string, pending pendingUpdate) error {
	var cleanupErr error
	for _, path := range []string{metadataPath, pending.TargetPath + ".new", pending.PendingPath} {
		cleanupErr = errors.Join(cleanupErr, removeUpdateArtifactWith(removeUpdateHelperArtifact, path))
	}
	return cleanupErr
}

func startVerifiedUpdatedExecutable(pending pendingUpdate) error {
	verifier, err := pendingUpdateVerifierForBuild(pending)
	if err != nil {
		logUpdateResult(boundedUpdateResult(err, "artifact_verification_failed"))
		return errors.New("更新后程序安全校验失败")
	}
	target := pending
	target.PendingPath = pending.TargetPath
	if err := verifyPendingExecutable(target, verifier); err != nil {
		logUpdateResult(boundedUpdateResult(err, "artifact_verification_failed"))
		return errors.New("更新后程序安全校验失败")
	}
	if err := startUpdatedTargetExecutable(pending.TargetPath); err != nil {
		logPendingUpdateDiagnostic(pending, "启动更新后程序诊断", err, "restart_launch_failed")
		return errors.New("启动更新后程序失败")
	}
	return nil
}

func defaultPendingUpdateVerifier(pending pendingUpdate) (func(string) error, error) {
	normalized, _, err := normalizePendingUpdateMetadata(pending)
	if err != nil {
		return nil, err
	}
	pending = normalized
	if pending.Verification.Provenance == pendingVerificationLegacyMigrated || pending.Verification.Provenance == pendingVerificationLegacyCompatibility {
		return defaultVerifyUpdateExecutable, nil
	}
	cacheDir := filepath.Join(filepath.Dir(pending.PendingPath), "update-trust")
	store, _, err := defaultEmbeddedUpdateTrust(cacheDir, time.Now)
	if err != nil || store == nil {
		return nil, policyError("policy_embedded_invalid")
	}
	updater := &autoUpdater{
		currentVersion:      strings.TrimPrefix(pending.Version, "v"),
		client:              newUpdateHTTPClient(maxUpdateTrustSourceWait),
		trustStore:          store,
		releaseSources:      defaultUpdateReleaseSources(),
		verifyExecutable:    defaultVerifyUpdateExecutable,
		inspectAuthenticode: inspectAuthenticode,
	}
	return updater.pendingUpdateVerifier(context.Background(), pending, nil)
}

func startDetachedExecutable(path string, args ...string) error {
	command := exec.Command(path, args...)
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
