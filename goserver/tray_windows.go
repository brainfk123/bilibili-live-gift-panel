//go:build windows

package main

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	wmDestroy       = 0x0002
	wmCommand       = 0x0111
	wmLButtonUp     = 0x0202
	wmRButtonUp     = 0x0205
	wmApp           = 0x8000
	trayMessage     = wmApp + 1
	trayExitCommand = 1001

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
	nimAdd     = 0x00000000
	nimDelete  = 0x00000002

	mfString       = 0x00000000
	tpmRightButton = 0x00000002
	tpmReturnCmd   = 0x00000100
	swShowNormal   = 1
	mbIconError    = 0x00000010
	mbOK           = 0x00000000
)

var (
	user32                  = syscall.NewLazyDLL("user32.dll")
	shell32                 = syscall.NewLazyDLL("shell32.dll")
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procAppendMenuW         = user32.NewProc("AppendMenuW")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procLoadIconW           = user32.NewProc("LoadIconW")
	procMessageBoxW         = user32.NewProc("MessageBoxW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procGetModuleHandleW    = kernel32.NewProc("GetModuleHandleW")
	procCreateMutexW        = kernel32.NewProc("CreateMutexW")
	procCloseHandle         = kernel32.NewProc("CloseHandle")
	procShellExecuteW       = shell32.NewProc("ShellExecuteW")
	procShellNotifyIconW    = shell32.NewProc("Shell_NotifyIconW")
	trayConfigURL           string
	trayIconData            notifyIconData
	trayWindowProcCallback  = syscall.NewCallback(trayWindowProc)
)

func acquireSingleInstance() (bool, func(), error) {
	name, _ := syscall.UTF16PtrFromString("Local\\BilibiliLiveGiftPanelSingleton")
	handle, _, callErr := procCreateMutexW.Call(0, 1, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return false, func() {}, callErr
	}
	if callErr == syscall.Errno(183) {
		_, _, _ = procCloseHandle.Call(handle)
		return true, func() {}, nil
	}
	return false, func() { _, _, _ = procCloseHandle.Call(handle) }, nil
}

type point struct {
	x int32
	y int32
}

type message struct {
	hWnd     uintptr
	message  uint32
	padding1 uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	pt       point
	private  uint32
}

type windowClassEx struct {
	cbSize     uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   uintptr
	icon       uintptr
	cursor     uintptr
	background uintptr
	menuName   *uint16
	className  *uint16
	iconSmall  uintptr
}

type notifyIconData struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	callbackMessage  uint32
	hIcon            uintptr
	tip              [128]uint16
	state            uint32
	stateMask        uint32
	info             [256]uint16
	timeoutOrVersion uint32
	infoTitle        [64]uint16
	infoFlags        uint32
	guidItem         [16]byte
	balloonIcon      uintptr
}

func openURL(url string) {
	operation, _ := syscall.UTF16PtrFromString("open")
	target, _ := syscall.UTF16PtrFromString(url)
	_, _, _ = procShellExecuteW.Call(0, uintptr(unsafe.Pointer(operation)), uintptr(unsafe.Pointer(target)), 0, 0, swShowNormal)
}

func showStartupError(message string) {
	title, _ := syscall.UTF16PtrFromString("直播礼物面板")
	body, _ := syscall.UTF16PtrFromString(message)
	_, _, _ = procMessageBoxW.Call(0, uintptr(unsafe.Pointer(body)), uintptr(unsafe.Pointer(title)), mbOK|mbIconError)
}

func runTrayApp(configURL string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	trayConfigURL = configURL

	instance, _, _ := procGetModuleHandleW.Call(0)
	className, _ := syscall.UTF16PtrFromString("BilibiliLiveGiftPanelTray")
	iconName, _ := syscall.UTF16PtrFromString("APP")
	icon, _, _ := procLoadIconW.Call(instance, uintptr(unsafe.Pointer(iconName)))
	if icon == 0 {
		icon, _, _ = procLoadIconW.Call(0, 32512)
	}
	class := windowClassEx{
		cbSize:    uint32(unsafe.Sizeof(windowClassEx{})),
		wndProc:   trayWindowProcCallback,
		instance:  instance,
		icon:      icon,
		className: className,
		iconSmall: icon,
	}
	if registered, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class))); registered == 0 {
		return fmt.Errorf("注册托盘窗口失败：%v", callErr)
	}

	hWnd, _, callErr := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(className)),
		0, 0, 0, 0, 0, 0, 0, instance, 0,
	)
	if hWnd == 0 {
		return fmt.Errorf("创建托盘窗口失败：%v", callErr)
	}

	trayIconData = notifyIconData{
		cbSize:          uint32(unsafe.Sizeof(notifyIconData{})),
		hWnd:            hWnd,
		uID:             1,
		uFlags:          nifMessage | nifIcon | nifTip,
		callbackMessage: trayMessage,
		hIcon:           icon,
	}
	copy(trayIconData.tip[:], syscall.StringToUTF16("直播礼物面板"))
	if added, _, callErr := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&trayIconData))); added == 0 {
		_, _, _ = procDestroyWindow.Call(hWnd)
		return fmt.Errorf("添加托盘图标失败：%v", callErr)
	}

	var msg message
	for {
		result, _, callErr := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(result) == -1 {
			return fmt.Errorf("读取托盘消息失败：%v", callErr)
		}
		if result == 0 {
			return nil
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func trayWindowProc(hWnd, msg, wParam, lParam uintptr) uintptr {
	switch msg {
	case trayMessage:
		switch uint32(lParam) {
		case wmLButtonUp:
			openURL(trayConfigURL)
		case wmRButtonUp:
			showTrayMenu(hWnd)
		}
		return 0
	case wmCommand:
		if uint16(wParam&0xffff) == trayExitCommand {
			_, _, _ = procDestroyWindow.Call(hWnd)
		}
		return 0
	case wmDestroy:
		_, _, _ = procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&trayIconData)))
		_, _, _ = procPostQuitMessage.Call(0)
		return 0
	}
	result, _, _ := procDefWindowProcW.Call(hWnd, msg, wParam, lParam)
	return result
}

func showTrayMenu(hWnd uintptr) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	exitText, _ := syscall.UTF16PtrFromString("退出")
	_, _, _ = procAppendMenuW.Call(menu, mfString, trayExitCommand, uintptr(unsafe.Pointer(exitText)))
	var cursor point
	_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&cursor)))
	_, _, _ = procSetForegroundWindow.Call(hWnd)
	command, _, _ := procTrackPopupMenu.Call(menu, tpmRightButton|tpmReturnCmd, uintptr(cursor.x), uintptr(cursor.y), 0, hWnd, 0)
	if command == trayExitCommand {
		_, _, _ = procDestroyWindow.Call(hWnd)
	}
}
