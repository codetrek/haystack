package invertedstore

import (
	"reflect"
	"sort"
	"testing"
)

// pdFromOps builds a postingDelta from an ordered (docid, isAdd) op log, mirroring the worker's
// append order. Used by the resolve tests to feed an exact op sequence (incl. full-int64-range
// docids) through resolveOps without going through the Store.
func pdFromOps(ops ...struct {
	docid int64
	isAdd bool
}) *postingDelta {
	pd := &postingDelta{}
	for _, o := range ops {
		pd.appendOp(o.docid, o.isAdd)
	}
	return pd
}

func op(docid int64, isAdd bool) struct {
	docid int64
	isAdd bool
} {
	return struct {
		docid int64
		isAdd bool
	}{docid, isAdd}
}

func TestResolveOps_LatestWinsMatchesMap(t *testing.T) {
	type o = struct {
		docid int64
		isAdd bool
	}
	cases := []struct {
		name       string
		ops        []o
		adds, dels []int64
	}{
		{"add only", []o{op(5, true)}, []int64{5}, nil},
		{"del only", []o{op(5, false)}, nil, []int64{5}},
		{"add then del (latest=del)", []o{op(5, true), op(5, false)}, nil, []int64{5}},
		{"del then add (latest=add)", []o{op(5, false), op(5, true)}, []int64{5}, nil},
		{"add del add (latest=add)", []o{op(5, true), op(5, false), op(5, true)}, []int64{5}, nil},
		{"repeated add dedups", []o{op(5, true), op(5, true)}, []int64{5}, nil},
		{"interleaved", []o{op(1, true), op(2, false), op(1, false), op(2, true)}, []int64{2}, []int64{1}},
		{"cold-build append-only", []o{op(3, true), op(7, true), op(1, true)}, []int64{1, 3, 7}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			adds, dels := resolveOps(pdFromOps(c.ops...))
			sort.Slice(adds, func(i, j int) bool { return adds[i] < adds[j] })
			sort.Slice(dels, func(i, j int) bool { return dels[i] < dels[j] })
			if !eqInt64s(adds, c.adds) || !eqInt64s(dels, c.dels) { // reuse the existing eqInt64s; do NOT add eqInt64
				t.Fatalf("resolveOps(%v) = adds %v dels %v, want adds %v dels %v", c.ops, adds, dels, c.adds, c.dels)
			}
		})
	}
}

// Full-int64-range docids (>= 2^62, incl. math.MaxInt64) MUST round-trip through resolveOps — the raw
// docid + parallel isAdd bitset carries them with no packable-range limit (item H, spec §5b fallback).
func TestResolveOps_FullInt64Range(t *testing.T) {
	const maxInt64 = int64(^uint64(0) >> 1) // math.MaxInt64 without importing math
	pd := pdFromOps(
		op(1<<62, true),     // an add at exactly 2^62 (the old packing's panic point)
		op(maxInt64, false), // del at MaxInt64
		op(maxInt64, true),  // re-add at MaxInt64 -> latest is add
		op(1<<62, false),    // del at 2^62 -> latest is del
	)
	adds, dels := resolveOps(pd)
	sort.Slice(adds, func(i, j int) bool { return adds[i] < adds[j] })
	sort.Slice(dels, func(i, j int) bool { return dels[i] < dels[j] })
	if !eqInt64s(adds, []int64{maxInt64}) {
		t.Fatalf("adds = %v, want {MaxInt64} (re-add wins at MaxInt64)", adds)
	}
	if !eqInt64s(dels, []int64{1 << 62}) {
		t.Fatalf("dels = %v, want {1<<62} (del wins at 2^62)", dels)
	}
}

// resolveOps MUST NOT mutate its input postingDelta (concurrent Search + the read-only detached-head
// encode share the head's pd): it resolves on a fresh scratch copy of the docids + isAdd bitset.
func TestResolveOps_DoesNotMutateInput(t *testing.T) {
	pd := pdFromOps(op(2, true), op(1, false), op(2, false))
	docidsCp := append([]int64(nil), pd.docids...)
	isAddCp := append([]uint64(nil), pd.isAdd...)
	resolveOps(pd)
	if !reflect.DeepEqual(pd.docids, docidsCp) || !reflect.DeepEqual(pd.isAdd, isAddCp) {
		t.Fatalf("resolveOps mutated its input: docids %v (want %v) isAdd %v (want %v)",
			pd.docids, docidsCp, pd.isAdd, isAddCp)
	}
}
