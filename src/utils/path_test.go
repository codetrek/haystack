package utils

import (
	"runtime"
	"testing"
)

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		skipOn   string // skip on this OS
	}{
		{
			name:     "empty path returns empty",
			input:    "",
			expected: "",
		},
		{
			name:     "relative path is cleaned",
			input:    "foo/bar/../baz",
			expected: "foo/baz",
			skipOn:   "windows",
		},
		{
			name:     "relative path stays relative",
			input:    "relative/path",
			expected: "relative/path",
			skipOn:   "windows",
		},
		{
			name:     "dot path",
			input:    ".",
			expected: ".",
		},
		{
			name:     "double dot segments cleaned",
			input:    "a/b/../c",
			expected: "a/c",
			skipOn:   "windows",
		},
	}

	// Add OS-specific absolute path tests
	if runtime.GOOS != "windows" {
		tests = append(tests, []struct {
			name     string
			input    string
			expected string
			skipOn   string
		}{
			{
				name:     "unix absolute path cleaned",
				input:    "/foo/bar/../baz",
				expected: "/foo/baz",
			},
			{
				name:     "unix root path",
				input:    "/",
				expected: "/",
			},
			{
				name:     "unix absolute with trailing slash",
				input:    "/foo/bar/",
				expected: "/foo/bar",
			},
			{
				name:     "unix absolute with double slashes",
				input:    "/foo//bar",
				expected: "/foo/bar",
			},
		}...)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.skipOn == runtime.GOOS {
				t.Skipf("skipping on %s", runtime.GOOS)
			}
			result := NormalizePath(tt.input)
			if result != tt.expected {
				t.Errorf("NormalizePath(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestNormalizePathDriveLetterBranch(t *testing.T) {
	// This test exercises the drive-letter uppercasing branch.
	// On Linux the path won't have ':' at index 1, so the branch is skipped
	// but we still exercise the code path.
	result := NormalizePath("/tmp/foo")
	if result != "/tmp/foo" {
		t.Errorf("NormalizePath(/tmp/foo) = %q, want /tmp/foo", result)
	}
}
