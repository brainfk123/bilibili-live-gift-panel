//go:build windows

package main

import (
	"bytes"
	"context"
	"os/exec"
	"testing"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/trustpolicy/bundlefs"
)

func TestImportedBundleRejectsBroadenedWindowsDACL(t *testing.T) {
	fixture := newImportBundleFixture(t)
	if err := run(context.Background(), fixture.args(), nil, nil, &bytes.Buffer{}, nil); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("icacls.exe", fixture.parent, "/grant", "*S-1-1-0:(OI)(CI)M")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("broaden imported parent DACL: %v: %s", err, output)
	}
	committed, err := bundlefs.ReadCommittedBundle(fixture.policyPath(), fixture.auditPath())
	if err == nil || len(committed.Policy) != 0 || len(committed.Audit) != 0 {
		t.Fatal("bundle with broadened parent DACL exposed trusted bytes")
	}
}
