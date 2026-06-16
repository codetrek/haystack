package vectorindex

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// Error-path coverage for the VEC-007 entry-point maintenance added to
// deleteNodeLocked: the previously-swallowed `_ = SetEntryPoint(...)` is now
// `return err`, and the newLevel<0 fallback can fail in HighestLiveNodeExcluding,
// SetEntryPoint (reseat), or ClearEntryPoint. Each must abort the delete (and,
// for MmapStore, the surrounding transaction) rather than silently leaving a
// stale entry point — the exact class of bug VEC-007 grew from.

// deleteEPErrorScenario builds an index over an errorStore whose inner store
// holds isolated node A (the entry point, no edges) plus `extra` other isolated
// live nodes, then returns the index and store ready for an injected-error Delete
// of docId 100 (== node A).
func deleteEPErrorScenario(t *testing.T, extra int) (*HNSWIndex, *errorStore) {
	t.Helper()
	es := newErrorStore()
	idx := NewHNSWIndex(es)
	idA := buildIsolatedMemNode(t, es.inner, 0, []float32{1, 0, 0, 0}, 100)
	for i := 0; i < extra; i++ {
		buildIsolatedMemNode(t, es.inner, 0, []float32{0, 1, 0, 0}, int64(200+i))
	}
	require.NoError(t, es.inner.SetEntryPoint(idA, 0)) // A is the EP, A has no neighbors
	return idx, es
}

func TestDeleteEntryPointReseatErrorsPropagate(t *testing.T) {
	injected := errors.New("injected store error")

	// newLevel<0 fallback, other live node present: HighestLiveNodeExcluding fails.
	t.Run("HighestLiveNodeExcluding", func(t *testing.T) {
		idx, es := deleteEPErrorScenario(t, 1)
		es.HighestLiveNodeExcludingErr = injected
		require.ErrorIs(t, idx.Delete(100), injected)
	})

	// newLevel<0 fallback, reseating to the live node: SetEntryPoint fails.
	t.Run("SetEntryPoint", func(t *testing.T) {
		idx, es := deleteEPErrorScenario(t, 1)
		es.SetEntryPointErr = injected
		require.ErrorIs(t, idx.Delete(100), injected)
	})

	// newLevel<0 fallback, no other live node: ClearEntryPoint fails.
	t.Run("ClearEntryPoint", func(t *testing.T) {
		idx, es := deleteEPErrorScenario(t, 0)
		es.ClearEntryPointErr = injected
		require.ErrorIs(t, idx.Delete(100), injected)
	})
}

// TestMmapClearEntryPointFaulted covers MmapStore.ClearEntryPoint's faulted
// guard: once faulted, clearing the entry point is rejected like every other
// write (recovery is via reopen).
func TestMmapClearEntryPointFaulted(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	require.NoError(t, err)
	defer s.Close()

	injected := errors.New("injected fault")
	_ = s.fault(injected)
	require.ErrorIs(t, s.ClearEntryPoint(), injected)
}
