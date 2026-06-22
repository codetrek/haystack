package invertedindex

import (
	"fmt"
	"testing"
)

// B1 — GetDocs is an EXACT keyword match: the prefix "<tid>|a|" also byte-matches
// rows of keyword "a|x", so GetDocs("a") must not leak the "a|x" postings.
func TestGetDocs_NoPipePrefixLeak(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()
	env.idx.Update(1, makeDocID("d1"), []string{"a"}, nil)
	env.idx.Update(1, makeDocID("d2"), []string{"a|x"}, nil)
	forceFlush(env.idx)

	a := env.idx.GetDocs(1, "a")
	if _, leak := a.DocIds[makeDocID("d2")]; leak {
		t.Error(`GetDocs("a") leaked the "a|x" doc`)
	}
	if _, ok := a.DocIds[makeDocID("d1")]; !ok {
		t.Error(`GetDocs("a") lost its own doc`)
	}
	if _, ok := env.idx.GetDocs(1, "a|x").DocIds[makeDocID("d2")]; !ok {
		t.Error(`GetDocs("a|x") missing its doc`)
	}
}

// B1 — deleting a doc from keyword "a" must not rewrite/destroy keyword "a|x".
func TestRemoveDocuments_NoPipeCrossDelete(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()
	env.idx.Update(1, makeDocID("d1"), []string{"a"}, nil)
	env.idx.Update(1, makeDocID("d2"), []string{"a|x"}, nil)
	forceFlush(env.idx)

	env.idx.Update(1, makeDocID("d1"), nil, []string{"a"}) // delete d1 from "a"
	forceFlush(env.idx)

	if _, ok := env.idx.GetDocs(1, "a|x").DocIds[makeDocID("d2")]; !ok {
		t.Error(`deleting from "a" destroyed keyword "a|x"`)
	}
}

// B3 — encodeInvertedKey must be unique even for the same (tableId,keyword,
// doccount) within one microsecond (else one row silently overwrites another).
func TestEncodeInvertedKey_UniqueWithinMicrosecond(t *testing.T) {
	idx := &Index{keyTypeRow: DefaultKeyTypeRow}
	seen := make(map[string]struct{}, 2000)
	for i := 0; i < 2000; i++ {
		k := string(idx.encodeInvertedKey(7, "a", 5))
		if _, dup := seen[k]; dup {
			t.Fatalf("duplicate key at i=%d: %q", i, k)
		}
		seen[k] = struct{}{}
	}
}

// B4/B5/B8 — docids are int64 by type, so the fixed-width value codec can never
// be fed a wrong-length docid (the old string-ingress length guard is no longer
// representable). This pins the surviving invariant: arbitrary int64 docids —
// including 0, negative, and max — round-trip through Update/GetDocs intact and
// every stored value stays a multiple of docIDSize.
func TestUpdate_Int64DocidRoundTrip(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	docids := []int64{0, 1, -1, 1 << 40, 9223372036854775807 /* max int64 */}
	for _, id := range docids {
		env.idx.Update(1, id, []string{"kw"}, nil)
	}
	forceFlush(env.idx)

	res := env.idx.GetDocs(1, "kw")
	if len(res.DocIds) != len(docids) {
		t.Errorf("expected %d docs indexed, got %d: %v", len(docids), len(res.DocIds), res.DocIds)
	}
	for _, id := range docids {
		if _, ok := res.DocIds[id]; !ok {
			t.Errorf("docid %d was not indexed/retrieved", id)
		}
	}
	// No stored value may have a non-multiple-of-docIDSize length (would corrupt decode).
	_ = env.DB.Scan([]byte{env.idx.keyTypeRow}, func(k, v []byte) bool {
		if len(v)%docIDSize != 0 {
			t.Errorf("corrupt value length %d for key %q", len(v), k)
		}
		return true
	})
}

// B6 — a garbage row that decodes to InvalidId must be skipped by the merger, not
// folded into a real keyword's posting list.
func TestMerge_SkipsInvalidIdGarbageRow(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()
	for r := 0; r < 3; r++ {
		env.idx.Update(1, makeDocID(fmt.Sprintf("d%d", r)), []string{"kw"}, nil)
		forceFlush(env.idx)
	}
	// Garbage key with a non-numeric tableId ("\x01") -> decodes to InvalidId, and
	// its \x01 second byte sorts it just before the real "\x141|kw|..." rows.
	garbage := append([]byte{env.idx.keyTypeRow}, []byte("\x01|kw|3|999")...)
	_ = env.DB.Put(garbage, encodeInvertedValue([]int64{makeDocID("evil")}))

	m := merging{NextIter: string(env.idx.keyTypeRow)}
	for i := 0; i < 5; i++ {
		m = env.idx.mergeKeywordsIndex(m, MaxInvertedIndexSize)
	}

	if _, ok := env.idx.GetDocs(1, "kw").DocIds[makeDocID("evil")]; ok {
		t.Error(`garbage InvalidId row contaminated real keyword "kw"`)
	}
	// Real docs intact.
	got := env.idx.GetDocs(1, "kw")
	for r := 0; r < 3; r++ {
		if _, ok := got.DocIds[makeDocID(fmt.Sprintf("d%d", r))]; !ok {
			t.Errorf("real keyword lost doc d%d", r)
		}
	}
}

// B7 — empty-keyword rows are a legitimate, reachable case and must be compacted
// by the merger (the old tail guard `current.Keyword != ""` dropped them).
func TestMerge_CompactsEmptyKeyword(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()
	// "" is below docIDSize? No: keyword may be empty; docids are 8 bytes. Index
	// several rows under the empty keyword across flushes.
	for r := 0; r < 6; r++ {
		env.idx.Update(1, makeDocID(fmt.Sprintf("e%d", r)), []string{""}, nil)
		forceFlush(env.idx)
	}
	rowsBefore := countRows(env, "")
	m := merging{NextIter: string(env.idx.keyTypeRow)}
	for i := 0; i < 5; i++ {
		m = env.idx.mergeKeywordsIndex(m, MaxInvertedIndexSize)
	}
	rowsAfter := countRows(env, "")
	if rowsAfter >= rowsBefore {
		t.Errorf("empty-keyword rows not compacted: before=%d after=%d", rowsBefore, rowsAfter)
	}
	if len(env.idx.GetDocs(1, "").DocIds) != 6 {
		t.Errorf("empty-keyword docs lost during compaction: got %d, want 6", len(env.idx.GetDocs(1, "").DocIds))
	}
}

// B9 — after a delete repacks survivors, the rewritten row must carry the TRUE
// doccount, not a stale inflated one, or the merger quarantines it forever.
func TestRemoveDocuments_RewritesTrueDoccount(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()
	// One row with a large doccount, then delete all but two docs.
	for i := 0; i < 10; i++ {
		env.idx.Update(1, makeDocID(fmt.Sprintf("d%02d", i)), []string{"kw"}, nil)
	}
	forceFlush(env.idx)
	for i := 0; i < 8; i++ {
		env.idx.Update(1, makeDocID(fmt.Sprintf("d%02d", i)), nil, []string{"kw"})
	}
	forceFlush(env.idx)

	// Every surviving "kw" row's key doccount must equal its actual docid count.
	_ = env.DB.Scan(env.idx.encodeInvertedKeyPrefix(1, "kw"), func(k, v []byte) bool {
		_, kw, doccount, _ := env.idx.decodeInvertedKey(string(k))
		if kw != "kw" {
			return true
		}
		actual := len(decodeInvertedValue(v))
		if doccount != actual {
			t.Errorf("doccount drift: key says %d, value has %d docids", doccount, actual)
		}
		return true
	})
}

// countRows counts stored rows whose decoded keyword == kw in table 1.
func countRows(env *testEnv, kw string) int {
	n := 0
	_ = env.DB.Scan([]byte{env.idx.keyTypeRow}, func(k, _ []byte) bool {
		if tid, gotKw, _, _ := env.idx.decodeInvertedKey(string(k)); tid == 1 && gotKw == kw {
			n++
		}
		return true
	})
	return n
}
