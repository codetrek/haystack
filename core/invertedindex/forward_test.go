package invertedindex

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestEncodeForwardValue_ByteIdentity is the non-drift guard: every case must
// stay byte-for-byte identical to both a hand-written literal AND
// strings.Join(kw, "|") (the encoding the retired doc-words value used). These
// pass on the current strings.Join body; they exist to catch any drift when the
// body is hand-rolled to a single allocation.
func TestEncodeForwardValue_ByteIdentity(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []byte
	}{
		{"multi", []string{"a", "b", "c"}, []byte("a|b|c")},
		{"single", []string{"only"}, []byte("only")},
		{"nil-empty", []string{}, []byte{}},
		{"single-empty", []string{""}, []byte{}}, // len==1 empty branch, distinct from {}
		{"interior-empty", []string{"a", "", "b"}, []byte("a||b")},
		{"pipe-in-keyword", []string{"a|x", "b"}, []byte("a|x|b")}, // lossy-identical to today
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := encodeForwardValue(tc.in)
			assert.Equal(t, tc.want, got, "literal golden")
			assert.Equal(t, []byte(strings.Join(tc.in, "|")), got, "strings.Join parity")
			assert.Len(t, got, len(tc.want))
		})
	}

	// Round-trip: decode reverses encode for a multi-element set.
	assert.Equal(t, []string{"a", "b", "c"},
		decodeForwardValue(encodeForwardValue([]string{"a", "b", "c"})))
}

// forwardSink defeats dead-store elision of the encodeForwardValue result in the
// allocation benchmark below.
var forwardSink []byte

// TestEncodeForwardValue_SingleAlloc is the load-bearing RED for T-I3: today the
// []byte(strings.Join(...)) body allocates twice (the joined string + the []byte
// copy) for a >=2-element input, so this fails at 2.0 allocs; the hand-rolled
// single-alloc body passes at 1.0. A 1-element input cannot serve as the red
// because strings.Join special-cases len==1 to a single alloc.
//
// testing.AllocsPerRun reads the process-global runtime.MemStats.Mallocs delta,
// so a stray malloc from a background goroutine (flush/merge tickers, db worker)
// or a GC cycle firing inside the microsecond measurement window is misattributed
// to encodeForwardValue, inflating the average upward from 1 to 2 and flaking the
// gate. Contamination is upward-only, so we take the MINIMUM across a few trials:
// the single-alloc body reports 1 on at least one uncontended trial, while a
// regression to the 2-alloc strings.Join body reports >=2 on EVERY trial (the 2
// allocs are intrinsic), so the RED is preserved.
func TestEncodeForwardValue_SingleAlloc(t *testing.T) {
	kws := []string{"a", "b", "c"} // >=2 elements: strings.Join+[]byte = 2 allocs today
	best := testing.AllocsPerRun(100, func() { forwardSink = encodeForwardValue(kws) })
	for i := 0; i < 4; i++ {
		if got := testing.AllocsPerRun(100, func() { forwardSink = encodeForwardValue(kws) }); got < best {
			best = got
		}
	}
	if best > 1 {
		t.Fatalf("encodeForwardValue allocs = %v, want <= 1", best)
	}
}

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

func TestForward_AddWritesForwardAndPostings(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tid, _ := env.idx.CreateTable("add")
	d := makeDocID("doc1")

	env.idx.Add(tid, d, []string{"alpha", "beta"})

	// Forward map is committed immediately (no flush needed).
	old, known := env.idx.readForward(tid, d)
	if !known {
		t.Fatal("expected doc known in forward map after Add")
	}
	if len(old) != 2 || old[0] != "alpha" || old[1] != "beta" {
		t.Fatalf("forward set = %v, want [alpha beta]", old)
	}

	// Postings are queryable after a flush.
	forceFlush(env.idx)
	if _, ok := env.idx.Search(tid, "alpha", 0, nil).DocIds[d]; !ok {
		t.Error("expected doc1 in 'alpha' results after Add")
	}
}

func TestForward_UpdateDiffsAgainstStoredSet(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tid, _ := env.idx.CreateTable("update")
	d := makeDocID("doc1")

	env.idx.Add(tid, d, []string{"keep", "remove"})
	forceFlush(env.idx)

	// No old set passed — Update reads it from the forward map.
	env.idx.Update(tid, d, []string{"keep", "addnew"})
	forceFlush(env.idx)

	if _, ok := env.idx.Search(tid, "keep", 0, nil).DocIds[d]; !ok {
		t.Error("expected doc1 still in 'keep'")
	}
	if _, ok := env.idx.Search(tid, "addnew", 0, nil).DocIds[d]; !ok {
		t.Error("expected doc1 in 'addnew'")
	}
	if _, ok := env.idx.Search(tid, "remove", 0, nil).DocIds[d]; ok {
		t.Error("expected doc1 NOT in 'remove'")
	}
}

func TestForward_UpdateRewritesStoredSet(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tid, _ := env.idx.CreateTable("rewrite")
	d := makeDocID("doc1")

	env.idx.Add(tid, d, []string{"a"})
	env.idx.Update(tid, d, []string{"b"}) // diffs against {a}; forward becomes {b}
	env.idx.Update(tid, d, []string{"c"}) // must diff against {b}, not {a}
	forceFlush(env.idx)

	if _, ok := env.idx.Search(tid, "a", 0, nil).DocIds[d]; ok {
		t.Error("expected 'a' gone")
	}
	if _, ok := env.idx.Search(tid, "b", 0, nil).DocIds[d]; ok {
		t.Error("expected 'b' gone after second Update")
	}
	if _, ok := env.idx.Search(tid, "c", 0, nil).DocIds[d]; !ok {
		t.Error("expected 'c' present")
	}
	got, known := env.idx.readForward(tid, d)
	if !known || len(got) != 1 || got[0] != "c" {
		t.Errorf("forward set = %v (known=%v), want [c]", got, known)
	}
}

func TestForward_UpdateUnknownDocIsAdd(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tid, _ := env.idx.CreateTable("unknown")
	d := makeDocID("doc1")

	env.idx.Update(tid, d, []string{"x", "y"})
	forceFlush(env.idx)

	if _, ok := env.idx.Search(tid, "x", 0, nil).DocIds[d]; !ok {
		t.Error("expected doc1 in 'x' after unknown-doc Update")
	}
	if _, known := env.idx.readForward(tid, d); !known {
		t.Error("expected forward entry written")
	}
}

func TestForward_UpdateEmptyDeletes(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tid, _ := env.idx.CreateTable("empty")
	d := makeDocID("doc1")

	env.idx.Add(tid, d, []string{"a"})
	forceFlush(env.idx)

	env.idx.Update(tid, d, nil) // empty => Delete
	forceFlush(env.idx)

	if _, ok := env.idx.Search(tid, "a", 0, nil).DocIds[d]; ok {
		t.Error("expected doc1 removed from 'a'")
	}
	if _, known := env.idx.readForward(tid, d); known {
		t.Error("expected forward entry gone after empty Update")
	}
}

func TestForward_Delete(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tid, _ := env.idx.CreateTable("delete")
	d := makeDocID("doc1")

	env.idx.Add(tid, d, []string{"a", "b"})
	forceFlush(env.idx)

	env.idx.Delete(tid, d)
	forceFlush(env.idx)

	if _, ok := env.idx.Search(tid, "a", 0, nil).DocIds[d]; ok {
		t.Error("expected doc1 removed from 'a'")
	}
	if _, ok := env.idx.Search(tid, "b", 0, nil).DocIds[d]; ok {
		t.Error("expected doc1 removed from 'b'")
	}
	if _, known := env.idx.readForward(tid, d); known {
		t.Error("expected forward entry gone after Delete")
	}
}

func TestForward_DeleteUnknownIsNoOp(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tid, _ := env.idx.CreateTable("delete-unknown")
	env.idx.Delete(tid, makeDocID("ghost"))
	forceFlush(env.idx)
}

func TestForward_DeleteTableClearsForward(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tid, _ := env.idx.CreateTable("dt")
	d := makeDocID("doc1")
	env.idx.Add(tid, d, []string{"a"})

	if err := env.idx.DeleteTable(tid); err != nil {
		t.Fatalf("DeleteTable: %v", err)
	}
	if _, known := env.idx.readForward(tid, d); known {
		t.Error("expected forward entry cleared by DeleteTable")
	}
}

func TestForward_ReadForwardKnownEmpty(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tid, _ := env.idx.CreateTable("known-empty")
	d := makeDocID("doc1")

	// Stored value "" decodes to a zero-length slice but is still a KNOWN doc.
	env.idx.Add(tid, d, []string{""})
	got, known := env.idx.readForward(tid, d)
	if !known {
		t.Fatal("expected known doc for stored empty value")
	}
	if len(got) != 0 {
		t.Errorf("expected zero-length keyword set, got %v", got)
	}
}

func TestForward_AddEmptyIsNoOp(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tid, _ := env.idx.CreateTable("add-empty")
	d := makeDocID("doc1")

	// An empty keyword set indexes nothing and writes no forward entry.
	env.idx.Add(tid, d, nil)
	env.idx.Add(tid, d, []string{})

	if _, known := env.idx.readForward(tid, d); known {
		t.Error("empty Add must not write a forward entry")
	}
}

func TestForward_DbErrorsAreLoggedNotPanicked(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tid, _ := env.idx.CreateTable("db-err")
	d := makeDocID("doc1")

	// Swap in a store whose every op errors. Each forward method must log and
	// continue without panicking — covering the db.Get/Put/Delete error branches.
	restore := simulateClosedDB(env.idx)
	defer restore()

	if _, known := env.idx.readForward(tid, d); known {
		t.Error("readForward on a db error should report the doc as unknown")
	}
	env.idx.Add(tid, d, []string{"a"})    // db.Put(forward) errors
	env.idx.Update(tid, d, []string{"b"}) // readForward errors -> Add -> db.Put errors
	env.idx.Delete(tid, d)                // readForward errors -> db.Delete errors
}
