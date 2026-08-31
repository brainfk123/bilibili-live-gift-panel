//go:build windows

package main

import (
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
	procUpdateOpenProcess          = kernel32.NewProc("OpenProcess")
	procUpdateWaitForSingleObj     = kernel32.NewProc("WaitForSingleObject")
	removeWindowsUpdateFile        = os.Remove
	startUpdateInstallerExecutable = startDetachedExecutable
	renameWindowsUpdateFile        = renameUpdateFile
)

func isAutoUpdateSupported() bool {
	return true
}

func launchUpdateInstaller(metadataPath string, waitPID int, restart bool) error {
	pending, err := readPendingUpdateMetadata(metadataPath)
	if err != nil {
		return err
	}
	verifier, err := pendingUpdateVerifierForBuild(pending)
	if err != nil {
		logUpdateResult(boundedUpdateResult(err, "artifact_verification_failed"))
		return errors.New("待安装更新安全校验失败")
	}
	if err := verifyPendingExecutable(pending, verifier); err != nil {
		logPendingUpdateDiagnostic(pending, "启动更新替换器前安全校验诊断", err, "artifact_verification_failed")
		return errors.New("待安装更新安全校验失败")
	}
	for _, path := range []string{pending.TargetPath + ".old", pending.TargetPath + ".new"} {
		if err := removeUpdateArtifactWith(removeWindowsUpdateFile, path); err != nil {
			logPendingUpdateDiagnostic(pending, "启动更新替换器前残留文件清理诊断", err, "artifact_cleanup_failed")
			return errors.New("待安装更新清理失败")
		}
	}
	if err := verifyPendingExecutable(pending, verifier); err != nil {
		logPendingUpdateDiagnostic(pending, "启动更新替换器最终安全校验诊断", err, "artifact_verification_failed")
		return errors.New("待安装更新安全校验失败")
	}
	args := []string{"--apply-update", "--state", metadataPath, strconv.Itoa(waitPID)}
	if restart {
		args = append(args, "--restart")
	}
	return startUpdateInstallerExecutable(pending.PendingPath, args...)
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
	verifier, err := pendingUpdateVerifierForBuild(pending)
	if err != nil {
		logUpdateResult(boundedUpdateResult(err, "artifact_verification_failed"))
		return errors.New("待安装文件安全校验失败")
	}
	if err := verifyPendingExecutable(pending, verifier); err != nil {
		logPendingUpdateDiagnostic(pending, "更新替换器源文件安全校验诊断", err, "artifact_verification_failed")
		return errors.New("待安装文件安全校验失败")
	}
	waitForWindowsProcess(waitPID)

	newPath := targetPath + ".new"
	backupPath := targetPath + ".old"
	if err := removeUpdateArtifactWith(removeWindowsUpdateFile, newPath); err != nil {
		return fmt.Errorf("清理残留新版本失败：%w", err)
	}
	if err := removeUpdateArtifactWith(removeWindowsUpdateFile, backupPath); err != nil {
		return fmt.Errorf("清理旧备份失败：%w", err)
	}
	if err := copyUpdateFile(self, newPath); err != nil {
		return fmt.Errorf("准备新版本失败：%w", err)
	}
	newPending := pending
	newPending.PendingPath = newPath
	if err := verifyPendingExecutable(newPending, verifier); err != nil {
		logPendingUpdateDiagnostic(pending, "新版本落盘安全校验诊断", err, "artifact_verification_failed")
		verificationErr := errors.New("新版本落盘安全校验失败")
		return errors.Join(verificationErr, removeUpdateArtifactWith(removeWindowsUpdateFile, newPath))
	}
	if err := renameWindowsUpdateFile(targetPath, backupPath); err != nil {
		return errors.Join(fmt.Errorf("备份旧版本失败：%w", err), removeUpdateArtifactWith(removeWindowsUpdateFile, newPath))
	}
	if err := renameWindowsUpdateFile(newPath, targetPath); err != nil {
		restoreErr := renameWindowsUpdateFile(backupPath, targetPath)
		cleanupErr := removeUpdateArtifactWith(removeWindowsUpdateFile, newPath)
		if restoreErr != nil {
			return errors.Join(fmt.Errorf("替换程序失败且恢复旧版本失败：%w", err), restoreErr, cleanupErr)
		}
		return errors.Join(fmt.Errorf("替换程序失败，已恢复旧版本：%w", err), cleanupErr)
	}
	finalTarget := pending
	finalTarget.PendingPath = targetPath
	if err := verifyPendingExecutable(finalTarget, verifier); err != nil {
		logPendingUpdateDiagnostic(pending, "最终更新目标安全校验诊断", err, "artifact_verification_failed")
		removeErr := removeUpdateArtifactWith(removeWindowsUpdateFile, targetPath)
		var restoreErr error
		if removeErr == nil {
			restoreErr = renameWindowsUpdateFile(backupPath, targetPath)
		}
		if removeErr != nil || restoreErr != nil {
			logPendingUpdateDiagnostic(pending, "最终更新目标校验失败后的恢复诊断", errors.Join(removeErr, restoreErr), "rollback_failed")
			return errors.New("最终更新目标安全校验失败且恢复失败")
		}
		return errors.New("最终更新目标安全校验失败，已恢复旧版本")
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
		closeErr := target.Close()
		cleanupErr := removeUpdateArtifactWith(removeWindowsUpdateFile, targetPath)
		return errors.Join(err, closeErr, cleanupErr)
	}
	if err := target.Sync(); err != nil {
		closeErr := target.Close()
		cleanupErr := removeUpdateArtifactWith(removeWindowsUpdateFile, targetPath)
		return errors.Join(err, closeErr, cleanupErr)
	}
	if err := target.Close(); err != nil {
		return errors.Join(err, removeUpdateArtifactWith(removeWindowsUpdateFile, targetPath))
	}
	return nil
}
