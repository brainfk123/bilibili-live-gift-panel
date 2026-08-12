//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const giftClipDefaultEncoderMode = giftClipEncoderHardware

const (
	jobObjectExtendedLimitInformationClass = 9
	jobObjectLimitKillOnJobClose           = 0x00002000
	createSuspended                        = 0x00000004
	pipeAccessInbound                      = 0x00000001
	fileFlagFirstPipeInstance              = 0x00080000
	processTerminate                       = 0x0001
	processSetQuota                        = 0x0100
	processQueryLimitedInformation         = 0x1000
	threadSuspendResume                    = 0x0002
	th32csSnapThread                       = 0x00000004
)

var (
	giftClipKernel32                 = syscall.NewLazyDLL("kernel32.dll")
	giftClipCreateJobObjectW         = giftClipKernel32.NewProc("CreateJobObjectW")
	giftClipSetInformationJobObject  = giftClipKernel32.NewProc("SetInformationJobObject")
	giftClipAssignProcessToJobObject = giftClipKernel32.NewProc("AssignProcessToJobObject")
	giftClipTerminateJobObject       = giftClipKernel32.NewProc("TerminateJobObject")
	giftClipCloseHandle              = giftClipKernel32.NewProc("CloseHandle")
	giftClipOpenProcess              = giftClipKernel32.NewProc("OpenProcess")
	giftClipCreateToolhelp32Snapshot = giftClipKernel32.NewProc("CreateToolhelp32Snapshot")
	giftClipThread32First            = giftClipKernel32.NewProc("Thread32First")
	giftClipThread32Next             = giftClipKernel32.NewProc("Thread32Next")
	giftClipOpenThread               = giftClipKernel32.NewProc("OpenThread")
	giftClipResumeThread             = giftClipKernel32.NewProc("ResumeThread")
	giftClipCreateNamedPipeW         = giftClipKernel32.NewProc("CreateNamedPipeW")
)

var giftClipPipeSequence atomic.Uint64

type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type jobObjectIoCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobObjectExtendedLimitInformation struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IoInfo                jobObjectIoCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

type giftClipWindowsAPI struct {
	createJobObject     func() (syscall.Handle, error)
	startSuspended      func(string, []string, io.Writer, io.Writer) (*giftClipStartedProcess, error)
	openProcess         func(int) (syscall.Handle, error)
	assignProcess       func(syscall.Handle, syscall.Handle) error
	resumePrimaryThread func(int) error
	terminateJob        func(syscall.Handle) error
	closeJob            func(syscall.Handle)
	closeProcess        func(syscall.Handle)
	killProcess         func(*exec.Cmd) error
	waitProcess         func(*exec.Cmd) error
	releaseProcess      func(*exec.Cmd) error
}

var giftClipDefaultWindowsAPI = giftClipWindowsAPI{
	createJobObject:     createGiftClipJobObject,
	startSuspended:      startGiftClipProcessSuspended,
	openProcess:         openGiftClipProcess,
	assignProcess:       assignGiftClipProcessToJob,
	resumePrimaryThread: resumeGiftClipPrimaryThread,
	terminateJob:        terminateGiftClipJob,
	closeJob:            closeGiftClipHandle,
	closeProcess:        closeGiftClipHandle,
	killProcess:         killGiftClipProcess,
	waitProcess:         waitGiftClipProcess,
	releaseProcess:      releaseGiftClipProcess,
}

type giftClipWindowsProcessRunner struct {
	api giftClipWindowsAPI
}

// giftClipStartedProcess owns the output pipes and their copier goroutines.
// exec.Cmd sees *os.File writers, so os/exec does not create hidden pipes that
// only Cmd.Wait can close. That lets the assignment-failure path stop copying
// before releasing a process that could not be killed or waited for.
type giftClipStartedProcess struct {
	command *exec.Cmd
	stdout  *giftClipProcessPipe
	stderr  *giftClipProcessPipe
}

type giftClipProcessPipe struct {
	reader      *os.File
	writer      *os.File
	destination io.Writer
	done        chan struct{}

	readerCloseOnce sync.Once
	readerCloseErr  error
	writerCloseOnce sync.Once
	writerCloseErr  error
	stopOnce        sync.Once
	stopErr         error
	copyErr         error
}

type giftClipSerializedWriter struct {
	destination io.Writer
	mu          sync.Mutex
}

func newGiftClipProcessRunner() giftClipProcessRunner {
	return giftClipWindowsProcessRunner{api: giftClipDefaultWindowsAPI}
}

func (runner giftClipWindowsProcessRunner) Run(ctx context.Context, path string, args []string, stdout, stderr io.Writer) error {
	api := runner.api
	if api.createJobObject == nil {
		api = giftClipDefaultWindowsAPI
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	job, err := api.createJobObject()
	if err != nil {
		return err
	}
	defer func() {
		if job != 0 {
			api.closeJob(job)
		}
	}()

	started, err := api.startSuspended(absPath, args, stdout, stderr)
	if err != nil {
		return err
	}
	command := started.command
	process, err := api.openProcess(command.Process.Pid)
	if err != nil {
		return cleanupGiftClipUnassignedProcess(api, started, err)
	}
	defer api.closeProcess(process)
	if err := api.assignProcess(job, process); err != nil {
		return cleanupGiftClipUnassignedProcess(api, started, err)
	}
	if err := api.resumePrimaryThread(command.Process.Pid); err != nil {
		api.closeJob(job)
		job = 0
		return joinGiftClipProcessError(err, "wait after resume failure", waitGiftClipStartedProcess(api, started))
	}

	done := make(chan error, 1)
	go func() { done <- waitGiftClipStartedProcess(api, started) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		terminateErr := api.terminateJob(job)
		if terminateErr != nil {
			api.closeJob(job)
			job = 0
		}
		<-done
		if terminateErr != nil {
			return errors.Join(ctx.Err(), fmt.Errorf("terminate gift clip job: %w", terminateErr))
		}
		return ctx.Err()
	}
}

func cleanupGiftClipUnassignedProcess(api giftClipWindowsAPI, started *giftClipStartedProcess, cause error) error {
	if err := api.killProcess(started.command); err != nil {
		result := errors.Join(cause, fmt.Errorf("kill unassigned gift clip process: %w", err))
		if copierErr := started.stopCopiers(); copierErr != nil {
			result = errors.Join(result, fmt.Errorf("stop unassigned gift clip process output: %w", copierErr))
		}
		if releaseErr := api.releaseProcess(started.command); releaseErr != nil {
			return errors.Join(result, fmt.Errorf("release unassigned gift clip process: %w", releaseErr))
		}
		return result
	}
	return joinGiftClipProcessError(cause, "wait after killing unassigned gift clip process", waitGiftClipStartedProcess(api, started))
}

func waitGiftClipStartedProcess(api giftClipWindowsAPI, started *giftClipStartedProcess) error {
	waitErr := api.waitProcess(started.command)
	copierErr := started.waitCopiers()
	if waitErr != nil {
		return waitErr
	}
	return copierErr
}

func joinGiftClipProcessError(cause error, operation string, err error) error {
	if err == nil || isGiftClipExpectedKilledProcessError(err) {
		return cause
	}
	return errors.Join(cause, fmt.Errorf("%s: %w", operation, err))
}

func isGiftClipExpectedKilledProcessError(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

func killGiftClipProcess(command *exec.Cmd) error {
	return command.Process.Kill()
}

func waitGiftClipProcess(command *exec.Cmd) error {
	return command.Wait()
}

func releaseGiftClipProcess(command *exec.Cmd) error {
	return command.Process.Release()
}

func startGiftClipProcessSuspended(path string, args []string, stdout, stderr io.Writer) (*giftClipStartedProcess, error) {
	stdout, stderr = serializeGiftClipSharedOutput(stdout, stderr)
	stdoutPipe, err := newGiftClipProcessPipe(stdout)
	if err != nil {
		return nil, err
	}
	stderrPipe, err := newGiftClipProcessPipe(stderr)
	if err != nil {
		return nil, errors.Join(err, stdoutPipe.closeBeforeStart())
	}
	command := exec.Command(path, args...)
	command.Stdout = stdoutPipe.writer
	command.Stderr = stderrPipe.writer
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createSuspended}
	if err := command.Start(); err != nil {
		return nil, errors.Join(err, stdoutPipe.closeBeforeStart(), stderrPipe.closeBeforeStart())
	}
	stdoutPipe.closeWriter()
	stderrPipe.closeWriter()
	stdoutPipe.startCopier()
	stderrPipe.startCopier()
	return &giftClipStartedProcess{command: command, stdout: stdoutPipe, stderr: stderrPipe}, nil
}

func newGiftClipProcessPipe(destination io.Writer) (*giftClipProcessPipe, error) {
	reader, writer, err := newGiftClipOverlappedPipe()
	if err != nil {
		return nil, err
	}
	if destination == nil {
		destination = io.Discard
	}
	return &giftClipProcessPipe{
		reader:      reader,
		writer:      writer,
		destination: destination,
		done:        make(chan struct{}),
	}, nil
}

func newGiftClipOverlappedPipe() (*os.File, *os.File, error) {
	name, err := syscall.UTF16PtrFromString(fmt.Sprintf(
		`\\.\pipe\bilibili-gift-clip-%d-%d`,
		os.Getpid(),
		giftClipPipeSequence.Add(1),
	))
	if err != nil {
		return nil, nil, err
	}
	readerHandle, _, callErr := giftClipCreateNamedPipeW.Call(
		uintptr(unsafe.Pointer(name)),
		pipeAccessInbound|syscall.FILE_FLAG_OVERLAPPED|fileFlagFirstPipeInstance,
		0,
		1,
		64*1024,
		64*1024,
		0,
		0,
	)
	if readerHandle == uintptr(syscall.InvalidHandle) {
		return nil, nil, callErr
	}
	closeReaderHandle := true
	defer func() {
		if closeReaderHandle {
			closeGiftClipHandle(syscall.Handle(readerHandle))
		}
	}()
	inherit := syscall.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(syscall.SecurityAttributes{})),
		InheritHandle: 1,
	}
	writerHandle, err := syscall.CreateFile(
		name,
		syscall.GENERIC_WRITE,
		0,
		&inherit,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, nil, err
	}
	closeWriterHandle := true
	defer func() {
		if closeWriterHandle {
			closeGiftClipHandle(writerHandle)
		}
	}()
	reader := os.NewFile(readerHandle, "gift-clip-output-reader")
	writer := os.NewFile(uintptr(writerHandle), "gift-clip-output-writer")
	if reader == nil || writer == nil {
		return nil, nil, errors.New("create gift clip output pipe files")
	}
	closeReaderHandle = false
	closeWriterHandle = false
	return reader, writer, nil
}

func serializeGiftClipSharedOutput(stdout, stderr io.Writer) (io.Writer, io.Writer) {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if !giftClipWritersEqual(stdout, stderr) {
		return stdout, stderr
	}
	shared := &giftClipSerializedWriter{destination: stdout}
	return shared, shared
}

func giftClipWritersEqual(left, right io.Writer) (equal bool) {
	defer func() { _ = recover() }()
	return left == right
}

func (writer *giftClipSerializedWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.destination.Write(data)
}

func (pipe *giftClipProcessPipe) startCopier() {
	go func() {
		_, pipe.copyErr = io.Copy(pipe.destination, pipe.reader)
		pipe.closeReader()
		close(pipe.done)
	}()
}

func (pipe *giftClipProcessPipe) closeReader() error {
	pipe.readerCloseOnce.Do(func() { pipe.readerCloseErr = pipe.reader.Close() })
	return pipe.readerCloseErr
}

func (pipe *giftClipProcessPipe) closeWriter() error {
	pipe.writerCloseOnce.Do(func() { pipe.writerCloseErr = pipe.writer.Close() })
	return pipe.writerCloseErr
}

func (pipe *giftClipProcessPipe) closeBeforeStart() error {
	return errors.Join(pipe.closeWriter(), pipe.closeReader())
}

func (pipe *giftClipProcessPipe) stopCopier() error {
	pipe.stopOnce.Do(func() { pipe.stopErr = pipe.reader.SetReadDeadline(time.Now()) })
	return pipe.stopErr
}

func (pipe *giftClipProcessPipe) waitResult(ignoreForcedClose bool) error {
	<-pipe.done
	copyErr := pipe.copyErr
	if ignoreForcedClose && (errors.Is(copyErr, os.ErrClosed) || errors.Is(copyErr, os.ErrDeadlineExceeded)) {
		copyErr = nil
	}
	return errors.Join(pipe.writerCloseErr, pipe.readerCloseErr, copyErr)
}

func (started *giftClipStartedProcess) waitCopiers() error {
	return errors.Join(started.stdout.waitResult(false), started.stderr.waitResult(false))
}

func (started *giftClipStartedProcess) stopCopiers() error {
	stdoutStopErr := started.stdout.stopCopier()
	stderrStopErr := started.stderr.stopCopier()
	return errors.Join(
		stdoutStopErr,
		stderrStopErr,
		started.stdout.waitResult(true),
		started.stderr.waitResult(true),
	)
}

func createGiftClipJobObject() (syscall.Handle, error) {
	job, _, callErr := giftClipCreateJobObjectW.Call(0, 0)
	if job == 0 {
		return 0, callErr
	}
	limit := jobObjectExtendedLimitInformation{}
	limit.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	result, _, callErr := giftClipSetInformationJobObject.Call(
		job,
		jobObjectExtendedLimitInformationClass,
		uintptr(unsafe.Pointer(&limit)),
		unsafe.Sizeof(limit),
	)
	if result == 0 {
		closeGiftClipHandle(syscall.Handle(job))
		return 0, callErr
	}
	return syscall.Handle(job), nil
}

func openGiftClipProcess(pid int) (syscall.Handle, error) {
	process, _, callErr := giftClipOpenProcess.Call(processTerminate|processSetQuota|processQueryLimitedInformation, 0, uintptr(pid))
	if process == 0 {
		return 0, callErr
	}
	return syscall.Handle(process), nil
}

func assignGiftClipProcessToJob(job, process syscall.Handle) error {
	result, _, callErr := giftClipAssignProcessToJobObject.Call(uintptr(job), uintptr(process))
	if result == 0 {
		return callErr
	}
	return nil
}

func terminateGiftClipJob(job syscall.Handle) error {
	result, _, callErr := giftClipTerminateJobObject.Call(uintptr(job), 1)
	if result == 0 {
		return callErr
	}
	return nil
}

func closeGiftClipHandle(handle syscall.Handle) {
	if handle != 0 {
		_, _, _ = giftClipCloseHandle.Call(uintptr(handle))
	}
}

type threadEntry32 struct {
	Size           uint32
	Usage          uint32
	ThreadID       uint32
	OwnerProcessID uint32
	BasePriority   int32
	DeltaPriority  int32
	Flags          uint32
}

func resumeGiftClipPrimaryThread(pid int) error {
	snapshot, _, callErr := giftClipCreateToolhelp32Snapshot.Call(th32csSnapThread, 0)
	if snapshot == ^uintptr(0) {
		return callErr
	}
	defer closeGiftClipHandle(syscall.Handle(snapshot))
	entry := threadEntry32{Size: uint32(unsafe.Sizeof(threadEntry32{}))}
	result, _, callErr := giftClipThread32First.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	for result != 0 {
		if entry.OwnerProcessID == uint32(pid) {
			thread, _, callErr := giftClipOpenThread.Call(threadSuspendResume, 0, uintptr(entry.ThreadID))
			if thread == 0 {
				return callErr
			}
			result, _, callErr := giftClipResumeThread.Call(thread)
			closeGiftClipHandle(syscall.Handle(thread))
			if result == ^uintptr(0) {
				return callErr
			}
			return nil
		}
		entry.Size = uint32(unsafe.Sizeof(threadEntry32{}))
		result, _, callErr = giftClipThread32Next.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	}
	if callErr != syscall.Errno(0) {
		return callErr
	}
	return errors.New("gift clip suspended process has no primary thread")
}

func giftClipWindowsProcessIsGone(pid int) bool {
	process, _, _ := giftClipOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if process == 0 {
		return true
	}
	closeGiftClipHandle(syscall.Handle(process))
	return false
}
