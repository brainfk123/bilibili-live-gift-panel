//go:build !windows

package main

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
)

const giftClipDefaultEncoderMode = giftClipEncoderSoftware

type giftClipCommandRunner struct{}

func newGiftClipProcessRunner() giftClipProcessRunner {
	return giftClipCommandRunner{}
}

func (giftClipCommandRunner) Run(ctx context.Context, path string, args []string, stdout, stderr io.Writer) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, absPath, args...)
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}
