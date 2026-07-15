package fsutils

import (
	"os"
)

func ReadFileWithDefault(path string, defaultBytes []byte) []byte {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return defaultBytes
	}
	return bytes
}
