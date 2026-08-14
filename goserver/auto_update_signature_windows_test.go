//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestVerifyAuthenticodePublisherAcceptsValidExactSubject(t *testing.T) {
	const (
		powershell      = `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
		executablePath  = `C:\更新\odd '$(); executable.exe`
		expectedSubject = "CN=Expected Publisher, O=Expected Publisher"
	)
	var gotName string
	var gotArgs []string
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return []byte(`{"status":"Valid","subject":"CN=Expected Publisher, O=Expected Publisher"}`), nil
	}

	if err := verifyAuthenticodePublisherWithRunner(context.Background(), executablePath, expectedSubject, powershell, runner); err != nil {
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
			runner := func(context.Context, string, ...string) ([]byte, error) {
				return []byte(test.output), test.runnerErr
			}
			err := verifyAuthenticodePublisherWithRunner(context.Background(), `C:\download\candidate.exe`, expectedSubject, `C:\Windows\powershell.exe`, runner)
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
	runner := func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"status":"Valid","subject":"CN=Expected Publisher"}`), nil
	}
	err := verifyAuthenticodePublisherWithRunner(context.Background(), `C:\download\candidate.exe`, "", `C:\Windows\powershell.exe`, runner)
	if err == nil || !strings.Contains(err.Error(), "发布者") {
		t.Fatalf("error = %v, want missing publisher error", err)
	}
}

func TestVerifyAuthenticodePublisherCommandHidesWindow(t *testing.T) {
	command := newAuthenticodeCommand(context.Background(), `C:\Windows\powershell.exe`, "-NoProfile")
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
		context.Background(),
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

func TestVerifyAuthenticodePublisherThreadsContextToRunner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	runner := func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	started := time.Now()
	err := verifyAuthenticodePublisherWithRunner(ctx, `C:\download\candidate.exe`, "CN=Expected Publisher", `C:\Windows\powershell.exe`, runner)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("verification returned after %v, want bounded cancellation", elapsed)
	}
}

func TestVerifyAuthenticodePublisherCommandTerminatesOnDeadline(t *testing.T) {
	if os.Getenv("GO_WANT_AUTHENTICODE_TIMEOUT_HELPER") == "1" {
		time.Sleep(10 * time.Second)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	command := newAuthenticodeCommand(ctx, os.Args[0], "-test.run=^TestVerifyAuthenticodePublisherCommandTerminatesOnDeadline$")
	command.Env = append(os.Environ(), "GO_WANT_AUTHENTICODE_TIMEOUT_HELPER=1")
	started := time.Now()
	err := command.Run()
	if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("command error = %v, context error = %v", err, ctx.Err())
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("hung PowerShell-equivalent process terminated after %v", elapsed)
	}
}

func TestVerifyAuthenticodePublisherRejectsCombinedOutputOverflow(t *testing.T) {
	if os.Getenv("GO_WANT_AUTHENTICODE_OUTPUT_HELPER") == "1" {
		_, _ = os.Stdout.Write(bytes.Repeat([]byte{'o'}, 80))
		_, _ = os.Stderr.Write(bytes.Repeat([]byte{'e'}, 80))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := newAuthenticodeCommand(ctx, os.Args[0], "-test.run=^TestVerifyAuthenticodePublisherRejectsCombinedOutputOverflow$")
	command.Env = append(os.Environ(), "GO_WANT_AUTHENTICODE_OUTPUT_HELPER=1")
	output, err := runBoundedAuthenticodeCommand(command, 128)
	if err == nil || !strings.Contains(err.Error(), "输出超过限制") {
		t.Fatalf("error = %v, output bytes = %d", err, len(output))
	}
	if len(output) > 128 {
		t.Fatalf("captured output bytes = %d, want at most 128", len(output))
	}
}
