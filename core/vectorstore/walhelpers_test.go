package vectorstore

import (
	"errors"
	"os"
	"testing"
)

var errInjected = errors.New("injected failure")

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// faultFile wraps an osFile and fails the selected operation on demand. Close
// always releases the underlying fd even when injecting a Close error.
type faultFile struct {
	osFile
	failWrite    bool
	failSync     bool
	failTruncate bool
	failClose    bool
	failReadAt   bool
}

func (f *faultFile) Write(p []byte) (int, error) {
	if f.failWrite {
		return 0, errInjected
	}
	return f.osFile.Write(p)
}
func (f *faultFile) ReadAt(p []byte, off int64) (int, error) {
	if f.failReadAt {
		return 0, errInjected
	}
	return f.osFile.ReadAt(p, off)
}
func (f *faultFile) Sync() error {
	if f.failSync {
		return errInjected
	}
	return f.osFile.Sync()
}
func (f *faultFile) Truncate(size int64) error {
	if f.failTruncate {
		return errInjected
	}
	return f.osFile.Truncate(size)
}
func (f *faultFile) Close() error {
	cerr := f.osFile.Close()
	if f.failClose {
		return errInjected
	}
	return cerr
}

// statFaultFile wraps an osFile and always fails Stat, to drive fileSize's
// Stat-error branch.
type statFaultFile struct {
	osFile
}

func (f *statFaultFile) Stat() (os.FileInfo, error) { return nil, errInjected }

func withOpenFileFault(t *testing.T, cfg func(*faultFile)) {
	t.Helper()
	orig := fsOpenFile
	t.Cleanup(func() { fsOpenFile = orig })
	fsOpenFile = func(name string, flag int, perm os.FileMode) (osFile, error) {
		f, err := orig(name, flag, perm)
		if err != nil {
			return nil, err
		}
		ff := &faultFile{osFile: f}
		cfg(ff)
		return ff, nil
	}
}
