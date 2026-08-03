//go:build windows

package main

import (
	"fmt"
	"os"
	"testing"
)

func TestSingleInstanceMutexRejectsSecondOwner(t *testing.T) {
	mutexName := fmt.Sprintf("Local\\BilibiliLiveGiftPanelSingletonTest-%d", os.Getpid())
	alreadyRunning, release, err := acquireNamedSingleInstance(mutexName)
	if err != nil {
		t.Fatal(err)
	}
	if alreadyRunning {
		t.Fatal("test process unexpectedly found an existing panel instance")
	}
	defer release()

	secondAlreadyRunning, releaseSecond, err := acquireNamedSingleInstance(mutexName)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSecond()
	if !secondAlreadyRunning {
		t.Fatal("second instance acquired the singleton mutex")
	}
}
