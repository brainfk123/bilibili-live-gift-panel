package main

import (
	"errors"

	"bilibili-live-gift-panel/internal/certidentity"
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
	certificate, err := certidentity.ParseCertificateDER(der)
	if err != nil {
		return inspectedUpdateCertificate{}, fmtCertificateError("结构化身份无效", err)
	}
	return inspectedUpdateCertificate{
		LegalIdentity: updateCertificateIdentity{
			Country:        certificate.Identity.Country,
			Organization:   certificate.Identity.Organization,
			OrganizationID: certificate.Identity.OrganizationID,
		},
		DER: certificate.DER,
	}, nil
}

func fmtCertificateError(message string, err error) error {
	if err == nil {
		return errors.New("Authenticode 证书" + message)
	}
	return errors.New("Authenticode 证书" + message + "：" + err.Error())
}
