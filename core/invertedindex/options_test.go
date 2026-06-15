package invertedindex

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestOptionsDefaults exercises the default branch of each Options getter.
func TestOptionsDefaults(t *testing.T) {
	o := &Options{}
	assert.Equal(t, 1*time.Second, o.flushTicker())
	assert.Equal(t, 3*time.Second, o.flushWaitTimeout())
	assert.Equal(t, 200, o.flushWaitBatchSize())
	assert.Equal(t, 50, o.flushDeleteWaitBatchSize())
	assert.Equal(t, 5*time.Second, o.flushDeleteWaitTimeout())
	assert.Equal(t, 1*time.Second, o.flushCooldown())
	assert.Equal(t, MaxInvertedIndexSize, o.maxInvertedIndexSize())
}

// TestOptionsOverrides exercises the override branch (configured value wins).
func TestOptionsOverrides(t *testing.T) {
	o := &Options{
		FlushTicker:              7 * time.Second,
		FlushWaitTimeout:         8 * time.Second,
		FlushWaitBatchSize:       11,
		FlushDeleteWaitBatchSize: 12,
		FlushDeleteWaitTimeout:   9 * time.Second,
		FlushCooldown:            10 * time.Second,
		MaxInvertedIndexSize:     1234,
	}
	assert.Equal(t, 7*time.Second, o.flushTicker())
	assert.Equal(t, 8*time.Second, o.flushWaitTimeout())
	assert.Equal(t, 11, o.flushWaitBatchSize())
	assert.Equal(t, 12, o.flushDeleteWaitBatchSize())
	assert.Equal(t, 9*time.Second, o.flushDeleteWaitTimeout())
	assert.Equal(t, 10*time.Second, o.flushCooldown())
	assert.Equal(t, 1234, o.maxInvertedIndexSize())
}
