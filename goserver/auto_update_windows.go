//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	updateSynchronize = 0x00100000
	updateInfinite    = 0xffffffff
)

var (
	procUpdateOpenProcess      = kernel32.NewProc("OpenProcess")
	procUpdateWaitForSingleObj = kernel32.NewProc("WaitForSingleObject")
)

func isAutoUpdateSupported() bool {
	return true
}

func launchUpdateInstaller(metadataPath string, waitPID int, restart bool) error {
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return err
	}
	var pending pendingUpdate
	if err := json.Unmarshal(data, &pending); err != nil {
		return err
	}
	if err := verifyFileSHA256(pending.PendingPath, pending.SHA256); err != nil {
		return err
	}
	args := []string{"--apply-update", "--state", metadataPath, strconv.Itoa(waitPID)}
	if restart {
		args = append(args, "--restart")
	}
	return startDetachedExecutable(pending.PendingPath, args...)
}

func applyDownloadedUpdate(pending pendingUpdate, waitPID int) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, err = filepath.Abs(self)
	if err != nil {
		return err
	}
	return replaceDownloadedExecutable(self, pending, waitPID)
}

func replaceDownloadedExecutable(self string, pending pendingUpdate, waitPID int) error {
	pendingPath, err := filepath.Abs(pending.PendingPath)
	if err != nil {
		return err
	}
	targetPath, err := filepath.Abs(pending.TargetPath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(self), filepath.Clean(pendingPath)) {
		return errors.New("更新替换器不是已校验的待安装文件")
	}
	if strings.EqualFold(filepath.Clean(self), filepath.Clean(targetPath)) {
		return errors.New("待安装文件不能覆盖自身")
	}
	if filepath.Ext(targetPath) != ".exe" {
		return errors.New("更新目标不是 EXE 文件")
	}
	if err := verifyFileSHA256(self, pending.SHA256); err != nil {
		return fmt.Errorf("待安装文件校验失败：%w", err)
	}
	waitForWindowsProcess(waitPID)

	newPath := targetPath + ".new"
	backupPath := targetPath + ".old"
	_ = os.Remove(newPath)
	_ = os.Remove(backupPath)
	if err := copyUpdateFile(self, newPath); err != nil {
		return fmt.Errorf("准备新版本失败：%w", err)
	}
	if err := verifyFileSHA256(newPath, pending.SHA256); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("新版本落盘校验失败：%w", err)
	}
	if err := renameUpdateFile(targetPath, backupPath); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("备份旧版本失败：%w", err)
	}
	if err := renameUpdateFile(newPath, targetPath); err != nil {
		_ = renameUpdateFile(backupPath, targetPath)
		_ = os.Remove(newPath)
		return fmt.Errorf("替换程序失败，已恢复旧版本：%w", err)
	}
	return nil
}

func renameUpdateFile(sourcePath, targetPath string) error {
	var lastErr error
	for attempt := 0; attempt < 40; attempt++ {
		if err := os.Rename(sourcePath, targetPath); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(250 * time.Millisecond)
	}
	return lastErr
}

func waitForWindowsProcess(pid int) {
	handle, _, _ := procUpdateOpenProcess.Call(updateSynchronize, 0, uintptr(uint32(pid)))
	if handle == 0 {
		return
	}
	defer procCloseHandle.Call(handle)
	_, _, _ = procUpdateWaitForSingleObj.Call(handle, updateInfinite)
}

func copyUpdateFile(sourcePath, targetPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(target, source); err != nil {
		target.Close()
		_ = os.Remove(targetPath)
		return err
	}
	if err := target.Sync(); err != nil {
		target.Close()
		_ = os.Remove(targetPath)
		return err
	}
	return target.Close()
}
