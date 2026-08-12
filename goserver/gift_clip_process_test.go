//go:build windows

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

var giftClipProcessHelperFile = flag.String("test.gift_clip_helper_file", "", "gift clip process helper pid file")

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
	command, err := startGiftClipProcessSuspended(os.Args[0], []string{
		"-test.run=^TestGiftClipWindowsProcessHelper$",
		"-test.gift_clip_helper_file=" + path,
	}, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	recordedPID := command.Process.Pid
	defer func() {
		if recordedPID != command.Process.Pid {
			t.Fatal("attempted to terminate an unrecorded process")
		}
		_ = command.Process.Kill()
		_ = command.Wait()
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
