//go:build !windows

package artifactinspect

import (
	"bilibili-live-gift-panel/internal/certidentity"
	"errors"
)

func InspectAuthenticodeFile(string) (certidentity.Identity, error) {
	return certidentity.Identity{}, errors.New("Authenticode inspection requires Windows")
}

func InspectAuthenticodeCertificate(string) (certidentity.Certificate, error) {
	return certidentity.Certificate{}, errors.New("Authenticode inspection requires Windows")
}
