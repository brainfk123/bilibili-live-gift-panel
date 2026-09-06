// Package certidentity parses the exact structured legal identity used by
// Authenticode update policy. It never relies on a formatted Subject string.
package certidentity

import (
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"strings"
)

var (
	oidCountryName             = asn1.ObjectIdentifier{2, 5, 4, 6}
	oidOrganizationName        = asn1.ObjectIdentifier{2, 5, 4, 10}
	oidSubjectSerialNumberName = asn1.ObjectIdentifier{2, 5, 4, 5}
)

// Identity is the only legal-entity tuple accepted by update signing checks.
type Identity struct {
	Country        string `json:"country"`
	Organization   string `json:"organization"`
	OrganizationID string `json:"organizationId"`
}

// Certificate retains the verified DER alongside its structured identity.
type Certificate struct {
	Identity Identity
	DER      []byte
}

// ParseCertificateDER requires exactly one C, O, and serialNumber (OID
// 2.5.4.5) plus a Code Signing EKU. organizationIdentifier (2.5.4.97) is not
// an organization ID for this contract and is deliberately ignored.
func ParseCertificateDER(der []byte) (Certificate, error) {
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return Certificate{}, errors.New("certificate DER is invalid")
	}
	if !hasCodeSigningEKU(certificate.ExtKeyUsage) {
		return Certificate{}, errors.New("certificate is missing Code Signing EKU")
	}
	country, err := exactSubjectValue(certificate, oidCountryName, "country")
	if err != nil {
		return Certificate{}, err
	}
	organization, err := exactSubjectValue(certificate, oidOrganizationName, "organization")
	if err != nil {
		return Certificate{}, err
	}
	organizationID, err := exactSubjectValue(certificate, oidSubjectSerialNumberName, "organization ID")
	if err != nil {
		return Certificate{}, err
	}
	return Certificate{
		Identity: Identity{Country: country, Organization: organization, OrganizationID: organizationID},
		DER:      append([]byte(nil), der...),
	}, nil
}

func hasCodeSigningEKU(usages []x509.ExtKeyUsage) bool {
	for _, usage := range usages {
		if usage == x509.ExtKeyUsageCodeSigning {
			return true
		}
	}
	return false
}

func exactSubjectValue(certificate *x509.Certificate, oid asn1.ObjectIdentifier, label string) (string, error) {
	values := make([]string, 0, 1)
	for _, attribute := range certificate.Subject.Names {
		if !attribute.Type.Equal(oid) {
			continue
		}
		value, ok := attribute.Value.(string)
		if !ok {
			return "", errors.New("certificate " + label + " is invalid")
		}
		values = append(values, value)
	}
	if len(values) != 1 || values[0] == "" || values[0] != strings.TrimSpace(values[0]) {
		return "", errors.New("certificate " + label + " is missing or ambiguous")
	}
	return values[0], nil
}
