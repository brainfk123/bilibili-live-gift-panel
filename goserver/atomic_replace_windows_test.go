//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsExtendedPathConversion(t *testing.T) {
	longTail := strings.Repeat(`directory\`, 30) + "file.json"
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "short absolute", path: `C:\short\file.json`, want: `C:\short\file.json`},
		{name: "extended", path: `\\?\C:\` + longTail, want: `\\?\C:\` + longTail},
		{name: "NT extended", path: `\??\C:\` + longTail, want: `\??\C:\` + longTail},
		{name: "device", path: `\\.\pipe\` + longTail, want: `\\.\pipe\` + longTail},
		{name: "drive", path: `C:\` + longTail, want: `\\?\C:\` + longTail},
		{name: "UNC", path: `\\server\share\` + longTail, want: `\\?\UNC\server\share\` + longTail},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := windowsExtendedPath(test.path); got != test.want {
				t.Fatalf("windowsExtendedPath(%q)=%q, want %q", test.path, got, test.want)
			}
		})
	}

	syntacticallyLong := `C:\` + strings.Repeat(`directory\..\`, 25) + "file.json"
	if len(syntacticallyLong) < 248 {
		t.Fatalf("syntactically long test path has only %d characters", len(syntacticallyLong))
	}
	if got, want := windowsExtendedPath(syntacticallyLong), `\\?\C:\file.json`; got != want {
		t.Fatalf("windowsExtendedPath(collapsible long path)=%q, want %q", got, want)
	}

	relative := strings.Repeat(`relative\`, 30) + "file.json"
	absolute, err := filepath.Abs(relative)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := windowsExtendedPath(relative), `\\?\`+absolute; got != want {
		t.Fatalf("windowsExtendedPath(long relative)=%q, want %q", got, want)
	}
}

func TestReplaceFileAtomicallySupportsWindowsLongPaths(t *testing.T) {
	directory := t.TempDir()
	for len(filepath.Join(directory, "final.json")) <= 280 {
		directory = filepath.Join(directory, strings.Repeat("d", 40))
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	temporaryPath := filepath.Join(directory, "temporary.json")
	finalPath := filepath.Join(directory, "final.json")
	if err := os.WriteFile(temporaryPath, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(finalPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	outcome := replaceFileAtomically(temporaryPath, finalPath)
	if outcome.Err != nil {
		t.Fatal(outcome.Err)
	}
	if !outcome.Committed || !outcome.Durable {
		t.Fatalf("long-path replacement outcome=%#v", outcome)
	}
	if data, err := os.ReadFile(finalPath); err != nil {
		t.Fatal(err)
	} else if string(data) != "new" {
		t.Fatalf("final data=%q, want new", data)
	}
}
