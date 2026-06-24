package invertedindex

import (
	"fmt"
	"testing"
)

// These tests pin the root cause of the "orphan posting" bug: inverted-index row
// keys are `<keyTypeRow><tableId>|<keyword>|<doccount>|<tick>`, and a keyword
// that itself contains the `|` field delimiter broke decodeInvertedKey, which in
// turn corrupted the background merger — it regrouped every undecodable key under
// an empty keyword / tableId -1 and rewrote the data under a garbage key, so the
// original keyword's postings were lost and could never be matched or deleted.

// TestDecodeInvertedKey_KeywordWithDelimiter is the unit-level reproduction:
// encode→decode must round-trip the keyword verbatim, even when it contains the
// `|` delimiter or other in-band bytes such as 0xff.
func TestDecodeInvertedKey_KeywordWithDelimiter(t *testing.T) {
	idx := &Index{
		keyTypeRow:    DefaultKeyTypeRow,
		keyTypeTable:  DefaultKeyTypeTable,
		keyTypeNextId: DefaultKeyTypeNextId,
	}
	cases := []string{
		"hello",     // control
		"a|b",       // single embedded delimiter
		"p|q|r",     // multiple delimiters
		"|",         // delimiter only
		"|lead",     // leading delimiter
		"trail|",    // trailing delimiter
		"x\xffy",    // 0xff in keyword
		"a|b\xff|c", // delimiter + 0xff together
	}
	for _, kw := range cases {
		key := idx.encodeInvertedKey(7, kw, 3)
		tid, gotKw, dc, tick := idx.decodeInvertedKey(string(key))
		if tid != 7 || gotKw != kw || dc != 3 || tick == "" {
			t.Errorf("decode(encode(kw=%q)) = (tableId=%d keyword=%q doccount=%d tick=%q); want (7, %q, 3, non-empty)",
				kw, tid, gotKw, dc, tick, kw)
		}
	}
}

// TestMerge_KeywordWithDelimiter_NoOrphan is the integration reproduction: after
// the background merger compacts keywords that contain `|` (and one that contains
// 0xff), the data must still be retrievable under each real keyword and the store
// must hold no undecodable (orphan) rows.
func TestMerge_KeywordWithDelimiter_NoOrphan(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	const tableId = 1
	keywords := []string{"a|b", "c|d", "x\xffy", "plain"}

	// Index several distinct docs per keyword across separate flushes (each flush
	// writes a distinct tick'd row) so the merger's rewriteIndex path
	// (len(Rows) >= 2) actually fires for every keyword.
	docsByKw := map[string][]int64{}
	for round := 0; round < 3; round++ {
		for ki, kw := range keywords {
			doc := makeDocID(fmt.Sprintf("d%d-%d", ki, round))
			env.idx.Add(tableId, doc, []string{kw})
			docsByKw[kw] = append(docsByKw[kw], doc)
		}
		forceFlush(env.idx)
	}

	// Run synchronous merge passes over the whole row keyspace.
	m := merging{NextIter: string(env.idx.keyTypeRow)}
	for i := 0; i < 5; i++ {
		m = env.idx.mergeKeywordsIndex(m, MaxInvertedIndexSize)
	}

	// 1. Every keyword's docs must still be retrievable under its REAL keyword.
	for _, kw := range keywords {
		res := env.idx.GetDocs(tableId, kw)
		for _, doc := range docsByKw[kw] {
			if _, ok := res.DocIds[doc]; !ok {
				t.Errorf("after merge, GetDocs(%q) missing doc %q (orphaned/lost)", kw, doc)
			}
		}
	}

	// 2. No row in the store may be undecodable or carry a bogus tableId — those
	//    are the orphan rows that can never be matched or deleted.
	_ = env.DB.Scan([]byte{env.idx.keyTypeRow}, func(key, _ []byte) bool {
		tid, kw, _, _ := env.idx.decodeInvertedKey(string(key))
		if tid != tableId {
			t.Errorf("orphan row: key %q decodes to tableId=%d keyword=%q (want tableId=%d)", key, tid, kw, tableId)
		}
		return true
	})
}

// TestSearch_KeywordWithFF_Found is the end-to-end guard for the 0xff bug: a
// keyword containing 0xff right after the search prefix must still be returned by
// Search. (The prefix-scan upper bound used to exclude prefix+0xff+... keys.)
func TestSearch_KeywordWithFF_Found(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	const tableId = 1
	doc := makeDocID("ffdoc")
	// Keyword "x\xffy": the search prefix "x" is immediately followed by 0xff.
	env.idx.Add(tableId, doc, []string{"x\xffy"})
	forceFlush(env.idx)

	res := env.idx.Search(tableId, "x", 0, nil)
	if _, ok := res.DocIds[doc]; !ok {
		t.Errorf("Search(%q) did not return the doc indexed under keyword \"x\\xffy\" (0xff key dropped by prefix scan)", "x")
	}
}
