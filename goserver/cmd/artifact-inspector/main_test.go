package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFFmpegClosureCommandsFailClosedOnMissingExplicitPaths(t *testing.T) {
	for _, test := range []struct {
		command string
		want    string
	}{
		{command: "seal-ffmpeg", want: "FFmpeg seal arguments"},
		{command: "publish-ffmpeg", want: "FFmpeg publication arguments"},
	} {
		t.Run(test.command, func(t *testing.T) {
			err := run([]string{test.command}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestCertificateCommandUsesGeneratedDERSerialNumberIdentity(t *testing.T) {
	der := commandCertificate(t, false)
	path := filepath.Join(t.TempDir(), "certificate.der")
	if err := os.WriteFile(path, der, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := run([]string{"certificate", "--der", path, "--country", "CN", "--organization", "RushRush Network Technology Ltd", "--organization-id", "91450900MADM3GLG5P"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output.Bytes(), []byte(`"organizationId":"91450900MADM3GLG5P"`)) {
		t.Fatalf("output = %s", output.Bytes())
	}
}

func TestCertificateCommandRejectsOrganizationIdentifierOIDDecoy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "certificate.der")
	if err := os.WriteFile(path, commandCertificate(t, true), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"certificate", "--der", path, "--country", "CN", "--organization", "RushRush Network Technology Ltd", "--organization-id", "91450900MADM3GLG5P"}, &bytes.Buffer{}); err == nil {
		t.Fatal("2.5.4.97 decoy accepted")
	}
}

func commandCertificate(t testing.TB, decoy bool) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	organizationIDOid := asn1.ObjectIdentifier{2, 5, 4, 5}
	if decoy {
		organizationIDOid = asn1.ObjectIdentifier{2, 5, 4, 97}
	}
	rdns := pkix.RDNSequence{
		{{Type: asn1.ObjectIdentifier{2, 5, 4, 6}, Value: "CN"}},
		{{Type: asn1.ObjectIdentifier{2, 5, 4, 10}, Value: "RushRush Network Technology Ltd"}},
		{{Type: organizationIDOid, Value: "91450900MADM3GLG5P"}},
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
