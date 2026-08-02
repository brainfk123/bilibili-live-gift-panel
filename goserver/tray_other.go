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

func runTrayApp(_ string) error {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	return nil
}
