//go:build windows

package artifactinspect

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"

	"bilibili-live-gift-panel/internal/certidentity"
)

const inspectAuthenticodeScript = `$path = [Console]::In.ReadToEnd(); $signature = Get-AuthenticodeSignature -LiteralPath $path; [pscustomobject]@{status=[string]$signature.Status;certificateDerBase64=if($null -eq $signature.SignerCertificate){''}else{[Convert]::ToBase64String($signature.SignerCertificate.RawData)}} | ConvertTo-Json -Compress`

func InspectAuthenticodeFile(path string) (certidentity.Identity, error) {
	certificate, err := InspectAuthenticodeCertificate(path)
	if err != nil {
		return certidentity.Identity{}, err
	}
	return certificate.Identity, nil
}

func InspectAuthenticodeCertificate(path string) (certidentity.Certificate, error) {
	command := powershellLiteralPathCommand(inspectAuthenticodeScript, path)
	output, err := command.Output()
	if err != nil || len(output) > 16<<10 {
		return certidentity.Certificate{}, errors.New("Authenticode inspection failed")
	}
	return parseAuthenticodeCertificateOutput(output)
}

func powershellLiteralPathCommand(script, path string) *exec.Cmd {
	command := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	command.Stdin = strings.NewReader(path)
	for _, entry := range os.Environ() {
		name, _, present := strings.Cut(entry, "=")
		if present && strings.EqualFold(name, "PSModulePath") {
			continue
		}
		command.Env = append(command.Env, entry)
	}
	return command
}

func parseAuthenticodeCertificateOutput(output []byte) (certidentity.Certificate, error) {
	var result struct {
		Status      string `json:"status"`
		Certificate string `json:"certificateDerBase64"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil || result.Status != "Valid" || result.Certificate == "" {
		return certidentity.Certificate{}, errors.New("Authenticode is not Valid")
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) || token != nil {
		return certidentity.Certificate{}, errors.New("Authenticode is not Valid")
	}
	der, err := base64.StdEncoding.Strict().DecodeString(result.Certificate)
	if err != nil {
		return certidentity.Certificate{}, errors.New("Authenticode certificate is invalid")
	}
	parsed, err := certidentity.ParseCertificateDER(der)
	if err != nil {
		return certidentity.Certificate{}, errors.New("Authenticode certificate identity is invalid")
	}
	return parsed, nil
}
