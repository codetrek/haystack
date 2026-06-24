package invertedindex

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestForwardValueCodec_WireFormat(t *testing.T) {
	assert.Equal(t, []byte("a|b|c"), encodeForwardValue([]string{"a", "b", "c"}))
	assert.Equal(t, []string{"a", "b", "c"}, decodeForwardValue([]byte("a|b|c")))
}

func TestForwardValueCodec_Empty(t *testing.T) {
	// An empty value decodes to a zero-length slice, NOT []string{""}.
	assert.Equal(t, []string{}, decodeForwardValue([]byte{}))
	assert.Equal(t, []string{}, decodeForwardValue(nil))
}

func TestForwardValueCodec_Single(t *testing.T) {
	assert.Equal(t, []byte("only"), encodeForwardValue([]string{"only"}))
	assert.Equal(t, []string{"only"}, decodeForwardValue([]byte("only")))
}

func TestForwardKey_TableScopedPrefix(t *testing.T) {
	idx := &Index{keyTypeForward: DefaultKeyTypeForward}
	// Table 5's prefix must NOT be a byte-prefix of table 50's keys.
	p5 := idx.encodeForwardKeyPrefix(5)
	k50 := idx.encodeForwardKey(50, 123)
	assert.False(t, len(k50) >= len(p5) && string(k50[:len(p5)]) == string(p5),
		"table 5 prefix must not match table 50 keys")
	// The real key for (5, docid) IS under table 5's prefix.
	k5 := idx.encodeForwardKey(5, 123)
	assert.Equal(t, string(p5), string(k5[:len(p5)]))
	assert.Equal(t, DefaultKeyTypeForward, k5[0])
}
