package vectorstore

import "os"

// osFile is the subset of *os.File the WAL uses, abstracted behind an interface
// so tests can inject IO failures (a healthy descriptor's Write/Sync/Truncate/
// Close failing) that are otherwise impossible to trigger. *os.File satisfies it.
type osFile interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Seek(offset int64, whence int) (int64, error)
	Truncate(size int64) error
	Sync() error
	Close() error
}

// fsOpenFile is the injectable file constructor. Production uses os.OpenFile;
// tests override it to return files that fail on chosen operations. It returns a
// true nil interface on error to avoid the typed-nil pitfall.
var fsOpenFile = func(name string, flag int, perm os.FileMode) (osFile, error) {
	f, err := os.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return f, nil
}
