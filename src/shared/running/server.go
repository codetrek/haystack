package running

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

var (
	lockFile   string
	ErrRunning = errors.New("server is running")
)

func RegisterLockFile(file string) {
	if len(lockFile) > 0 {
		return
	}
	lockFile = file
}

// ResetLockFileForTest clears the lockFile so RegisterLockFile can be called
// again. This is only intended for use in tests.
func ResetLockFileForTest() {
	lockFile = ""
}

func CheckAndLockServer() (func(), error) {
	if len(lockFile) == 0 {
		return nil, fmt.Errorf("lock file not registered")
	}

	// Ensure the directory exists
	dir := filepath.Dir(lockFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create lock directory: %v", err)
	}

	// Create a new file lock
	fileLock := flock.New(lockFile)

	// Try to acquire an exclusive lock
	locked, err := fileLock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("failed to acquire lock: %v", err)
	}
	if !locked {
		return nil, ErrRunning
	}

	// Return cleanup function
	cleanup := func() {
		fileLock.Unlock()
		os.Remove(lockFile)
	}

	return cleanup, nil
}

func IsServerRunning() bool {
	cancel, err := CheckAndLockServer()
	if err != nil {
		return err == ErrRunning
	}

	cancel()
	return false
}
