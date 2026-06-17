package vectorstore

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
//
// It is a package var (not a plain func) so tests can inject mapping failures.
var mmapAlloc = func(fd uintptr, offset int64, length int, flags int) ([]byte, error) {
	if length <= 0 {
		return nil, fmt.Errorf("mmap: length must be > 0, got %d", length)
	}
	return mmapPlatform(fd, offset, length, flags)
}

// mmapFree unmaps a previously mapped region. The slice must not be used after
// this call returns. It is a package var so tests can inject unmap failures.
var mmapFree = func(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	return munmapPlatform(data)
}

// mmapSync flushes dirty pages in the mapped region to disk. It is a package var
// so tests can inject msync failures.
var mmapSync = func(data []byte) error {
	return mmapSyncPlatform(data)
}
