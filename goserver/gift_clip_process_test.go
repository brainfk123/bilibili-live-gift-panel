//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var giftClipProcessHelperFile = flag.String("test.gift_clip_helper_file", "", "gift clip process helper pid file")
var giftClipProcessOutputHelper = flag.Bool("test.gift_clip_output_helper", false, "emit gift clip process helper output")
var giftClipProcessOutputBursts = flag.Int("test.gift_clip_output_bursts", 0, "number of concurrent helper output bursts")

func TestGiftClipWindowsProcessRunnerTerminatesTree(t *testing.T) {
	path := t.TempDir() + "\\pids.txt"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errs := make(chan error, 1)
	go func() {
		errs <- newGiftClipProcessRunner().Run(ctx, os.Args[0], []string{
			"-test.run=^TestGiftClipWindowsProcessHelper$",
			"-test.gift_clip_helper_file=" + path,
		}, io.Discard, io.Discard)
	}()
	pids := waitForGiftClipHelperPIDs(t, path)
	cancel()
	if err := <-errs; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context cancellation", err)
	}
	for _, pid := range pids {
		deadline := time.Now().Add(3 * time.Second)
		for !giftClipWindowsProcessIsGone(pid) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if !giftClipWindowsProcessIsGone(pid) {
			t.Fatalf("recorded helper PID %d survived cancellation", pid)
		}
	}
}

func TestGiftClipWindowsProcessRunnerStartsHelperSuspended(t *testing.T) {
	path := t.TempDir() + "\\pids.txt"
	started, err := startGiftClipProcessSuspended(os.Args[0], []string{
		"-test.run=^TestGiftClipWindowsProcessHelper$",
		"-test.gift_clip_helper_file=" + path,
	}, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	recordedPID := started.command.Process.Pid
	defer func() {
		if recordedPID != started.command.Process.Pid {
			t.Fatal("attempted to terminate an unrecorded process")
		}
		_ = started.command.Process.Kill()
		_ = waitGiftClipStartedProcess(giftClipDefaultWindowsAPI, started)
	}()
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("suspended helper executed before Job assignment: %v", err)
	}
}

func TestGiftClipWindowsProcessRunnerAssignsJobBeforeResuming(t *testing.T) {
	path := t.TempDir() + "\\pids.txt"
	api := giftClipDefaultWindowsAPI
	assigned := false
	assign := api.assignProcess
	api.assignProcess = func(job, process syscall.Handle) error {
		err := assign(job, process)
		assigned = err == nil
		return err
	}
	resume := api.resumePrimaryThread
	api.resumePrimaryThread = func(pid int) error {
		if !assigned {
			return errors.New("resume attempted before Job assignment")
		}
		return resume(pid)
	}
	runner := giftClipWindowsProcessRunner{api: api}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errs := make(chan error, 1)
	go func() {
		errs <- runner.Run(ctx, os.Args[0], []string{
			"-test.run=^TestGiftClipWindowsProcessHelper$",
			"-test.gift_clip_helper_file=" + path,
		}, io.Discard, io.Discard)
	}()
	_ = waitForGiftClipHelperPIDs(t, path)
	cancel()
	if err := <-errs; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context cancellation", err)
	}
}

func TestGiftClipWindowsProcessRunnerCopiesOutputAndWaitsNormally(t *testing.T) {
	api := giftClipDefaultWindowsAPI
	startedProcesses := make(chan *giftClipStartedProcess, 1)
	startSuspended := api.startSuspended
	api.startSuspended = func(path string, args []string, stdout, stderr io.Writer) (*giftClipStartedProcess, error) {
		started, err := startSuspended(path, args, stdout, stderr)
		if started != nil {
			startedProcesses <- started
		}
		return started, err
	}
	waits, releases, jobCloses, processCloses := 0, 0, 0, 0
	wait, release, closeJob, closeProcess := api.waitProcess, api.releaseProcess, api.closeJob, api.closeProcess
	api.waitProcess = func(command *exec.Cmd) error { waits++; return wait(command) }
	api.releaseProcess = func(command *exec.Cmd) error { releases++; return release(command) }
	api.closeJob = func(handle syscall.Handle) { jobCloses++; closeJob(handle) }
	api.closeProcess = func(handle syscall.Handle) { processCloses++; closeProcess(handle) }
	var stdout, stderr bytes.Buffer
	runner := giftClipWindowsProcessRunner{api: api}
	if err := runner.Run(context.Background(), os.Args[0], []string{
		"-test.run=^TestGiftClipWindowsProcessOutputHelper$",
		"-test.gift_clip_output_helper=true",
	}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	started := <-startedProcesses
	if !strings.Contains(stdout.String(), "stdout sentinel") || !strings.Contains(stderr.String(), "stderr sentinel") {
		t.Fatalf("stdout=%q stderr=%q, want copied sentinels", stdout.String(), stderr.String())
	}
	if waits != 1 || releases != 0 || jobCloses != 1 || processCloses != 1 {
		t.Fatalf("waits=%d releases=%d job closes=%d process closes=%d, want 1, 0, 1, 1", waits, releases, jobCloses, processCloses)
	}
	for name, pipe := range map[string]*giftClipProcessPipe{"stdout": started.stdout, "stderr": started.stderr} {
		select {
		case <-pipe.done:
		default:
			t.Fatalf("%s copier did not complete before normal Run returned", name)
		}
		assertGiftClipPipeFileClosed(t, name+" reader", pipe.reader)
		assertGiftClipPipeFileClosed(t, name+" writer", pipe.writer)
	}
}

func TestGiftClipWindowsProcessRunnerSerializesSharedOutputWriter(t *testing.T) {
	const bursts = 4096
	var output bytes.Buffer
	if err := newGiftClipProcessRunner().Run(context.Background(), os.Args[0], []string{
		"-test.run=^TestGiftClipWindowsProcessOutputHelper$",
		"-test.gift_clip_output_helper=true",
		"-test.gift_clip_output_bursts=" + strconv.Itoa(bursts),
	}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if got := bytes.Count(output.Bytes(), []byte("stdout sentinel\n")); got != bursts {
		t.Fatalf("stdout sentinel count = %d, want %d", got, bursts)
	}
	if got := bytes.Count(output.Bytes(), []byte("stderr sentinel\n")); got != bursts {
		t.Fatalf("stderr sentinel count = %d, want %d", got, bursts)
	}
}

func TestGiftClipWindowsProcessRunnerDoesNotSerializeDifferentOutputWriters(t *testing.T) {
	stdout := &giftClipBlockingWriter{started: make(chan struct{}), unblock: make(chan struct{})}
	stderr := &giftClipSignalingWriter{wrote: make(chan struct{})}
	errs := make(chan error, 1)
	go func() {
		errs <- newGiftClipProcessRunner().Run(context.Background(), os.Args[0], []string{
			"-test.run=^TestGiftClipWindowsProcessOutputHelper$",
			"-test.gift_clip_output_helper=true",
		}, stdout, stderr)
	}()
	select {
	case <-stdout.started:
	case <-time.After(3 * time.Second):
		close(stdout.unblock)
		<-errs
		t.Fatal("stdout writer was not reached")
	}
	select {
	case <-stderr.wrote:
		close(stdout.unblock)
	case <-time.After(500 * time.Millisecond):
		close(stdout.unblock)
		<-errs
		t.Fatal("different stderr writer was blocked behind stdout")
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}

type giftClipBlockingWriter struct {
	started chan struct{}
	unblock chan struct{}
	once    sync.Once
}

func (writer *giftClipBlockingWriter) Write(data []byte) (int, error) {
	writer.once.Do(func() { close(writer.started) })
	<-writer.unblock
	return len(data), nil
}

type giftClipSignalingWriter struct {
	wrote chan struct{}
	once  sync.Once
}

func (writer *giftClipSignalingWriter) Write(data []byte) (int, error) {
	writer.once.Do(func() { close(writer.wrote) })
	return len(data), nil
}

func TestGiftClipWindowsProcessRunnerClosesJobWhenTerminateFails(t *testing.T) {
	path := t.TempDir() + "\\pids.txt"
	api := giftClipDefaultWindowsAPI
	terminateErr := errors.New("terminate failed")
	api.terminateJob = func(syscall.Handle) error { return terminateErr }
	closeJob := api.closeJob
	closed := 0
	api.closeJob = func(handle syscall.Handle) {
		closed++
		closeJob(handle)
	}
	runner := giftClipWindowsProcessRunner{api: api}
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		errs <- runner.Run(ctx, os.Args[0], []string{
			"-test.run=^TestGiftClipWindowsProcessHelper$",
			"-test.gift_clip_helper_file=" + path,
		}, io.Discard, io.Discard)
	}()
	pids := waitForGiftClipHelperPIDs(t, path)
	cancel()
	select {
	case err := <-errs:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, terminateErr) {
			t.Fatalf("Run error = %v, want cancellation and terminate failure", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after TerminateJobObject failure")
	}
	if closed != 1 {
		t.Fatalf("job close calls = %d, want 1", closed)
	}
	for _, pid := range pids {
		if !waitForGiftClipProcessGone(pid, 3*time.Second) {
			t.Fatalf("recorded helper PID %d survived close fallback", pid)
		}
	}
}

func TestGiftClipWindowsProcessRunnerKillsSuspendedProcessWhenAssignmentFails(t *testing.T) {
	api := giftClipDefaultWindowsAPI
	assignErr := errors.New("assignment failed")
	api.assignProcess = func(syscall.Handle, syscall.Handle) error { return assignErr }
	started := make(chan *exec.Cmd, 1)
	startSuspended := api.startSuspended
	api.startSuspended = func(path string, args []string, stdout, stderr io.Writer) (*giftClipStartedProcess, error) {
		process, err := startSuspended(path, args, stdout, stderr)
		if process != nil {
			started <- process.command
		}
		return process, err
	}
	jobCloses, processCloses, resumes, kills, waits := 0, 0, 0, 0, 0
	closeJob, closeProcess, resume := api.closeJob, api.closeProcess, api.resumePrimaryThread
	kill, wait := api.killProcess, api.waitProcess
	api.closeJob = func(handle syscall.Handle) { jobCloses++; closeJob(handle) }
	api.closeProcess = func(handle syscall.Handle) { processCloses++; closeProcess(handle) }
	api.resumePrimaryThread = func(pid int) error { resumes++; return resume(pid) }
	api.killProcess = func(command *exec.Cmd) error { kills++; return kill(command) }
	api.waitProcess = func(command *exec.Cmd) error { waits++; return wait(command) }
	runner := giftClipWindowsProcessRunner{api: api}
	errs := make(chan error, 1)
	go func() {
		errs <- runner.Run(context.Background(), os.Args[0], []string{"-test.run=^TestGiftClipWindowsProcessHelper$"}, io.Discard, io.Discard)
	}()
	command := <-started
	recordedPID := command.Process.Pid
	select {
	case err := <-errs:
		if !errors.Is(err, assignErr) {
			t.Fatalf("Run error = %v, want assignment error", err)
		}
	case <-time.After(500 * time.Millisecond):
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("Run did not return after assignment failure for recorded PID %d", recordedPID)
	}
	if !waitForGiftClipProcessGone(recordedPID, 3*time.Second) {
		t.Fatalf("recorded suspended PID %d survived assignment failure", recordedPID)
	}
	if jobCloses != 1 || processCloses != 1 || resumes != 0 || kills != 1 || waits != 1 {
		t.Fatalf("job closes=%d process closes=%d resumes=%d kills=%d waits=%d, want 1, 1, 0, 1, 1", jobCloses, processCloses, resumes, kills, waits)
	}
}

func TestGiftClipWindowsProcessRunnerAssignmentKillFailureClosesCopiersAndReleases(t *testing.T) {
	api := giftClipDefaultWindowsAPI
	assignErr := errors.New("assignment failed")
	killErr := errors.New("kill failed")
	releaseErr := errors.New("release failed")
	allowAssignmentFailure := make(chan struct{})
	api.assignProcess = func(syscall.Handle, syscall.Handle) error {
		<-allowAssignmentFailure
		return assignErr
	}
	startedProcesses := make(chan *giftClipStartedProcess, 1)
	startSuspended := api.startSuspended
	api.startSuspended = func(path string, args []string, stdout, stderr io.Writer) (*giftClipStartedProcess, error) {
		started, err := startSuspended(path, args, stdout, stderr)
		if started != nil {
			startedProcesses <- started
		}
		return started, err
	}
	kills, waits, releases := 0, 0, 0
	release := api.releaseProcess
	api.killProcess = func(*exec.Cmd) error {
		kills++
		return killErr
	}
	api.waitProcess = func(*exec.Cmd) error { waits++; return nil }
	api.releaseProcess = func(command *exec.Cmd) error {
		releases++
		return errors.Join(release(command), releaseErr)
	}
	runner := giftClipWindowsProcessRunner{api: api}
	errs := make(chan error, 1)
	go func() {
		errs <- runner.Run(context.Background(), os.Args[0], []string{"-test.run=^TestGiftClipWindowsProcessHelper$"}, io.Discard, io.Discard)
	}()
	started := <-startedProcesses
	recordedPID := started.command.Process.Pid
	cleanupPID := recordedPID
	defer func() {
		if cleanupPID != 0 {
			_ = terminateRecordedGiftClipProcess(cleanupPID)
		}
	}()
	if giftClipWindowsProcessIsGone(recordedPID) {
		t.Fatalf("recorded suspended PID %d exited before the injected failures", recordedPID)
	}
	for name, pipe := range map[string]*giftClipProcessPipe{"stdout": started.stdout, "stderr": started.stderr} {
		select {
		case <-pipe.done:
			t.Fatalf("%s copier completed while recorded PID %d was suspended", name, recordedPID)
		default:
		}
		if _, err := pipe.reader.Stat(); err != nil {
			t.Fatalf("%s parent pipe was not open before cleanup: %v", name, err)
		}
	}
	close(allowAssignmentFailure)
	select {
	case err := <-errs:
		if !errors.Is(err, assignErr) || !errors.Is(err, killErr) || !errors.Is(err, releaseErr) {
			t.Fatalf("Run error = %v, want assignment, kill, and release failure", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run did not return after injected kill failure")
	}
	if giftClipWindowsProcessIsGone(recordedPID) {
		t.Fatalf("recorded suspended PID %d was terminated before independent test cleanup", recordedPID)
	}
	for name, pipe := range map[string]*giftClipProcessPipe{"stdout": started.stdout, "stderr": started.stderr} {
		select {
		case <-pipe.done:
		default:
			t.Fatalf("%s copier did not complete before Run returned", name)
		}
		assertGiftClipPipeFileClosed(t, name+" reader", pipe.reader)
		assertGiftClipPipeFileClosed(t, name+" writer", pipe.writer)
	}
	if kills != 1 || waits != 0 || releases != 1 {
		t.Fatalf("kills=%d waits=%d releases=%d, want 1, 0, 1", kills, waits, releases)
	}
	if err := terminateRecordedGiftClipProcess(recordedPID); err != nil {
		t.Fatalf("terminate recorded PID %d: %v", recordedPID, err)
	}
	cleanupPID = 0
}

func TestGiftClipWindowsProcessRunnerAssignmentWaitFailureIsReported(t *testing.T) {
	api := giftClipDefaultWindowsAPI
	assignErr, waitErr := errors.New("assignment failed"), errors.New("wait failed")
	api.assignProcess = func(syscall.Handle, syscall.Handle) error { return assignErr }
	started := make(chan *exec.Cmd, 1)
	startSuspended := api.startSuspended
	api.startSuspended = func(path string, args []string, stdout, stderr io.Writer) (*giftClipStartedProcess, error) {
		process, err := startSuspended(path, args, stdout, stderr)
		if process != nil {
			started <- process.command
		}
		return process, err
	}
	kills, waits := 0, 0
	kill, wait := api.killProcess, api.waitProcess
	api.killProcess = func(command *exec.Cmd) error { kills++; return kill(command) }
	api.waitProcess = func(command *exec.Cmd) error {
		waits++
		_ = wait(command)
		return waitErr
	}
	runner := giftClipWindowsProcessRunner{api: api}
	errs := make(chan error, 1)
	go func() {
		errs <- runner.Run(context.Background(), os.Args[0], []string{"-test.run=^TestGiftClipWindowsProcessHelper$"}, io.Discard, io.Discard)
	}()
	command := <-started
	recordedPID := command.Process.Pid
	select {
	case err := <-errs:
		if !errors.Is(err, assignErr) || !errors.Is(err, waitErr) {
			t.Fatalf("Run error = %v, want assignment and wait failure", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run did not return after injected wait failure")
	}
	if !waitForGiftClipProcessGone(recordedPID, 3*time.Second) {
		t.Fatalf("recorded PID %d survived injected wait failure", recordedPID)
	}
	if kills != 1 || waits != 1 {
		t.Fatalf("kills=%d waits=%d, want 1, 1", kills, waits)
	}
}

func TestGiftClipWindowsProcessHelper(t *testing.T) {
	if *giftClipProcessHelperFile == "" {
		return
	}
	if err := os.WriteFile(*giftClipProcessHelperFile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestGiftClipWindowsProcessChildHelper$", "-test.gift_clip_helper_file="+*giftClipProcessHelperFile)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(*giftClipProcessHelperFile, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintln(file, child.Process.Pid)
	closeErr := file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("child unexpectedly exited")
	}
}

func TestGiftClipWindowsProcessChildHelper(t *testing.T) {
	if *giftClipProcessHelperFile == "" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestGiftClipWindowsProcessOutputHelper(t *testing.T) {
	if !*giftClipProcessOutputHelper {
		return
	}
	if *giftClipProcessOutputBursts > 0 {
		var writers sync.WaitGroup
		writers.Add(2)
		go func() {
			defer writers.Done()
			for index := 0; index < *giftClipProcessOutputBursts; index++ {
				_, _ = fmt.Fprintln(os.Stdout, "stdout sentinel")
			}
		}()
		go func() {
			defer writers.Done()
			for index := 0; index < *giftClipProcessOutputBursts; index++ {
				_, _ = fmt.Fprintln(os.Stderr, "stderr sentinel")
			}
		}()
		writers.Wait()
		return
	}
	_, _ = fmt.Fprint(os.Stdout, "stdout sentinel")
	_, _ = fmt.Fprint(os.Stderr, "stderr sentinel")
}

func waitForGiftClipHelperPIDs(t *testing.T, path string) []int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			lines := strings.Fields(string(data))
			if len(lines) == 2 {
				pids := make([]int, 0, 2)
				for _, line := range lines {
					pid, err := strconv.Atoi(line)
					if err != nil {
						t.Fatal(err)
					}
					pids = append(pids, pid)
				}
				return pids
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the helper process tree")
	return nil
}

func waitForGiftClipProcessGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for !giftClipWindowsProcessIsGone(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	return giftClipWindowsProcessIsGone(pid)
}

func assertGiftClipPipeFileClosed(t *testing.T, name string, file *os.File) {
	t.Helper()
	if got := file.Fd(); got != uintptr(syscall.InvalidHandle) {
		t.Fatalf("%s handle = %#x, want syscall.InvalidHandle", name, got)
	}
}

func terminateRecordedGiftClipProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if process.Pid != pid {
		_ = process.Release()
		return fmt.Errorf("found PID %d, want recorded PID %d", process.Pid, pid)
	}
	if err := process.Kill(); err != nil {
		_ = process.Release()
		return err
	}
	_, err = process.Wait()
	return err
}
