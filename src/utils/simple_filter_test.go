package utils

import (
	"testing"
)

func TestSimpleFilterCaseSensitivity(t *testing.T) {
	tests := []struct {
		name        string
		patterns    []string
		testPath    string
		isDir       bool
		expected    bool
		description string
	}{
		{
			name:        "Lowercase pattern matches uppercase file",
			patterns:    []string{"*.go"},
			testPath:    "Main.go",
			isDir:       false,
			expected:    true,
			description: "lowercase pattern should match uppercase file extension",
		},
		{
			name:        "Uppercase pattern matches lowercase file",
			patterns:    []string{"*.GO"},
			testPath:    "main.go",
			isDir:       false,
			expected:    true,
			description: "uppercase pattern should match lowercase file extension",
		},
		{
			name:        "Mixed case pattern matches mixed case file",
			patterns:    []string{"*Test*"},
			testPath:    "MyTestFile.txt",
			isDir:       false,
			expected:    true,
			description: "mixed case pattern should match mixed case file name",
		},
		{
			name:        "Directory pattern with case insensitive matching",
			patterns:    []string{"SRC/*"},
			testPath:    "src/main.go",
			isDir:       false,
			expected:    true,
			description: "uppercase directory pattern should match lowercase directory path",
		},
		{
			name:        "Complex pattern with case insensitive matching",
			patterns:    []string{"**/Test/**/*.GO"},
			testPath:    "lib/test/unit/main.go",
			isDir:       false,
			expected:    true,
			description: "complex pattern with mixed case should match lowercase path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := NewSimpleFilter(tt.patterns)
			result := filter.Match(tt.testPath, tt.isDir)
			
			if result != tt.expected {
				t.Errorf("Test %s failed: %s. Expected %v, got %v", 
					tt.name, tt.description, tt.expected, result)
				t.Logf("Patterns: %v", tt.patterns)
				t.Logf("Test path: %s", tt.testPath)
			}
		})
	}
}

func TestSimpleFilterExcludeCaseSensitivity(t *testing.T) {
	tests := []struct {
		name        string
		patterns    []string
		testPath    string
		isDir       bool
		expected    bool
		description string
	}{
		{
			name:        "Exclude lowercase pattern matches uppercase file",
			patterns:    []string{"*.log"},
			testPath:    "Debug.LOG",
			isDir:       false,
			expected:    false,
			description: "exclude filter with lowercase pattern should exclude uppercase file",
		},
		{
			name:        "Exclude uppercase pattern matches lowercase file",
			patterns:    []string{"*.LOG"},
			testPath:    "debug.log",
			isDir:       false,
			expected:    false,
			description: "exclude filter with uppercase pattern should exclude lowercase file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := NewSimpleFilterExclude(tt.patterns)
			result := filter.Match(tt.testPath, tt.isDir)
			
			if result != tt.expected {
				t.Errorf("Test %s failed: %s. Expected %v, got %v", 
					tt.name, tt.description, tt.expected, result)
				t.Logf("Patterns: %v", tt.patterns)
				t.Logf("Test path: %s", tt.testPath)
			}
		})
	}
}
