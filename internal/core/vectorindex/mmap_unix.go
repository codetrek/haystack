//go:build !windows

package vectorindex

import (
	"syscall"
	"unsafe"
)

func mmapPlatform(fd uintptr, offset int64, length int, flags int) ([]byte, error) {
	prot := 0
	if flags&mmapRead != 0 {
		prot |= syscall.PROT_READ
	}
	if flags&mmapWrite != 0 {
		prot |= syscall.PROT_WRITE
	}
	return syscall.Mmap(int(fd), offset, length, prot, syscall.MAP_SHARED)
}

func munmapPlatform(data []byte) error {
	return syscall.Munmap(data)
}

// mmapSyncPlatform flushes mmap'd pages to disk using msync(2).
func mmapSyncPlatform(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	addr := uintptr(unsafe.Pointer(&data[0]))
	_, _, err := syscall.Syscall(syscall.SYS_MSYNC, addr, uintptr(len(data)), uintptr(syscall.MS_SYNC))
	if err != 0 {
		return err
	}
	return nil
}
