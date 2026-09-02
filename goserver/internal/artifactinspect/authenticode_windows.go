//go:build windows

package artifactinspect

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"

	"bilibili-live-gift-panel/internal/certidentity"
)

const inspectAuthenticodeScript = `$path = [Console]::In.ReadToEnd(); $signature = Get-AuthenticodeSignature -LiteralPath $path; [pscustomobject]@{status=[string]$signature.Status;certificateDerBase64=if($null -eq $signature.SignerCertificate){''}else{[Convert]::ToBase64String($signature.SignerCertificate.RawData)}} | ConvertTo-Json -Compress`

func InspectAuthenticodeFile(path string) (certidentity.Identity, error) {
	command := powershellLiteralPathCommand(inspectAuthenticodeScript, path)
	output, err := command.Output()
	if err != nil || len(output) > 16<<10 {
		return certidentity.Identity{}, errors.New("Authenticode inspection failed")
	}
	return parseAuthenticodeOutput(output)
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

func parseAuthenticodeOutput(output []byte) (certidentity.Identity, error) {
	var result struct {
		Status      string `json:"status"`
		Certificate string `json:"certificateDerBase64"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil || result.Status != "Valid" || result.Certificate == "" {
		return certidentity.Identity{}, errors.New("Authenticode is not Valid")
	}
	der, err := base64.StdEncoding.Strict().DecodeString(result.Certificate)
	if err != nil {
		return certidentity.Identity{}, errors.New("Authenticode certificate is invalid")
	}
	parsed, err := certidentity.ParseCertificateDER(der)
	if err != nil {
		return certidentity.Identity{}, errors.New("Authenticode certificate identity is invalid")
	}
	return parsed.Identity, nil
}
