//go:build windows

package main

import "testing"

func TestSingleInstanceMutexRejectsSecondOwner(t *testing.T) {
	alreadyRunning, release, err := acquireSingleInstance()
	if err != nil {
		t.Fatal(err)
	}
	if alreadyRunning {
		t.Fatal("test process unexpectedly found an existing panel instance")
	}
	defer release()

	secondAlreadyRunning, releaseSecond, err := acquireSingleInstance()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSecond()
	if !secondAlreadyRunning {
		t.Fatal("second instance acquired the singleton mutex")
	}
}
