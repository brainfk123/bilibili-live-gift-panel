//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const authenticodePowerShellScript = `& { [Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false); $signature = Get-AuthenticodeSignature -LiteralPath $args[0]; [pscustomobject]@{status=[string]$signature.Status;certificateDerBase64=if ($null -eq $signature.SignerCertificate) { "" } else { [Convert]::ToBase64String($signature.SignerCertificate.RawData) }} | ConvertTo-Json -Compress }`
const authenticodeVerificationTimeout = 30 * time.Second
const authenticodeOutputMaxBytes = 16 << 10

const (
	legacyExpectedNaisNetPublisher  = "NaisNet Technology Co., Ltd."
	legacyExpectedRushRushPublisher = "RushRush Network Technology Ltd"
)

type powershellRunner func(string) ([]byte, error)

type authenticodeInspection struct {
	Status               string `json:"status"`
	CertificateDERBase64 string `json:"certificateDerBase64"`
}

type boundedAuthenticodeOutput struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (output *boundedAuthenticodeOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	remaining := output.limit - output.buffer.Len()
	if remaining > 0 {
		written := len(data)
		if written > remaining {
			written = remaining
		}
		_, _ = output.buffer.Write(data[:written])
	}
	if len(data) > remaining {
		output.overflow = true
	}
	return len(data), nil
}

// verifyAuthenticodePublisher is retained only for pre-enrollment historical
// builds. Enrollment updater candidates use inspectAuthenticode and the signed
// policy authorization path in auto_update.go instead.
func verifyAuthenticodePublisher(path, expectedPublisher string) error {
	return verifyAuthenticodePublisherWithRunner(path, expectedPublisher, runAuthenticodePowerShell)
}

func inspectAuthenticode(path string) (inspectedUpdateCertificate, error) {
	return inspectAuthenticodeWithRunner(path, runAuthenticodePowerShell)
}

func runAuthenticodePowerShell(path string) ([]byte, error) {
	powershell, err := systemWindowsPowerShellPath()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), authenticodeVerificationTimeout)
	defer cancel()
	return runAuthenticodeCommand(
		ctx,
		powershell,
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-Command", authenticodePowerShellScript,
		path,
	)
}

func systemWindowsPowerShellPath() (string, error) {
	return systemWindowsPowerShellPathWith(windowsSystemDirectory, os.Stat)
}

func systemWindowsPowerShellPathWith(systemDirectory func() (string, error), stat func(string) (os.FileInfo, error)) (string, error) {
	if systemDirectory == nil || stat == nil {
		return "", errors.New("无法定位系统 PowerShell")
	}
	directory, err := systemDirectory()
	if err != nil || !filepath.IsAbs(directory) {
		return "", errors.New("无法定位系统 PowerShell")
	}
	powershell := filepath.Join(filepath.Clean(directory), "WindowsPowerShell", "v1.0", "powershell.exe")
	info, err := stat(powershell)
	if err != nil {
		return "", fmt.Errorf("无法定位系统 PowerShell：%w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("系统 PowerShell 路径不是文件")
	}
	return powershell, nil
}

var getSystemDirectoryW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemDirectoryW")

func windowsSystemDirectory() (string, error) {
	buffer := make([]uint16, 260)
	for attempts := 0; attempts < 2; attempts++ {
		length, _, callErr := getSystemDirectoryW.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
		if length == 0 {
			return "", fmt.Errorf("GetSystemDirectoryW failed: %w", callErr)
		}
		if length < uintptr(len(buffer)) {
			directory := syscall.UTF16ToString(buffer[:length])
			if directory == "" || !filepath.IsAbs(directory) {
				return "", errors.New("GetSystemDirectoryW returned an invalid path")
			}
			return directory, nil
		}
		buffer = make([]uint16, int(length)+1)
	}
	return "", errors.New("GetSystemDirectoryW path is too long")
}

func inspectAuthenticodeWithRunner(path string, run powershellRunner) (inspectedUpdateCertificate, error) {
	output, err := run(path)
	if err != nil {
		return inspectedUpdateCertificate{}, fmt.Errorf("执行 Authenticode 验证失败：%w", err)
	}
	output = bytes.TrimSpace(output)
	output = bytes.TrimPrefix(output, []byte{0xef, 0xbb, 0xbf})
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	var result authenticodeInspection
	if err := decoder.Decode(&result); err != nil {
		return inspectedUpdateCertificate{}, fmt.Errorf("解析 Authenticode JSON 失败：%w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return inspectedUpdateCertificate{}, errors.New("解析 Authenticode JSON 失败：包含额外数据")
	}
	if result.Status != "Valid" {
		return inspectedUpdateCertificate{}, fmt.Errorf("Authenticode 签名状态为 %q，预期 Valid", result.Status)
	}
	if result.CertificateDERBase64 == "" {
		return inspectedUpdateCertificate{}, errors.New("Authenticode 签名缺少发布者证书")
	}
	der, err := base64.StdEncoding.Strict().DecodeString(result.CertificateDERBase64)
	if err != nil {
		return inspectedUpdateCertificate{}, fmt.Errorf("解析 Authenticode 证书 Base64 失败：%w", err)
	}
	return parseUpdateSigningCertificate(der)
}

func verifyAuthenticodePublisherWithRunner(path, expectedPublisher string, run powershellRunner) error {
	expectedIdentity, err := reviewedLegacyPublisherIdentity(expectedPublisher)
	if err != nil {
		return err
	}
	certificate, err := inspectAuthenticodeWithRunner(path, run)
	if err != nil {
		return err
	}
	if normalizeUpdateCertificateIdentity(certificate.LegalIdentity) != expectedIdentity {
		return errors.New("Authenticode 发布者结构化身份不匹配")
	}
	return nil
}

func reviewedLegacyPublisherIdentity(expectedPublisher string) (updateCertificateIdentity, error) {
	switch expectedPublisher {
	case legacyExpectedNaisNetPublisher:
		return updateCertificateIdentity{Country: "CN", Organization: legacyExpectedNaisNetPublisher, OrganizationID: "91210103MA7CJ3C094"}, nil
	case legacyExpectedRushRushPublisher:
		return updateCertificateIdentity{Country: "CN", Organization: legacyExpectedRushRushPublisher, OrganizationID: "91450900MADM3GLG5P"}, nil
	default:
		return updateCertificateIdentity{}, errors.New("预期 Authenticode 发布者未审查")
	}
}

func newAuthenticodeCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, name, args...)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return command
}

func runAuthenticodeCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("PowerShell 命令缺少路径参数")
	}
	commandArgs := append([]string(nil), args...)
	lastArgument := len(commandArgs) - 1
	commandArgs[lastArgument] = "'" + strings.ReplaceAll(commandArgs[lastArgument], "'", "''") + "'"
	return runBoundedAuthenticodeCommand(newAuthenticodeCommand(ctx, name, commandArgs...), authenticodeOutputMaxBytes)
}

func runBoundedAuthenticodeCommand(command *exec.Cmd, limit int) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("PowerShell 输出限制无效")
	}
	output := &boundedAuthenticodeOutput{limit: limit}
	command.Stdout = output
	command.Stderr = output
	runErr := command.Run()
	output.mu.Lock()
	captured := append([]byte(nil), output.buffer.Bytes()...)
	overflow := output.overflow
	output.mu.Unlock()
	if overflow {
		return captured, errors.New("PowerShell 输出超过限制")
	}
	return captured, runErr
}
