package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// The IPv4 listener snapshot is taken only after a state read fails.
func listenerOwner(ctx context.Context, port int) string {
	query := syscall.NewLazyDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")
	var size uint32
	var table []byte
	for i := 0; i < 4 && ctx.Err() == nil; i++ {
		var ptr *byte
		if len(table) > 0 {
			ptr = &table[0]
		}
		code, _, _ := query.Call(uintptr(unsafe.Pointer(ptr)), uintptr(unsafe.Pointer(&size)), 0, syscall.AF_INET, 3, 0) // TCP_TABLE_OWNER_PID_LISTENER
		if code == 0 {
			owners := loopbackListenerPIDs(table, port)
			if len(owners) == 0 {
				return fmt.Sprintf("IPv4 监听快照：端口 %d 未找到匹配的监听项。", port)
			}
			entries := make([]string, 0, len(owners))
			for _, pid := range owners {
				entries = append(entries, fmt.Sprintf("%s (PID %d)", processName(pid), pid))
			}
			return fmt.Sprintf("IPv4 监听快照：端口 %d；%s", port, strings.Join(entries, ", "))
		}
		if syscall.Errno(code) != syscall.ERROR_INSUFFICIENT_BUFFER || size == 0 || size > 16<<20 {
			return fmt.Sprintf("IPv4 监听快照不可用（Windows 代码 %d）。", code)
		}
		table = make([]byte, size)
	}
	return "IPv4 监听表读取中止或正在变化。"
}

func processName(pid uint32) string {
	h, err := syscall.OpenProcess(0x1000, false, pid) // PROCESS_QUERY_LIMITED_INFORMATION
	if err != nil {
		return "进程名不可用"
	}
	defer syscall.CloseHandle(h)
	buf := make([]uint16, 32768)
	size := uint32(len(buf))
	ok, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("QueryFullProcessImageNameW").Call(uintptr(h), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if ok == 0 {
		return "进程名不可用"
	}
	return filepath.Base(syscall.UTF16ToString(buf[:size]))
}
