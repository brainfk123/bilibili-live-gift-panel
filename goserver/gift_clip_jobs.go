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
	"strings"
	"sync"
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
}

type giftClipJobManager struct {
	root     string
	resolver giftClipSourceResolver
	encoder  giftClipEncoder
	logger   *diagnosticLogger

	mu        sync.Mutex
	jobs      map[string]*giftClipJob
	queue     []string
	owned     map[string]string
	now       func() time.Time
	closed    bool
	wake      chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
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
		jobs: make(map[string]*giftClipJob), owned: make(map[string]string), now: time.Now,
		wake: make(chan struct{}, 1), ctx: ctx, cancel: cancel,
	}
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

	dir, id, err := manager.makeTaskDir()
	if err != nil {
		return giftClipJobSnapshot{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = removeGiftClipJobDirectory(manager.root, id, dir)
		}
	}()
	if _, err := writeGiftClipSourceContext(requestCtx, dir, "background.png", background); err != nil {
		return giftClipJobSnapshot{}, fmt.Errorf("写入背景图失败。")
	}
	if _, err := writeGiftClipSourceContext(requestCtx, dir, "overlay.png", overlay); err != nil {
		return giftClipJobSnapshot{}, fmt.Errorf("写入叠加图失败。")
	}
	if err := requestCtx.Err(); err != nil {
		return giftClipJobSnapshot{}, err
	}

	jobCtx, jobCancel := context.WithCancel(manager.ctx)
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		jobCancel()
		return giftClipJobSnapshot{}, errors.New("视频导出任务已关闭。")
	}
	job := &giftClipJob{
		id: id, receiptID: receiptID, crop: crop, state: giftClipJobQueued, message: "等待导出。",
		dir: dir, outputPath: filepath.Join(dir, "clip.mp4"), cancel: jobCancel,
		ctx: jobCtx, width: crop.Width, height: crop.Height, fps: giftClipFPS,
	}
	manager.jobs[id] = job
	manager.owned[id] = dir
	manager.queue = append(manager.queue, id)
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
	if !ok || job.state != giftClipJobReady || manager.owned[id] != job.dir {
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
		job.finishedAt = manager.now()
		job.cancel()
	}
	return true
}

func (manager *giftClipJobManager) Sweep() {
	if manager == nil {
		return
	}
	now := manager.now()
	removals := make([]struct{ id, dir string }, 0)
	manager.mu.Lock()
	for id, job := range manager.jobs {
		if !isGiftClipJobTerminal(job.state) || job.finishedAt.IsZero() || now.Sub(job.finishedAt) < giftClipJobTTL(job.state) {
			continue
		}
		if dir, ok := manager.owned[id]; ok && dir == job.dir {
			removals = append(removals, struct{ id, dir string }{id, dir})
		}
		delete(manager.jobs, id)
		delete(manager.owned, id)
	}
	manager.mu.Unlock()
	for _, removal := range removals {
		_ = removeGiftClipJobDirectory(manager.root, removal.id, removal.dir)
	}
}

func (manager *giftClipJobManager) Close() {
	if manager == nil {
		return
	}
	manager.closeOnce.Do(func() {
		manager.mu.Lock()
		manager.closed = true
		for _, job := range manager.jobs {
			job.cancel()
		}
		manager.mu.Unlock()
		manager.cancel()
		manager.wg.Wait()

		manager.mu.Lock()
		removals := make([]struct{ id, dir string }, 0, len(manager.owned))
		for id, dir := range manager.owned {
			removals = append(removals, struct{ id, dir string }{id, dir})
		}
		manager.jobs = make(map[string]*giftClipJob)
		manager.owned = make(map[string]string)
		manager.queue = nil
		manager.mu.Unlock()
		for _, removal := range removals {
			_ = removeGiftClipJobDirectory(manager.root, removal.id, removal.dir)
		}
	})
}

func (manager *giftClipJobManager) runWorker() {
	defer manager.wg.Done()
	for {
		id, ok := manager.nextJob()
		if !ok {
			select {
			case <-manager.ctx.Done():
				return
			case <-manager.wake:
			}
			continue
		}
		manager.encode(id)
	}
}

func (manager *giftClipJobManager) nextJob() (string, bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for len(manager.queue) > 0 {
		id := manager.queue[0]
		manager.queue = manager.queue[1:]
		job, ok := manager.jobs[id]
		if !ok || job.state == giftClipJobCancelled {
			continue
		}
		job.state = giftClipJobEncoding
		job.message = "正在导出视频。"
		return id, true
	}
	return "", false
}

func (manager *giftClipJobManager) encode(id string) {
	manager.mu.Lock()
	job, ok := manager.jobs[id]
	if !ok || job.state == giftClipJobCancelled {
		manager.mu.Unlock()
		return
	}
	dir, receiptID, crop, outputPath := job.dir, job.receiptID, job.crop, job.outputPath
	ctx := job.ctx
	manager.mu.Unlock()

	source, err := manager.resolver.Resolve(ctx, receiptID, dir)
	if err == nil {
		var profile giftClipOutputProfile
		profile, err = newGiftClipOutputProfile(crop, source.VisualWidth, source.VisualHeight, source.Duration)
		if err == nil {
			manager.mu.Lock()
			if current, exists := manager.jobs[id]; exists && current.state != giftClipJobCancelled {
				current.width, current.height, current.fps = profile.Width, profile.Height, profile.FPS
			}
			manager.mu.Unlock()
			err = manager.encoder.Encode(ctx, giftClipEncodeRequest{
				Source: source, Crop: crop, Profile: profile,
				BackgroundPath: filepath.Join(dir, "background.png"), OverlayPath: filepath.Join(dir, "overlay.png"), OutputPath: outputPath,
			}, func(update giftClipEncodingUpdate) { manager.updateEncoding(id, update) })
		}
	}
	manager.finishEncoding(id, err)
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

func (manager *giftClipJobManager) finishEncoding(id string, err error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	job, ok := manager.jobs[id]
	if !ok || job.state == giftClipJobCancelled {
		return
	}
	if err == nil {
		job.state, job.progress, job.message = giftClipJobReady, 1, "视频已生成。"
	} else if errors.Is(err, context.Canceled) || manager.ctx.Err() != nil {
		job.state, job.message = giftClipJobCancelled, "任务已取消。"
	} else {
		job.state, job.message = giftClipJobFailed, giftClipJobFailureMessage(err)
		if manager.logger != nil {
			manager.logger.Error("gift_clip_job_failed", "task_id", id, "phase", "encode", "mode", job.mode, "exit_class", giftClipJobExitClass(err))
		}
	}
	job.finishedAt = manager.now()
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

func (manager *giftClipJobManager) makeTaskDir() (string, string, error) {
	root, err := filepath.Abs(manager.root)
	if err != nil || strings.TrimSpace(manager.root) == "" {
		return "", "", errors.New("导出任务目录无效。")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", "", errors.New("创建导出任务目录失败。")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("导出任务目录无效。")
	}
	for attempts := 0; attempts < 8; attempts++ {
		id, err := newGiftClipJobID()
		if err != nil {
			return "", "", errors.New("创建导出任务失败。")
		}
		directory := filepath.Join(root, id)
		if filepath.Dir(directory) != root {
			continue
		}
		if err := os.Mkdir(directory, 0o700); err == nil {
			return directory, id, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", "", errors.New("创建导出任务目录失败。")
		}
	}
	return "", "", errors.New("创建导出任务失败。")
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
	if err != nil || config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > maxGiftClipLayerPixels || config.Width != crop.Width || config.Height != crop.Height {
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
func giftClipJobFailureMessage(err error) string {
	if errors.Is(err, errGiftClipPayloadIntegrity) {
		return "导出组件校验失败。"
	}
	return "视频导出失败。"
}
func giftClipJobExitClass(err error) string {
	if errors.Is(err, errGiftClipPayloadIntegrity) {
		return "payload_integrity"
	}
	return "encoder_error"
}

func removeGiftClipJobDirectory(root, id, directory string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absDirectory, err := filepath.Abs(directory)
	if err != nil || filepath.Dir(absDirectory) != absRoot || filepath.Base(absDirectory) != id {
		return errors.New("unsafe task directory")
	}
	info, err := os.Lstat(absDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return os.Remove(absDirectory)
	}
	return os.RemoveAll(absDirectory)
}
