package vectorstore

import (
	"os"
	"runtime"
)

// osFile is the subset of *os.File the mmap segment data files (seal/merge writes,
// mmap headers) use, abstracted so tests can inject IO failures. *os.File
// satisfies it. (Widened from the Phase-1 subset to add Fd/ReadAt/Stat for mmap and
// the page-aligned data-file headers.)
type osFile interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	ReadAt(p []byte, off int64) (int, error)
	Seek(offset int64, whence int) (int64, error)
	Truncate(size int64) error
	Sync() error
	Stat() (os.FileInfo, error)
	Fd() uintptr
	Close() error
}

// Compile-time guard: *os.File must satisfy the widened interface. This catches
// future drift (e.g. adding a method *os.File lacks). The existing faultFile test
// helper embeds osFile, so it inherits the new ReadAt/Stat/Fd transitively.
var _ osFile = (*os.File)(nil)

// Injectable filesystem constructors. Production uses the real os package; tests
// override them to fail on chosen operations. Each returns a true nil interface
// on error to avoid the typed-nil pitfall.
var (
	fsCreate = func(name string) (osFile, error) {
		f, err := os.Create(name)
		if err != nil {
			return nil, err
		}
		return f, nil
	}
	fsOpen = func(name string) (osFile, error) {
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		return f, nil
	}
	fsOpenFile = func(name string, flag int, perm os.FileMode) (osFile, error) {
		f, err := os.OpenFile(name, flag, perm)
		if err != nil {
			return nil, err
		}
		return f, nil
	}
	fsRemove = os.Remove
)

// fsyncDir fsyncs a directory so a rename or file creation within it is durable
// across a crash (POSIX does not persist the directory entry just because file
// contents were fsynced). On Windows a directory handle cannot be fsynced, so it
// is a no-op there.
func fsyncDir(dir string) error {
	d, err := fsOpen(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		if runtime.GOOS == "windows" {
			return nil
		}
		return err
	}
	return nil
}
