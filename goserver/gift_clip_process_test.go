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
var giftClipProcessExitCode = flag.Int("test.gift_clip_exit_code", 0, "exit code for the gift clip process helper")

func TestConfigureGiftClipWindowsCommandStartsSuspendedWithoutConsole(t *testing.T) {
	command := exec.Command(os.Args[0])
	configureGiftClipWindowsCommand(command)
	if command.SysProcAttr == nil {
		t.Fatal("SysProcAttr is nil")
	}
	want := uint32(createSuspended | createNoWindow)
	if command.SysProcAttr.CreationFlags != want {
		t.Fatalf("CreationFlags = %#x, want %#x", command.SysProcAttr.CreationFlags, want)
	}
}

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
		_ = waitGiftClipStartedProcess(giftClipDefaultWindowsAPI, started, true)
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

func TestGiftClipWindowsProcessRunnerNormalWaitJoinsWaitAndCopierErrors(t *testing.T) {
	tests := []struct {
		name     string
		waitErr  func(*testing.T) error
		copyErr  error
		closeErr error
	}{
		{
			name:    "ExitError and writer error",
			waitErr: newGiftClipInjectedExitError,
			copyErr: errors.New("stdout writer failed"),
		},
		{
			name:     "non-ExitError and reader close error",
			waitErr:  func(*testing.T) error { return errors.New("Wait failed") },
			closeErr: errors.New("stdout reader close failed"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			api := giftClipDefaultWindowsAPI
			injectedWaitErr := test.waitErr(t)
			waitProcess := api.waitProcess
			api.waitProcess = func(command *exec.Cmd) error {
				return errors.Join(waitProcess(command), injectedWaitErr)
			}
			if test.closeErr != nil {
				startSuspended := api.startSuspended
				api.startSuspended = func(path string, args []string, stdout, stderr io.Writer) (*giftClipStartedProcess, error) {
					started, err := startSuspended(path, args, stdout, stderr)
					if started != nil {
						control := started.stdout.control
						closeReader := control.closeReader
						control.closeReader = func(reader *os.File) error {
							return errors.Join(closeReader(reader), test.closeErr)
						}
						started.stdout.control = control
					}
					return started, err
				}
			}
			stdout := io.Writer(io.Discard)
			if test.copyErr != nil {
				stdout = giftClipErrorWriter{err: test.copyErr}
			}
			err := (giftClipWindowsProcessRunner{api: api}).Run(context.Background(), os.Args[0], []string{
				"-test.run=^TestGiftClipWindowsProcessOutputHelper$",
				"-test.gift_clip_output_helper=true",
			}, stdout, io.Discard)
			for name, expected := range map[string]error{
				"Wait":        injectedWaitErr,
				"writer/copy": test.copyErr,
				"close":       test.closeErr,
			} {
				if expected != nil && !errors.Is(err, expected) {
					t.Errorf("Run error = %v, missing %s error %v", err, name, expected)
				}
				if expected != nil && strings.Count(err.Error(), expected.Error()) != 1 {
					t.Errorf("Run error = %q, want one %s occurrence %q", err, name, expected)
				}
			}
		})
	}
}

func TestGiftClipWindowsProcessRunnerCancellationJoinsDrainErrorsAndFiltersForcedExit(t *testing.T) {
	tests := []struct {
		name         string
		terminateErr error
		waitErr      error
	}{
		{name: "TerminateJobObject success retains copier error"},
		{
			name:    "TerminateJobObject success retains non-ExitError Wait failure",
			waitErr: errors.New("cancel Wait failed"),
		},
		{
			name:         "TerminateJobObject failure retains terminate and drain failures",
			terminateErr: errors.New("TerminateJobObject failed"),
			waitErr:      errors.New("close fallback Wait failed"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			api := giftClipDefaultWindowsAPI
			closeErr := errors.New("cancel drain reader close failed")
			resumed := make(chan struct{})
			resumePrimaryThread := api.resumePrimaryThread
			api.resumePrimaryThread = func(pid int) error {
				err := resumePrimaryThread(pid)
				if err == nil {
					close(resumed)
				}
				return err
			}
			startSuspended := api.startSuspended
			api.startSuspended = func(path string, args []string, stdout, stderr io.Writer) (*giftClipStartedProcess, error) {
				started, err := startSuspended(path, args, stdout, stderr)
				if started != nil {
					control := started.stdout.control
					closeReader := control.closeReader
					control.closeReader = func(reader *os.File) error {
						return errors.Join(closeReader(reader), closeErr)
					}
					started.stdout.control = control
				}
				return started, err
			}
			waitProcess := api.waitProcess
			api.waitProcess = func(command *exec.Cmd) error {
				waitErr := waitProcess(command)
				if test.waitErr != nil {
					return test.waitErr
				}
				return waitErr
			}
			if test.terminateErr != nil {
				api.terminateJob = func(syscall.Handle) error { return test.terminateErr }
			}

			ctx, cancel := context.WithCancel(context.Background())
			errs := make(chan error, 1)
			helperFile := t.TempDir() + "\\live-child"
			go func() {
				errs <- (giftClipWindowsProcessRunner{api: api}).Run(ctx, os.Args[0], []string{
					"-test.run=^TestGiftClipWindowsProcessChildHelper$",
					"-test.gift_clip_helper_file=" + helperFile,
				}, io.Discard, io.Discard)
			}()
			select {
			case <-resumed:
			case <-time.After(3 * time.Second):
				cancel()
				t.Fatal("live helper did not resume")
			}
			cancel()
			var err error
			select {
			case err = <-errs:
			case <-time.After(3 * time.Second):
				t.Fatal("Run did not return after cancellation")
			}
			for name, expected := range map[string]error{
				"context":   context.Canceled,
				"terminate": test.terminateErr,
				"Wait":      test.waitErr,
				"close":     closeErr,
			} {
				if expected != nil && !errors.Is(err, expected) {
					t.Errorf("Run error = %v, missing %s error %v", err, name, expected)
				}
				if expected != nil && strings.Count(err.Error(), expected.Error()) != 1 {
					t.Errorf("Run error = %q, want one %s occurrence %q", err, name, expected)
				}
			}
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				t.Errorf("Run error = %v, retained expected forced-termination ExitError %v", err, exitErr)
			}
		})
	}
}

type giftClipErrorWriter struct {
	err error
}

func (writer giftClipErrorWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

func TestGiftClipNamedPipeUsesRandomNonceLocalSecurityAndWriterOnlyInheritance(t *testing.T) {
	api := giftClipDefaultNamedPipeAPI
	nonces := [][16]byte{
		{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f},
		{0xf0, 0xe1, 0xd2, 0xc3, 0xb4, 0xa5, 0x96, 0x87, 0x78, 0x69, 0x5a, 0x4b, 0x3c, 0x2d, 0x1e, 0x0f},
	}
	nonceIndex := 0
	api.fillNonce = func(target []byte) error {
		if len(target) != 16 {
			return fmt.Errorf("nonce length = %d, want 16", len(target))
		}
		copy(target, nonces[nonceIndex][:])
		nonceIndex++
		return nil
	}
	securityBuilds, securityReleases := 0, 0
	buildSecurity := api.buildSecurity
	api.buildSecurity = func() (*giftClipPipeSecurity, error) {
		securityBuilds++
		security, err := buildSecurity()
		if security == nil {
			return nil, err
		}
		release := security.localFree
		security.localFree = func(descriptor uintptr) error {
			securityReleases++
			return release(descriptor)
		}
		return security, err
	}
	type createCall struct {
		name       string
		openMode   uint32
		pipeMode   uint32
		attributes syscall.SecurityAttributes
	}
	createCalls := make([]createCall, 0, len(nonces))
	createNamedPipe := api.createNamedPipe
	api.createNamedPipe = func(name string, openMode, pipeMode uint32, attributes *syscall.SecurityAttributes) (syscall.Handle, error) {
		if securityReleases != len(createCalls) {
			return syscall.InvalidHandle, errors.New("security descriptor released before CreateNamedPipeW")
		}
		if attributes == nil {
			return syscall.InvalidHandle, errors.New("CreateNamedPipeW security attributes are nil")
		}
		createCalls = append(createCalls, createCall{name: name, openMode: openMode, pipeMode: pipeMode, attributes: *attributes})
		return createNamedPipe(name, openMode, pipeMode, attributes)
	}
	type writerCall struct {
		access     uint32
		attributes syscall.SecurityAttributes
	}
	writerCalls := make([]writerCall, 0, len(nonces))
	openWriter := api.openWriter
	api.openWriter = func(name string, access uint32, attributes *syscall.SecurityAttributes) (syscall.Handle, error) {
		if attributes == nil {
			return syscall.InvalidHandle, errors.New("CreateFile writer security attributes are nil")
		}
		writerCalls = append(writerCalls, writerCall{access: access, attributes: *attributes})
		return openWriter(name, access, attributes)
	}

	for range nonces {
		reader, writer, err := newGiftClipOverlappedPipeWithAPI(api)
		if err != nil {
			t.Fatal(err)
		}
		if err := errors.Join(writer.Close(), reader.Close()); err != nil {
			t.Fatal(err)
		}
	}
	wantNames := []string{
		`\\.\pipe\bilibili-gift-clip-000102030405060708090a0b0c0d0e0f`,
		`\\.\pipe\bilibili-gift-clip-f0e1d2c3b4a5968778695a4b3c2d1e0f`,
	}
	if len(createCalls) != len(wantNames) || len(writerCalls) != len(wantNames) {
		t.Fatalf("create calls=%d writer calls=%d, want %d", len(createCalls), len(writerCalls), len(wantNames))
	}
	for index, call := range createCalls {
		if call.name != wantNames[index] {
			t.Errorf("pipe name[%d] = %q, want %q", index, call.name, wantNames[index])
		}
		wantOpenMode := uint32(pipeAccessInbound | syscall.FILE_FLAG_OVERLAPPED | fileFlagFirstPipeInstance)
		if call.openMode != wantOpenMode {
			t.Errorf("CreateNamedPipeW open mode = %#x, want %#x", call.openMode, wantOpenMode)
		}
		if call.pipeMode&pipeRejectRemoteClients == 0 {
			t.Errorf("CreateNamedPipeW pipe mode = %#x, missing PIPE_REJECT_REMOTE_CLIENTS", call.pipeMode)
		}
		if call.attributes.SecurityDescriptor == 0 || call.attributes.InheritHandle != 0 {
			t.Errorf("reader security attributes = %#v, want non-nil descriptor and non-inheritable handle", call.attributes)
		}
		if writerCalls[index].access != giftClipPipeClientWriteAccess {
			t.Errorf("writer access = %#x, want constrained write access %#x", writerCalls[index].access, giftClipPipeClientWriteAccess)
		}
		if writerCalls[index].attributes.InheritHandle != 1 {
			t.Errorf("writer security attributes = %#v, want inheritable handle", writerCalls[index].attributes)
		}
	}
	if createCalls[0].name == createCalls[1].name {
		t.Fatalf("independent 128-bit nonces produced the same pipe name %q", createCalls[0].name)
	}
	if nonceIndex != len(nonces) || securityBuilds != len(nonces) || securityReleases != len(nonces) {
		t.Fatalf("nonce calls=%d security builds=%d releases=%d, want %d each", nonceIndex, securityBuilds, securityReleases, len(nonces))
	}
}

func TestGiftClipPipeSecurityRestrictsDACLToCurrentUserAndReleasesDescriptor(t *testing.T) {
	const userSID = "S-1-5-21-111-222-333-444"
	const descriptor = uintptr(0x1234)
	freeErr := errors.New("LocalFree failed")
	converted := ""
	frees := 0
	security, err := newGiftClipPipeSecurityWithAPI(giftClipPipeSecurityAPI{
		currentUserSID: func() (string, error) { return userSID, nil },
		convertSDDL: func(sddl string) (uintptr, error) {
			converted = sddl
			return descriptor, nil
		},
		localFree: func(got uintptr) error {
			frees++
			if got != descriptor {
				return fmt.Errorf("LocalFree descriptor = %#x, want %#x", got, descriptor)
			}
			return freeErr
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if converted != "D:P(A;;0x00120192;;;"+userSID+")" {
		t.Fatalf("pipe SDDL = %q, want protected current-user write DACL without FILE_CREATE_PIPE_INSTANCE", converted)
	}
	if security.attributes.SecurityDescriptor != descriptor || security.attributes.InheritHandle != 0 {
		t.Fatalf("security attributes = %#v", security.attributes)
	}
	if err := security.release(); !errors.Is(err, freeErr) {
		t.Fatalf("first release error = %v, want %v", err, freeErr)
	}
	if err := security.release(); !errors.Is(err, freeErr) {
		t.Fatalf("second release error = %v, want retained %v", err, freeErr)
	}
	if frees != 1 {
		t.Fatalf("LocalFree calls = %d, want 1", frees)
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

func TestGiftClipWindowsProcessRunnerSerializesSameUncomparableOutputWriter(t *testing.T) {
	const bursts = 4096
	tests := []struct {
		name   string
		writer func(*bytes.Buffer) io.Writer
	}{
		{
			name: "dynamic type contains slice",
			writer: func(destination *bytes.Buffer) io.Writer {
				return giftClipUncomparableSliceWriter{destination: destination, marker: []byte("uncomparable")}
			},
		},
		{
			name: "dynamic type contains function",
			writer: func(destination *bytes.Buffer) io.Writer {
				return giftClipUncomparableFunctionWriter{destination: destination, marker: func() {}}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			writer := test.writer(&output)
			if err := newGiftClipProcessRunner().Run(context.Background(), os.Args[0], []string{
				"-test.run=^TestGiftClipWindowsProcessOutputHelper$",
				"-test.gift_clip_output_helper=true",
				"-test.gift_clip_output_bursts=" + strconv.Itoa(bursts),
			}, writer, writer); err != nil {
				t.Fatal(err)
			}
			if got := bytes.Count(output.Bytes(), []byte("stdout sentinel\n")); got != bursts {
				t.Fatalf("stdout sentinel count = %d, want %d", got, bursts)
			}
			if got := bytes.Count(output.Bytes(), []byte("stderr sentinel\n")); got != bursts {
				t.Fatalf("stderr sentinel count = %d, want %d", got, bursts)
			}
		})
	}
}

func TestGiftClipWindowsProcessRunnerPreservesDistinctUncomparableOutputDestinations(t *testing.T) {
	var stdout, stderr bytes.Buffer
	stdoutWriter := giftClipUncomparableSliceWriter{destination: &stdout, marker: []byte("stdout")}
	stderrWriter := giftClipUncomparableSliceWriter{destination: &stderr, marker: []byte("stderr")}
	if err := newGiftClipProcessRunner().Run(context.Background(), os.Args[0], []string{
		"-test.run=^TestGiftClipWindowsProcessOutputHelper$",
		"-test.gift_clip_output_helper=true",
	}, stdoutWriter, stderrWriter); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); !strings.Contains(got, "stdout sentinel") || strings.Contains(got, "stderr sentinel") {
		t.Fatalf("stdout = %q, want only stdout output", got)
	}
	if got := stderr.String(); !strings.Contains(got, "stderr sentinel") || strings.Contains(got, "stdout sentinel") {
		t.Fatalf("stderr = %q, want only stderr output", got)
	}
}

type giftClipUncomparableSliceWriter struct {
	destination *bytes.Buffer
	marker      []byte
}

func (writer giftClipUncomparableSliceWriter) Write(data []byte) (int, error) {
	return writer.destination.Write(data)
}

type giftClipUncomparableFunctionWriter struct {
	destination *bytes.Buffer
	marker      func()
}

func (writer giftClipUncomparableFunctionWriter) Write(data []byte) (int, error) {
	return writer.destination.Write(data)
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

func TestGiftClipWindowsProcessRunnerDeadlineFailureCancelsExactReaderAndReturnsBounded(t *testing.T) {
	api := giftClipDefaultWindowsAPI
	assignErr := errors.New("assignment failed")
	killErr := errors.New("kill failed")
	cancelErr := errors.New("cancel read failed")
	closeErr := errors.New("close reader failed")
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
	api.killProcess = func(*exec.Cmd) error { return killErr }
	release := api.releaseProcess
	api.releaseProcess = func(command *exec.Cmd) error { return release(command) }

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
	pipe := started.stdout
	readerHandle, err := giftClipTestFileHandle(pipe.reader)
	if err != nil {
		t.Fatal(err)
	}
	cancelledHandles := make(chan syscall.Handle, 1)
	control := pipe.control
	realCancelRead := control.cancelRead
	realCloseReader := control.closeReader
	control.setReadDeadline = func(*os.File, time.Time) error { return os.ErrNoDeadline }
	control.cancelRead = func(reader *os.File) (syscall.Handle, error) {
		handle, err := realCancelRead(reader)
		cancelledHandles <- handle
		return handle, errors.Join(err, cancelErr)
	}
	control.closeReader = func(reader *os.File) error {
		return errors.Join(realCloseReader(reader), closeErr)
	}
	pipe.control = control

	if giftClipWindowsProcessIsGone(recordedPID) {
		t.Fatalf("recorded suspended PID %d exited before cleanup", recordedPID)
	}
	select {
	case <-pipe.done:
		t.Fatal("stdout copier completed before the injected deadline failure")
	default:
	}
	close(allowAssignmentFailure)
	select {
	case err := <-errs:
		for name, expected := range map[string]error{
			"assignment": assignErr,
			"kill":       killErr,
			"deadline":   os.ErrNoDeadline,
			"cancel":     cancelErr,
			"close":      closeErr,
		} {
			if !errors.Is(err, expected) {
				t.Errorf("Run error = %v, missing %s error %v", err, name, expected)
			}
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run blocked after SetReadDeadline failed for a live child")
	}
	select {
	case got := <-cancelledHandles:
		if got != readerHandle {
			t.Fatalf("CancelIoEx handle = %#x, want exact stdout reader %#x", got, readerHandle)
		}
	default:
		t.Fatal("deadline failure did not cancel the pending reader")
	}
	for name, outputPipe := range map[string]*giftClipProcessPipe{"stdout": started.stdout, "stderr": started.stderr} {
		select {
		case <-outputPipe.done:
		default:
			t.Fatalf("%s copier did not reach a terminal state before Run returned", name)
		}
		assertGiftClipPipeFileClosed(t, name+" reader", outputPipe.reader)
		assertGiftClipPipeFileClosed(t, name+" writer", outputPipe.writer)
	}
	if giftClipWindowsProcessIsGone(recordedPID) {
		t.Fatalf("recorded suspended PID %d was terminated before independent cleanup", recordedPID)
	}
	if err := terminateRecordedGiftClipProcess(recordedPID); err != nil {
		t.Fatalf("terminate recorded PID %d: %v", recordedPID, err)
	}
	cleanupPID = 0
}

func TestGiftClipProcessPipeDeadlineTimeoutFallsBackToCancelAndClose(t *testing.T) {
	pipe, err := newGiftClipProcessPipe(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pipe.closeBeforeStart() }()
	cancelErr := errors.New("fallback cancel failed")
	closeErr := errors.New("fallback close failed")
	waitErr := errors.New("copier wait timed out")
	writerCloseErr := errors.New("writer close failed before timeout")
	cancelledHandles := make(chan syscall.Handle, 1)
	waits := 0
	control := pipe.control
	closeReader := control.closeReader
	control.setReadDeadline = func(*os.File, time.Time) error { return nil }
	control.cancelRead = func(reader *os.File) (syscall.Handle, error) {
		handle, err := giftClipTestFileHandle(reader)
		cancelledHandles <- handle
		return handle, errors.Join(err, cancelErr)
	}
	control.closeReader = func(reader *os.File) error {
		return errors.Join(closeReader(reader), closeErr)
	}
	control.waitForDone = func(<-chan struct{}) error {
		waits++
		return waitErr
	}
	pipe.control = control
	pipe.writerCloseErr = writerCloseErr

	pipe.requestStop()
	err = pipe.finishStop()
	for name, expected := range map[string]error{"wait": waitErr, "cancel": cancelErr, "close": closeErr, "writer close": writerCloseErr} {
		if !errors.Is(err, expected) {
			t.Errorf("finishStop error = %v, missing %s error %v", err, name, expected)
		}
	}
	select {
	case <-cancelledHandles:
	default:
		t.Fatal("deadline timeout did not fall back to cancelling the reader")
	}
	if waits != 2 {
		t.Fatalf("bounded copier waits = %d, want deadline wait and one post-close wait", waits)
	}
	assertGiftClipPipeFileClosed(t, "fallback reader", pipe.reader)
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

func TestGiftClipWindowsProcessExitHelper(t *testing.T) {
	if *giftClipProcessExitCode == 0 {
		return
	}
	os.Exit(*giftClipProcessExitCode)
}

func newGiftClipInjectedExitError(t *testing.T) error {
	t.Helper()
	command := exec.Command(os.Args[0],
		"-test.run=^TestGiftClipWindowsProcessExitHelper$",
		"-test.gift_clip_exit_code=23",
	)
	err := command.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("exit helper error = %v, want *exec.ExitError", err)
	}
	return exitErr
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

func giftClipTestFileHandle(file *os.File) (syscall.Handle, error) {
	raw, err := file.SyscallConn()
	if err != nil {
		return 0, err
	}
	var handle syscall.Handle
	if err := raw.Control(func(rawHandle uintptr) { handle = syscall.Handle(rawHandle) }); err != nil {
		return 0, err
	}
	return handle, nil
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
