//go:build windows

package main

import (
	"context"
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
	processTerminate                       = 0x0001
	processSetQuota                        = 0x0100
	processQueryLimitedInformation         = 0x1000
)

var (
	giftClipKernel32                 = syscall.NewLazyDLL("kernel32.dll")
	giftClipCreateJobObjectW         = giftClipKernel32.NewProc("CreateJobObjectW")
	giftClipSetInformationJobObject  = giftClipKernel32.NewProc("SetInformationJobObject")
	giftClipAssignProcessToJobObject = giftClipKernel32.NewProc("AssignProcessToJobObject")
	giftClipTerminateJobObject       = giftClipKernel32.NewProc("TerminateJobObject")
	giftClipCloseHandle              = giftClipKernel32.NewProc("CloseHandle")
	giftClipOpenProcess              = giftClipKernel32.NewProc("OpenProcess")
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

type giftClipWindowsProcessRunner struct{}

func newGiftClipProcessRunner() giftClipProcessRunner {
	return giftClipWindowsProcessRunner{}
}

func (giftClipWindowsProcessRunner) Run(ctx context.Context, path string, args []string, stdout, stderr io.Writer) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	job, err := createGiftClipJobObject()
	if err != nil {
		return err
	}
	defer closeGiftClipHandle(job)

	command := exec.Command(absPath, args...)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return err
	}
	process, err := openGiftClipProcess(command.Process.Pid)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return err
	}
	defer closeGiftClipHandle(process)
	if err := assignGiftClipProcessToJob(job, process); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return err
	}

	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = terminateGiftClipJob(job)
		<-done
		return ctx.Err()
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

func giftClipWindowsProcessIsGone(pid int) bool {
	process, _, _ := giftClipOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if process == 0 {
		return true
	}
	closeGiftClipHandle(syscall.Handle(process))
	return false
}
