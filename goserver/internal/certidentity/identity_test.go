package certidentity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"math/big"
	"strings"
	"testing"
	"time"
)

var (
	oidCountry             = asn1.ObjectIdentifier{2, 5, 4, 6}
	oidOrganization        = asn1.ObjectIdentifier{2, 5, 4, 10}
	oidSubjectSerialNumber = asn1.ObjectIdentifier{2, 5, 4, 5}
	oidOrganizationIDDecoy = asn1.ObjectIdentifier{2, 5, 4, 97}
)

func TestParseCertificateRequiresExactSerialNumberOIDIdentityAndCodeSigning(t *testing.T) {
	der := generatedCertificate(t, []pkix.AttributeTypeAndValue{
		{Type: oidCountry, Value: "CN"},
		{Type: oidOrganization, Value: "NaisNet Technology Co., Ltd."},
		{Type: oidSubjectSerialNumber, Value: "91210103MA7CJ3C094"},
	}, []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning})
	parsed, err := ParseCertificateDER(der)
	if err != nil {
		t.Fatal(err)
	}
	want := Identity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}
	if parsed.Identity != want || len(parsed.DER) == 0 {
		t.Fatalf("parsed = %#v, want %#v", parsed, want)
	}
}

func TestParseCertificateRejectsOrganizationIdentifierOIDDisplayDecoy(t *testing.T) {
	der := generatedCertificate(t, []pkix.AttributeTypeAndValue{
		{Type: oidCountry, Value: "CN"},
		{Type: oidOrganization, Value: "RushRush Network Technology Ltd"},
		{Type: oidOrganizationIDDecoy, Value: "91450900MADM3GLG5P"},
	}, []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning})
	if _, err := ParseCertificateDER(der); err == nil || !strings.Contains(err.Error(), "organization ID") {
		t.Fatalf("2.5.4.97 decoy error = %v", err)
	}
}

func TestParseCertificateRejectsDuplicateOrAmbiguousStructuredFields(t *testing.T) {
	tests := []struct {
		name       string
		attributes []pkix.AttributeTypeAndValue
	}{
		{name: "duplicate serial number", attributes: []pkix.AttributeTypeAndValue{
			{Type: oidCountry, Value: "CN"}, {Type: oidOrganization, Value: "RushRush Network Technology Ltd"},
			{Type: oidSubjectSerialNumber, Value: "91450900MADM3GLG5P"}, {Type: oidSubjectSerialNumber, Value: "other"},
		}},
		{name: "duplicate organization", attributes: []pkix.AttributeTypeAndValue{
			{Type: oidCountry, Value: "CN"}, {Type: oidOrganization, Value: "RushRush Network Technology Ltd"},
			{Type: oidOrganization, Value: "Other"}, {Type: oidSubjectSerialNumber, Value: "91450900MADM3GLG5P"},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseCertificateDER(generatedCertificate(t, test.attributes, []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning})); err == nil {
				t.Fatal("ambiguous identity accepted")
			}
		})
	}
}

func TestParseCertificateRejectsMissingCodeSigningEKU(t *testing.T) {
	der := generatedCertificate(t, []pkix.AttributeTypeAndValue{
		{Type: oidCountry, Value: "CN"},
		{Type: oidOrganization, Value: "NaisNet Technology Co., Ltd."},
		{Type: oidSubjectSerialNumber, Value: "91210103MA7CJ3C094"},
	}, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	if _, err := ParseCertificateDER(der); err == nil || !strings.Contains(err.Error(), "Code Signing") {
		t.Fatalf("EKU error = %v", err)
	}
}

func generatedCertificate(t testing.TB, attributes []pkix.AttributeTypeAndValue, usages []x509.ExtKeyUsage) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		RawSubject:   mustMarshalRDNSequence(t, attributes),
		NotBefore:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  usages,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func mustMarshalRDNSequence(t testing.TB, attributes []pkix.AttributeTypeAndValue) []byte {
	t.Helper()
	rdns := make(pkix.RDNSequence, 0, len(attributes))
	for _, attribute := range attributes {
		rdns = append(rdns, []pkix.AttributeTypeAndValue{attribute})
	}
	der, err := asn1.Marshal(rdns)
	if err != nil {
		t.Fatal(err)
	}
	return der
}
