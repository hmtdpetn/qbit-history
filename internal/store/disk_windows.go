//go:build windows

package store

import (
	"syscall"
	"unsafe"
)

func freeSpace(path string) int64 {
	p, e := syscall.UTF16PtrFromString(path)
	if e != nil {
		return -1
	}
	var available, total, free uint64
	fn := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")
	r, _, _ := fn.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&available)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&free)))
	if r == 0 {
		return -1
	}
	return int64(available)
}
