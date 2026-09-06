package artifactinspect

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
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

func TestPEParserRejectsMalformedOptionalHeaderSectionsAndSecurityAlignment(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]byte)
	}{
		{name: "short optional header", mutate: func(binary []byte) { binary[0x80+20], binary[0x80+21] = 0x20, 0 }},
		{name: "missing security RVA directory", mutate: func(binary []byte) { putUint32(binary[0x98+108:0x98+112], 4) }},
		{name: "section raw data overlaps headers", mutate: func(binary []byte) { putUint32(binary[0x188+20:0x188+24], 0x100) }},
		{name: "section raw data beyond image", mutate: func(binary []byte) { putUint32(binary[0x188+16:0x188+20], 0xfffffff0) }},
		{name: "misaligned certificate table", mutate: func(binary []byte) {
			security := 0x98 + 112 + 8*4
			putUint32(binary[security:security+4], uint32(len(binary)-7))
			putUint32(binary[security+4:security+8], 7)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binary := syntheticPE(t, 0x41)
			test.mutate(binary)
			if _, err := AuthenticodeContentSHA256(binary); err == nil {
				t.Fatal("malformed PE accepted")
			}
		})
	}
}

func TestAuthenticodeCoveredContentExcludesCertificateTableDecoys(t *testing.T) {
	unsigned := syntheticPE(t, 0x41)
	decoys := []byte("0.4.11\x00" + strings.Repeat("a", 40) + "\x00root-policy-decoy\x00full-ffmpeg-archive-decoy")
	signed := append(append([]byte(nil), unsigned...), decoys...)
	for len(signed)%8 != 0 {
		signed = append(signed, 0)
	}
	security := 0x98 + 112 + 8*4
	putUint32(signed[security:security+4], uint32(len(unsigned)))
	putUint32(signed[security+4:security+8], uint32(len(signed)-len(unsigned)))
	covered, err := AuthenticodeCoveredContent(signed)
	if err != nil {
		t.Fatal(err)
	}
	for _, decoy := range [][]byte{[]byte("0.4.11"), []byte(strings.Repeat("a", 40)), []byte("root-policy-decoy"), []byte("full-ffmpeg-archive-decoy")} {
		if bytes.Contains(covered, decoy) {
			t.Fatalf("certificate-table decoy entered covered content: %q", decoy)
		}
	}
}

func TestPE32ImageUsesItsOwnOptionalHeaderAndSecurityDirectoryLayout(t *testing.T) {
	binary := syntheticPE(t, 0x41)
	optional := 0x98
	oldSection := optional + 0xf0
	newSection := optional + 0xe0
	section := append([]byte(nil), binary[oldSection:oldSection+40]...)
	clear(binary[newSection : oldSection+40])
	copy(binary[newSection:newSection+40], section)
	binary[0x80+20], binary[0x80+21] = 0xe0, 0
	binary[optional], binary[optional+1] = 0x0b, 0x01
	putUint32(binary[optional+92:optional+96], 16)
	clear(binary[optional+96+8*4 : optional+96+8*4+8])
	if _, err := AuthenticodeContentSHA256(binary); err != nil {
		t.Fatal(err)
	}
}

func syntheticPE(t testing.TB, fill byte) []byte {
	t.Helper()
	binary := bytes.Repeat([]byte{fill}, 512)
	binary[0], binary[1] = 'M', 'Z'
	putUint32(binary[0x3c:0x40], 0x80)
	copy(binary[0x80:0x84], []byte{'P', 'E', 0, 0})
	binary[0x80+6], binary[0x80+7] = 1, 0
	binary[0x80+20], binary[0x80+21] = 0xf0, 0
	binary[0x98], binary[0x99] = 0x0b, 0x02
	putUint32(binary[0x98+60:0x98+64], 0x200)
	putUint32(binary[0x98+108:0x98+112], 16)
	clear(binary[0x98+64 : 0x98+68])
	clear(binary[0x98+112+8*4 : 0x98+112+8*4+8])
	section := 0x98 + 0xf0
	copy(binary[section:section+8], []byte(".text\x00\x00\x00"))
	putUint32(binary[section+8:section+12], 0x100)
	putUint32(binary[section+12:section+16], 0x1000)
	putUint32(binary[section+16:section+20], uint32(len(binary)-0x200))
	putUint32(binary[section+20:section+24], 0)
	if len(binary) > 0x200 {
		putUint32(binary[section+20:section+24], 0x200)
	}
	return binary
}

func putUint32(destination []byte, value uint32) {
	destination[0] = byte(value)
	destination[1] = byte(value >> 8)
	destination[2] = byte(value >> 16)
	destination[3] = byte(value >> 24)
}
