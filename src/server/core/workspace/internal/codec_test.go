package internal

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKeyEncodingKey(t *testing.T) {
	// Test case: empty key
	key := 10
	expected := 10

	encoded := EncodeWorkspaceKey(key)
	decoded, _ := DecodeWorkspaceKey(string(encoded))

	assert.Equal(t, expected, decoded, "Key mismatch")
}
