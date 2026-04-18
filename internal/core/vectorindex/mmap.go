package vectorindex

import "fmt"

// MmapFlags controls mmap protection and sharing.
const (
	mmapRead  = 1 << iota // PROT_READ
	mmapWrite             // PROT_WRITE
)

// mmapAlloc maps a region of the given file descriptor into memory.
// offset must be page-aligned. length must be > 0.
// flags is a bitmask of mmapRead | mmapWrite.
// The returned slice is backed by the OS page cache.
func mmapAlloc(fd uintptr, offset int64, length int, flags int) ([]byte, error) {
	if length <= 0 {
		return nil, fmt.Errorf("mmap: length must be > 0, got %d", length)
	}
	return mmapPlatform(fd, offset, length, flags)
}

// mmapFree unmaps a previously mapped region. The slice must not be used after
// this call returns.
func mmapFree(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	return munmapPlatform(data)
}

// mmapSync flushes dirty pages in the mapped region to disk.
func mmapSync(data []byte) error {
	return mmapSyncPlatform(data)
}
