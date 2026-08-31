package main

import (
	"crypto/x509"
	"errors"
	"strings"
)

type updateCertificateIdentity struct {
	Country        string
	Organization   string
	OrganizationID string
}

type inspectedUpdateCertificate struct {
	LegalIdentity updateCertificateIdentity
	DER           []byte
}

func parseUpdateSigningCertificate(der []byte) (inspectedUpdateCertificate, error) {
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return inspectedUpdateCertificate{}, fmtCertificateError("DER 无效", err)
	}
	if !hasCodeSigningEKU(certificate.ExtKeyUsage) {
		return inspectedUpdateCertificate{}, errors.New("Authenticode 证书缺少 Code Signing EKU")
	}
	identity, err := certificateLegalIdentity(certificate)
	if err != nil {
		return inspectedUpdateCertificate{}, err
	}
	return inspectedUpdateCertificate{LegalIdentity: identity, DER: append([]byte(nil), der...)}, nil
}

func hasCodeSigningEKU(usages []x509.ExtKeyUsage) bool {
	for _, usage := range usages {
		if usage == x509.ExtKeyUsageCodeSigning {
			return true
		}
	}
	return false
}

func certificateLegalIdentity(certificate *x509.Certificate) (updateCertificateIdentity, error) {
	if certificate == nil {
		return updateCertificateIdentity{}, errors.New("Authenticode 证书为空")
	}
	country, err := exactlyOneCertificateField(certificate.Subject.Country, "国家")
	if err != nil {
		return updateCertificateIdentity{}, err
	}
	organization, err := exactlyOneCertificateField(certificate.Subject.Organization, "组织")
	if err != nil {
		return updateCertificateIdentity{}, err
	}
	organizationID := strings.TrimSpace(certificate.Subject.SerialNumber)
	if organizationID == "" {
		return updateCertificateIdentity{}, errors.New("Authenticode 证书缺少组织标识")
	}
	return updateCertificateIdentity{Country: country, Organization: organization, OrganizationID: organizationID}, nil
}

func exactlyOneCertificateField(values []string, name string) (string, error) {
	if len(values) != 1 {
		return "", fmtCertificateError(""+name+"字段不明确", nil)
	}
	value := strings.TrimSpace(values[0])
	if value == "" {
		return "", fmtCertificateError(""+name+"字段为空", nil)
	}
	return value, nil
}

func fmtCertificateError(message string, err error) error {
	if err == nil {
		return errors.New("Authenticode 证书" + message)
	}
	return errors.New("Authenticode 证书" + message + "：" + err.Error())
}
