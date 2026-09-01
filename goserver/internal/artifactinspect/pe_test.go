package artifactinspect

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestAuthenticodeContentDigestIgnoresOnlyChecksumSecurityDirectoryAndCertificateTable(t *testing.T) {
	unsigned := syntheticPE(t, 0x41)
	signed := append([]byte(nil), unsigned...)
	signed = append(signed, bytes.Repeat([]byte{0xa5}, 32)...)
	securityDirectory := 0x98 + 112 + 8*4
	putUint32(signed[securityDirectory:securityDirectory+4], uint32(len(unsigned)))
	putUint32(signed[securityDirectory+4:securityDirectory+8], 32)
	putUint32(signed[0x98+64:0x98+68], 0x12345678)

	unsignedDigest, err := AuthenticodeContentSHA256(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	signedDigest, err := AuthenticodeContentSHA256(signed)
	if err != nil {
		t.Fatal(err)
	}
	if signedDigest != unsignedDigest {
		t.Fatalf("signature-only change altered content digest: %s != %s", signedDigest, unsignedDigest)
	}
	checksum := 0x98 + 64
	wantHash := sha256.New()
	wantHash.Write(unsigned[:checksum])
	wantHash.Write(unsigned[checksum+4 : securityDirectory])
	wantHash.Write(unsigned[securityDirectory+8:])
	if unsignedDigest != hex.EncodeToString(wantHash.Sum(nil)) {
		t.Fatalf("digest hashes normalized placeholders instead of excluding Authenticode fields: %s", unsignedDigest)
	}
}

func TestAuthenticodeContentDigestRejectsModifiedPESectionOrSubstitutedBinary(t *testing.T) {
	original := syntheticPE(t, 0x41)
	modified := append([]byte(nil), original...)
	modified[400] ^= 0xff
	substitute := syntheticPE(t, 0x42)
	originalDigest, _ := AuthenticodeContentSHA256(original)
	modifiedDigest, _ := AuthenticodeContentSHA256(modified)
	substituteDigest, _ := AuthenticodeContentSHA256(substitute)
	if originalDigest == modifiedDigest || originalDigest == substituteDigest {
		t.Fatal("non-signature PE substitution retained Authenticode content digest")
	}
}

func TestAuthenticodeContentDigestRejectsMalformedCertificateTable(t *testing.T) {
	malformed := syntheticPE(t, 0x41)
	securityDirectory := 0x98 + 112 + 8*4
	putUint32(malformed[securityDirectory:securityDirectory+4], uint32(len(malformed)-8))
	putUint32(malformed[securityDirectory+4:securityDirectory+8], 32)
	if _, err := AuthenticodeContentSHA256(malformed); err == nil {
		t.Fatal("out-of-bounds certificate table accepted")
	}
}

func syntheticPE(t testing.TB, fill byte) []byte {
	t.Helper()
	binary := bytes.Repeat([]byte{fill}, 512)
	binary[0], binary[1] = 'M', 'Z'
	putUint32(binary[0x3c:0x40], 0x80)
	copy(binary[0x80:0x84], []byte{'P', 'E', 0, 0})
	binary[0x98], binary[0x99] = 0x0b, 0x02
	clear(binary[0x98+64 : 0x98+68])
	clear(binary[0x98+112+8*4 : 0x98+112+8*4+8])
	return binary
}

func putUint32(destination []byte, value uint32) {
	destination[0] = byte(value)
	destination[1] = byte(value >> 8)
	destination[2] = byte(value >> 16)
	destination[3] = byte(value >> 24)
}
