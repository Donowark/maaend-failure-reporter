//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

type guid struct {
	A    uint32
	B, C uint16
	D    [8]byte
}

func desktopPath() (string, error) {
	ole := syscall.NewLazyDLL("ole32.dll")
	hr, _, _ := ole.NewProc("CoInitializeEx").Call(0, 2)
	if int32(hr) < 0 && uint32(hr) != 0x80010106 {
		return "", fmt.Errorf("COM 初始化失败：0x%x", hr)
	}
	if int32(hr) >= 0 {
		defer ole.NewProc("CoUninitialize").Call()
	}
	id := guid{0xB4BFCC3A, 0xDB2C, 0x424C, [8]byte{0xB0, 0x29, 0x7F, 0xE9, 0x9A, 0x87, 0xC6, 0x41}}
	var p *uint16
	hr, _, _ = syscall.NewLazyDLL("shell32.dll").NewProc("SHGetKnownFolderPath").Call(uintptr(unsafe.Pointer(&id)), 0, 0, uintptr(unsafe.Pointer(&p)))
	if int32(hr) < 0 || p == nil {
		return "", fmt.Errorf("无法取得 Windows 桌面路径：0x%x", hr)
	}
	defer ole.NewProc("CoTaskMemFree").Call(uintptr(unsafe.Pointer(p)))
	var u []uint16
	for n := uintptr(0); n < 32768; n++ {
		c := *(*uint16)(unsafe.Add(unsafe.Pointer(p), n*2))
		if c == 0 {
			return syscall.UTF16ToString(u), nil
		}
		u = append(u, c)
	}
	return "", fmt.Errorf("桌面路径过长")
}
func fallbackPath() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return ""
	}
	return filepath.Join(base, "MaaEndFailureReporter")
}
