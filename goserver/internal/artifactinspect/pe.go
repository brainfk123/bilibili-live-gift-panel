package artifactinspect

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
)

type peImage struct{ checksum, securityDirectory, coveredEnd int }

func AuthenticodeContentSHA256(contents []byte) (string, error) {
	ranges, err := authenticodeCoveredRanges(contents)
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	for _, covered := range ranges {
		_, _ = hasher.Write(covered)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func AuthenticodeCoveredContent(contents []byte) ([]byte, error) {
	ranges, err := authenticodeCoveredRanges(contents)
	if err != nil {
		return nil, err
	}
	length := 0
	for _, covered := range ranges {
		length += len(covered)
	}
	result := make([]byte, 0, length)
	for _, covered := range ranges {
		result = append(result, covered...)
	}
	return result, nil
}

func CoveredContains(contents, value []byte) (bool, error) {
	if len(value) == 0 {
		return false, errors.New("covered search value is empty")
	}
	ranges, err := authenticodeCoveredRanges(contents)
	if err != nil {
		return false, err
	}
	for _, covered := range ranges {
		if bytes.Contains(covered, value) {
			return true, nil
		}
	}
	return false, nil
}

func authenticodeCoveredRanges(contents []byte) ([][]byte, error) {
	image, err := parsePEImage(contents)
	if err != nil {
		return nil, err
	}
	return [][]byte{contents[:image.checksum], contents[image.checksum+4 : image.securityDirectory], contents[image.securityDirectory+8 : image.coveredEnd]}, nil
}

func parsePEImage(contents []byte) (peImage, error) {
	if len(contents) < 0x100 || binary.LittleEndian.Uint16(contents[:2]) != 0x5a4d {
		return peImage{}, errors.New("PE image is invalid")
	}
	peOffset := int(binary.LittleEndian.Uint32(contents[0x3c:0x40]))
	if peOffset < 0x40 || peOffset > len(contents)-24 || binary.LittleEndian.Uint32(contents[peOffset:peOffset+4]) != 0x00004550 {
		return peImage{}, errors.New("PE header is invalid")
	}
	numberOfSections := int(binary.LittleEndian.Uint16(contents[peOffset+6 : peOffset+8]))
	sizeOptional := int(binary.LittleEndian.Uint16(contents[peOffset+20 : peOffset+22]))
	if numberOfSections < 1 || numberOfSections > 96 {
		return peImage{}, errors.New("PE section count is invalid")
	}
	optional := peOffset + 24
	if sizeOptional <= 0 || optional > len(contents)-sizeOptional {
		return peImage{}, errors.New("PE optional header is invalid")
	}
	magic := binary.LittleEndian.Uint16(contents[optional : optional+2])
	minimum, dataDirectory, rvaCountOffset := 0, 0, 0
	switch magic {
	case 0x20b:
		minimum, dataDirectory, rvaCountOffset = 240, optional+112, optional+108
	case 0x10b:
		minimum, dataDirectory, rvaCountOffset = 224, optional+96, optional+92
	default:
		return peImage{}, errors.New("PE optional header magic is invalid")
	}
	if sizeOptional < minimum || rvaCountOffset > optional+sizeOptional-4 {
		return peImage{}, errors.New("PE optional header size is invalid")
	}
	if binary.LittleEndian.Uint32(contents[rvaCountOffset:rvaCountOffset+4]) < 5 || dataDirectory+5*8 > optional+sizeOptional {
		return peImage{}, errors.New("PE data directories are invalid")
	}
	checksum, securityDirectory := optional+64, dataDirectory+8*4
	sectionTable := optional + sizeOptional
	sectionTableEnd := sectionTable + numberOfSections*40
	if sectionTableEnd < sectionTable || sectionTableEnd > len(contents) {
		return peImage{}, errors.New("PE section table is invalid")
	}
	sizeHeaders := int(binary.LittleEndian.Uint32(contents[optional+60 : optional+64]))
	if sizeHeaders < sectionTableEnd || sizeHeaders > len(contents) {
		return peImage{}, errors.New("PE header bounds are invalid")
	}
	certOffset := int(binary.LittleEndian.Uint32(contents[securityDirectory : securityDirectory+4]))
	certSize := int(binary.LittleEndian.Uint32(contents[securityDirectory+4 : securityDirectory+8]))
	if (certOffset == 0) != (certSize == 0) || certOffset > len(contents) || certSize > len(contents)-certOffset ||
		(certSize != 0 && (certOffset%8 != 0 || certSize < 8 || certSize%8 != 0 || certOffset+certSize != len(contents))) {
		return peImage{}, errors.New("PE certificate table is invalid")
	}
	coveredEnd := len(contents)
	if certSize != 0 {
		coveredEnd = certOffset
	}
	if coveredEnd < sizeHeaders || checksum+4 > coveredEnd || securityDirectory+8 > coveredEnd {
		return peImage{}, errors.New("PE covered bounds are invalid")
	}
	previousEnd := sizeHeaders
	for index := 0; index < numberOfSections; index++ {
		section := sectionTable + index*40
		rawSize := int(binary.LittleEndian.Uint32(contents[section+16 : section+20]))
		rawOffset := int(binary.LittleEndian.Uint32(contents[section+20 : section+24]))
		if rawSize == 0 {
			if rawOffset != 0 {
				return peImage{}, errors.New("PE empty section offset is invalid")
			}
			continue
		}
		if rawOffset < sizeHeaders || rawOffset < previousEnd || rawOffset > coveredEnd || rawSize > coveredEnd-rawOffset {
			return peImage{}, errors.New("PE sections overlap or are unordered")
		}
		previousEnd = rawOffset + rawSize
	}
	if certSize != 0 && certOffset < previousEnd {
		return peImage{}, errors.New("PE certificate overlaps section data")
	}
	return peImage{checksum: checksum, securityDirectory: securityDirectory, coveredEnd: coveredEnd}, nil
}
