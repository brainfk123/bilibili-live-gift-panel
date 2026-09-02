//go:build windows

package artifactinspect

import "testing"

func TestPowerShellLiteralPathCommandTransportsPathOverStdin(t *testing.T) {
	path := `C:\release path\quote' semicolon; dollar$.exe`
	command := powershellLiteralPathCommand(`$path = [Console]::In.ReadToEnd(); [Console]::Out.Write($path)`, path)
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != path {
		t.Fatalf("path = %q, want %q", output, path)
	}
}

func TestPowerShellLiteralPathCommandRestoresWindowsModuleLookup(t *testing.T) {
	t.Setenv("PSModulePath", `Z:\missing-powershell-modules`)
	path := `C:\release path\signed.exe`
	command := powershellLiteralPathCommand(`$path = [Console]::In.ReadToEnd(); Import-Module Microsoft.PowerShell.Security -ErrorAction Stop; [Console]::Out.Write($path)`, path)
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != path {
		t.Fatalf("path = %q, want %q", output, path)
	}
}
