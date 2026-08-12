package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	giftClipReadyTTL       = 30 * time.Minute
	giftClipTerminalTTL    = 5 * time.Minute
	maxGiftClipLayerPixels = 16_777_216
)

type giftClipJobState string

const (
	giftClipJobQueued    giftClipJobState = "queued"
	giftClipJobEncoding  giftClipJobState = "encoding"
	giftClipJobRetrying  giftClipJobState = "retrying"
	giftClipJobReady     giftClipJobState = "ready"
	giftClipJobFailed    giftClipJobState = "failed"
	giftClipJobCancelled giftClipJobState = "cancelled"
)

type giftClipJobSnapshot struct {
	ID       string
	State    giftClipJobState
	Progress float64
	Message  string
	Width    int
	Height   int
	FPS      int
}

type giftClipJob struct {
	id         string
	receiptID  string
	crop       giftClipCrop
	state      giftClipJobState
	progress   float64
	message    string
	dir        string
	outputPath string
	ctx        context.Context
	cancel     context.CancelFunc
	finishedAt time.Time
	width      int
	height     int
	fps        int
	mode       giftClipEncoderMode
	active     bool
	released   bool
}

type giftClipOwnedDirectory struct {
	id       string
	path     string
	rootInfo os.FileInfo
	dirInfo  os.FileInfo
}

type giftClipJobPhase string

const (
	giftClipJobPhaseResolve giftClipJobPhase = "resolve"
	giftClipJobPhaseProfile giftClipJobPhase = "profile"
	giftClipJobPhaseEncode  giftClipJobPhase = "encode"
)

type giftClipJobFailure struct {
	phase     giftClipJobPhase
	exitClass string
	message   string
	cause     error
}

func (failure giftClipJobFailure) Error() string { return failure.message }
func (failure giftClipJobFailure) Unwrap() error { return failure.cause }

type giftClipEncoderModeSource interface {
	initialMode() giftClipEncoderMode
}

type giftClipUserError struct {
	message string
	cause   error
}

func (err *giftClipUserError) Error() string { return err.message }
func (err *giftClipUserError) Unwrap() error { return err.cause }

type giftClipJobManager struct {
	root     string
	resolver giftClipSourceResolver
	encoder  giftClipEncoder
	logger   *diagnosticLogger

	mu            sync.Mutex
	fsMu          sync.Mutex
	jobs          map[string]*giftClipJob
	queue         []string
	owned         map[string]giftClipOwnedDirectory
	now           func() time.Time
	writeLayer    func(context.Context, string, string, []byte) (string, error)
	removeTaskDir func(giftClipOwnedDirectory) error
	beforeEncode  func(string)
	rootInfo      os.FileInfo
	childCount    int
	closed        bool
	wake          chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	closeOnce     sync.Once
}

func defaultGiftClipTaskRoot() string {
	root := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if root == "" {
		root, _ = os.UserCacheDir()
	}
	if root == "" {
		return ""
	}
	return filepath.Join(root, "BilibiliLiveGiftPanel", "gift-clip", "tasks")
}

func newGiftClipJobManager(root string, resolver giftClipSourceResolver, encoder giftClipEncoder, logger *diagnosticLogger) *giftClipJobManager {
	ctx, cancel := context.WithCancel(context.Background())
	manager := &giftClipJobManager{
		root: strings.TrimSpace(root), resolver: resolver, encoder: encoder, logger: logger,
		jobs: make(map[string]*giftClipJob), owned: make(map[string]giftClipOwnedDirectory), now: time.Now,
		writeLayer: writeGiftClipSourceContext,
		wake:       make(chan struct{}, 1), ctx: ctx, cancel: cancel,
	}
	manager.removeTaskDir = manager.removeOwnedDirectory
	manager.wg.Add(2)
	go manager.runWorker()
	go manager.runSweeper()
	return manager
}

func (manager *giftClipJobManager) Create(requestCtx context.Context, receiptID string, crop giftClipCrop, background, overlay []byte) (giftClipJobSnapshot, error) {
	if manager == nil || manager.resolver == nil || manager.encoder == nil {
		return giftClipJobSnapshot{}, errors.New("视频导出任务不可用。")
	}
	if requestCtx == nil {
		return giftClipJobSnapshot{}, errors.New("导出请求上下文无效。")
	}
	if err := requestCtx.Err(); err != nil {
		return giftClipJobSnapshot{}, err
	}
	if strings.TrimSpace(receiptID) == "" {
		return giftClipJobSnapshot{}, errors.New("送礼记录不存在。")
	}
	if err := validateGiftClipJobLayer(background, crop, true); err != nil {
		return giftClipJobSnapshot{}, err
	}
	if err := validateGiftClipJobLayer(overlay, crop, false); err != nil {
		return giftClipJobSnapshot{}, err
	}

	owned, err := manager.makeTaskDir()
	if err != nil {
		return giftClipJobSnapshot{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = manager.removeOwnedDirectory(owned)
		}
	}()
	if _, err := manager.writeLayer(requestCtx, owned.path, "background.png", background); err != nil {
		return giftClipJobSnapshot{}, giftClipCreateFailure(err)
	}
	if _, err := manager.writeLayer(requestCtx, owned.path, "overlay.png", overlay); err != nil {
		return giftClipJobSnapshot{}, giftClipCreateFailure(err)
	}
	if err := requestCtx.Err(); err != nil {
		return giftClipJobSnapshot{}, err
	}

	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return giftClipJobSnapshot{}, errors.New("视频导出任务已关闭。")
	}
	jobCtx, jobCancel := context.WithCancel(manager.ctx)
	job := &giftClipJob{
		id: owned.id, receiptID: receiptID, crop: crop, state: giftClipJobQueued, message: "等待导出。",
		dir: owned.path, outputPath: filepath.Join(owned.path, "clip.mp4"), cancel: jobCancel,
		ctx: jobCtx, width: crop.Width, height: crop.Height, fps: giftClipFPS,
		mode: initialGiftClipJobEncoderMode(manager.encoder),
	}
	manager.childCount++
	manager.jobs[owned.id] = job
	manager.owned[owned.id] = owned
	manager.queue = append(manager.queue, owned.id)
	snapshot := job.snapshot()
	manager.mu.Unlock()
	committed = true
	manager.signal()
	return snapshot, nil
}

func (manager *giftClipJobManager) Snapshot(id string) (giftClipJobSnapshot, bool) {
	if manager == nil {
		return giftClipJobSnapshot{}, false
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	job, ok := manager.jobs[id]
	if !ok {
		return giftClipJobSnapshot{}, false
	}
	return job.snapshot(), true
}

func (manager *giftClipJobManager) VideoPath(id string) (string, bool) {
	if manager == nil {
		return "", false
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	job, ok := manager.jobs[id]
	owned, ownedOK := manager.owned[id]
	if !ok || job.state != giftClipJobReady || !ownedOK || owned.path != job.dir {
		return "", false
	}
	return job.outputPath, true
}

func (manager *giftClipJobManager) Cancel(id string) bool {
	if manager == nil {
		return false
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	job, ok := manager.jobs[id]
	if !ok {
		return false
	}
	if !isGiftClipJobTerminal(job.state) {
		job.state = giftClipJobCancelled
		job.message = "任务已取消。"
		job.cancel()
		if !job.active {
			job.finishedAt = manager.now()
			manager.releaseJobLocked(job)
		}
	}
	return true
}

func (manager *giftClipJobManager) Sweep() {
	if manager == nil {
		return
	}
	now := manager.now()
	manager.mu.Lock()
	for id, job := range manager.jobs {
		if !isGiftClipJobTerminal(job.state) || job.finishedAt.IsZero() || now.Sub(job.finishedAt) < giftClipJobTTL(job.state) {
			continue
		}
		owned, ok := manager.owned[id]
		if !ok || owned.path != job.dir {
			continue
		}
		if err := manager.removeTaskDir(owned); err != nil {
			manager.logCleanupFailure(id)
			continue
		}
		delete(manager.jobs, id)
		delete(manager.owned, id)
	}
	manager.mu.Unlock()
}

func (manager *giftClipJobManager) Close() {
	if manager == nil {
		return
	}
	manager.closeOnce.Do(func() {
		manager.mu.Lock()
		manager.closed = true
		manager.queue = nil
		for _, job := range manager.jobs {
			if job.state == giftClipJobQueued {
				job.state = giftClipJobCancelled
				job.message = "任务已取消。"
				job.finishedAt = manager.now()
				job.cancel()
				manager.releaseJobLocked(job)
				continue
			}
			if job.active && !isGiftClipJobTerminal(job.state) {
				job.state = giftClipJobCancelled
				job.message = "任务已取消。"
			}
			job.cancel()
		}
		manager.mu.Unlock()
		manager.cancel()
		manager.wg.Wait()

		manager.mu.Lock()
		for id, owned := range manager.owned {
			if err := manager.removeTaskDir(owned); err != nil {
				manager.logCleanupFailure(id)
				continue
			}
			delete(manager.jobs, id)
			delete(manager.owned, id)
		}
		manager.mu.Unlock()
	})
}

func (manager *giftClipJobManager) runWorker() {
	defer manager.wg.Done()
	for {
		if manager.ctx.Err() != nil {
			return
		}
		id, ok := manager.nextJob()
		if !ok {
			select {
			case <-manager.ctx.Done():
				return
			case <-manager.wake:
			}
			continue
		}
		if manager.beforeEncode != nil {
			manager.beforeEncode(id)
		}
		manager.encode(id)
	}
}

func (manager *giftClipJobManager) nextJob() (string, bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed || manager.ctx.Err() != nil {
		return "", false
	}
	for len(manager.queue) > 0 {
		id := manager.queue[0]
		manager.queue = manager.queue[1:]
		job, ok := manager.jobs[id]
		if !ok || job.state == giftClipJobCancelled {
			continue
		}
		job.state = giftClipJobEncoding
		job.message = "正在导出视频。"
		job.active = true
		return id, true
	}
	return "", false
}

func (manager *giftClipJobManager) encode(id string) {
	manager.mu.Lock()
	job, ok := manager.jobs[id]
	if !ok {
		manager.mu.Unlock()
		return
	}
	if job.state == giftClipJobCancelled {
		job.active = false
		job.finishedAt = manager.now()
		manager.releaseJobLocked(job)
		manager.mu.Unlock()
		return
	}
	dir, receiptID, crop, outputPath := job.dir, job.receiptID, job.crop, job.outputPath
	ctx := job.ctx
	manager.mu.Unlock()

	phase := giftClipJobPhaseResolve
	err := ctx.Err()
	var source giftClipSource
	if err == nil {
		source, err = manager.resolver.Resolve(ctx, receiptID, dir)
	}
	if err == nil {
		phase = giftClipJobPhaseProfile
		var profile giftClipOutputProfile
		if err = ctx.Err(); err == nil {
			profile, err = newGiftClipOutputProfile(crop, source.VisualWidth, source.VisualHeight, source.Duration)
		}
		if err == nil {
			manager.mu.Lock()
			if current, exists := manager.jobs[id]; exists && current.state != giftClipJobCancelled {
				current.width, current.height, current.fps = profile.Width, profile.Height, profile.FPS
			}
			manager.mu.Unlock()
			phase = giftClipJobPhaseEncode
			if err = ctx.Err(); err == nil {
				err = manager.encoder.Encode(ctx, giftClipEncodeRequest{
					Source: source, Crop: crop, Profile: profile,
					BackgroundPath: filepath.Join(dir, "background.png"), OverlayPath: filepath.Join(dir, "overlay.png"), OutputPath: outputPath,
				}, func(update giftClipEncodingUpdate) { manager.updateEncoding(id, update) })
			}
		}
	}
	manager.finishEncoding(id, phase, err)
}

func (manager *giftClipJobManager) updateEncoding(id string, update giftClipEncodingUpdate) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	job, ok := manager.jobs[id]
	if !ok || job.state == giftClipJobCancelled || isGiftClipJobTerminal(job.state) {
		return
	}
	job.mode = update.Mode
	if update.Retrying {
		job.state = giftClipJobRetrying
		job.progress = 0
		job.message = "已切换兼容编码模式。"
		return
	}
	job.state = giftClipJobEncoding
	job.message = "正在导出视频。"
	if update.Progress > job.progress {
		if update.Progress > 1 {
			update.Progress = 1
		}
		job.progress = update.Progress
	}
}

func (manager *giftClipJobManager) finishEncoding(id string, phase giftClipJobPhase, err error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	job, ok := manager.jobs[id]
	if !ok {
		return
	}
	job.active = false
	if job.state == giftClipJobCancelled || errors.Is(err, context.Canceled) || job.ctx.Err() != nil || manager.ctx.Err() != nil {
		job.state, job.message = giftClipJobCancelled, "任务已取消。"
	} else if err == nil {
		job.state, job.progress, job.message = giftClipJobReady, 1, "视频已生成。"
	} else {
		failure := classifyGiftClipJobFailure(phase, err)
		job.state, job.message = giftClipJobFailed, failure.message
		if manager.logger != nil {
			mode := "none"
			if phase == giftClipJobPhaseEncode {
				mode = string(job.mode)
			}
			manager.logger.Error("gift_clip_job_failed", "task_id", id, "phase", failure.phase, "mode", mode, "exit_class", failure.exitClass)
		}
	}
	job.finishedAt = manager.now()
	manager.releaseJobLocked(job)
}

func (manager *giftClipJobManager) runSweeper() {
	defer manager.wg.Done()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-manager.ctx.Done():
			return
		case <-ticker.C:
			manager.Sweep()
		}
	}
}

func (manager *giftClipJobManager) signal() {
	select {
	case manager.wake <- struct{}{}:
	default:
	}
}

func (manager *giftClipJobManager) makeTaskDir() (giftClipOwnedDirectory, error) {
	root, err := filepath.Abs(manager.root)
	if err != nil || strings.TrimSpace(manager.root) == "" {
		return giftClipOwnedDirectory{}, errors.New("导出任务目录无效。")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return giftClipOwnedDirectory{}, errors.New("创建导出任务目录失败。")
	}
	if err := validateGiftClipDirectorySegments(root); err != nil {
		return giftClipOwnedDirectory{}, errors.New("导出任务目录无效。")
	}
	info, err := os.Lstat(root)
	if err != nil || !safeGiftClipDirectoryInfo(info) {
		return giftClipOwnedDirectory{}, errors.New("导出任务目录无效。")
	}
	manager.fsMu.Lock()
	defer manager.fsMu.Unlock()
	rootFile, openErr := os.Open(root)
	if openErr != nil {
		return giftClipOwnedDirectory{}, errors.New("导出任务目录无效。")
	}
	handleInfo, handleErr := rootFile.Stat()
	_ = rootFile.Close()
	if handleErr != nil || !sameGiftClipFile(info, handleInfo) {
		return giftClipOwnedDirectory{}, errors.New("导出任务目录无效。")
	}
	if manager.rootInfo == nil {
		manager.rootInfo = info
	} else if !sameGiftClipFile(info, manager.rootInfo) {
		return giftClipOwnedDirectory{}, errors.New("导出任务目录无效。")
	}
	for attempts := 0; attempts < 8; attempts++ {
		id, err := newGiftClipJobID()
		if err != nil {
			return giftClipOwnedDirectory{}, errors.New("创建导出任务失败。")
		}
		directory := filepath.Join(root, id)
		if filepath.Dir(directory) != root {
			continue
		}
		if err := os.Mkdir(directory, 0o700); err == nil {
			dirInfo, statErr := os.Lstat(directory)
			if statErr != nil || !safeGiftClipDirectoryInfo(dirInfo) {
				return giftClipOwnedDirectory{}, errors.New("创建导出任务目录失败。")
			}
			dirFile, openErr := os.Open(directory)
			if openErr != nil {
				return giftClipOwnedDirectory{}, errors.New("创建导出任务目录失败。")
			}
			handleInfo, handleErr := dirFile.Stat()
			_ = dirFile.Close()
			if handleErr != nil || !sameGiftClipFile(dirInfo, handleInfo) {
				return giftClipOwnedDirectory{}, errors.New("创建导出任务目录失败。")
			}
			return giftClipOwnedDirectory{id: id, path: directory, rootInfo: manager.rootInfo, dirInfo: dirInfo}, nil
		} else if !errors.Is(err, os.ErrExist) {
			return giftClipOwnedDirectory{}, errors.New("创建导出任务目录失败。")
		}
	}
	return giftClipOwnedDirectory{}, errors.New("创建导出任务失败。")
}

func newGiftClipJobID() (string, error) {
	bytes := make([]byte, 18)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func validateGiftClipJobLayer(data []byte, crop giftClipCrop, requireOpaque bool) error {
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || validateGiftClipLayerDimensions(config.Width, config.Height, crop) != nil {
		return errors.New("导出图层 PNG 尺寸无效。")
	}
	if !requireOpaque {
		return nil
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return errors.New("导出背景 PNG 无效。")
	}
	for y := 0; y < decoded.Bounds().Dy(); y++ {
		for x := 0; x < decoded.Bounds().Dx(); x++ {
			_, _, _, alpha := decoded.At(decoded.Bounds().Min.X+x, decoded.Bounds().Min.Y+y).RGBA()
			if alpha != 0xffff {
				return errors.New("导出背景 PNG 必须完全不透明。")
			}
		}
	}
	return nil
}

func validateGiftClipLayerDimensions(width, height int, crop giftClipCrop) error {
	if width < 1 || height < 1 || int64(width)*int64(height) > maxGiftClipLayerPixels || width != crop.Width || height != crop.Height {
		return errors.New("导出图层 PNG 尺寸无效。")
	}
	return nil
}

func giftClipCreateFailure(err error) error {
	if isGiftClipDiskFull(err) {
		return &giftClipUserError{message: "磁盘空间不足，无法生成视频。", cause: err}
	}
	return &giftClipUserError{message: "视频生成失败，请重试。", cause: err}
}

func classifyGiftClipJobFailure(phase giftClipJobPhase, err error) giftClipJobFailure {
	failure := giftClipJobFailure{phase: phase, cause: err}
	if isGiftClipDiskFull(err) {
		failure.exitClass, failure.message = "disk_full", "磁盘空间不足，无法生成视频。"
		return failure
	}
	if errors.Is(err, errGiftClipPayloadIntegrity) {
		failure.exitClass, failure.message = "payload_integrity", "视频编码组件校验失败，请重启程序后重试。"
		return failure
	}
	switch phase {
	case giftClipJobPhaseResolve:
		failure.exitClass, failure.message = "source_error", "礼物动画素材无法解码，请重试或更换素材。"
	case giftClipJobPhaseProfile:
		failure.exitClass, failure.message = "invalid_profile", "视频尺寸无效，宽高必须为 64–4096 范围内的偶数像素。"
	default:
		failure.exitClass, failure.message = "encoder_error", "视频生成失败，请重试。"
	}
	return failure
}

func isGiftClipDiskFull(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || strings.Contains(strings.ToLower(fmt.Sprint(err)), "no space left on device") || strings.Contains(strings.ToLower(fmt.Sprint(err)), "disk full") || strings.Contains(strings.ToLower(fmt.Sprint(err)), "not enough space")
}

func initialGiftClipJobEncoderMode(encoder giftClipEncoder) giftClipEncoderMode {
	if source, ok := encoder.(giftClipEncoderModeSource); ok {
		return source.initialMode()
	}
	return giftClipDefaultEncoderMode
}

func (manager *giftClipJobManager) releaseJobLocked(job *giftClipJob) {
	if job.released {
		return
	}
	job.released = true
	job.cancel()
	manager.childCount--
}

func (manager *giftClipJobManager) activeChildCount() int {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.childCount
}

func (manager *giftClipJobManager) logCleanupFailure(id string) {
	if manager.logger != nil {
		manager.logger.Error("gift_clip_job_cleanup_failed", "task_id", id, "phase", "cleanup", "mode", "none", "exit_class", "filesystem_error")
	}
}

func (manager *giftClipJobManager) removeOwnedDirectory(owned giftClipOwnedDirectory) error {
	manager.fsMu.Lock()
	defer manager.fsMu.Unlock()
	absRoot, err := filepath.Abs(manager.root)
	if err != nil {
		return err
	}
	absDirectory, err := filepath.Abs(owned.path)
	if err != nil || filepath.Dir(absDirectory) != absRoot || filepath.Base(absDirectory) != owned.id {
		return errors.New("unsafe task directory")
	}
	rootPathInfo, err := os.Lstat(absRoot)
	if err != nil || !safeGiftClipDirectoryInfo(rootPathInfo) || !sameGiftClipFile(rootPathInfo, owned.rootInfo) {
		return errors.New("task root identity changed")
	}
	if err := validateGiftClipDirectorySegments(absRoot); err != nil {
		return errors.New("task root path is unsafe")
	}
	rootFile, err := os.Open(absRoot)
	if err != nil {
		return errors.New("task root handle unavailable")
	}
	rootHandleInfo, err := rootFile.Stat()
	_ = rootFile.Close()
	if err != nil || !sameGiftClipFile(rootHandleInfo, owned.rootInfo) {
		return errors.New("task root handle identity changed")
	}
	dirPathInfo, err := os.Lstat(absDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !safeGiftClipDirectoryInfo(dirPathInfo) || !sameGiftClipFile(dirPathInfo, owned.dirInfo) {
		return errors.New("task directory identity changed")
	}
	dirFile, err := os.Open(absDirectory)
	if err != nil {
		return errors.New("task directory handle unavailable")
	}
	dirHandleInfo, err := dirFile.Stat()
	_ = dirFile.Close()
	if err != nil || !sameGiftClipFile(dirHandleInfo, owned.dirInfo) || !sameGiftClipFile(dirHandleInfo, dirPathInfo) {
		return errors.New("task directory handle identity changed")
	}
	if err := os.RemoveAll(absDirectory); err != nil {
		return err
	}
	return nil
}

func safeGiftClipDirectoryInfo(info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && !giftClipFileInfoReparsePoint(info)
}

func validateGiftClipDirectorySegments(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(absPath)
	current := volume + string(os.PathSeparator)
	relative := strings.TrimPrefix(absPath, current)
	for _, segment := range strings.Split(relative, string(os.PathSeparator)) {
		if segment == "" {
			continue
		}
		current = filepath.Join(current, segment)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return statErr
		}
		if !safeGiftClipDirectoryInfo(info) {
			return errors.New("unsafe directory segment")
		}
	}
	return nil
}

func giftClipFileInfoReparsePoint(info os.FileInfo) bool {
	value := reflect.ValueOf(info.Sys())
	if !value.IsValid() {
		return false
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return false
	}
	field := value.FieldByName("FileAttributes")
	return field.IsValid() && field.CanUint() && field.Uint()&0x400 != 0
}

func sameGiftClipFile(left, right os.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right)
}

func (job *giftClipJob) snapshot() giftClipJobSnapshot {
	return giftClipJobSnapshot{ID: job.id, State: job.state, Progress: job.progress, Message: job.message, Width: job.width, Height: job.height, FPS: job.fps}
}
func isGiftClipJobTerminal(state giftClipJobState) bool {
	return state == giftClipJobReady || state == giftClipJobFailed || state == giftClipJobCancelled
}
func giftClipJobTTL(state giftClipJobState) time.Duration {
	if state == giftClipJobReady {
		return giftClipReadyTTL
	}
	return giftClipTerminalTTL
}
