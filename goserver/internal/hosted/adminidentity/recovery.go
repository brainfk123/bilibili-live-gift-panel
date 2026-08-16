package adminidentity

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"

	"golang.org/x/crypto/scrypt"
)

const (
	RecoveryCodeCount = 10
	RecoveryCodeBytes = 16

	recoveryPasswordBytes = 15
	recoverySaltBytes     = 16
	recoveryArchiveN      = 32768
	recoveryArchiveR      = 8
	recoveryArchiveP      = 1
	recoveryArchiveKeyLen = 32
	recoveryArchiveHeader = 24
)

var recoveryArchiveMagic = [4]byte{'G', 'P', 'R', 'A'}

type scryptParameters struct {
	N         int
	R         int
	P         int
	KeyLength int
}

type recoveryPackage struct {
	Codes    []string
	Password string
	Archive  []byte
}

type recoveryPayload struct {
	RecoveryCodes []string `json:"recoveryCodes"`
}

func buildRecoveryPackage(random io.Reader) (recoveryPackage, error) {
	if random == nil {
		return recoveryPackage{}, ErrInvalidInput
	}
	codes := make([]string, RecoveryCodeCount)
	for index := range codes {
		raw := make([]byte, RecoveryCodeBytes)
		if _, err := io.ReadFull(random, raw); err != nil {
			return recoveryPackage{}, ErrUnavailable
		}
		codes[index] = base64.RawURLEncoding.EncodeToString(raw)
		clear(raw)
	}
	passwordBytes := make([]byte, recoveryPasswordBytes)
	if _, err := io.ReadFull(random, passwordBytes); err != nil {
		return recoveryPackage{}, ErrUnavailable
	}
	password := base64.RawURLEncoding.EncodeToString(passwordBytes)
	clear(passwordBytes)
	archive, err := sealRecoveryArchive(random, codes, password)
	if err != nil {
		return recoveryPackage{}, err
	}
	return recoveryPackage{Codes: codes, Password: password, Archive: archive}, nil
}

func sealRecoveryArchive(random io.Reader, codes []string, password string) ([]byte, error) {
	if random == nil || len(codes) != RecoveryCodeCount || len(password) != 20 {
		return nil, ErrInvalidInput
	}
	plaintext, err := json.Marshal(recoveryPayload{RecoveryCodes: codes})
	if err != nil {
		return nil, ErrUnavailable
	}
	defer clear(plaintext)

	salt := make([]byte, recoverySaltBytes)
	if _, err := io.ReadFull(random, salt); err != nil {
		return nil, ErrUnavailable
	}
	key, err := scrypt.Key([]byte(password), salt, recoveryArchiveN, recoveryArchiveR, recoveryArchiveP, recoveryArchiveKeyLen)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrUnavailable
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrUnavailable
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(random, nonce); err != nil {
		return nil, ErrUnavailable
	}

	header := make([]byte, recoveryArchiveHeader, recoveryArchiveHeader+len(salt)+len(nonce)+len(plaintext)+aead.Overhead())
	copy(header[:4], recoveryArchiveMagic[:])
	header[4] = 1
	header[5] = byte(len(salt))
	header[6] = byte(len(nonce))
	header[7] = byte(recoveryArchiveKeyLen)
	binary.BigEndian.PutUint32(header[8:12], recoveryArchiveN)
	binary.BigEndian.PutUint32(header[12:16], recoveryArchiveR)
	binary.BigEndian.PutUint32(header[16:20], recoveryArchiveP)
	binary.BigEndian.PutUint32(header[20:24], uint32(len(plaintext)+aead.Overhead()))
	header = append(header, salt...)
	header = append(header, nonce...)
	aad := header
	return aead.Seal(header, nonce, plaintext, aad), nil
}

func openRecoveryArchive(archive []byte, password string) (scryptParameters, []string, error) {
	parameters := scryptParameters{}
	if len(archive) < recoveryArchiveHeader || !bytes.Equal(archive[:4], recoveryArchiveMagic[:]) || archive[4] != 1 {
		return parameters, nil, ErrArchiveAuthentication
	}
	saltLength := int(archive[5])
	nonceLength := int(archive[6])
	parameters = scryptParameters{
		N: int(binary.BigEndian.Uint32(archive[8:12])), R: int(binary.BigEndian.Uint32(archive[12:16])),
		P: int(binary.BigEndian.Uint32(archive[16:20])), KeyLength: int(archive[7]),
	}
	ciphertextLength := int(binary.BigEndian.Uint32(archive[20:24]))
	if saltLength != recoverySaltBytes || nonceLength != 12 || parameters.N != recoveryArchiveN || parameters.R != recoveryArchiveR || parameters.P != recoveryArchiveP || parameters.KeyLength != recoveryArchiveKeyLen || ciphertextLength < 16 {
		return scryptParameters{}, nil, ErrArchiveAuthentication
	}
	prefixLength := recoveryArchiveHeader + saltLength + nonceLength
	if prefixLength > len(archive) || ciphertextLength != len(archive)-prefixLength {
		return scryptParameters{}, nil, ErrArchiveAuthentication
	}
	salt := archive[recoveryArchiveHeader : recoveryArchiveHeader+saltLength]
	nonce := archive[recoveryArchiveHeader+saltLength : prefixLength]
	key, err := scrypt.Key([]byte(password), salt, parameters.N, parameters.R, parameters.P, parameters.KeyLength)
	if err != nil {
		return scryptParameters{}, nil, ErrArchiveAuthentication
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return scryptParameters{}, nil, ErrArchiveAuthentication
	}
	aead, err := cipher.NewGCM(block)
	if err != nil || nonceLength != aead.NonceSize() {
		return scryptParameters{}, nil, ErrArchiveAuthentication
	}
	plaintext, err := aead.Open(nil, nonce, archive[prefixLength:], archive[:prefixLength])
	if err != nil {
		return scryptParameters{}, nil, ErrArchiveAuthentication
	}
	defer clear(plaintext)
	var payload recoveryPayload
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || len(payload.RecoveryCodes) != RecoveryCodeCount {
		return scryptParameters{}, nil, ErrArchiveAuthentication
	}
	for _, code := range payload.RecoveryCodes {
		raw, err := base64.RawURLEncoding.DecodeString(code)
		if err != nil || len(raw) != RecoveryCodeBytes {
			return scryptParameters{}, nil, ErrArchiveAuthentication
		}
	}
	return parameters, payload.RecoveryCodes, nil
}

func recoveryCodeHashes(codes []string) ([][]byte, error) {
	if len(codes) != RecoveryCodeCount {
		return nil, ErrInvalidInput
	}
	result := make([][]byte, len(codes))
	seen := make(map[[sha256.Size]byte]struct{}, len(codes))
	for index, code := range codes {
		raw, err := base64.RawURLEncoding.DecodeString(code)
		if err != nil || len(raw) != RecoveryCodeBytes {
			return nil, ErrInvalidInput
		}
		hash := sha256.Sum256([]byte(code))
		if _, duplicate := seen[hash]; duplicate {
			return nil, ErrUnavailable
		}
		seen[hash] = struct{}{}
		result[index] = append([]byte(nil), hash[:]...)
	}
	return result, nil
}
