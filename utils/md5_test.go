package utils

import (
	"crypto/md5"
	"fmt"
	"testing"
)

func TestMd5Hash(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected string
	}{
		{
			name:     "empty input",
			input:    []byte{},
			expected: fmt.Sprintf("%x", md5.Sum([]byte{})),
		},
		{
			name:     "hello world",
			input:    []byte("hello world"),
			expected: "5eb63bbbe01eeed093cb22bb8f5acdc3",
		},
		{
			name:     "binary data",
			input:    []byte{0x00, 0x01, 0x02, 0xFF},
			expected: fmt.Sprintf("%x", md5.Sum([]byte{0x00, 0x01, 0x02, 0xFF})),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Md5Hash(tt.input)
			if result != tt.expected {
				t.Errorf("Md5Hash(%v) = %s, want %s", tt.input, result, tt.expected)
			}
		})
	}
}

func TestMd5HashString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: Md5Hash([]byte("")),
		},
		{
			name:     "hello world",
			input:    "hello world",
			expected: "5eb63bbbe01eeed093cb22bb8f5acdc3",
		},
		{
			name:     "unicode string",
			input:    "日本語テスト",
			expected: Md5Hash([]byte("日本語テスト")),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Md5HashString(tt.input)
			if result != tt.expected {
				t.Errorf("Md5HashString(%q) = %s, want %s", tt.input, result, tt.expected)
			}
		})
	}
}

func TestMd5HashConsistency(t *testing.T) {
	// Md5HashString should equal Md5Hash on the same bytes
	input := "consistency check"
	if Md5HashString(input) != Md5Hash([]byte(input)) {
		t.Error("Md5HashString and Md5Hash should produce the same result for the same data")
	}
}
