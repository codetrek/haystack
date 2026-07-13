package collection

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestUnmarshalRecord_InvalidJSON covers unmarshalRecord's error path (the
// success path is exercised by the catalog round-trip tests).
func TestUnmarshalRecord_InvalidJSON(t *testing.T) {
	_, err := unmarshalRecord([]byte("{not valid json"))
	assert.Error(t, err)
}
