package invertedindex

import (
	"log"
	"strings"
)

const MaxInvertedIndexSize = 1000

type SearchResult struct {
	DocIds     map[string]struct{} `json:"docIds"`
	WildDocIds map[string]struct{} `json:"wildDocIds,omitempty"`
}

func (idx *Index) Search(tableId int, query string, limit int, filterKeyword func(string) bool) SearchResult {
	results := SearchResult{
		DocIds: make(map[string]struct{}),
	}

	err := idx.db.Scan(idx.encodeInvertedSearchKey(tableId, strings.ToLower(query)), func(key, value []byte) bool {
		if filterKeyword != nil && !filterKeyword(string(key)) {
			return true
		}

		docids := decodeInvertedValue(value)
		if len(docids) > 0 {
			for _, docid := range docids {
				results.DocIds[docid] = struct{}{}
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

func (idx *Index) GetDocs(tableId int, key string) SearchResult {
	results := SearchResult{
		DocIds: make(map[string]struct{}),
	}

	err := idx.db.Scan(idx.encodeInvertedKeyPrefix(tableId, key), func(key, value []byte) bool {
		for _, docid := range decodeInvertedValue(value) {
			results.DocIds[docid] = struct{}{}
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
func (idx *Index) Update(tableId int, docid string, newKeywords, oldKeywords []string) {
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
