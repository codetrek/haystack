package fulltext

import (
	"os"
	"testing"

	"github.com/ai-microsoft/haystack/conf"
)

func setupTestEnvironment(t *testing.T) (string, func()) {
	// Set up a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "haystack-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// Set configuration
	conf.Get().Global.DataPath = tempDir

	// Initialize storage
	err = Init()
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Return cleanup function
	cleanup := func() {
		CloseAndWait()
		os.RemoveAll(tempDir)
	}

	return tempDir, cleanup
}

func TestCodecSimpleKeyValue(t *testing.T) {
	_, cleanup := setupTestEnvironment(t)
	defer cleanup()

	// Test case: simple key value
	key := "test-key"
	value := "test-value"
	expected := "test-value"

	// Encode and save
	encodedKey := EncodeWorkspaceKey(key)
	encodedValue := []byte(value)
	err := db.Put(encodedKey, encodedValue)
	if err != nil {
		t.Fatalf("Failed to set value: %v", err)
	}

	// Read and decode
	result, err := db.Get(encodedKey)
	if err != nil {
		t.Fatalf("Failed to get value: %v", err)
	}

	if string(result) != expected {
		t.Errorf("Value mismatch, got %s, want %s", string(result), expected)
	}
}

func TestCodecEmptyValue(t *testing.T) {
	_, cleanup := setupTestEnvironment(t)
	defer cleanup()

	// Test case: empty value
	key := "empty-key"
	value := ""
	expected := ""

	// Encode and save
	encodedKey := EncodeWorkspaceKey(key)
	encodedValue := []byte(value)
	err := db.Put(encodedKey, encodedValue)
	if err != nil {
		t.Fatalf("Failed to set value: %v", err)
	}

	// Read and decode
	result, err := db.Get(encodedKey)
	if err != nil {
		t.Fatalf("Failed to get value: %v", err)
	}

	if string(result) != expected {
		t.Errorf("Value mismatch, got %s, want %s", string(result), expected)
	}
}

func TestCodecSpecialCharacters(t *testing.T) {
	_, cleanup := setupTestEnvironment(t)
	defer cleanup()

	// Test case: special characters
	key := "special-key"
	value := "test\nvalue\twith\rspecial chars"
	expected := "test\nvalue\twith\rspecial chars"

	// Encode and save
	encodedKey := EncodeWorkspaceKey(key)
	encodedValue := []byte(value)
	err := db.Put(encodedKey, encodedValue)
	if err != nil {
		t.Fatalf("Failed to set value: %v", err)
	}

	// Read and decode
	result, err := db.Get(encodedKey)
	if err != nil {
		t.Fatalf("Failed to get value: %v", err)
	}

	if string(result) != expected {
		t.Errorf("Value mismatch, got %s, want %s", string(result), expected)
	}
}

func TestKeyEncodingSimpleKey(t *testing.T) {
	// Test case: simple key
	key := "test-key"
	expected := "test-key"

	encoded := EncodeWorkspaceKey(key)
	decoded := DecodeWorkspaceKey(string(encoded))
	if decoded != expected {
		t.Errorf("Key mismatch, got %s, want %s", decoded, expected)
	}
}

func TestKeyEncodingEmptyKey(t *testing.T) {
	// Test case: empty key
	key := ""
	expected := ""

	encoded := EncodeWorkspaceKey(key)
	decoded := DecodeWorkspaceKey(string(encoded))
	if decoded != expected {
		t.Errorf("Key mismatch, got %s, want %s", decoded, expected)
	}
}

func TestKeyEncodingSpecialCharacters(t *testing.T) {
	// Test case: special characters
	key := "test\nkey\twith\rspecial chars"
	expected := "test\nkey\twith\rspecial chars"

	encoded := EncodeWorkspaceKey(key)
	decoded := DecodeWorkspaceKey(string(encoded))
	if decoded != expected {
		t.Errorf("Key mismatch, got %s, want %s", decoded, expected)
	}
}
