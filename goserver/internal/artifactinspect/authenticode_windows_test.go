//go:build windows

package artifactinspect

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"math/big"
	"testing"
	"time"
)

func TestInspectAuthenticodeCertificateParsesOnlyValidStructuredCertificate(t *testing.T) {
	der := authenticodeTestCertificate(t)
	encoded := base64.StdEncoding.EncodeToString(der)
	certificate, err := parseAuthenticodeCertificateOutput([]byte(`{"status":"Valid","certificateDerBase64":"` + encoded + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(certificate.DER) != string(der) || certificate.Identity.Country != "CN" || certificate.Identity.Organization != "FutureCo Technology Co., Ltd." || certificate.Identity.OrganizationID != "91110000EXAMPLE01" {
		t.Fatalf("certificate = %#v", certificate)
	}
	for _, output := range []string{
		`{"status":"NotSigned","certificateDerBase64":"` + encoded + `"}`,
		`{"status":"Valid","certificateDerBase64":""}`,
		`{"status":"Valid","certificateDerBase64":"%%%"}`,
	} {
		if _, err := parseAuthenticodeCertificateOutput([]byte(output)); err == nil {
			t.Fatalf("invalid Authenticode output accepted: %s", output)
		}
	}
}

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

func authenticodeTestCertificate(t testing.TB) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rdns := pkix.RDNSequence{
		{{Type: asn1.ObjectIdentifier{2, 5, 4, 6}, Value: "CN"}},
		{{Type: asn1.ObjectIdentifier{2, 5, 4, 10}, Value: "FutureCo Technology Co., Ltd."}},
		{{Type: asn1.ObjectIdentifier{2, 5, 4, 5}, Value: "91110000EXAMPLE01"}},
	}
	rawSubject, err := asn1.Marshal(rdns)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), RawSubject: rawSubject, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}
