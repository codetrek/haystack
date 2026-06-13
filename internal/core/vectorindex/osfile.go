package vectorindex

import "os"

// osFile is the subset of *os.File the mmap store and WAL use, abstracted behind
// an interface so tests can inject IO failures (a healthy descriptor's
// Write/Sync/Close/Truncate failing) that are otherwise impossible to trigger.
// *os.File satisfies osFile.
type osFile interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	ReadAt(p []byte, off int64) (int, error)
	WriteAt(p []byte, off int64) (int, error)
	Seek(offset int64, whence int) (int64, error)
	Truncate(size int64) error
	Sync() error
	Stat() (os.FileInfo, error)
	Fd() uintptr
	Close() error
}

// Injectable filesystem constructors. Production uses the real os package; tests
// override these to return files that fail on chosen operations. Each returns a
// true nil interface on error to avoid the typed-nil pitfall.
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
	fsRename = os.Rename
	fsRemove = os.Remove
)
