package invertedstore

import (
	"reflect"
	"testing"
)

// ids extracts the .Id sequence of a segMeta slice for order assertions.
func ids(metas []segMeta) []uint64 {
	out := make([]uint64, len(metas))
	for i, m := range metas {
		out[i] = m.Id
	}
	return out
}

// TestSortSegMetasByIdOrdersAscending pins the in-place insertion sort that orders segMetas
// oldest->newest by Id. Its production callers are timing-dependent merge paths, so this direct,
// deterministic unit test forces the swap and early-stop branches every run (independent of merge
// ordering): {3,1,2} needs two swaps to become {1,2,3} and its second insertion hits the
// metas[j-1].Id <= metas[j].Id false-condition early stop, while the single/empty cases cover the
// len<2 no-op (the outer loop never runs).
func TestSortSegMetasByIdOrdersAscending(t *testing.T) {
	cases := []struct {
		name string
		in   []uint64
		want []uint64
	}{
		{"unsorted forces swaps and early stop", []uint64{3, 1, 2}, []uint64{1, 2, 3}},
		{"already sorted stays put", []uint64{1, 2, 3}, []uint64{1, 2, 3}},
		{"reverse fully sorts", []uint64{5, 4, 3, 2, 1}, []uint64{1, 2, 3, 4, 5}},
		{"single element no-op", []uint64{5}, []uint64{5}},
		{"empty no-op", []uint64{}, []uint64{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metas := make([]segMeta, len(tc.in))
			for i, id := range tc.in {
				metas[i] = segMeta{Id: id}
			}
			sortSegMetasById(metas)
			got := ids(metas)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("sortSegMetasById(%v) got Id order %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
