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

		// Iterate the packed 8-byte docids straight into the result set, decoding
		// each big-endian int64 in place — no per-docid string allocation.
		if len(value)%docIDSize == 0 {
			for i := 0; i < len(value); i += docIDSize {
				results.DocIds[int64(binary.BigEndian.Uint64(value[i:i+docIDSize]))] = struct{}{}
			}
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
		if len(value)%docIDSize == 0 {
			for i := 0; i < len(value); i += docIDSize {
				results.DocIds[int64(binary.BigEndian.Uint64(value[i:i+docIDSize]))] = struct{}{}
			}
		}
		return true
	})

	if err != nil {
		log.Printf("[Inverted] Error getting docs for key %s: %v", key, err)
	}
	return results
}

// Update updates the keywords index for a document.
// If len(newKeywords) == 0, it will remove the document from the keywords index.
// This function MUST be called in dbMPSCQueue.
func (idx *Index) Update(tableId int, docid int64, newKeywords, oldKeywords []string) {
	// Handle the case of complete deletion
	if len(newKeywords) == 0 {
		if len(oldKeywords) > 0 {
			idx.removeIndex(tableId, docid, oldKeywords)
		}
		return
	}

	// Handle the case of complete addition
	if len(oldKeywords) == 0 {
		idx.updateIndex(tableId, docid, newKeywords)
		return
	}

	// Convert the updated document words to a map for faster lookup
	newMap := map[string]struct{}{}
	for _, kw := range newKeywords {
		if kw != "" {
			newMap[kw] = struct{}{}
		}
	}

	// Convert the current document words to a map for faster lookup
	oldMap := map[string]struct{}{}
	for _, kw := range oldKeywords {
		if kw != "" {
			oldMap[kw] = struct{}{}
		}
	}

	removedWords := make([]string, 0, len(oldKeywords))
	newWords := make([]string, 0, len(newKeywords))

	// Find the words that are added to the current document
	for _, kw := range newKeywords {
		if _, ok := oldMap[kw]; !ok {
			newWords = append(newWords, kw)
		}
	}

	// Find the words that are removed from the current document
	for _, kw := range oldKeywords {
		if _, ok := newMap[kw]; !ok {
			removedWords = append(removedWords, kw)
		}
	}

	idx.removeIndex(tableId, docid, removedWords)

	// Add new words to the keywords index
	if len(newWords) > 0 {
		idx.updateIndex(tableId, docid, newWords)
	}
}
