package utils

import (
	"testing"
)

// TestSimpleFilterCaseInsensitiveIntegration performs integration tests for case-insensitive matching
func TestSimpleFilterCaseInsensitiveIntegration(t *testing.T) {
	tests := []struct {
		name      string
		patterns  []string
		testCases []struct {
			path     string
			isDir    bool
			expected bool
		}
		description string
	}{
		{
			name:        "Go files with mixed case extensions",
			patterns:    []string{"*.go"},
			description: "Should match Go files regardless of case",
			testCases: []struct {
				path     string
				isDir    bool
				expected bool
			}{
				{"main.go", false, true},
				{"Main.GO", false, true},
				{"test.Go", false, true},
				{"utils.gO", false, true},
				{"readme.txt", false, false},
			},
		},
		{
			name:        "Source directory patterns",
			patterns:    []string{"src/*"},
			description: "Should match files in src directory regardless of case",
			testCases: []struct {
				path     string
				isDir    bool
				expected bool
			}{
				{"src/main.go", false, true},
				{"SRC/main.go", false, true},
				{"Src/main.go", false, true},
				{"src/SUB/file.txt", false, true},
				{"lib/main.go", false, false},
			},
		},
		{
			name:        "Complex glob patterns",
			patterns:    []string{"**/Test/**/*.JS"},
			description: "Should match JS files in test directories regardless of case",
			testCases: []struct {
				path     string
				isDir    bool
				expected bool
			}{
				{"lib/test/unit/main.js", false, true},
				{"lib/TEST/unit/main.js", false, true},
				{"lib/Test/unit/main.JS", false, true},
				{"lib/test/unit/main.TS", false, false},
				{"lib/spec/unit/main.js", false, false},
			},
		},
		{
			name:        "Multiple pattern types",
			patterns:    []string{"*.LOG", "*.TXT", "SRC/*"},
			description: "Should match multiple patterns with case insensitive matching",
			testCases: []struct {
				path     string
				isDir    bool
				expected bool
			}{
				{"debug.log", false, true},
				{"DEBUG.LOG", false, true},
				{"readme.txt", false, true},
				{"README.TXT", false, true},
				{"src/main.go", false, true},
				{"SRC/main.go", false, true},
				{"lib/main.go", false, false},
				{"test.json", false, false},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := NewSimpleFilter(tt.patterns)

			for _, tc := range tt.testCases {
				t.Run(tc.path, func(t *testing.T) {
					result := filter.Match(tc.path, tc.isDir)

					if result != tc.expected {
						t.Errorf("Test failed for path '%s': %s. Expected %v, got %v",
							tc.path, tt.description, tc.expected, result)
						t.Logf("Patterns: %v", tt.patterns)
					}
				})
			}
		})
	}
}

// TestSimpleFilterExcludeCaseInsensitiveIntegration performs integration tests for case-insensitive exclude matching
func TestSimpleFilterExcludeCaseInsensitiveIntegration(t *testing.T) {
	tests := []struct {
		name      string
		patterns  []string
		testCases []struct {
			path     string
			isDir    bool
			expected bool
		}
		description string
	}{
		{
			name:        "Exclude log files with mixed case",
			patterns:    []string{"*.LOG"},
			description: "Should exclude log files regardless of case",
			testCases: []struct {
				path     string
				isDir    bool
				expected bool
			}{
				{"debug.log", false, false},
				{"DEBUG.LOG", false, false},
				{"Error.Log", false, false},
				{"main.go", false, true},
				{"README.TXT", false, true},
			},
		},
		{
			name:        "Exclude node_modules directories",
			patterns:    []string{"**/NODE_MODULES/**"},
			description: "Should exclude node_modules directory contents regardless of case",
			testCases: []struct {
				path     string
				isDir    bool
				expected bool
			}{
				{"node_modules/package/index.js", false, false},
				{"NODE_MODULES/package/index.js", false, false},
				{"lib/node_modules/package/index.js", false, false},
				{"lib/NODE_MODULES/package/index.js", false, false},
				{"src/main.js", false, true},
				{"lib/src/main.js", false, true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := NewSimpleFilterExclude(tt.patterns)

			for _, tc := range tt.testCases {
				t.Run(tc.path, func(t *testing.T) {
					result := filter.Match(tc.path, tc.isDir)

					if result != tc.expected {
						t.Errorf("Test failed for path '%s': %s. Expected %v, got %v",
							tc.path, tt.description, tc.expected, result)
						t.Logf("Patterns: %v", tt.patterns)
					}
				})
			}
		})
	}
}
