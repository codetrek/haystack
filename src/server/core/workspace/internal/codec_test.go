package internal

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKeyEncodingKey(t *testing.T) {
	key := 10
	expected := 10

	encoded := EncodeWorkspaceKey(key)
	decoded, _ := DecodeWorkspaceKey(string(encoded))

	assert.Equal(t, expected, decoded, "Key mismatch")
}

func TestEncodeWorkspaceIncrIdKey(t *testing.T) {
	key := EncodeWorkspaceIncrIdKey()
	assert.Equal(t, 1, len(key), "EncodeWorkspaceIncrIdKey should produce a single-byte key")
	assert.Equal(t, KeyTypeWorkspaceIncrId, key[0], "Key byte should match KeyTypeWorkspaceIncrId")
}

func TestEncodeWorkspaceKey_MultipleIds(t *testing.T) {
	tests := []struct {
		name string
		id   int
	}{
		{"zero", 0},
		{"one", 1},
		{"large", 99999},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeWorkspaceKey(tt.id)
			decoded, err := DecodeWorkspaceKey(string(encoded))
			assert.NoError(t, err)
			assert.Equal(t, tt.id, decoded)
		})
	}
}

func TestDecodeWorkspaceKey_InvalidKeyType(t *testing.T) {
	// Use a key whose first byte is NOT KeyTypeWorkspace
	invalidKey := string([]byte{KeyTypeWorkspaceIncrId}) + "42"
	id, err := DecodeWorkspaceKey(invalidKey)
	assert.Equal(t, -1, id, "should return -1 for invalid key type")
	assert.Error(t, err, "should return error for invalid key type")
	assert.Contains(t, err.Error(), "invalid key type")
}

func TestDecodeWorkspaceKey_NonNumericSuffix(t *testing.T) {
	// Valid key type prefix but non-numeric suffix
	badKey := string([]byte{KeyTypeWorkspace}) + "abc"
	_, err := DecodeWorkspaceKey(badKey)
	assert.Error(t, err, "should return error for non-numeric suffix")
}

func TestDecodeWorkspaceKey_EmptyString(t *testing.T) {
	id, err := DecodeWorkspaceKey("")
	assert.Equal(t, -1, id)
	assert.Error(t, err)
}
