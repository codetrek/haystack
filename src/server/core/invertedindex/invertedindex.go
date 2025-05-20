package invertedindex

const MaxInvertedIndexSize = 1000

type SearchResult struct {
	DocIds map[string]struct{} `json:"docIds"`
}

func Search(tableId int, query string, limit int) SearchResult {
	results := SearchResult{
		DocIds: make(map[string]struct{}),
	}

	db.Scan(encodeInvertedSearchKey(tableId, query), func(key, value []byte) bool {
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
	return results
}

// Update updates the keywords index for a document
// It will add the document to the keywords index and remove the document from the keywords index
// if len(newKeywords) == 0, it will remove the document from the keywords index
// This function MUSTE be called in dbMPSCQueue
func Update(tableId int, docid string, newKeywords, oldKeywords []string) {
	if len(oldKeywords) == 0 {
		updateIndex(tableId, docid, newKeywords)
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

	removedWords := []string{}
	newWords := []string{}

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

	removeIndex(tableId, docid, removedWords)

	// Add new words to the keywords index
	if len(newWords) > 0 {
		updateIndex(tableId, docid, newWords)
	}
}
