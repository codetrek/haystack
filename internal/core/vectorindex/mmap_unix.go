//go:build !windows

package vectorindex

import "syscall"

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
