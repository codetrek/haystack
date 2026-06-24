package documents

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsKeyType covers both branches of isKeyType (empty key, match, mismatch).
func TestIsKeyType(t *testing.T) {
	assert.False(t, isKeyType("", 'a'), "empty key is never a match")
	assert.True(t, isKeyType("abc", 'a'), "first byte matches")
	assert.False(t, isKeyType("abc", 'x'), "first byte differs")
}

// TestStoreIndexSeams_NilIdx covers the s.idx == nil guard in the index seams
// (a Store with no inverted index is a no-op for these notifications).
func TestStoreIndexSeams_NilIdx(t *testing.T) {
	s := &Store{} // idx is nil

	id, err := s.indexCreateTable("x")
	assert.NoError(t, err)
	assert.Equal(t, 0, id)

	// Must not panic / touch a nil index.
	s.indexDeleteTable(1)
	s.indexAddDocument(1, "doc", []string{"a"})
	s.indexUpdateDocument(1, "doc", []string{"a"})
	s.indexDeleteDocument(1, "doc")
}
