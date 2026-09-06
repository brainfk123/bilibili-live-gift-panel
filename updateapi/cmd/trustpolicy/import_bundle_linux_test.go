//go:build linux

package main

import (
	"bytes"
	"context"
	"os"
	"testing"
)

func TestImportedBundleUsesOwnerOnlyLinuxModes(t *testing.T) {
	fixture := newImportBundleFixture(t)
	if err := run(context.Background(), fixture.args(), nil, nil, &bytes.Buffer{}, nil); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		fixture.parent:                         0o700,
		fixture.parent + "/bundle":             0o700,
		fixture.policyPath():                   0o600,
		fixture.auditPath():                    0o600,
		fixture.parent + "/bundle/commit.json": 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("mode %s = %o, want %o", path, got, want)
		}
	}
}
