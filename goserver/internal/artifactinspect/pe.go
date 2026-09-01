// Package artifactinspect verifies release artifacts without signing credentials.
package artifactinspect

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
)

// AuthenticodeContentSHA256 hashes the PE image after normalizing exactly the
// checksum and security-directory fields and excluding the terminal
// certificate table. Any other byte change alters the result.
func AuthenticodeContentSHA256(contents []byte) (string, error) {
	if len(contents) < 0x100 || binary.LittleEndian.Uint16(contents[:2]) != 0x5a4d {
		return "", errors.New("PE image is invalid")
	}
	peOffset := int(binary.LittleEndian.Uint32(contents[0x3c:0x40]))
	if peOffset < 0x40 || peOffset > len(contents)-24 || binary.LittleEndian.Uint32(contents[peOffset:peOffset+4]) != 0x00004550 {
		return "", errors.New("PE header is invalid")
	}
	optional := peOffset + 24
	if optional > len(contents)-2 {
		return "", errors.New("PE optional header is invalid")
	}
	magic := binary.LittleEndian.Uint16(contents[optional : optional+2])
	dataDirectory := 0
	switch magic {
	case 0x20b:
		dataDirectory = optional + 112
	case 0x10b:
		dataDirectory = optional + 96
	default:
		return "", errors.New("PE optional header magic is invalid")
	}
	checksum := optional + 64
	securityDirectory := dataDirectory + 8*4
	if checksum > len(contents)-4 || securityDirectory > len(contents)-8 {
		return "", errors.New("PE optional header is truncated")
	}
	certificateOffset := int(binary.LittleEndian.Uint32(contents[securityDirectory : securityDirectory+4]))
	certificateSize := int(binary.LittleEndian.Uint32(contents[securityDirectory+4 : securityDirectory+8]))
	if (certificateOffset == 0) != (certificateSize == 0) || certificateOffset < 0 || certificateSize < 0 || certificateOffset > len(contents) || certificateSize > len(contents)-certificateOffset ||
		(certificateSize != 0 && certificateOffset+certificateSize != len(contents)) {
		return "", errors.New("PE certificate table is invalid")
	}
	end := len(contents)
	if certificateSize != 0 {
		end = certificateOffset
	}
	if checksum+4 > end || securityDirectory+8 > end {
		return "", errors.New("PE certificate table overlaps headers")
	}
	hasher := sha256.New()
	_, _ = hasher.Write(contents[:checksum])
	_, _ = hasher.Write(contents[checksum+4 : securityDirectory])
	_, _ = hasher.Write(contents[securityDirectory+8 : end])
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
