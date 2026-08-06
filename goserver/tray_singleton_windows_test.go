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

func TestSingleInstanceMutexNameIncludesNormalizedVersion(t *testing.T) {
	if got, want := singleInstanceMutexName("v0.2.3"), singletonMutexName+"-0.2.3"; got != want {
		t.Fatalf("singleInstanceMutexName() = %q, want %q", got, want)
	}
	if current, previous := singleInstanceMutexName("0.2.3"), singleInstanceMutexName("0.2.2"); current == previous {
		t.Fatalf("different versions share mutex %q", current)
	}
	if got, want := singleInstanceMutexName("  "), singletonMutexName+"-dev"; got != want {
		t.Fatalf("blank version mutex = %q, want %q", got, want)
	}
}
