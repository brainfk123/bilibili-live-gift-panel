//go:build !windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type updateTrustCacheLock struct {
	file *os.File
}

func normalizeUpdateTrustCacheLockPath(path string) string {
	return filepath.Clean(path)
}

func acquireUpdateTrustCacheLock(ctx context.Context, cacheDir string) (*updateTrustCacheLock, error) {
	lockID, err := updateTrustCacheLockID(cacheDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return nil, policyError("policy_cache_lock_unavailable")
	}
	file, err := os.OpenFile(filepath.Join(cacheDir, "publisher-policy-cache-lock-"+lockID+".lck"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, policyError("policy_cache_lock_unavailable")
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			lock := &updateTrustCacheLock{file: file}
			if err := ctx.Err(); err != nil {
				lock.Release()
				return nil, err
			}
			return lock, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, policyError("policy_cache_lock_unavailable")
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (lock *updateTrustCacheLock) Release() {
	if lock == nil || lock.file == nil {
		return
	}
	_ = syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
	_ = lock.file.Close()
	lock.file = nil
}
