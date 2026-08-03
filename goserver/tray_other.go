//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
)

func acquireSingleInstance() (bool, func(), error) {
	return false, func() {}, nil
}

func openURL(url string) {
	command := "xdg-open"
	if runtime.GOOS == "darwin" {
		command = "open"
	}
	_ = exec.Command(command, url).Start()
}

func showStartupError(message string) {
	_, _ = fmt.Fprintln(os.Stderr, message)
}

func runTrayApp(_ string, notifications *notificationCenter, updateExit <-chan struct{}) (bool, error) {
	notifications.AttachSink(func(notification desktopNotification) {
		_, _ = fmt.Fprintf(os.Stderr, "%s：%s\n", notification.Title, notification.Body)
	})
	defer notifications.DetachSink()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case <-stop:
		return false, nil
	case <-updateExit:
		return true, nil
	}
}
