// Package invertedindex implements a low-level posting-list engine that maps
// search terms to document identifiers, with batched async writes and a
// background keyword-merging compactor.
package invertedindex

import (
	"encoding/binary"
	"log"
	"strings"
)

const MaxInvertedIndexSize = 1000

// RecommendedMaxPendingPostings is a measured-good value for
// Options.MaxPendingPostings, which is OFF by default. It caps the in-memory
// caches at 2,000,000 buffered posting entries — (keyword, docid) pairs, not
// documents; at typical code keyword density (~440 unique terms/file) that is a
// few thousand files in flight. On a 41M-posting corpus, setting the bound to
// this value cut build-phase peak RSS ~69% (1.7GiB -> ~0.5GiB) for ~12% more
// build time — the knee of the RSS/cost curve; lower values barely reduce RSS
// further while raising build cost.
const RecommendedMaxPendingPostings = 2_000_000

// SearchResult holds the set of document ids matched by a query. DocIds are
// exact keyword matches; WildDocIds (populated by wildcard-aware callers) are
// matches that came from wildcard expansion and may be filtered further by the
// caller. Both are sets keyed by document id.
type SearchResult struct {
	DocIds     map[int64]struct{} `json:"docIds"`
	WildDocIds map[int64]struct{} `json:"wildDocIds,omitempty"`
}

// Search returns the union of document ids whose keywords match query within
// the given table. The query is lower-cased and matched as a keyword prefix.
// If filterKeyword is non-nil it is called with each candidate key and must
// return true for the key's documents to be included. A positive limit caps
// the number of distinct document ids collected; limit <= 0 means unlimited.
func (idx *Index) Search(tableId int, query string, limit int, filterKeyword func(string) bool) SearchResult {
	results := SearchResult{
		DocIds: make(map[int64]struct{}),
	}

	err := idx.db.Scan(idx.encodeInvertedSearchKey(tableId, strings.ToLower(query)), func(key, value []byte) bool {
		if filterKeyword != nil && !filterKeyword(string(key)) {
			return true
		}

		// Accumulate the value's delta-varint docids straight into the result set,
		// reconstructing each id from the running gap — no per-docid allocation.
		var cur uint64
		for i := 0; i < len(value); {
			delta, n := binary.Uvarint(value[i:])
			if n <= 0 {
				break
			}
			cur += delta
			results.DocIds[int64(cur)] = struct{}{}
			i += n
		}

		if limit > 0 && len(results.DocIds) >= limit {
			return false
		}

		return true
	})

	if err != nil {
		log.Printf("[Inverted] Error searching for %s: %v", query, err)
	}
	return results
}

// GetDocs returns the union of document ids stored under the exact keyword key
// within the given table. Unlike Search, the key is matched verbatim (no
// lower-casing) as an inverted-key prefix, with no limit or filtering.
func (idx *Index) GetDocs(tableId int, key string) SearchResult {
	results := SearchResult{
		DocIds: make(map[int64]struct{}),
	}

	want := key
	err := idx.db.Scan(idx.encodeInvertedKeyPrefix(tableId, key), func(k, value []byte) bool {
		// The prefix "<tid>|<key>|" is ALSO a byte-prefix of rows for any keyword
		// "key|..." (keywords may contain the '|' delimiter), so re-verify the
		// decoded keyword is exactly the requested one before counting its docids
		// — otherwise GetDocs("a") leaks the postings of keyword "a|x".
		if _, kw, _, _ := idx.decodeInvertedKey(string(k)); kw != want {
			return true
		}
		var cur uint64
		for i := 0; i < len(value); {
			delta, n := binary.Uvarint(value[i:])
			if n <= 0 {
				break
			}
			cur += delta
			results.DocIds[int64(cur)] = struct{}{}
			i += n
		}
		return true
	})

	if err != nil {
		log.Printf("[Inverted] Error getting docs for key %s: %v", key, err)
	}
	return results
}

// readForward returns the document's stored keyword set from the forward map and
// whether the document is known to the index. A nil db value (missing key) means
// the document is unknown (ok=false); a non-nil value — including an empty one —
// means it is known (ok=true) and decodes to its keyword set (a zero-length slice
// for an empty value). This unknown / known-empty distinction mirrors db.Get's
// nil vs non-nil-empty contract and drives Update's add-vs-diff branch.
func (idx *Index) readForward(tableId int, docid int64) ([]string, bool) {
	v, err := idx.db.Get(idx.encodeForwardKey(tableId, docid))
	if err != nil {
		log.Printf("[Inverted] Error reading forward map (table=%d): %v", tableId, err)
		return nil, false
	}
	if v == nil {
		return nil, false
	}
	return decodeForwardValue(v), true
}

// Add indexes a brand-new document's keywords and records them in the forward
// map. It performs NO read and removes nothing, so it must only be used for
// documents the index has not seen. An empty keyword set is a no-op. This
// function MUST be called in dbMPSCQueue.
func (idx *Index) Add(tableId int, docid int64, keywords []string) {
	if len(keywords) == 0 {
		return
	}
	idx.updateIndex(tableId, docid, keywords)
	if err := idx.db.Put(idx.encodeForwardKey(tableId, docid), encodeForwardValue(keywords)); err != nil {
		log.Printf("[Inverted] Error writing forward map (table=%d): %v", tableId, err)
	}
}

// Update re-indexes a document to the given keyword set, diffing against the set
// the index previously stored in its forward map (so the caller no longer passes
// the old set). An empty keyword set deletes the document; an unknown document is
// added. This function MUST be called in dbMPSCQueue.
func (idx *Index) Update(tableId int, docid int64, keywords []string) {
	if len(keywords) == 0 {
		idx.Delete(tableId, docid)
		return
	}

	oldKeywords, known := idx.readForward(tableId, docid)
	if !known {
		idx.Add(tableId, docid, keywords)
		return
	}

	// Diff old vs new. Empty strings are excluded only when building the
	// comparison maps (exactly as the previous caller-supplied-old Update did);
	// the add/remove sets are still taken from the raw slices, so the result is
	// byte-for-byte identical to the pre-forward-map behavior.
	newMap := map[string]struct{}{}
	for _, kw := range keywords {
		if kw != "" {
			newMap[kw] = struct{}{}
		}
	}
	oldMap := map[string]struct{}{}
	for _, kw := range oldKeywords {
		if kw != "" {
			oldMap[kw] = struct{}{}
		}
	}

	removedWords := make([]string, 0, len(oldKeywords))
	newWords := make([]string, 0, len(keywords))
	for _, kw := range keywords {
		if _, ok := oldMap[kw]; !ok {
			newWords = append(newWords, kw)
		}
	}
	for _, kw := range oldKeywords {
		if _, ok := newMap[kw]; !ok {
			removedWords = append(removedWords, kw)
		}
	}

	idx.removeIndex(tableId, docid, removedWords)
	if len(newWords) > 0 {
		idx.updateIndex(tableId, docid, newWords)
	}

	if err := idx.db.Put(idx.encodeForwardKey(tableId, docid), encodeForwardValue(keywords)); err != nil {
		log.Printf("[Inverted] Error writing forward map (table=%d): %v", tableId, err)
	}
}

// Delete removes a document from every posting of its stored keyword set and
// drops its forward-map entry. A document the index never saw is a no-op. This
// function MUST be called in dbMPSCQueue.
func (idx *Index) Delete(tableId int, docid int64) {
	oldKeywords, known := idx.readForward(tableId, docid)
	if known && len(oldKeywords) > 0 {
		idx.removeIndex(tableId, docid, oldKeywords)
	}
	if err := idx.db.Delete(idx.encodeForwardKey(tableId, docid)); err != nil {
		log.Printf("[Inverted] Error deleting forward map (table=%d): %v", tableId, err)
	}
}
