//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInspectAuthenticodeAcceptsValidDER(t *testing.T) {
	der := makeUpdateSigningCertificateDER(t, updateCertificateFixture{OrganizationID: "91210103MA7CJ3C094"})
	certificate, err := inspectAuthenticodeWithRunner(`C:\download\candidate.exe`, func(string) ([]byte, error) {
		return []byte(`{"status":"Valid","certificateDerBase64":"` + base64.StdEncoding.EncodeToString(der) + `"}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := updateCertificateIdentity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}
	if certificate.LegalIdentity != want {
		t.Fatalf("legal identity = %#v, want %#v", certificate.LegalIdentity, want)
	}
}

func TestInspectAuthenticodeRejectsInvalidResults(t *testing.T) {
	validDER := base64.StdEncoding.EncodeToString(makeUpdateSigningCertificateDER(t, updateCertificateFixture{OrganizationID: "91210103MA7CJ3C094"}))
	tests := []struct {
		name        string
		output      string
		runnerErr   error
		wantMessage string
	}{
		{name: "not signed", output: `{"status":"NotSigned","certificateDerBase64":""}`, wantMessage: "NotSigned"},
		{name: "empty certificate", output: `{"status":"Valid","certificateDerBase64":""}`, wantMessage: "证书"},
		{name: "malformed Base64", output: `{"status":"Valid","certificateDerBase64":"not-base64"}`, wantMessage: "Base64"},
		{name: "display subject is rejected", output: `{"status":"Valid","certificateDerBase64":"` + validDER + `","subject":"CN=untrusted"}`, wantMessage: "JSON"},
		{name: "malformed JSON", output: `{"status":`, wantMessage: "JSON"},
		{name: "command failure", runnerErr: errors.New("PowerShell failed"), wantMessage: "PowerShell failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := inspectAuthenticodeWithRunner(`C:\download\candidate.exe`, func(string) ([]byte, error) {
				return []byte(test.output), test.runnerErr
			})
			if err == nil || !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("error = %v, want message containing %q", err, test.wantMessage)
			}
		})
	}
}

func TestInspectAuthenticodeRunnerReceivesLiteralPath(t *testing.T) {
	const executablePath = `C:\更新\odd '$(); executable.exe`
	_, _ = inspectAuthenticodeWithRunner(executablePath, func(path string) ([]byte, error) {
		if path != executablePath {
			t.Fatalf("PowerShell path = %q, want %q", path, executablePath)
		}
		return nil, errors.New("stop after path assertion")
	})
}

func TestVerifyAuthenticodePublisherFailsClosedForUnreviewedConfiguration(t *testing.T) {
	err := verifyAuthenticodePublisher(`C:\download\candidate.exe`, "CN=Expected Publisher")
	if err == nil || !strings.Contains(err.Error(), "未审查") {
		t.Fatalf("error = %v, want unreviewed publisher error", err)
	}
}

func TestVerifyAuthenticodePublisherWithRunnerAcceptsReviewedLegalIdentities(t *testing.T) {
	tests := []struct {
		name     string
		expected string
		fixture  updateCertificateFixture
	}{
		{name: "NaisNet", expected: legacyExpectedNaisNetPublisher, fixture: updateCertificateFixture{OrganizationID: "91210103MA7CJ3C094"}},
		{name: "RushRush", expected: legacyExpectedRushRushPublisher, fixture: updateCertificateFixture{Organizations: []string{"RushRush Network Technology Ltd"}, OrganizationID: "91450900MADM3GLG5P"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			der := makeUpdateSigningCertificateDER(t, test.fixture)
			err := verifyAuthenticodePublisherWithRunner(`C:\download\candidate.exe`, test.expected, func(string) ([]byte, error) {
				return []byte(`{"status":"Valid","certificateDerBase64":"` + base64.StdEncoding.EncodeToString(der) + `"}`), nil
			})
			if err != nil {
				t.Fatalf("reviewed %s certificate rejected: %v", test.name, err)
			}
		})
	}
}

func TestVerifyAuthenticodePublisherWithRunnerRejectsUnknownOrMismatchedIdentity(t *testing.T) {
	naisNetDER := makeUpdateSigningCertificateDER(t, updateCertificateFixture{OrganizationID: "91210103MA7CJ3C094"})
	rushRushDER := makeUpdateSigningCertificateDER(t, updateCertificateFixture{
		Organizations: []string{"RushRush Network Technology Ltd"}, OrganizationID: "91450900MADM3GLG5P",
	})
	tests := []struct {
		name      string
		expected  string
		der       []byte
		wantError string
	}{
		{name: "unknown configured publisher", expected: "CN=Unreviewed Publisher", der: naisNetDER, wantError: "未审查"},
		{name: "reviewed NaisNet configuration with RushRush certificate", expected: legacyExpectedNaisNetPublisher, der: rushRushDER, wantError: "不匹配"},
		{name: "reviewed RushRush configuration with NaisNet certificate", expected: legacyExpectedRushRushPublisher, der: naisNetDER, wantError: "不匹配"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := verifyAuthenticodePublisherWithRunner(`C:\download\candidate.exe`, test.expected, func(string) ([]byte, error) {
				return []byte(`{"status":"Valid","certificateDerBase64":"` + base64.StdEncoding.EncodeToString(test.der) + `"}`), nil
			})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want message containing %q", err, test.wantError)
			}
		})
	}
}

func TestInspectAuthenticodeRejectsMissingSystemPowerShell(t *testing.T) {
	t.Setenv("WINDIR", t.TempDir())
	_, err := inspectAuthenticode(`C:\download\candidate.exe`)
	if err == nil || !strings.Contains(err.Error(), "PowerShell") {
		t.Fatalf("error = %v, want missing PowerShell error", err)
	}
}

func TestAuthenticodeCommandHidesWindow(t *testing.T) {
	command := newAuthenticodeCommand(context.Background(), `C:\Windows\powershell.exe`, "-NoProfile")
	if command.Path == "" || command.SysProcAttr == nil || !command.SysProcAttr.HideWindow {
		t.Fatalf("command = %#v", command)
	}
	if got, want := filepath.Clean(command.Path), filepath.Clean(`C:\Windows\powershell.exe`); got != want {
		t.Fatalf("command path = %q, want %q", got, want)
	}
}

func TestAuthenticodeCommandPreservesLiteralPath(t *testing.T) {
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

func TestAuthenticodeCommandTerminatesOnDeadline(t *testing.T) {
	if os.Getenv("GO_WANT_AUTHENTICODE_TIMEOUT_HELPER") == "1" {
		time.Sleep(10 * time.Second)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	command := newAuthenticodeCommand(ctx, os.Args[0], "-test.run=^TestAuthenticodeCommandTerminatesOnDeadline$")
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

func TestAuthenticodeCommandRejectsCombinedOutputOverflow(t *testing.T) {
	if os.Getenv("GO_WANT_AUTHENTICODE_OUTPUT_HELPER") == "1" {
		_, _ = os.Stdout.Write(bytes.Repeat([]byte{'o'}, 80))
		_, _ = os.Stderr.Write(bytes.Repeat([]byte{'e'}, 80))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := newAuthenticodeCommand(ctx, os.Args[0], "-test.run=^TestAuthenticodeCommandRejectsCombinedOutputOverflow$")
	command.Env = append(os.Environ(), "GO_WANT_AUTHENTICODE_OUTPUT_HELPER=1")
	output, err := runBoundedAuthenticodeCommand(command, 128)
	if err == nil || !strings.Contains(err.Error(), "输出超过限制") {
		t.Fatalf("error = %v, output bytes = %d", err, len(output))
	}
	if len(output) > 128 {
		t.Fatalf("captured output bytes = %d, want at most 128", len(output))
	}
}
