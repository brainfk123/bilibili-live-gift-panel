package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
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
	waitGiftClipJobProgress(t, manager, job.ID, .75)
	encoder.updates <- giftClipEncodingUpdate{Progress: .5}
	runtime.Gosched()
	got, ok := manager.Snapshot(job.ID)
	if !ok || got.Progress != .75 {
		t.Fatalf("progress regressed: %#v", got)
	}
	encoder.updates <- giftClipEncodingUpdate{Retrying: true, Mode: giftClipEncoderSoftware}
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
	if _, err := manager.Create(context.Background(), "one", crop, background, testGiftClipJobPNGHeader(4097, 4097)); err == nil {
		t.Fatal("accepted layer over pixel limit")
	}
}

func TestGiftClipJobSweepTTLAndCloseOwnDirectories(t *testing.T) {
	root := t.TempDir()
	encoder := newBlockingGiftClipJobEncoder()
	manager := newGiftClipJobManager(root, testGiftClipJobResolver{}, encoder, nil)
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
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
	now = now.Add(giftClipTerminalTTL)
	manager.Sweep()
	if _, ok := manager.Snapshot(failed.ID); ok {
		t.Fatal("failed job was not swept")
	}
	if _, ok := manager.Snapshot(cancelled.ID); ok {
		t.Fatal("cancelled job was not swept")
	}
	now = now.Add(giftClipReadyTTL - giftClipTerminalTTL)
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

type testGiftClipJobResolver struct{}

func (testGiftClipJobResolver) Resolve(_ context.Context, _ string, taskDir string) (giftClipSource, error) {
	return giftClipSource{Kind: giftClipSourceGIF, Playback: giftClipPlaybackSingleGIF, Path: filepath.Join(taskDir, "source.gif"), VisualWidth: 64, VisualHeight: 64, Duration: time.Second}, nil
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
	started chan string
	updates chan giftClipEncodingUpdate
	finish  chan error
}

func (e *scriptedGiftClipJobEncoder) Encode(ctx context.Context, request giftClipEncodeRequest, notify func(giftClipEncodingUpdate)) error {
	if e.updates == nil {
		e.updates = make(chan giftClipEncodingUpdate, 8)
	}
	e.started <- filepath.Base(filepath.Dir(request.OutputPath))
	for {
		select {
		case update := <-e.updates:
			notify(update)
		case err := <-e.finish:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
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
func testGiftClipJobPNGHeader(width, height int) []byte {
	output := make([]byte, 8+4+4+13+4)
	copy(output, "\x89PNG\r\n\x1a\n")
	binary.BigEndian.PutUint32(output[8:12], 13)
	copy(output[12:16], "IHDR")
	binary.BigEndian.PutUint32(output[16:20], uint32(width))
	binary.BigEndian.PutUint32(output[20:24], uint32(height))
	output[24] = 8
	output[25] = 6
	binary.BigEndian.PutUint32(output[29:33], crc32.ChecksumIEEE(output[12:29]))
	return output
}
func assertNoGiftClipJobValue[T any](t *testing.T, channel <-chan T, wait time.Duration) {
	t.Helper()
	select {
	case value := <-channel:
		t.Fatalf("unexpected value: %#v", value)
	case <-time.After(wait):
	}
}
func waitGiftClipJobState(t *testing.T, manager *giftClipJobManager, id string, state giftClipJobState) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		snapshot, ok := manager.Snapshot(id)
		if ok && snapshot.State == state {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("job %s did not reach %s", id, state)
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
