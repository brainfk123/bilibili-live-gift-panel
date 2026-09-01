//go:build windows

package bundlefs

import (
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
	bundleAccessDeniedACEType      = 0x01
	bundleObjectInheritACE         = 0x01
	bundleContainerInheritACE      = 0x02
	bundleInheritedACE             = 0x10
	bundleFileAllAccess            = 0x001f01ff
	bundleUntrustedWriteMask       = 0x500d0156

	bundleOBJCaseInsensitive = 0x00000040
	bundleOBJDontReparse     = 0x00001000
	bundleFileOpen           = 0x00000001
	bundleFileCreate         = 0x00000002
	bundleFileDirectory      = 0x00000001
	bundleFileWriteThrough   = 0x00000002
	bundleFileSyncNonAlert   = 0x00000020
	bundleFileNonDirectory   = 0x00000040
	bundleFileBackupIntent   = 0x00004000
	bundleFileOpenReparse    = 0x00200000
	bundleFileGenericRead    = 0x00120089
	bundleFileGenericWrite   = 0x00120116
	bundleWriteDAC           = 0x00040000
	bundleWriteOwner         = 0x00080000
	bundleOBJSuccess         = 0
)

var (
	bundleKernel32        = syscall.NewLazyDLL("kernel32.dll")
	bundleLocalFree       = bundleKernel32.NewProc("LocalFree")
	bundleAdvapi32        = syscall.NewLazyDLL("advapi32.dll")
	bundleConvertSDDL     = bundleAdvapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	bundleGetSecurityInfo = bundleAdvapi32.NewProc("GetSecurityInfo")
	bundleGetSDControl    = bundleAdvapi32.NewProc("GetSecurityDescriptorControl")
	bundleGetACE          = bundleAdvapi32.NewProc("GetAce")
	bundleNtdll           = syscall.NewLazyDLL("ntdll.dll")
	bundleNtCreateFile    = bundleNtdll.NewProc("NtCreateFile")
)

type fileIdentity struct {
	volume uint64
	file   uint64
}

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

type bundleAccessACE struct {
	Header   bundleACEHeader
	Mask     uint32
	SIDStart uint32
}

type bundleUnicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

type bundleObjectAttributes struct {
	Length                   uint32
	RootDirectory            syscall.Handle
	ObjectName               *bundleUnicodeString
	Attributes               uint32
	SecurityDescriptor       unsafe.Pointer
	SecurityQualityOfService unsafe.Pointer
}

type bundleIOStatusBlock struct {
	Status      uintptr
	Information uintptr
}

type bundleACLRequirement uint8

const (
	bundleParentACL bundleACLRequirement = iota
	bundlePrivateDirectoryACL
	bundlePrivateFileACL
)

func identityFromOpenFile(file *os.File) (fileIdentity, error) {
	var information syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &information); err != nil {
		return fileIdentity{}, errBundleFilesystem
	}
	if information.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fileIdentity{}, errBundleFilesystem
	}
	return fileIdentity{
		volume: uint64(information.VolumeSerialNumber),
		file:   uint64(information.FileIndexHigh)<<32 | uint64(information.FileIndexLow),
	}, nil
}

func openRetainedDirectoryHandle(root *os.Root, _ string) (*os.File, error) {
	if root == nil {
		return nil, errBundleFilesystem
	}
	rootDot, err := root.Open(".")
	if err != nil {
		return nil, errBundleFilesystem
	}
	defer rootDot.Close()
	return openRelativeWindowsFile(
		rootDot,
		"",
		bundleFileGenericRead|bundleFileGenericWrite,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
		bundleFileOpen,
		bundleFileDirectory|bundleFileWriteThrough|bundleFileSyncNonAlert|bundleFileBackupIntent|bundleFileOpenReparse,
		nil,
	)
}

func createPrivateChildDirectory(parent *os.File, name string) (*os.File, error) {
	userSID, err := currentBundleUserSID()
	if err != nil {
		return nil, errBundleFilesystem
	}
	descriptor, err := convertBundleSDDL(fmt.Sprintf("O:%sD:P(A;OICI;FA;;;%s)(A;OICI;FA;;;SY)", userSID, userSID))
	if err != nil {
		return nil, errBundleFilesystem
	}
	defer localFreeBundlePointer(descriptor)
	file, err := openRelativeWindowsFile(
		parent,
		name,
		bundleFileGenericRead|bundleFileGenericWrite|bundleWriteDAC|bundleWriteOwner,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
		bundleFileCreate,
		bundleFileDirectory|bundleFileWriteThrough|bundleFileSyncNonAlert|bundleFileBackupIntent|bundleFileOpenReparse,
		descriptor,
	)
	if err != nil {
		return nil, errBundleFilesystem
	}
	if verifyPrivateDirectoryHandle(file) != nil {
		_ = file.Close()
		return nil, errBundleFilesystem
	}
	return file, nil
}

func openPrivateBundleFile(parent *os.File, name string, create bool) (*os.File, error) {
	access := uint32(bundleFileGenericRead)
	disposition := uint32(bundleFileOpen)
	options := uint32(bundleFileNonDirectory | bundleFileSyncNonAlert | bundleFileBackupIntent | bundleFileOpenReparse)
	if create {
		access = bundleFileGenericRead | bundleFileGenericWrite
		disposition = bundleFileCreate
		options |= bundleFileWriteThrough
	}
	return openRelativeWindowsFile(parent, name, access, syscall.FILE_SHARE_READ, disposition, options, nil)
}

func openRelativeWindowsFile(parent *os.File, name string, access, share, disposition, options uint32, descriptor unsafe.Pointer) (*os.File, error) {
	if parent == nil {
		return nil, errBundleFilesystem
	}
	nameBuffer, err := syscall.UTF16FromString(name)
	if err != nil || len(nameBuffer) < 1 || len(nameBuffer)-1 > int(^uint16(0))/2 {
		return nil, errBundleFilesystem
	}
	unicodeName := bundleUnicodeString{
		Length:        uint16((len(nameBuffer) - 1) * 2),
		MaximumLength: uint16(len(nameBuffer) * 2),
		Buffer:        &nameBuffer[0],
	}
	attributes := bundleObjectAttributes{
		Length:             uint32(unsafe.Sizeof(bundleObjectAttributes{})),
		RootDirectory:      syscall.Handle(parent.Fd()),
		ObjectName:         &unicodeName,
		Attributes:         bundleOBJCaseInsensitive | bundleOBJDontReparse,
		SecurityDescriptor: descriptor,
	}
	var handle syscall.Handle
	status, _, _ := bundleNtCreateFile.Call(
		uintptr(unsafe.Pointer(&handle)),
		uintptr(access),
		uintptr(unsafe.Pointer(&attributes)),
		uintptr(unsafe.Pointer(&bundleIOStatusBlock{})),
		0,
		syscall.FILE_ATTRIBUTE_NORMAL,
		uintptr(share),
		uintptr(disposition),
		uintptr(options),
		0,
		0,
	)
	if int32(status) != bundleOBJSuccess || handle == syscall.InvalidHandle || handle == 0 {
		if handle != syscall.InvalidHandle && handle != 0 {
			_ = syscall.CloseHandle(handle)
		}
		return nil, errBundleFilesystem
	}
	file := os.NewFile(uintptr(handle), name)
	if file == nil {
		_ = syscall.CloseHandle(handle)
		return nil, errBundleFilesystem
	}
	return file, nil
}

func verifyParentDirectoryHandle(file *os.File) error {
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		return errBundleFilesystem
	}
	if _, err := identityFromOpenFile(file); err != nil {
		return errBundleFilesystem
	}
	return verifyHandleACL(file, bundleParentACL)
}

func verifyPrivateDirectoryHandle(file *os.File) error {
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		return errBundleFilesystem
	}
	if _, err := identityFromOpenFile(file); err != nil {
		return errBundleFilesystem
	}
	return verifyHandleACL(file, bundlePrivateDirectoryACL)
}

func verifyPrivateFileHandle(file *os.File) error {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errBundleFilesystem
	}
	if _, err := identityFromOpenFile(file); err != nil {
		return errBundleFilesystem
	}
	return verifyHandleACL(file, bundlePrivateFileACL)
}

func verifyPrivateFileInfo(info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errBundleFilesystem
	}
	return nil
}

func verifyHandleACL(file *os.File, requirement bundleACLRequirement) error {
	userSID, err := currentBundleUserSID()
	if err != nil {
		return errBundleFilesystem
	}
	var owner *syscall.SID
	var dacl *bundleACL
	var descriptor unsafe.Pointer
	result, _, _ := bundleGetSecurityInfo.Call(
		file.Fd(),
		bundleSEFileObject,
		bundleOwnerSecurityInformation|bundleDACLSecurityInformation,
		uintptr(unsafe.Pointer(&owner)),
		0,
		uintptr(unsafe.Pointer(&dacl)),
		0,
		uintptr(unsafe.Pointer(&descriptor)),
	)
	if result != 0 || descriptor == nil || owner == nil || dacl == nil {
		return errBundleFilesystem
	}
	defer localFreeBundlePointer(descriptor)
	ownerString, err := owner.String()
	if err != nil || ownerString != userSID {
		return errBundleFilesystem
	}
	var control uint16
	var revision uint32
	ok, _, _ := bundleGetSDControl.Call(uintptr(descriptor), uintptr(unsafe.Pointer(&control)), uintptr(unsafe.Pointer(&revision)))
	if ok == 0 || (requirement == bundlePrivateDirectoryACL && control&bundleSEDACLProtected == 0) {
		return errBundleFilesystem
	}
	if requirement != bundleParentACL && dacl.ACECount != 2 {
		return errBundleFilesystem
	}
	seenUser := false
	seenSystem := false
	for index := uint16(0); index < dacl.ACECount; index++ {
		var ace *bundleAccessACE
		ok, _, _ := bundleGetACE.Call(uintptr(unsafe.Pointer(dacl)), uintptr(index), uintptr(unsafe.Pointer(&ace)))
		if ok == 0 || ace == nil || ace.Header.Size < uint16(unsafe.Sizeof(bundleAccessACE{})) {
			return errBundleFilesystem
		}
		if ace.Header.Type == bundleAccessDeniedACEType {
			continue
		}
		if ace.Header.Type != bundleAccessAllowedACEType {
			return errBundleFilesystem
		}
		sid := (*syscall.SID)(unsafe.Pointer(&ace.SIDStart))
		sidString, err := sid.String()
		if err != nil {
			return errBundleFilesystem
		}
		trusted := sidString == userSID || sidString == "S-1-5-18" || sidString == "S-1-5-32-544"
		if !trusted && ace.Mask&bundleUntrustedWriteMask != 0 {
			return errBundleFilesystem
		}
		if requirement == bundleParentACL {
			continue
		}
		wantFlags := uint8(bundleInheritedACE)
		if requirement == bundlePrivateDirectoryACL {
			wantFlags = bundleObjectInheritACE | bundleContainerInheritACE
		}
		if ace.Header.Flags != wantFlags || ace.Mask != bundleFileAllAccess {
			return errBundleFilesystem
		}
		switch sidString {
		case userSID:
			if seenUser {
				return errBundleFilesystem
			}
			seenUser = true
		case "S-1-5-18":
			if seenSystem {
				return errBundleFilesystem
			}
			seenSystem = true
		default:
			return errBundleFilesystem
		}
	}
	if requirement != bundleParentACL && (!seenUser || !seenSystem) {
		return errBundleFilesystem
	}
	return nil
}

func pathIsReparsePoint(path string) bool {
	pointer, err := syscall.UTF16PtrFromString(windowsBundlePath(path))
	if err != nil {
		return true
	}
	attributes, err := syscall.GetFileAttributes(pointer)
	return err != nil || attributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

func syncDirectoryHandle(file *os.File) error {
	if err := syscall.FlushFileBuffers(syscall.Handle(file.Fd())); err != nil {
		return errBundleFilesystem
	}
	return nil
}

func currentBundleUserSID() (string, error) {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return "", errBundleFilesystem
	}
	user, userErr := token.GetTokenUser()
	closeErr := token.Close()
	if userErr != nil || closeErr != nil {
		return "", errBundleFilesystem
	}
	sid, err := user.User.Sid.String()
	if err != nil || sid == "" {
		return "", errBundleFilesystem
	}
	return sid, nil
}

func convertBundleSDDL(value string) (unsafe.Pointer, error) {
	pointer, err := syscall.UTF16PtrFromString(value)
	if err != nil {
		return nil, errBundleFilesystem
	}
	var descriptor unsafe.Pointer
	result, _, _ := bundleConvertSDDL.Call(
		uintptr(unsafe.Pointer(pointer)),
		bundleSDDLRevision,
		uintptr(unsafe.Pointer(&descriptor)),
		0,
	)
	if result == 0 || descriptor == nil {
		return nil, errBundleFilesystem
	}
	return descriptor, nil
}

func localFreeBundlePointer(pointer unsafe.Pointer) error {
	result, _, _ := bundleLocalFree.Call(uintptr(pointer))
	if result != 0 {
		return errBundleFilesystem
	}
	return nil
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
