//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const (
	bundleSDDLRevision             = 1
	bundleSEFileObject             = 1
	bundleOwnerSecurityInformation = 0x00000001
	bundleDACLSecurityInformation  = 0x00000004
	bundleSEDACLProtected          = 0x1000
	bundleAccessAllowedACEType     = 0x00
	bundleObjectInheritACE         = 0x01
	bundleContainerInheritACE      = 0x02
	bundleFileAllAccess            = 0x001f01ff
)

var (
	bundleKernel32             = syscall.NewLazyDLL("kernel32.dll")
	bundleCreateDirectoryW     = bundleKernel32.NewProc("CreateDirectoryW")
	bundleLocalFree            = bundleKernel32.NewProc("LocalFree")
	bundleAdvapi32             = syscall.NewLazyDLL("advapi32.dll")
	bundleConvertSDDL          = bundleAdvapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	bundleGetNamedSecurityInfo = bundleAdvapi32.NewProc("GetNamedSecurityInfoW")
	bundleGetSDControl         = bundleAdvapi32.NewProc("GetSecurityDescriptorControl")
	bundleGetACE               = bundleAdvapi32.NewProc("GetAce")
)

type bundleACL struct {
	Revision uint8
	Sbz1     uint8
	Size     uint16
	ACECount uint16
	Sbz2     uint16
}

type bundleACEHeader struct {
	Type  uint8
	Flags uint8
	Size  uint16
}

type bundleAccessAllowedACE struct {
	Header   bundleACEHeader
	Mask     uint32
	SIDStart uint32
}

func createPrivateDirectory(path string) error {
	userSID, err := currentBundleUserSID()
	if err != nil {
		return errCommand
	}
	descriptor, err := convertBundleSDDL(fmt.Sprintf("O:%sD:P(A;OICI;FA;;;%s)(A;OICI;FA;;;SY)", userSID, userSID))
	if err != nil {
		return errCommand
	}
	attributes := syscall.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(syscall.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
		InheritHandle:      0,
	}
	createErr := createWindowsBundleDirectory(path, &attributes)
	releaseErr := localFreeBundlePointer(descriptor)
	if createErr != nil || releaseErr != nil {
		if createErr == nil {
			_ = os.Remove(path)
		}
		return errCommand
	}
	if err := verifyPrivateDirectory(path); err != nil {
		_ = os.Remove(path)
		return errCommand
	}
	return nil
}

func verifyPrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errCommand
	}
	userSID, err := currentBundleUserSID()
	if err != nil {
		return errCommand
	}
	pathPointer, err := syscall.UTF16PtrFromString(windowsBundlePath(path))
	if err != nil {
		return errCommand
	}
	var owner *syscall.SID
	var dacl *bundleACL
	var descriptor unsafe.Pointer
	result, _, _ := bundleGetNamedSecurityInfo.Call(
		uintptr(unsafe.Pointer(pathPointer)),
		bundleSEFileObject,
		bundleOwnerSecurityInformation|bundleDACLSecurityInformation,
		uintptr(unsafe.Pointer(&owner)),
		0,
		uintptr(unsafe.Pointer(&dacl)),
		0,
		uintptr(unsafe.Pointer(&descriptor)),
	)
	if result != 0 || descriptor == nil {
		return errCommand
	}
	defer localFreeBundlePointer(uintptr(descriptor))
	if owner == nil || dacl == nil {
		return errCommand
	}
	ownerString, err := owner.String()
	if err != nil || ownerString != userSID {
		return errCommand
	}
	var control uint16
	var revision uint32
	ok, _, _ := bundleGetSDControl.Call(uintptr(descriptor), uintptr(unsafe.Pointer(&control)), uintptr(unsafe.Pointer(&revision)))
	if ok == 0 || control&bundleSEDACLProtected == 0 {
		return errCommand
	}
	if dacl.ACECount != 2 {
		return errCommand
	}
	seenUser := false
	seenSystem := false
	for index := uint16(0); index < dacl.ACECount; index++ {
		var ace *bundleAccessAllowedACE
		ok, _, _ := bundleGetACE.Call(uintptr(unsafe.Pointer(dacl)), uintptr(index), uintptr(unsafe.Pointer(&ace)))
		if ok == 0 || ace == nil {
			return errCommand
		}
		if ace.Header.Type != bundleAccessAllowedACEType || ace.Header.Flags != bundleObjectInheritACE|bundleContainerInheritACE ||
			ace.Header.Size < uint16(unsafe.Sizeof(bundleAccessAllowedACE{})) || ace.Mask != bundleFileAllAccess {
			return errCommand
		}
		sid := (*syscall.SID)(unsafe.Pointer(&ace.SIDStart))
		sidString, err := sid.String()
		if err != nil {
			return errCommand
		}
		switch sidString {
		case userSID:
			if seenUser {
				return errCommand
			}
			seenUser = true
		case "S-1-5-18":
			if seenSystem {
				return errCommand
			}
			seenSystem = true
		default:
			return errCommand
		}
	}
	if !seenUser || !seenSystem {
		return errCommand
	}
	return nil
}

func readBundleFileIdentity(path string) (bundleFileIdentity, error) {
	directory, err := os.Open(path)
	if err != nil {
		return bundleFileIdentity{}, errCommand
	}
	var information syscall.ByHandleFileInformation
	identityErr := syscall.GetFileInformationByHandle(syscall.Handle(directory.Fd()), &information)
	closeErr := directory.Close()
	if identityErr != nil || closeErr != nil {
		return bundleFileIdentity{}, errCommand
	}
	return bundleFileIdentity{
		volume: uint64(information.VolumeSerialNumber),
		file:   uint64(information.FileIndexHigh)<<32 | uint64(information.FileIndexLow),
	}, nil
}

func currentBundleUserSID() (string, error) {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return "", errCommand
	}
	user, userErr := token.GetTokenUser()
	closeErr := token.Close()
	if userErr != nil || closeErr != nil {
		return "", errCommand
	}
	sid, err := user.User.Sid.String()
	if err != nil || sid == "" {
		return "", errCommand
	}
	return sid, nil
}

func convertBundleSDDL(value string) (uintptr, error) {
	pointer, err := syscall.UTF16PtrFromString(value)
	if err != nil {
		return 0, errCommand
	}
	var descriptor uintptr
	result, _, _ := bundleConvertSDDL.Call(
		uintptr(unsafe.Pointer(pointer)),
		bundleSDDLRevision,
		uintptr(unsafe.Pointer(&descriptor)),
		0,
	)
	if result == 0 || descriptor == 0 {
		return 0, errCommand
	}
	return descriptor, nil
}

func createWindowsBundleDirectory(path string, attributes *syscall.SecurityAttributes) error {
	pointer, err := syscall.UTF16PtrFromString(windowsBundlePath(path))
	if err != nil {
		return errCommand
	}
	result, _, _ := bundleCreateDirectoryW.Call(uintptr(unsafe.Pointer(pointer)), uintptr(unsafe.Pointer(attributes)))
	if result == 0 {
		return errCommand
	}
	return nil
}

func localFreeBundlePointer(pointer uintptr) error {
	result, _, _ := bundleLocalFree.Call(pointer)
	if result != 0 {
		return errCommand
	}
	return nil
}

func isUnsupportedBundleDirectorySyncError(err error) bool {
	return errors.Is(err, syscall.Errno(1)) || errors.Is(err, syscall.Errno(5)) || errors.Is(err, syscall.Errno(6))
}

func windowsBundlePath(path string) string {
	if len(path) >= 4 {
		namespace := filepath.ToSlash(path[:4])
		if namespace == `/??/` || namespace == `//?/` || namespace == `//./` {
			return path
		}
	}
	resolved, err := syscall.FullPath(path)
	if err != nil {
		return path
	}
	if len(resolved) < 248 {
		return resolved
	}
	if strings.HasPrefix(resolved, `\\`) {
		return `\\?\UNC\` + resolved[2:]
	}
	return `\\?\` + resolved
}
