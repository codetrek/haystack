package utils

import (
	"path/filepath"
	"strings"
)

// NormalizePath normalizes the path to a canonical form.
// Uppercase the drive letter if the path is on Windows.
func NormalizePath(path string) string {
	if path == "" {
		return ""
	}

	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return path
	}

	if len(path) > 1 && path[1] == ':' {
		path = strings.ToUpper(path[:1]) + path[1:]
	}

	return path
}
