//go:build windows

package main

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	updateTrustWaitObject0   = 0x00000000
	updateTrustWaitAbandoned = 0x00000080
	updateTrustWaitTimeout   = 0x00000102
)

var updateTrustKernel32 = syscall.NewLazyDLL("kernel32.dll")
var updateTrustCreateMutexW = updateTrustKernel32.NewProc("CreateMutexW")
var updateTrustWaitForSingleObject = updateTrustKernel32.NewProc("WaitForSingleObject")
var updateTrustReleaseMutex = updateTrustKernel32.NewProc("ReleaseMutex")
var updateTrustCloseHandle = updateTrustKernel32.NewProc("CloseHandle")

type updateTrustCacheLock struct {
	handle uintptr
}

func normalizeUpdateTrustCacheLockPath(path string) string {
	return strings.ToLower(filepath.ToSlash(filepath.Clean(path)))
}

func acquireUpdateTrustCacheLock(ctx context.Context, cacheDir string) (*updateTrustCacheLock, error) {
	lockID, err := updateTrustCacheLockID(cacheDir)
	if err != nil {
		return nil, err
	}
	name, err := syscall.UTF16PtrFromString(`Global\BilibiliGiftPanelUpdateTrust-` + lockID)
	if err != nil {
		return nil, policyError("policy_cache_lock_unavailable")
	}
	runtime.LockOSThread()
	handle, _, _ := updateTrustCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		runtime.UnlockOSThread()
		return nil, policyError("policy_cache_lock_unavailable")
	}
	for {
		if err := ctx.Err(); err != nil {
			_, _, _ = updateTrustCloseHandle.Call(handle)
			runtime.UnlockOSThread()
			return nil, err
		}
		wait := 50 * time.Millisecond
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				_, _, _ = updateTrustCloseHandle.Call(handle)
				runtime.UnlockOSThread()
				return nil, context.DeadlineExceeded
			}
			if remaining < wait {
				wait = remaining
			}
		}
		milliseconds := uint32((wait + time.Millisecond - 1) / time.Millisecond)
		result, _, _ := updateTrustWaitForSingleObject.Call(handle, uintptr(milliseconds))
		switch result {
		case updateTrustWaitObject0, updateTrustWaitAbandoned:
			lock := &updateTrustCacheLock{handle: handle}
			if err := ctx.Err(); err != nil {
				lock.Release()
				return nil, err
			}
			return lock, nil
		case updateTrustWaitTimeout:
			continue
		default:
			_, _, _ = updateTrustCloseHandle.Call(handle)
			runtime.UnlockOSThread()
			return nil, policyError("policy_cache_lock_unavailable")
		}
	}
}

func (lock *updateTrustCacheLock) Release() {
	if lock == nil || lock.handle == 0 {
		return
	}
	_, _, _ = updateTrustReleaseMutex.Call(lock.handle)
	_, _, _ = updateTrustCloseHandle.Call(lock.handle)
	lock.handle = 0
	runtime.UnlockOSThread()
}
