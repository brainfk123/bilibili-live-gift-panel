//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

const cryptProtectUIForbidden = 0x1

type credentialDataBlob struct {
	length uint32
	data   *byte
}

var (
	crypt32DLL              = syscall.NewLazyDLL("crypt32.dll")
	kernel32DLL             = syscall.NewLazyDLL("kernel32.dll")
	cryptProtectDataProc    = crypt32DLL.NewProc("CryptProtectData")
	cryptUnprotectDataProc  = crypt32DLL.NewProc("CryptUnprotectData")
	credentialLocalFreeProc = kernel32DLL.NewProc("LocalFree")
)

func protectLoginCredentialData(plain []byte) ([]byte, error) {
	input := makeCredentialDataBlob(plain)
	var output credentialDataBlob
	description, _ := syscall.UTF16PtrFromString("BilibiliLiveGiftPanel")
	result, _, callErr := cryptProtectDataProc.Call(
		uintptr(unsafe.Pointer(&input)),
		uintptr(unsafe.Pointer(description)),
		0,
		0,
		0,
		cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&output)),
	)
	if result == 0 {
		return nil, fmt.Errorf("CryptProtectData: %w", callErr)
	}
	defer credentialLocalFreeProc.Call(uintptr(unsafe.Pointer(output.data)))
	return copyCredentialDataBlob(output), nil
}

func unprotectLoginCredentialData(encrypted []byte) ([]byte, error) {
	input := makeCredentialDataBlob(encrypted)
	var output credentialDataBlob
	var description *uint16
	result, _, callErr := cryptUnprotectDataProc.Call(
		uintptr(unsafe.Pointer(&input)),
		uintptr(unsafe.Pointer(&description)),
		0,
		0,
		0,
		cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&output)),
	)
	if result == 0 {
		return nil, fmt.Errorf("CryptUnprotectData: %w", callErr)
	}
	if description != nil {
		defer credentialLocalFreeProc.Call(uintptr(unsafe.Pointer(description)))
	}
	defer credentialLocalFreeProc.Call(uintptr(unsafe.Pointer(output.data)))
	return copyCredentialDataBlob(output), nil
}

func makeCredentialDataBlob(data []byte) credentialDataBlob {
	if len(data) == 0 {
		return credentialDataBlob{}
	}
	return credentialDataBlob{length: uint32(len(data)), data: &data[0]}
}

func copyCredentialDataBlob(blob credentialDataBlob) []byte {
	if blob.length == 0 || blob.data == nil {
		return []byte{}
	}
	return append([]byte(nil), unsafe.Slice(blob.data, int(blob.length))...)
}
