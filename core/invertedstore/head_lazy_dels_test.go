package invertedstore

import (
	"reflect"
	"testing"
)

func TestHeadFix_DelsLazyOnAddsOnly(t *testing.T) {
	h := newHeadTable()
	h.addPosting("alpha", 1)
	h.addPosting("alpha", 2)
	pd := h.inv["alpha"]
	if pd.dels != nil {
		t.Fatalf("dels allocated on an adds-only keyword; want nil (lazy)")
	}
	if !reflect.DeepEqual(setToSlice(pd.adds), []int64{1, 2}) && len(pd.adds) != 2 {
		t.Fatalf("adds = %v, want {1,2}", pd.adds)
	}
}

// add -> tombstone -> re-add on the same (kw,docid) must collapse to the survivor (PRESENT), exactly
// as the eager-map version did, exercising the nil->alloc transition both ways.
func TestHeadFix_AddDelReaddResolves(t *testing.T) {
	h := newHeadTable()
	h.addPosting("k", 5)       // adds={5}, dels=nil
	h.tombstonePosting("k", 5) // adds={}, dels={5}
	h.addPosting("k", 5)       // adds={5}, dels={}
	pd := h.inv["k"]
	if _, ok := pd.adds[5]; !ok {
		t.Fatalf("docid 5 should be a live add after add/del/re-add")
	}
	if _, ok := pd.dels[5]; ok {
		t.Fatalf("docid 5 should NOT be tombstoned after the final re-add")
	}
	// tombstone-first path allocates adds lazily and stays correct.
	h.tombstonePosting("t", 9) // adds=nil, dels={9}
	if h.inv["t"].adds != nil {
		t.Fatalf("adds allocated on a tombstone-only keyword; want nil (lazy)")
	}
}
