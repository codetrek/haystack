package vectorindex

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- helpers ---

// =====================================================================
// randomLevel
// =====================================================================

func TestRandomLevelDistribution(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance, WithM(16), WithRand(rand.New(rand.NewSource(12345))))

	const iterations = 10_000
	counts := make(map[int]int)
	for i := 0; i < iterations; i++ {
		l := idx.randomLevel()
		assert.GreaterOrEqual(t, l, 0, "level must be non-negative")
		counts[l]++
	}

	// Level 0 should be the most common (exponential distribution).
	assert.Greater(t, counts[0], iterations/2, "level 0 should appear > 50%% of the time")

	nonZero := 0
	for l, c := range counts {
		if l > 0 {
			nonZero += c
		}
	}
	assert.Greater(t, nonZero, 0, "should produce some levels > 0")
}

// zeroSource is a rand.Source that always returns 0, making Float64() == 0.
type zeroSource struct{}

func (zeroSource) Int63() int64 { return 0 }
func (zeroSource) Seed(int64)   {}

// Test the r == 0 guard in randomLevel by injecting a source that returns 0.
func TestRandomLevelZeroGuard(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance, WithM(16), WithRand(rand.New(zeroSource{})))

	level := idx.randomLevel()
	assert.GreaterOrEqual(t, level, 0, "level from r=0 must be non-negative")
	assert.Less(t, level, 200, "level from r=0 (guarded to 1e-18) must be reasonable")
}

// =====================================================================
// nodeDistance
// =====================================================================

func TestNodeDistanceGetVectorRefError(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance)

	// Node 999 does not exist — GetVectorRef returns an error.
	_, err := idx.nodeDistance(999, []float32{1.0, 2.0})
	assert.Error(t, err, "nodeDistance should propagate GetVectorRef error")
}

func TestNodeDistanceSuccess(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance)

	requireNoError(t, store.PutNode(1, 0, []float32{1.0, 0.0}))

	dist, err := idx.nodeDistance(1, []float32{1.0, 0.0})
	requireNoError(t, err)
	assert.InDelta(t, 0.0, dist, 1e-6, "distance to self should be ~0")
}

// =====================================================================
// randomLevel — additional maxLevel parameter coverage
// =====================================================================

func TestRandomLevelDifferentM(t *testing.T) {
	for _, m := range []int{4, 8, 32, 64} {
		store := NewMemNodeStore()
		idx := NewHNSWIndex(store, CosineDistance, WithM(m),
			WithRand(rand.New(rand.NewSource(42))))

		for i := 0; i < 500; i++ {
			level := idx.randomLevel()
			assert.GreaterOrEqual(t, level, 0,
				"M=%d: level must be non-negative", m)
		}
	}
}
