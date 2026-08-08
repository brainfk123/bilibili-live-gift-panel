package assistant

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	ggufTypeUint8   = 0
	ggufTypeInt8    = 1
	ggufTypeUint16  = 2
	ggufTypeInt16   = 3
	ggufTypeUint32  = 4
	ggufTypeInt32   = 5
	ggufTypeFloat32 = 6
	ggufTypeBool    = 7
	ggufTypeString  = 8
	ggufTypeArray   = 9
	ggufTypeUint64  = 10
	ggufTypeInt64   = 11
	ggufTypeFloat64 = 12
	ggufMostlyQ8_0  = 7
)

func validateModelFile(path string, manifest ModelManifest) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("读取模型失败：%w", err)
	}
	if info.Size() != manifest.SizeBytes {
		return fmt.Errorf("模型大小校验失败")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		return fmt.Errorf("计算模型 SHA-256 失败：%w", err)
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), manifest.SHA256) {
		_ = file.Close()
		return fmt.Errorf("模型 SHA-256 校验失败")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return err
	}
	architecture, fileType, err := readGGUFMetadata(file)
	_ = file.Close()
	if err != nil {
		return fmt.Errorf("GGUF 元数据无效：%w", err)
	}
	if !strings.EqualFold(architecture, manifest.Architecture) {
		return fmt.Errorf("GGUF 架构不符：%s", architecture)
	}
	if fileType != ggufMostlyQ8_0 {
		return fmt.Errorf("GGUF 量化不是 Q8_0")
	}
	return nil
}

func readGGUFMetadata(source io.Reader) (string, uint64, error) {
	reader := bufio.NewReader(source)
	magic := make([]byte, 4)
	if _, err := io.ReadFull(reader, magic); err != nil || string(magic) != "GGUF" {
		return "", 0, fmt.Errorf("缺少 GGUF 文件头")
	}
	version, err := readUint32(reader)
	if err != nil || (version != 2 && version != 3) {
		return "", 0, fmt.Errorf("不支持的 GGUF 版本 %d", version)
	}
	if _, err := readUint64(reader); err != nil { // tensor count
		return "", 0, err
	}
	metadataCount, err := readUint64(reader)
	if err != nil || metadataCount > 100000 {
		return "", 0, fmt.Errorf("GGUF 元数据数量无效")
	}
	architecture := ""
	var fileType uint64
	fileTypeFound := false
	for index := uint64(0); index < metadataCount; index++ {
		key, err := readGGUFString(reader)
		if err != nil {
			return "", 0, err
		}
		valueType, err := readUint32(reader)
		if err != nil {
			return "", 0, err
		}
		switch key {
		case "general.architecture":
			if valueType != ggufTypeString {
				return "", 0, fmt.Errorf("general.architecture 类型无效")
			}
			architecture, err = readGGUFString(reader)
		case "general.file_type":
			fileType, err = readGGUFUnsigned(reader, valueType)
			fileTypeFound = err == nil
		default:
			err = skipGGUFValue(reader, valueType, 0)
		}
		if err != nil {
			return "", 0, err
		}
	}
	if architecture == "" || !fileTypeFound {
		return "", 0, fmt.Errorf("缺少 architecture 或 file_type")
	}
	return architecture, fileType, nil
}

func readGGUFString(reader io.Reader) (string, error) {
	length, err := readUint64(reader)
	if err != nil {
		return "", err
	}
	if length > 16<<20 {
		return "", fmt.Errorf("GGUF 字符串过长")
	}
	data := make([]byte, int(length))
	if _, err := io.ReadFull(reader, data); err != nil {
		return "", err
	}
	return string(data), nil
}

func readGGUFUnsigned(reader io.Reader, valueType uint32) (uint64, error) {
	switch valueType {
	case ggufTypeUint8:
		var value uint8
		err := binary.Read(reader, binary.LittleEndian, &value)
		return uint64(value), err
	case ggufTypeUint16:
		var value uint16
		err := binary.Read(reader, binary.LittleEndian, &value)
		return uint64(value), err
	case ggufTypeUint32:
		value, err := readUint32(reader)
		return uint64(value), err
	case ggufTypeUint64:
		return readUint64(reader)
	default:
		return 0, fmt.Errorf("整数类型 %d 无效", valueType)
	}
}

func skipGGUFValue(reader io.Reader, valueType uint32, depth int) error {
	if depth > 4 {
		return fmt.Errorf("GGUF 数组嵌套过深")
	}
	sizes := map[uint32]int64{
		ggufTypeUint8: 1, ggufTypeInt8: 1, ggufTypeBool: 1,
		ggufTypeUint16: 2, ggufTypeInt16: 2,
		ggufTypeUint32: 4, ggufTypeInt32: 4, ggufTypeFloat32: 4,
		ggufTypeUint64: 8, ggufTypeInt64: 8, ggufTypeFloat64: 8,
	}
	if size, exists := sizes[valueType]; exists {
		_, err := io.CopyN(io.Discard, reader, size)
		return err
	}
	switch valueType {
	case ggufTypeString:
		_, err := readGGUFString(reader)
		return err
	case ggufTypeArray:
		elementType, err := readUint32(reader)
		if err != nil {
			return err
		}
		count, err := readUint64(reader)
		if err != nil || count > 100000000 {
			return fmt.Errorf("GGUF 数组长度无效")
		}
		if size, exists := sizes[elementType]; exists {
			if count > uint64((1<<63-1)/size) {
				return fmt.Errorf("GGUF 数组过大")
			}
			_, err = io.CopyN(io.Discard, reader, int64(count)*size)
			return err
		}
		for index := uint64(0); index < count; index++ {
			if err := skipGGUFValue(reader, elementType, depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("未知 GGUF 类型 %d", valueType)
	}
}

func readUint32(reader io.Reader) (uint32, error) {
	var value uint32
	err := binary.Read(reader, binary.LittleEndian, &value)
	return value, err
}

func readUint64(reader io.Reader) (uint64, error) {
	var value uint64
	err := binary.Read(reader, binary.LittleEndian, &value)
	return value, err
}
