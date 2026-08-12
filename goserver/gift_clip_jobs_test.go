package main

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestGiftClipJobFIFOOneWorker(t *testing.T) {
	encoder := newBlockingGiftClipJobEncoder()
	manager := newTestGiftClipJobManager(t, encoder)
	defer manager.Close()

	first, err := manager.Create(context.Background(), "receipt-1", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true))
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Create(context.Background(), "receipt-2", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true))
	if err != nil {
		t.Fatal(err)
	}
	if got := <-encoder.started; got != first.ID {
		t.Fatalf("first started = %s, want %s", got, first.ID)
	}
	assertNoGiftClipJobValue(t, encoder.started, 100*time.Millisecond)
	encoder.finish <- nil
	if got := <-encoder.started; got != second.ID {
		t.Fatalf("second started = %s, want %s", got, second.ID)
	}
}

func TestGiftClipJobRequestContextDoesNotOwnQueuedWork(t *testing.T) {
	encoder := newBlockingGiftClipJobEncoder()
	manager := newTestGiftClipJobManager(t, encoder)
	defer manager.Close()
	ctx, cancel := context.WithCancel(context.Background())
	job, err := manager.Create(ctx, "receipt", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if got := <-encoder.started; got != job.ID {
		t.Fatalf("started = %s, want %s", got, job.ID)
	}
	assertNoGiftClipJobValue(t, encoder.cancelled, 100*time.Millisecond)
	encoder.finish <- nil
}

func TestGiftClipJobCancelQueuedDoesNotStartEncoder(t *testing.T) {
	encoder := newBlockingGiftClipJobEncoder()
	manager := newTestGiftClipJobManager(t, encoder)
	defer manager.Close()
	first, err := manager.Create(context.Background(), "one", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true))
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Create(context.Background(), "two", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true))
	if err != nil {
		t.Fatal(err)
	}
	if <-encoder.started != first.ID {
		t.Fatal("first job did not start")
	}
	if !manager.Cancel(second.ID) {
		t.Fatal("Cancel returned false")
	}
	encoder.finish <- nil
	assertNoGiftClipJobValue(t, encoder.started, 100*time.Millisecond)
	got, ok := manager.Snapshot(second.ID)
	if !ok || got.State != giftClipJobCancelled {
		t.Fatalf("snapshot = %#v, ok=%v", got, ok)
	}
}

func TestGiftClipJobCancelRunningCancelsEncoderContext(t *testing.T) {
	encoder := newBlockingGiftClipJobEncoder()
	manager := newTestGiftClipJobManager(t, encoder)
	defer manager.Close()
	job, err := manager.Create(context.Background(), "one", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true))
	if err != nil {
		t.Fatal(err)
	}
	if <-encoder.started != job.ID {
		t.Fatal("job did not start")
	}
	if !manager.Cancel(job.ID) {
		t.Fatal("Cancel returned false")
	}
	select {
	case <-encoder.cancelled:
	case <-time.After(time.Second):
		t.Fatal("encoder did not receive cancellation")
	}
	waitGiftClipJobState(t, manager, job.ID, giftClipJobCancelled)
}

func TestGiftClipJobProgressAndRetryState(t *testing.T) {
	encoder := &scriptedGiftClipJobEncoder{started: make(chan string, 1), finish: make(chan error, 1)}
	manager := newTestGiftClipJobManager(t, encoder)
	defer manager.Close()
	job, err := manager.Create(context.Background(), "one", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true))
	if err != nil {
		t.Fatal(err)
	}
	if <-encoder.started != job.ID {
		t.Fatal("job did not start")
	}
	encoder.updates <- giftClipEncodingUpdate{Progress: .75}
	<-encoder.updateAck
	waitGiftClipJobProgress(t, manager, job.ID, .75)
	encoder.updates <- giftClipEncodingUpdate{Progress: .5}
	<-encoder.updateAck
	got, ok := manager.Snapshot(job.ID)
	if !ok || got.Progress != .75 {
		t.Fatalf("progress regressed: %#v", got)
	}
	encoder.updates <- giftClipEncodingUpdate{Retrying: true, Mode: giftClipEncoderSoftware}
	<-encoder.updateAck
	waitGiftClipJobState(t, manager, job.ID, giftClipJobRetrying)
	got, _ = manager.Snapshot(job.ID)
	if got.Progress != 0 || got.Message != "已切换兼容编码模式。" {
		t.Fatalf("retry snapshot = %#v", got)
	}
	encoder.finish <- nil
	waitGiftClipJobState(t, manager, job.ID, giftClipJobReady)
	got, _ = manager.Snapshot(job.ID)
	if got.Progress != 1 {
		t.Fatalf("ready progress = %v", got.Progress)
	}
}

func TestGiftClipJobReadyVideoPath(t *testing.T) {
	encoder := newBlockingGiftClipJobEncoder()
	manager := newTestGiftClipJobManager(t, encoder)
	defer manager.Close()
	job, err := manager.Create(context.Background(), "one", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true))
	if err != nil {
		t.Fatal(err)
	}
	if <-encoder.started != job.ID {
		t.Fatal("job did not start")
	}
	encoder.finish <- nil
	waitGiftClipJobState(t, manager, job.ID, giftClipJobReady)
	path, ok := manager.VideoPath(job.ID)
	if !ok || filepath.Base(path) != "clip.mp4" {
		t.Fatalf("VideoPath = %q, %v", path, ok)
	}
}

func TestGiftClipJobValidatesLayersAndUsesCryptoIDs(t *testing.T) {
	manager := newTestGiftClipJobManager(t, &immediateGiftClipJobEncoder{})
	defer manager.Close()
	crop, background, overlay := testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true)
	job, err := manager.Create(context.Background(), "one", crop, background, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{24}$`).MatchString(job.ID) {
		t.Fatalf("ID is not an 18-byte base64url token: %q", job.ID)
	}
	if _, err := manager.Create(context.Background(), "one", crop, testGiftClipJobPNGSize(t, 65, 64, false), overlay); err == nil {
		t.Fatal("accepted mismatched background")
	}
	if _, err := manager.Create(context.Background(), "one", crop, testGiftClipJobPNG(t, true), overlay); err == nil {
		t.Fatal("accepted transparent background")
	}
	if err := validateGiftClipLayerDimensions(4096, 4098, giftClipCrop{Width: 4096, Height: 4098}); err == nil {
		t.Fatal("accepted layer over pixel limit with matching crop")
	}
}

func TestGiftClipJobStableFailureMessagesAndDiagnostics(t *testing.T) {
	tests := []struct {
		name      string
		resolver  giftClipSourceResolver
		encoder   giftClipEncoder
		wantText  string
		wantPhase string
		wantClass string
		wantMode  string
	}{
		{name: "source", resolver: errorGiftClipJobResolver{err: errors.New("corrupt source C:\\private\\gift.gif")}, encoder: &immediateGiftClipJobEncoder{}, wantText: "礼物动画素材无法解码，请重试或更换素材。", wantPhase: "resolve", wantClass: "source_error", wantMode: "none"},
		{name: "profile", resolver: fixedGiftClipJobResolver{source: giftClipSource{Kind: giftClipSourceGIF, Playback: giftClipPlaybackSingleGIF, VisualWidth: 32, VisualHeight: 32, Duration: time.Second}}, encoder: &immediateGiftClipJobEncoder{}, wantText: "视频尺寸无效，宽高必须为 64–4096 范围内的偶数像素。", wantPhase: "profile", wantClass: "invalid_profile", wantMode: "none"},
		{name: "disk", resolver: testGiftClipJobResolver{}, encoder: errorGiftClipJobEncoder{err: syscall.ENOSPC}, wantText: "磁盘空间不足，无法生成视频。", wantPhase: "encode", wantClass: "disk_full", wantMode: string(giftClipDefaultEncoderMode)},
		{name: "integrity", resolver: testGiftClipJobResolver{}, encoder: errorGiftClipJobEncoder{err: errGiftClipPayloadIntegrity}, wantText: "视频编码组件校验失败，请重启程序后重试。", wantPhase: "encode", wantClass: "payload_integrity", wantMode: string(giftClipDefaultEncoderMode)},
		{name: "final", resolver: testGiftClipJobResolver{}, encoder: errorGiftClipJobEncoder{err: errors.New("encoder failed at D:\\private\\clip.mp4")}, wantText: "视频生成失败，请重试。", wantPhase: "encode", wantClass: "encoder_error", wantMode: string(giftClipDefaultEncoderMode)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			logger, err := newDiagnosticLogger(filepath.Join(root, "diagnostic.log"))
			if err != nil {
				t.Fatal(err)
			}
			manager := newGiftClipJobManager(filepath.Join(root, "tasks"), test.resolver, test.encoder, logger)
			defer manager.Close()
			job, err := manager.Create(context.Background(), "receipt", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true))
			if err != nil {
				t.Fatal(err)
			}
			got := waitGiftClipJobState(t, manager, job.ID, giftClipJobFailed)
			if got.Message != test.wantText {
				t.Fatalf("message = %q, want %q", got.Message, test.wantText)
			}
			logData, err := os.ReadFile(logger.path)
			if err != nil {
				t.Fatal(err)
			}
			logText := string(logData)
			for _, token := range []string{`task_id="` + job.ID + `"`, `phase="` + test.wantPhase + `"`, `exit_class="` + test.wantClass + `"`, `mode="` + test.wantMode + `"`} {
				if !strings.Contains(logText, token) {
					t.Fatalf("log %q missing %q", logText, token)
				}
			}
			if strings.Contains(logText, "private") || strings.Contains(logText, "gift.gif") || strings.Contains(logText, "clip.mp4") {
				t.Fatalf("diagnostic leaked path: %q", logText)
			}
		})
	}
}

func TestGiftClipJobCreatePreservesLayerWriteDiskCause(t *testing.T) {
	manager := newTestGiftClipJobManager(t, &immediateGiftClipJobEncoder{})
	defer manager.Close()
	manager.writeLayer = func(context.Context, string, string, []byte) (string, error) { return "", syscall.ENOSPC }
	_, err := manager.Create(context.Background(), "receipt", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true))
	if err == nil || err.Error() != "磁盘空间不足，无法生成视频。" {
		t.Fatalf("Create error = %v", err)
	}
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("Create lost disk cause: %v", err)
	}
}

func TestGiftClipJobSweepTTLAndCloseOwnDirectories(t *testing.T) {
	root := t.TempDir()
	encoder := newBlockingGiftClipJobEncoder()
	manager := newGiftClipJobManager(root, testGiftClipJobResolver{}, encoder, nil)
	clock := newTestGiftClipJobClock(time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC))
	manager.now = clock.Now
	ready, err := manager.Create(context.Background(), "ready", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true))
	if err != nil {
		t.Fatal(err)
	}
	if <-encoder.started != ready.ID {
		t.Fatal("ready job did not start")
	}
	encoder.finish <- nil
	waitGiftClipJobState(t, manager, ready.ID, giftClipJobReady)
	failed, err := manager.Create(context.Background(), "failed", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true))
	if err != nil {
		t.Fatal(err)
	}
	if <-encoder.started != failed.ID {
		t.Fatal("failed job did not start")
	}
	encoder.finish <- errors.New("failed")
	waitGiftClipJobState(t, manager, failed.ID, giftClipJobFailed)
	cancelled, err := manager.Create(context.Background(), "cancelled", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true))
	if err != nil {
		t.Fatal(err)
	}
	if <-encoder.started != cancelled.ID {
		t.Fatal("cancelled job did not start")
	}
	if !manager.Cancel(cancelled.ID) {
		t.Fatal("Cancel returned false")
	}
	waitGiftClipJobState(t, manager, cancelled.ID, giftClipJobCancelled)
	other := filepath.Join(root, "not-created-by-manager")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	clock.Advance(giftClipTerminalTTL)
	manager.Sweep()
	if _, ok := manager.Snapshot(failed.ID); ok {
		t.Fatal("failed job was not swept")
	}
	if _, ok := manager.Snapshot(cancelled.ID); ok {
		t.Fatal("cancelled job was not swept")
	}
	clock.Advance(giftClipReadyTTL - giftClipTerminalTTL)
	manager.Sweep()
	if _, ok := manager.Snapshot(ready.ID); ok {
		t.Fatal("ready job was not swept")
	}
	if _, err := os.Stat(filepath.Join(root, ready.ID)); !os.IsNotExist(err) {
		t.Fatalf("ready directory remains: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("unowned directory removed: %v", err)
	}
	manager.Close()
}

func TestGiftClipJobCloseCancelsAndWaits(t *testing.T) {
	encoder := newBlockingGiftClipJobEncoder()
	manager := newTestGiftClipJobManager(t, encoder)
	job, err := manager.Create(context.Background(), "one", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true))
	if err != nil {
		t.Fatal(err)
	}
	if <-encoder.started != job.ID {
		t.Fatal("job did not start")
	}
	done := make(chan struct{})
	go func() { manager.Close(); close(done) }()
	select {
	case <-encoder.cancelled:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel encoder")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not wait for worker")
	}
	if _, err := os.Stat(filepath.Join(manager.root, job.ID)); !os.IsNotExist(err) {
		t.Fatalf("Close left its task directory behind: %v", err)
	}
}

func TestGiftClipJobCloseNeverStartsQueuedResolver(t *testing.T) {
	resolver := &cancellableGiftClipJobResolver{started: make(chan string, 2)}
	manager := newGiftClipJobManager(t.TempDir(), resolver, &immediateGiftClipJobEncoder{}, nil)
	first, err := manager.Create(context.Background(), "active", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true))
	if err != nil {
		t.Fatal(err)
	}
	queued, err := manager.Create(context.Background(), "queued", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true))
	if err != nil {
		t.Fatal(err)
	}
	if got := <-resolver.started; got != "active" {
		t.Fatalf("resolver started %q", got)
	}
	done := make(chan struct{})
	go func() { manager.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after resolver cancellation")
	}
	assertNoGiftClipJobValue(t, resolver.started, 100*time.Millisecond)
	if snapshot, ok := manager.Snapshot(queued.ID); ok {
		t.Fatalf("queued job remains after Close: %#v", snapshot)
	}
	_ = first
}

func TestGiftClipJobRunningCancelWaitsForWorkerBeforeTTLDeletion(t *testing.T) {
	encoder := newLingeringGiftClipJobEncoder()
	manager := newTestGiftClipJobManager(t, encoder)
	clock := newTestGiftClipJobClock(time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC))
	manager.now = clock.Now
	job, err := manager.Create(context.Background(), "active", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true))
	if err != nil {
		t.Fatal(err)
	}
	<-encoder.started
	if !manager.Cancel(job.ID) {
		t.Fatal("Cancel returned false")
	}
	<-encoder.cancelled
	clock.Advance(giftClipTerminalTTL)
	manager.Sweep()
	if _, err := os.Stat(filepath.Join(manager.root, job.ID)); err != nil {
		t.Fatalf("running cancelled directory removed: %v", err)
	}
	if _, ok := manager.Snapshot(job.ID); !ok {
		t.Fatal("running cancelled job swept before worker unwind")
	}
	close(encoder.release)
	waitGiftClipJobFinished(t, manager, job.ID)
	clock.Advance(giftClipTerminalTTL)
	manager.Sweep()
	if _, err := os.Stat(filepath.Join(manager.root, job.ID)); !os.IsNotExist(err) {
		t.Fatalf("cancelled directory remains after unwind TTL: %v", err)
	}
	manager.Close()
}

func TestGiftClipJobSweepRetriesRemovalBeforeForgettingOwnership(t *testing.T) {
	manager := newTestGiftClipJobManager(t, &immediateGiftClipJobEncoder{})
	clock := newTestGiftClipJobClock(time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC))
	manager.now = clock.Now
	job, err := manager.Create(context.Background(), "ready", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true))
	if err != nil {
		t.Fatal(err)
	}
	waitGiftClipJobState(t, manager, job.ID, giftClipJobReady)
	original := manager.removeTaskDir
	attempts := 0
	manager.removeTaskDir = func(owned giftClipOwnedDirectory) error {
		attempts++
		if attempts == 1 {
			return errors.New("busy")
		}
		return original(owned)
	}
	clock.Advance(giftClipReadyTTL)
	manager.Sweep()
	if _, ok := manager.Snapshot(job.ID); !ok {
		t.Fatal("failed removal forgot job")
	}
	manager.Sweep()
	if _, ok := manager.Snapshot(job.ID); ok {
		t.Fatal("successful retry retained job")
	}
	if attempts != 2 {
		t.Fatalf("remove attempts = %d", attempts)
	}
	manager.Close()
}

func TestGiftClipJobSweepRefusesRenamedDirectoryReplacement(t *testing.T) {
	manager := newTestGiftClipJobManager(t, &immediateGiftClipJobEncoder{})
	job, err := manager.Create(context.Background(), "ready", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true))
	if err != nil {
		t.Fatal(err)
	}
	waitGiftClipJobState(t, manager, job.ID, giftClipJobReady)
	taskPath := filepath.Join(manager.root, job.ID)
	originalPath := taskPath + "-original"
	if err := os.Rename(taskPath, originalPath); err != nil {
		t.Fatalf("rename task directory: %v", err)
	}
	if err := os.Mkdir(taskPath, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(taskPath, "replacement.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	manager.jobs[job.ID].finishedAt = manager.now().Add(-giftClipReadyTTL)
	manager.mu.Unlock()
	manager.Sweep()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("replacement was deleted: %v", err)
	}
	if _, ok := manager.Snapshot(job.ID); !ok {
		t.Fatal("identity mismatch forgot ownership")
	}
	manager.Close()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("Close deleted replacement: %v", err)
	}
	_ = os.RemoveAll(originalPath)
	_ = os.RemoveAll(taskPath)
}

func TestGiftClipJobRejectsSymlinkTaskRootWhenSupported(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	manager := newGiftClipJobManager(link, testGiftClipJobResolver{}, &immediateGiftClipJobEncoder{}, nil)
	defer manager.Close()
	if _, err := manager.Create(context.Background(), "receipt", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true)); err == nil {
		t.Fatal("accepted symlink task root")
	}
}

func TestGiftClipJobTerminalReleasesManagerChildContexts(t *testing.T) {
	manager := newTestGiftClipJobManager(t, &immediateGiftClipJobEncoder{})
	defer manager.Close()
	jobs := make([]giftClipJobSnapshot, 8)
	for index := range jobs {
		job, err := manager.Create(context.Background(), "ready", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true))
		if err != nil {
			t.Fatal(err)
		}
		jobs[index] = job
	}
	for _, job := range jobs {
		waitGiftClipJobState(t, manager, job.ID, giftClipJobReady)
	}
	if got := manager.activeChildCount(); got != 0 {
		t.Fatalf("active child contexts = %d", got)
	}
}

func TestGiftClipJobCancelAfterDequeueReleasesChildContext(t *testing.T) {
	manager := newTestGiftClipJobManager(t, &immediateGiftClipJobEncoder{})
	dequeued, release := make(chan struct{}), make(chan struct{})
	manager.beforeEncode = func(string) { close(dequeued); <-release }
	job, err := manager.Create(context.Background(), "cancel-race", testGiftClipJobCrop(), testGiftClipJobPNG(t, false), testGiftClipJobPNG(t, true))
	if err != nil {
		t.Fatal(err)
	}
	<-dequeued
	if !manager.Cancel(job.ID) {
		t.Fatal("Cancel returned false")
	}
	close(release)
	waitGiftClipJobFinished(t, manager, job.ID)
	if got := manager.activeChildCount(); got != 0 {
		t.Fatalf("active child contexts = %d", got)
	}
	manager.Close()
}

type testGiftClipJobResolver struct{}

func (testGiftClipJobResolver) Resolve(_ context.Context, _ string, taskDir string) (giftClipSource, error) {
	return giftClipSource{Kind: giftClipSourceGIF, Playback: giftClipPlaybackSingleGIF, Path: filepath.Join(taskDir, "source.gif"), VisualWidth: 64, VisualHeight: 64, Duration: time.Second}, nil
}

type fixedGiftClipJobResolver struct{ source giftClipSource }

func (resolver fixedGiftClipJobResolver) Resolve(_ context.Context, _ string, taskDir string) (giftClipSource, error) {
	source := resolver.source
	source.Path = filepath.Join(taskDir, "source.gif")
	return source, nil
}

type errorGiftClipJobResolver struct{ err error }

func (resolver errorGiftClipJobResolver) Resolve(context.Context, string, string) (giftClipSource, error) {
	return giftClipSource{}, resolver.err
}

type cancellableGiftClipJobResolver struct{ started chan string }

func (resolver *cancellableGiftClipJobResolver) Resolve(ctx context.Context, receiptID, _ string) (giftClipSource, error) {
	resolver.started <- receiptID
	<-ctx.Done()
	return giftClipSource{}, ctx.Err()
}

type errorGiftClipJobEncoder struct{ err error }

func (encoder errorGiftClipJobEncoder) Encode(context.Context, giftClipEncodeRequest, func(giftClipEncodingUpdate)) error {
	return encoder.err
}

type blockingGiftClipJobEncoder struct {
	started   chan string
	finish    chan error
	cancelled chan struct{}
	once      sync.Once
}

func newBlockingGiftClipJobEncoder() *blockingGiftClipJobEncoder {
	return &blockingGiftClipJobEncoder{started: make(chan string, 8), finish: make(chan error, 8), cancelled: make(chan struct{})}
}
func (e *blockingGiftClipJobEncoder) Encode(ctx context.Context, request giftClipEncodeRequest, _ func(giftClipEncodingUpdate)) error {
	e.started <- filepath.Base(filepath.Dir(request.OutputPath))
	select {
	case err := <-e.finish:
		return err
	case <-ctx.Done():
		e.once.Do(func() { close(e.cancelled) })
		return ctx.Err()
	}
}

type immediateGiftClipJobEncoder struct{}

func (*immediateGiftClipJobEncoder) Encode(_ context.Context, _ giftClipEncodeRequest, _ func(giftClipEncodingUpdate)) error {
	return nil
}

type scriptedGiftClipJobEncoder struct {
	started   chan string
	updates   chan giftClipEncodingUpdate
	updateAck chan struct{}
	finish    chan error
}

func (e *scriptedGiftClipJobEncoder) Encode(ctx context.Context, request giftClipEncodeRequest, notify func(giftClipEncodingUpdate)) error {
	if e.updates == nil {
		e.updates = make(chan giftClipEncodingUpdate, 8)
	}
	if e.updateAck == nil {
		e.updateAck = make(chan struct{}, 8)
	}
	e.started <- filepath.Base(filepath.Dir(request.OutputPath))
	for {
		select {
		case update := <-e.updates:
			notify(update)
			e.updateAck <- struct{}{}
		case err := <-e.finish:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

type lingeringGiftClipJobEncoder struct{ started, cancelled, release chan struct{} }

func newLingeringGiftClipJobEncoder() *lingeringGiftClipJobEncoder {
	return &lingeringGiftClipJobEncoder{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
}
func (encoder *lingeringGiftClipJobEncoder) Encode(ctx context.Context, _ giftClipEncodeRequest, _ func(giftClipEncodingUpdate)) error {
	close(encoder.started)
	<-ctx.Done()
	close(encoder.cancelled)
	<-encoder.release
	return ctx.Err()
}

type testGiftClipJobClock struct {
	mu    sync.Mutex
	value time.Time
}

func newTestGiftClipJobClock(value time.Time) *testGiftClipJobClock {
	return &testGiftClipJobClock{value: value}
}
func (clock *testGiftClipJobClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.value
}
func (clock *testGiftClipJobClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.value = clock.value.Add(duration)
	clock.mu.Unlock()
}

func newTestGiftClipJobManager(t *testing.T, encoder giftClipEncoder) *giftClipJobManager {
	t.Helper()
	return newGiftClipJobManager(t.TempDir(), testGiftClipJobResolver{}, encoder, nil)
}
func testGiftClipJobCrop() giftClipCrop { return giftClipCrop{Width: 64, Height: 64} }
func testGiftClipJobPNG(t *testing.T, alpha bool) []byte {
	return testGiftClipJobPNGSize(t, 64, 64, alpha)
}
func testGiftClipJobPNGSize(t *testing.T, width, height int, alpha bool) []byte {
	t.Helper()
	picture := image.NewNRGBA(image.Rect(0, 0, width, height))
	if alpha {
		picture.Set(0, 0, color.NRGBA{R: 1, A: 127})
	} else {
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				picture.Set(x, y, color.NRGBA{R: 1, A: 255})
			}
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, picture); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
func assertNoGiftClipJobValue[T any](t *testing.T, channel <-chan T, wait time.Duration) {
	t.Helper()
	select {
	case value := <-channel:
		t.Fatalf("unexpected value: %#v", value)
	case <-time.After(wait):
	}
}
func waitGiftClipJobState(t *testing.T, manager *giftClipJobManager, id string, state giftClipJobState) giftClipJobSnapshot {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		snapshot, ok := manager.Snapshot(id)
		if ok && snapshot.State == state {
			return snapshot
		}
		select {
		case <-deadline:
			t.Fatalf("job %s did not reach %s", id, state)
		default:
			runtime.Gosched()
		}
	}
}
func waitGiftClipJobFinished(t *testing.T, manager *giftClipJobManager, id string) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		manager.mu.Lock()
		job, ok := manager.jobs[id]
		finished := ok && !job.finishedAt.IsZero()
		manager.mu.Unlock()
		if finished {
			return
		}
		select {
		case <-deadline:
			t.Fatal("job did not finish worker unwind")
		default:
			runtime.Gosched()
		}
	}
}
func waitGiftClipJobProgress(t *testing.T, manager *giftClipJobManager, id string, want float64) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		snapshot, ok := manager.Snapshot(id)
		if ok && snapshot.Progress == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("job %s did not reach progress %v", id, want)
		default:
			runtime.Gosched()
		}
	}
}
