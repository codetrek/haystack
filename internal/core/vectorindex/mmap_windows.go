//go:build windows

package vectorindex

import (
	"fmt"
	"syscall"
	"unsafe"
)

func mmapPlatform(fd uintptr, offset int64, length int, flags int) ([]byte, error) {
	// Determine protection flags.
	flProtect := uint32(syscall.PAGE_READONLY)
	dwAccess := uint32(syscall.FILE_MAP_READ)
	if flags&mmapWrite != 0 {
		flProtect = syscall.PAGE_READWRITE
		dwAccess = syscall.FILE_MAP_WRITE
	}

	maxSize := offset + int64(length)
	hi := uint32(maxSize >> 32)
	lo := uint32(maxSize & 0xFFFFFFFF)

	h, err := syscall.CreateFileMapping(syscall.Handle(fd), nil, flProtect, hi, lo, nil)
	if err != nil {
		return nil, fmt.Errorf("mmap: CreateFileMapping: %w", err)
	}
	defer syscall.CloseHandle(h)

	offHi := uint32(offset >> 32)
	offLo := uint32(offset & 0xFFFFFFFF)
	addr, err := syscall.MapViewOfFile(h, dwAccess, offHi, offLo, uintptr(length))
	if err != nil {
		return nil, fmt.Errorf("mmap: MapViewOfFile: %w", err)
	}

	return unsafe.Slice((*byte)(unsafe.Pointer(addr)), length), nil
}

func munmapPlatform(data []byte) error {
	addr := uintptr(unsafe.Pointer(&data[0]))
	return syscall.UnmapViewOfFile(addr)
}

// mmapSyncWindows flushes mmap'd pages to disk on Windows.
func mmapSyncWindows(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	return syscall.FlushViewOfFile(uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))
}
