//go:build !windows

package main

import "errors"

func isAutoUpdateSupported() bool {
	return false
}

func launchUpdateInstaller(_ string, _ int) error {
	return errors.New("当前系统暂不支持自动更新")
}

func applyDownloadedUpdate(_ pendingUpdate, _ int) error {
	return errors.New("当前系统暂不支持自动更新")
}
