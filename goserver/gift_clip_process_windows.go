//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"
)

const giftClipDefaultEncoderMode = giftClipEncoderHardware

const (
	jobObjectExtendedLimitInformationClass = 9
	jobObjectLimitKillOnJobClose           = 0x00002000
	createSuspended                        = 0x00000004
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
)

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
	startSuspended      func(string, []string, io.Writer, io.Writer) (*exec.Cmd, error)
	openProcess         func(int) (syscall.Handle, error)
	assignProcess       func(syscall.Handle, syscall.Handle) error
	resumePrimaryThread func(int) error
	terminateJob        func(syscall.Handle) error
	closeJob            func(syscall.Handle)
	closeProcess        func(syscall.Handle)
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
}

type giftClipWindowsProcessRunner struct {
	api giftClipWindowsAPI
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

	command, err := api.startSuspended(absPath, args, stdout, stderr)
	if err != nil {
		return err
	}
	process, err := api.openProcess(command.Process.Pid)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return err
	}
	defer api.closeProcess(process)
	if err := api.assignProcess(job, process); err != nil {
		api.closeJob(job)
		job = 0
		_ = command.Wait()
		return err
	}
	if err := api.resumePrimaryThread(command.Process.Pid); err != nil {
		api.closeJob(job)
		job = 0
		_ = command.Wait()
		return err
	}

	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
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

func startGiftClipProcessSuspended(path string, args []string, stdout, stderr io.Writer) (*exec.Cmd, error) {
	command := exec.Command(path, args...)
	command.Stdout = stdout
	command.Stderr = stderr
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createSuspended}
	if err := command.Start(); err != nil {
		return nil, err
	}
	return command, nil
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
