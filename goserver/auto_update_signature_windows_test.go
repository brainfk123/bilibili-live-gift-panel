//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestVerifyAuthenticodePublisherAcceptsValidExactSubject(t *testing.T) {
	const (
		powershell      = `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
		executablePath  = `C:\更新\odd '$(); executable.exe`
		expectedSubject = "CN=Expected Publisher, O=Expected Publisher"
	)
	var gotName string
	var gotArgs []string
	runner := func(name string, args ...string) ([]byte, error) {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return []byte(`{"status":"Valid","subject":"CN=Expected Publisher, O=Expected Publisher"}`), nil
	}

	if err := verifyAuthenticodePublisherWithRunner(executablePath, expectedSubject, powershell, runner); err != nil {
		t.Fatal(err)
	}
	if gotName != powershell {
		t.Fatalf("PowerShell executable = %q, want %q", gotName, powershell)
	}
	if len(gotArgs) != 7 {
		t.Fatalf("PowerShell args = %#v", gotArgs)
	}
	wantPrefix := []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command"}
	if !reflect.DeepEqual(gotArgs[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("PowerShell args prefix = %#v, want %#v", gotArgs[:len(wantPrefix)], wantPrefix)
	}
	script := gotArgs[5]
	if !strings.Contains(script, "Get-AuthenticodeSignature -LiteralPath $args[0]") {
		t.Fatalf("PowerShell script does not query the literal path argument: %q", script)
	}
	if !strings.Contains(script, "[Console]::OutputEncoding") {
		t.Fatalf("PowerShell script does not select an explicit output encoding: %q", script)
	}
	if strings.Contains(script, executablePath) {
		t.Fatalf("executable path was interpolated into PowerShell script: %q", script)
	}
	if gotArgs[6] != executablePath {
		t.Fatalf("PowerShell path argument = %q, want %q", gotArgs[6], executablePath)
	}
}

func TestVerifyAuthenticodePublisherRejectsInvalidResults(t *testing.T) {
	const expectedSubject = "CN=Expected Publisher, O=Expected Publisher"
	tests := []struct {
		name        string
		output      string
		runnerErr   error
		wantMessage string
	}{
		{name: "not signed", output: `{"status":"NotSigned","subject":""}`, wantMessage: "NotSigned"},
		{name: "hash mismatch", output: `{"status":"HashMismatch","subject":"CN=Expected Publisher, O=Expected Publisher"}`, wantMessage: "HashMismatch"},
		{name: "empty certificate", output: `{"status":"Valid","subject":""}`, wantMessage: "证书"},
		{name: "wrong subject", output: `{"status":"Valid","subject":"CN=Other Publisher, O=Other Publisher"}`, wantMessage: "发布者"},
		{name: "malformed JSON", output: `{"status":`, wantMessage: "JSON"},
		{name: "command failure", runnerErr: errors.New("PowerShell failed"), wantMessage: "PowerShell failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := func(string, ...string) ([]byte, error) {
				return []byte(test.output), test.runnerErr
			}
			err := verifyAuthenticodePublisherWithRunner(`C:\download\candidate.exe`, expectedSubject, `C:\Windows\powershell.exe`, runner)
			if err == nil || !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("error = %v, want message containing %q", err, test.wantMessage)
			}
		})
	}
}

func TestVerifyAuthenticodePublisherRejectsMissingSystemPowerShell(t *testing.T) {
	t.Setenv("WINDIR", t.TempDir())
	err := verifyAuthenticodePublisher(`C:\download\candidate.exe`, "CN=Expected Publisher")
	if err == nil || !strings.Contains(err.Error(), "PowerShell") {
		t.Fatalf("error = %v, want missing PowerShell error", err)
	}
}

func TestVerifyAuthenticodePublisherRejectsEmptyExpectedSubject(t *testing.T) {
	runner := func(string, ...string) ([]byte, error) {
		return []byte(`{"status":"Valid","subject":"CN=Expected Publisher"}`), nil
	}
	err := verifyAuthenticodePublisherWithRunner(`C:\download\candidate.exe`, "", `C:\Windows\powershell.exe`, runner)
	if err == nil || !strings.Contains(err.Error(), "发布者") {
		t.Fatalf("error = %v, want missing publisher error", err)
	}
}

func TestVerifyAuthenticodePublisherCommandHidesWindow(t *testing.T) {
	command := newAuthenticodeCommand(`C:\Windows\powershell.exe`, "-NoProfile")
	if command.Path == "" || command.SysProcAttr == nil || !command.SysProcAttr.HideWindow {
		t.Fatalf("command = %#v", command)
	}
	if got, want := filepath.Clean(command.Path), filepath.Clean(`C:\Windows\powershell.exe`); got != want {
		t.Fatalf("command path = %q, want %q", got, want)
	}
}

func TestVerifyAuthenticodePublisherRunnerPreservesLiteralPath(t *testing.T) {
	powershell, err := systemWindowsPowerShellPath()
	if err != nil {
		t.Fatal(err)
	}
	const executablePath = `C:\更新\odd '$(); executable.exe`
	const echoArgumentScript = `& { [Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false); [pscustomobject]@{path=$args[0]} | ConvertTo-Json -Compress }`
	output, err := runAuthenticodeCommand(
		powershell,
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-Command", echoArgumentScript,
		executablePath,
	)
	if err != nil {
		t.Fatalf("PowerShell literal-path round trip failed: %v, output = %s", err, output)
	}
	var result struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("PowerShell output = %q: %v", output, err)
	}
	if result.Path != executablePath {
		t.Fatalf("PowerShell path = %q, want %q", result.Path, executablePath)
	}
}
