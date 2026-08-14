//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const authenticodePowerShellScript = `& { [Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false); $signature = Get-AuthenticodeSignature -LiteralPath $args[0]; $subject = ''; if ($null -ne $signature.SignerCertificate) { $subject = $signature.SignerCertificate.Subject }; [pscustomobject]@{status=$signature.Status.ToString();subject=$subject} | ConvertTo-Json -Compress }`
const authenticodeVerificationTimeout = 30 * time.Second

type authenticodeCommandRunner func(context.Context, string, ...string) ([]byte, error)

type authenticodeQueryResult struct {
	Status  string `json:"status"`
	Subject string `json:"subject"`
}

func verifyAuthenticodePublisher(path, expectedSubject string) error {
	powershell, err := systemWindowsPowerShellPath()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), authenticodeVerificationTimeout)
	defer cancel()
	return verifyAuthenticodePublisherWithRunner(ctx, path, expectedSubject, powershell, runAuthenticodeCommand)
}

func systemWindowsPowerShellPath() (string, error) {
	windowsDirectory := strings.TrimSpace(os.Getenv("WINDIR"))
	if windowsDirectory == "" {
		return "", errors.New("无法定位系统 PowerShell：WINDIR 为空")
	}
	powershell := filepath.Join(windowsDirectory, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	info, err := os.Stat(powershell)
	if err != nil {
		return "", fmt.Errorf("无法定位系统 PowerShell：%w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("系统 PowerShell 路径不是文件")
	}
	return powershell, nil
}

func verifyAuthenticodePublisherWithRunner(ctx context.Context, path, expectedSubject, powershell string, runner authenticodeCommandRunner) error {
	if expectedSubject == "" {
		return errors.New("预期 Authenticode 发布者为空")
	}
	output, err := runner(
		ctx,
		powershell,
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-Command", authenticodePowerShellScript,
		path,
	)
	if err != nil {
		return fmt.Errorf("执行 Authenticode 验证失败：%w", err)
	}
	output = bytes.TrimSpace(output)
	output = bytes.TrimPrefix(output, []byte{0xef, 0xbb, 0xbf})
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	var result authenticodeQueryResult
	if err := decoder.Decode(&result); err != nil {
		return fmt.Errorf("解析 Authenticode JSON 失败：%w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("解析 Authenticode JSON 失败：包含额外数据")
	}
	if result.Status != "Valid" {
		return fmt.Errorf("Authenticode 签名状态为 %q，预期 Valid", result.Status)
	}
	if result.Subject == "" {
		return errors.New("Authenticode 签名缺少发布者证书")
	}
	if result.Subject != expectedSubject {
		return fmt.Errorf("Authenticode 发布者不匹配：得到 %q", result.Subject)
	}
	return nil
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
	return newAuthenticodeCommand(ctx, name, commandArgs...).CombinedOutput()
}
