package prompts

import (
	"testing"
)

func TestParseWorkspaceId(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{"Valid positive number", "123", 123},
		{"Valid zero", "0", 0},
		{"Valid negative number", "-1", -1},
		{"Invalid string", "abc", InvalidWorkspaceId},
		{"Invalid empty string", "", InvalidWorkspaceId},
		{"Invalid mixed", "123abc", InvalidWorkspaceId},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseWorkspaceId(tt.input)
			if result != tt.expected {
				t.Errorf("ParseWorkspaceId(%q) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestEncodePromptPathKey(t *testing.T) {
	tests := []struct {
		name        string
		workspaceId int
		promptPath  string
		expected    string
	}{
		{"Basic case", 123, "test.prompt.md", "(123|test.prompt.md"},
		{"Zero workspace", 0, "example.prompt.md", "(0|example.prompt.md"},
		{"Negative workspace", -1, "negative.prompt.md", "(-1|negative.prompt.md"},
		{"Empty path", 123, "", "(123|"},
		{"Path with subdirs", 456, "subdir/test.prompt.md", "(456|subdir/test.prompt.md"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := EncodePromptPathKey(tt.workspaceId, tt.promptPath)
			if string(result) != tt.expected {
				t.Errorf("EncodePromptPathKey(%d, %q) = %q, want %q", tt.workspaceId, tt.promptPath, string(result), tt.expected)
			}
		})
	}
}

func TestDecodePromptPathKey(t *testing.T) {
	tests := []struct {
		name              string
		input             string
		expectedWorkspace int
		expectedPath      string
	}{
		{"Valid key", "(123|test.prompt.md", 123, "test.prompt.md"},
		{"Zero workspace", "(0|example.prompt.md", 0, "example.prompt.md"},
		{"Empty path", "(123|", 123, ""},
		{"Path with subdirs", "(456|subdir/test.prompt.md", 456, "subdir/test.prompt.md"},
		{"Invalid key type", ")123|test.prompt.md", InvalidWorkspaceId, ""},
		{"Missing separator", "(123test.prompt.md", InvalidWorkspaceId, ""},
		{"Invalid workspace id", "(abc|test.prompt.md", InvalidWorkspaceId, "test.prompt.md"},
		{"Empty key", "", InvalidWorkspaceId, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspaceId, path := DecodePromptPathKey(tt.input)
			if workspaceId != tt.expectedWorkspace {
				t.Errorf("DecodePromptPathKey(%q) workspace = %d, want %d", tt.input, workspaceId, tt.expectedWorkspace)
			}
			if path != tt.expectedPath {
				t.Errorf("DecodePromptPathKey(%q) path = %q, want %q", tt.input, path, tt.expectedPath)
			}
		})
	}
}

func TestEncodeDecodeFloat32Vector(t *testing.T) {
	tests := []struct {
		name   string
		vector []float32
	}{
		{"Empty vector", []float32{}},
		{"Single element", []float32{1.5}},
		{"Multiple elements", []float32{1.0, 2.5, -3.7, 0.0, 999.999}},
		{"Large vector", make([]float32, 1000)},
	}

	// Initialize large vector with test data
	for i := range tests[3].vector {
		tests[3].vector[i] = float32(i) * 0.1
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test encoding
			encoded, err := EncodeFloat32Vector(tt.vector)
			if err != nil {
				t.Fatalf("EncodeFloat32Vector failed: %v", err)
			}

			// Test decoding
			decoded, err := DecodeToFloat32Vector(encoded)
			if err != nil {
				t.Fatalf("DecodeToFloat32Vector failed: %v", err)
			}

			// Compare lengths
			if len(decoded) != len(tt.vector) {
				t.Errorf("Length mismatch: got %d, want %d", len(decoded), len(tt.vector))
			}

			// Compare values
			for i, v := range tt.vector {
				if i < len(decoded) && decoded[i] != v {
					t.Errorf("Value mismatch at index %d: got %f, want %f", i, decoded[i], v)
				}
			}
		})
	}
}

func TestDecodeToFloat32VectorErrors(t *testing.T) {
	tests := []struct {
		name        string
		data        []byte
		expectError bool
	}{
		{"Invalid length - 1 byte", []byte{0x01}, true},
		{"Invalid length - 2 bytes", []byte{0x01, 0x02}, true},
		{"Invalid length - 3 bytes", []byte{0x01, 0x02, 0x03}, true},
		{"Valid length - 4 bytes", []byte{0x00, 0x00, 0x80, 0x3f}, false},                         // 1.0 in little-endian
		{"Valid length - 8 bytes", []byte{0x00, 0x00, 0x80, 0x3f, 0x00, 0x00, 0x00, 0x40}, false}, // 1.0, 2.0
		{"Empty data", []byte{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeToFloat32Vector(tt.data)
			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	// Test round-trip encoding/decoding
	original := []float32{-100.5, 0.0, 1.23456789, 999999.999}

	encoded, err := EncodeFloat32Vector(original)
	if err != nil {
		t.Fatalf("Encoding failed: %v", err)
	}

	decoded, err := DecodeToFloat32Vector(encoded)
	if err != nil {
		t.Fatalf("Decoding failed: %v", err)
	}

	if len(decoded) != len(original) {
		t.Fatalf("Length mismatch: got %d, want %d", len(decoded), len(original))
	}

	for i, v := range original {
		if decoded[i] != v {
			t.Errorf("Round-trip failed at index %d: got %f, want %f", i, decoded[i], v)
		}
	}
}
