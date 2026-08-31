package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestCertificateLegalIdentityIgnoresLeafRenewalFields(t *testing.T) {
	a := mustParseUpdateCertificate(t, makeUpdateSigningCertificateDER(t, updateCertificateFixture{
		SerialNumber: 101, OrganizationID: "91210103MA7CJ3C094", ValidFor: 365 * 24 * time.Hour,
	}))
	b := mustParseUpdateCertificate(t, makeUpdateSigningCertificateDER(t, updateCertificateFixture{
		SerialNumber: 202, OrganizationID: "91210103MA7CJ3C094", ValidFor: 730 * 24 * time.Hour,
	}))
	if bytes.Equal(a.DER, b.DER) {
		t.Fatal("renewal certificates unexpectedly have identical DER")
	}
	if a.LegalIdentity != b.LegalIdentity {
		t.Fatalf("same publisher renewal changed legal identity: %#v != %#v", a.LegalIdentity, b.LegalIdentity)
	}
	want := updateCertificateIdentity{Country: "CN", Organization: "NaisNet Technology Co., Ltd.", OrganizationID: "91210103MA7CJ3C094"}
	if a.LegalIdentity != want {
		t.Fatalf("legal identity = %#v, want %#v", a.LegalIdentity, want)
	}
}

func TestCertificateLegalIdentityRejectsChangedOrganizationID(t *testing.T) {
	a := mustParseUpdateCertificate(t, makeUpdateSigningCertificateDER(t, updateCertificateFixture{SerialNumber: 101, OrganizationID: "91210103MA7CJ3C094"}))
	b := mustParseUpdateCertificate(t, makeUpdateSigningCertificateDER(t, updateCertificateFixture{SerialNumber: 202, OrganizationID: "91450900MADM3GLG5P"}))
	if a.LegalIdentity == b.LegalIdentity {
		t.Fatalf("different organization IDs produced the same legal identity: %#v", a.LegalIdentity)
	}
}

func TestCertificateLegalIdentityRejectsAmbiguousStructuredFields(t *testing.T) {
	tests := []struct {
		name  string
		input updateCertificateFixture
		want  string
	}{
		{name: "missing country", input: updateCertificateFixture{Countries: []string{}, OrganizationID: "91210103MA7CJ3C094"}, want: "国家"},
		{name: "multiple countries", input: updateCertificateFixture{Countries: []string{"CN", "US"}, OrganizationID: "91210103MA7CJ3C094"}, want: "国家"},
		{name: "empty country", input: updateCertificateFixture{Countries: []string{""}, OrganizationID: "91210103MA7CJ3C094"}, want: "国家"},
		{name: "missing organization", input: updateCertificateFixture{Organizations: []string{}, OrganizationID: "91210103MA7CJ3C094"}, want: "组织"},
		{name: "multiple organizations", input: updateCertificateFixture{Organizations: []string{"NaisNet Technology Co., Ltd.", "Other"}, OrganizationID: "91210103MA7CJ3C094"}, want: "组织"},
		{name: "empty organization", input: updateCertificateFixture{Organizations: []string{""}, OrganizationID: "91210103MA7CJ3C094"}, want: "组织"},
		{name: "missing organization ID", input: updateCertificateFixture{OrganizationID: ""}, want: "组织标识"},
		{name: "empty organization ID", input: updateCertificateFixture{OrganizationID: "   "}, want: "组织标识"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseUpdateSigningCertificate(makeUpdateSigningCertificateDER(t, test.input))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want message containing %q", err, test.want)
			}
		})
	}
}

func TestCertificateLegalIdentityRequiresCodeSigningEKU(t *testing.T) {
	tests := []struct {
		name    string
		usages  []x509.ExtKeyUsage
		wantErr bool
	}{
		{name: "missing code signing", usages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, wantErr: true},
		{name: "code signing among multiple EKUs", usages: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning, x509.ExtKeyUsageTimeStamping}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseUpdateSigningCertificate(makeUpdateSigningCertificateDER(t, updateCertificateFixture{OrganizationID: "91210103MA7CJ3C094", ExtKeyUsages: test.usages}))
			if test.wantErr && err == nil {
				t.Fatal("error = nil, want missing Code Signing EKU rejection")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
		})
	}
}

func mustParseUpdateCertificate(t testing.TB, der []byte) inspectedUpdateCertificate {
	t.Helper()
	certificate, err := parseUpdateSigningCertificate(der)
	if err != nil {
		t.Fatalf("parse update signing certificate: %v", err)
	}
	return certificate
}

type updateCertificateFixture struct {
	SerialNumber   int64
	Countries      []string
	Organizations  []string
	OrganizationID string
	ExtKeyUsages   []x509.ExtKeyUsage
	ValidFor       time.Duration
}

func makeUpdateSigningCertificateDER(t testing.TB, fixture updateCertificateFixture) []byte {
	t.Helper()
	if fixture.SerialNumber == 0 {
		fixture.SerialNumber = 1
	}
	if fixture.Countries == nil {
		fixture.Countries = []string{"CN"}
	}
	if fixture.Organizations == nil {
		fixture.Organizations = []string{"NaisNet Technology Co., Ltd."}
	}
	if fixture.ExtKeyUsages == nil {
		fixture.ExtKeyUsages = []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning}
	}
	if fixture.ValidFor == 0 {
		fixture.ValidFor = 365 * 24 * time.Hour
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(fixture.SerialNumber),
		Subject: pkix.Name{
			Country:      fixture.Countries,
			Organization: fixture.Organizations,
			SerialNumber: fixture.OrganizationID,
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(fixture.ValidFor),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           fixture.ExtKeyUsages,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return der
}
