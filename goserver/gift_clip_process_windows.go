//go:build windows

package main

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
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
	pipeRejectRemoteClients                = 0x00000008
	giftClipPipeClientWriteAccess          = 0x00000002 // FILE_WRITE_DATA
	// Windows checks the mapped write rights plus FILE_READ_ATTRIBUTES when a
	// write-only client connects. FILE_APPEND_DATA (0x4) is deliberately absent:
	// on named pipes it aliases FILE_CREATE_PIPE_INSTANCE.
	giftClipPipeClientAllowedAccess = 0x00120192
	sddlRevision1                   = 1
	processTerminate                = 0x0001
	processSetQuota                 = 0x0100
	processQueryLimitedInformation  = 0x1000
	threadSuspendResume             = 0x0002
	th32csSnapThread                = 0x00000004
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
	giftClipCancelIoEx               = giftClipKernel32.NewProc("CancelIoEx")
	giftClipLocalFree                = giftClipKernel32.NewProc("LocalFree")
	giftClipAdvapi32                 = syscall.NewLazyDLL("advapi32.dll")
	giftClipConvertSDDL              = giftClipAdvapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
)

var errGiftClipProcessPipeStopTimeout = errors.New("timed out stopping gift clip process output copier")

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

type giftClipNamedPipeAPI struct {
	fillNonce       func([]byte) error
	buildSecurity   func() (*giftClipPipeSecurity, error)
	createNamedPipe func(string, uint32, uint32, *syscall.SecurityAttributes) (syscall.Handle, error)
	openWriter      func(string, uint32, *syscall.SecurityAttributes) (syscall.Handle, error)
}

var giftClipDefaultNamedPipeAPI = giftClipNamedPipeAPI{
	fillNonce: func(target []byte) error {
		_, err := cryptorand.Read(target)
		return err
	},
	buildSecurity:   newGiftClipPipeSecurity,
	createNamedPipe: createGiftClipNamedPipe,
	openWriter:      openGiftClipNamedPipeWriter,
}

type giftClipPipeSecurityAPI struct {
	currentUserSID func() (string, error)
	convertSDDL    func(string) (uintptr, error)
	localFree      func(uintptr) error
}

var giftClipDefaultPipeSecurityAPI = giftClipPipeSecurityAPI{
	currentUserSID: currentGiftClipUserSID,
	convertSDDL:    convertGiftClipPipeSDDL,
	localFree:      localFreeGiftClipPipeSecurity,
}

type giftClipPipeSecurity struct {
	attributes  syscall.SecurityAttributes
	descriptor  uintptr
	localFree   func(uintptr) error
	releaseOnce sync.Once
	releaseErr  error
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
	control     giftClipProcessPipeControl

	readerCloseOnce  sync.Once
	readerCloseErr   error
	writerCloseOnce  sync.Once
	writerCloseErr   error
	stopOnce         sync.Once
	stopRequestErr   error
	stopClosedReader bool
	copyErr          error
}

type giftClipProcessPipeControl struct {
	setReadDeadline func(*os.File, time.Time) error
	cancelRead      func(*os.File) (syscall.Handle, error)
	closeReader     func(*os.File) error
	waitForDone     func(<-chan struct{}) error
}

var giftClipDefaultProcessPipeControl = giftClipProcessPipeControl{
	setReadDeadline: func(reader *os.File, deadline time.Time) error { return reader.SetReadDeadline(deadline) },
	cancelRead:      cancelGiftClipPipeRead,
	closeReader:     func(reader *os.File) error { return reader.Close() },
	waitForDone:     waitForGiftClipPipeCopier,
}

type giftClipSerializedWriter struct {
	destination io.Writer
	mu          *sync.Mutex
}

type giftClipWriterIdentity uint8

const (
	giftClipWriterIdentityIndeterminate giftClipWriterIdentity = iota
	giftClipWriterIdentityEqual
	giftClipWriterIdentityDistinct
)

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
		return joinGiftClipProcessError(err, "wait after resume failure", waitGiftClipStartedProcess(api, started, true))
	}

	done := make(chan giftClipProcessDrainResult, 1)
	go func() { done <- drainGiftClipStartedProcess(api, started) }()
	select {
	case result := <-done:
		return result.err(false)
	case <-ctx.Done():
		terminateErr := api.terminateJob(job)
		if terminateErr != nil {
			api.closeJob(job)
			job = 0
		}
		drainErr := (<-done).err(true)
		cause := ctx.Err()
		if terminateErr != nil {
			cause = errors.Join(cause, fmt.Errorf("terminate gift clip job: %w", terminateErr))
		}
		return joinGiftClipProcessError(cause, "drain gift clip process after forced termination", drainErr)
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
	return joinGiftClipProcessError(cause, "wait after killing unassigned gift clip process", waitGiftClipStartedProcess(api, started, true))
}

type giftClipProcessDrainResult struct {
	waitErr   error
	copierErr error
}

func drainGiftClipStartedProcess(api giftClipWindowsAPI, started *giftClipStartedProcess) giftClipProcessDrainResult {
	return giftClipProcessDrainResult{
		waitErr:   api.waitProcess(started.command),
		copierErr: started.waitCopiers(),
	}
}

func waitGiftClipStartedProcess(api giftClipWindowsAPI, started *giftClipStartedProcess, forcedTermination bool) error {
	return drainGiftClipStartedProcess(api, started).err(forcedTermination)
}

func (result giftClipProcessDrainResult) err(forcedTermination bool) error {
	waitErr := result.waitErr
	if forcedTermination {
		if _, expected := waitErr.(*exec.ExitError); expected {
			waitErr = nil
		}
	}
	return errors.Join(waitErr, result.copierErr)
}

func joinGiftClipProcessError(cause error, operation string, err error) error {
	if err == nil {
		return cause
	}
	return errors.Join(cause, fmt.Errorf("%s: %w", operation, err))
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
		control:     giftClipDefaultProcessPipeControl,
	}, nil
}

func newGiftClipOverlappedPipe() (*os.File, *os.File, error) {
	return newGiftClipOverlappedPipeWithAPI(giftClipDefaultNamedPipeAPI)
}

func newGiftClipOverlappedPipeWithAPI(api giftClipNamedPipeAPI) (*os.File, *os.File, error) {
	nonce := make([]byte, 16)
	if err := api.fillNonce(nonce); err != nil {
		return nil, nil, fmt.Errorf("generate gift clip pipe nonce: %w", err)
	}
	name := `\\.\pipe\bilibili-gift-clip-` + hex.EncodeToString(nonce)
	security, err := api.buildSecurity()
	if err != nil {
		return nil, nil, err
	}
	readerHandle, createErr := api.createNamedPipe(
		name,
		pipeAccessInbound|syscall.FILE_FLAG_OVERLAPPED|fileFlagFirstPipeInstance,
		pipeRejectRemoteClients,
		&security.attributes,
	)
	releaseErr := security.release()
	if createErr != nil || readerHandle == syscall.InvalidHandle || releaseErr != nil {
		if readerHandle != 0 && readerHandle != syscall.InvalidHandle {
			closeGiftClipHandle(readerHandle)
		}
		if createErr != nil {
			createErr = fmt.Errorf("create gift clip named pipe server: %w", createErr)
		}
		return nil, nil, errors.Join(createErr, releaseErr)
	}
	closeReaderHandle := true
	defer func() {
		if closeReaderHandle {
			closeGiftClipHandle(readerHandle)
		}
	}()
	inherit := syscall.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(syscall.SecurityAttributes{})),
		InheritHandle: 1,
	}
	writerHandle, err := api.openWriter(name, giftClipPipeClientWriteAccess, &inherit)
	if err != nil {
		return nil, nil, fmt.Errorf("open gift clip named pipe writer: %w", err)
	}
	closeWriterHandle := true
	defer func() {
		if closeWriterHandle {
			closeGiftClipHandle(writerHandle)
		}
	}()
	reader := os.NewFile(uintptr(readerHandle), "gift-clip-output-reader")
	writer := os.NewFile(uintptr(writerHandle), "gift-clip-output-writer")
	if reader == nil || writer == nil {
		return nil, nil, errors.New("create gift clip output pipe files")
	}
	closeReaderHandle = false
	closeWriterHandle = false
	return reader, writer, nil
}

func newGiftClipPipeSecurity() (*giftClipPipeSecurity, error) {
	return newGiftClipPipeSecurityWithAPI(giftClipDefaultPipeSecurityAPI)
}

func newGiftClipPipeSecurityWithAPI(api giftClipPipeSecurityAPI) (*giftClipPipeSecurity, error) {
	userSID, err := api.currentUserSID()
	if err != nil {
		return nil, fmt.Errorf("get current user SID for gift clip pipe: %w", err)
	}
	descriptor, err := api.convertSDDL(fmt.Sprintf("D:P(A;;0x%08x;;;%s)", giftClipPipeClientAllowedAccess, userSID))
	if err != nil {
		return nil, fmt.Errorf("build current-user gift clip pipe DACL: %w", err)
	}
	return &giftClipPipeSecurity{
		attributes: syscall.SecurityAttributes{
			Length:             uint32(unsafe.Sizeof(syscall.SecurityAttributes{})),
			SecurityDescriptor: descriptor,
			InheritHandle:      0,
		},
		descriptor: descriptor,
		localFree:  api.localFree,
	}, nil
}

func (security *giftClipPipeSecurity) release() error {
	security.releaseOnce.Do(func() { security.releaseErr = security.localFree(security.descriptor) })
	return security.releaseErr
}

func currentGiftClipUserSID() (string, error) {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	user, userErr := token.GetTokenUser()
	closeErr := token.Close()
	if userErr != nil {
		return "", errors.Join(userErr, closeErr)
	}
	userSID, sidErr := user.User.Sid.String()
	return userSID, errors.Join(sidErr, closeErr)
}

func convertGiftClipPipeSDDL(sddl string) (uintptr, error) {
	sddlPointer, err := syscall.UTF16PtrFromString(sddl)
	if err != nil {
		return 0, err
	}
	var descriptor uintptr
	result, _, callErr := giftClipConvertSDDL.Call(
		uintptr(unsafe.Pointer(sddlPointer)),
		sddlRevision1,
		uintptr(unsafe.Pointer(&descriptor)),
		0,
	)
	if result == 0 {
		return 0, callErr
	}
	return descriptor, nil
}

func localFreeGiftClipPipeSecurity(descriptor uintptr) error {
	result, _, _ := giftClipLocalFree.Call(descriptor)
	if result == 0 {
		return nil
	}
	return fmt.Errorf("LocalFree gift clip pipe security descriptor returned %#x", result)
}

func createGiftClipNamedPipe(name string, openMode, pipeMode uint32, attributes *syscall.SecurityAttributes) (syscall.Handle, error) {
	namePointer, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return syscall.InvalidHandle, err
	}
	readerHandle, _, callErr := giftClipCreateNamedPipeW.Call(
		uintptr(unsafe.Pointer(namePointer)),
		uintptr(openMode),
		uintptr(pipeMode),
		1,
		64*1024,
		64*1024,
		0,
		uintptr(unsafe.Pointer(attributes)),
	)
	if readerHandle == uintptr(syscall.InvalidHandle) {
		return syscall.InvalidHandle, callErr
	}
	return syscall.Handle(readerHandle), nil
}

func openGiftClipNamedPipeWriter(name string, access uint32, attributes *syscall.SecurityAttributes) (syscall.Handle, error) {
	namePointer, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return syscall.InvalidHandle, err
	}
	return syscall.CreateFile(
		namePointer,
		access,
		0,
		attributes,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
}

func serializeGiftClipSharedOutput(stdout, stderr io.Writer) (io.Writer, io.Writer) {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if compareGiftClipWriters(stdout, stderr) == giftClipWriterIdentityDistinct {
		return stdout, stderr
	}
	sharedMutex := &sync.Mutex{}
	return &giftClipSerializedWriter{destination: stdout, mu: sharedMutex},
		&giftClipSerializedWriter{destination: stderr, mu: sharedMutex}
}

func compareGiftClipWriters(left, right io.Writer) giftClipWriterIdentity {
	leftType, rightType := reflect.TypeOf(left), reflect.TypeOf(right)
	if leftType == nil || rightType == nil || !leftType.Comparable() || !rightType.Comparable() {
		return giftClipWriterIdentityIndeterminate
	}
	if left == right {
		return giftClipWriterIdentityEqual
	}
	return giftClipWriterIdentityDistinct
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
	pipe.readerCloseOnce.Do(func() { pipe.readerCloseErr = pipe.control.closeReader(pipe.reader) })
	return pipe.readerCloseErr
}

func (pipe *giftClipProcessPipe) closeWriter() error {
	pipe.writerCloseOnce.Do(func() { pipe.writerCloseErr = pipe.writer.Close() })
	return pipe.writerCloseErr
}

func (pipe *giftClipProcessPipe) closeBeforeStart() error {
	return errors.Join(pipe.closeWriter(), pipe.closeReader())
}

func (pipe *giftClipProcessPipe) requestStop() {
	pipe.stopOnce.Do(func() {
		deadlineErr := pipe.control.setReadDeadline(pipe.reader, time.Now())
		if deadlineErr == nil {
			return
		}
		pipe.stopRequestErr = deadlineErr
		pipe.cancelAndCloseReader()
	})
}

func (pipe *giftClipProcessPipe) cancelAndCloseReader() {
	if pipe.stopClosedReader {
		return
	}
	_, cancelErr := pipe.control.cancelRead(pipe.reader)
	closeErr := pipe.closeReader()
	pipe.stopClosedReader = true
	pipe.stopRequestErr = errors.Join(pipe.stopRequestErr, cancelErr, closeErr)
}

func (pipe *giftClipProcessPipe) waitResult(ignoreForcedClose bool) error {
	<-pipe.done
	return pipe.result(ignoreForcedClose, true)
}

func (pipe *giftClipProcessPipe) result(ignoreForcedClose, includeReaderClose bool) error {
	copyErr := pipe.copyErr
	if ignoreForcedClose && (errors.Is(copyErr, os.ErrClosed) || errors.Is(copyErr, os.ErrDeadlineExceeded)) {
		copyErr = nil
	}
	var readerCloseErr error
	if includeReaderClose {
		readerCloseErr = pipe.readerCloseErr
	}
	return errors.Join(pipe.writerCloseErr, readerCloseErr, copyErr)
}

func (pipe *giftClipProcessPipe) finishStop() error {
	if waitErr := pipe.control.waitForDone(pipe.done); waitErr != nil {
		if pipe.stopClosedReader {
			return errors.Join(pipe.stopRequestErr, pipe.writerCloseErr, waitErr)
		}
		pipe.cancelAndCloseReader()
		if finalWaitErr := pipe.control.waitForDone(pipe.done); finalWaitErr != nil {
			return errors.Join(pipe.stopRequestErr, pipe.writerCloseErr, finalWaitErr)
		}
	}
	return errors.Join(pipe.stopRequestErr, pipe.result(true, !pipe.stopClosedReader))
}

func (started *giftClipStartedProcess) waitCopiers() error {
	return errors.Join(started.stdout.waitResult(false), started.stderr.waitResult(false))
}

func (started *giftClipStartedProcess) stopCopiers() error {
	started.stdout.requestStop()
	started.stderr.requestStop()
	return errors.Join(
		started.stdout.finishStop(),
		started.stderr.finishStop(),
	)
}

func cancelGiftClipPipeRead(reader *os.File) (syscall.Handle, error) {
	raw, err := reader.SyscallConn()
	if err != nil {
		return 0, err
	}
	var handle syscall.Handle
	var cancelErr error
	controlErr := raw.Control(func(rawHandle uintptr) {
		handle = syscall.Handle(rawHandle)
		cancelErr = cancelGiftClipPipeHandle(handle)
	})
	return handle, errors.Join(controlErr, cancelErr)
}

func cancelGiftClipPipeHandle(reader syscall.Handle) error {
	result, _, callErr := giftClipCancelIoEx.Call(uintptr(reader), 0)
	if result != 0 || callErr == syscall.Errno(1168) { // ERROR_NOT_FOUND means no matching I/O remained pending.
		return nil
	}
	return callErr
}

func waitForGiftClipPipeCopier(done <-chan struct{}) error {
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return errGiftClipProcessPipeStopTimeout
	}
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
