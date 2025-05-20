package invertedindex

import (
	"log"
	"time"

	"github.com/ai-microsoft/haystack/server/core/pebble"
)

// updateInvertedIndexCached updates the keyword index in write cached
// It will add the document to the keyword index cache to merge with other documents and flush later
func updateIndex(tableId int, docid string, keywords []string) {
	cache := getPendingWrite(tableId)
	for _, kw := range keywords {
		// Add to write cache to merge with other documents and flush later
		cache.InvertedIndex[kw] = RelatedDocs{
			DocIds:    append(cache.InvertedIndex[kw].DocIds, docid),
			UpdatedAt: time.Now(),
		}
	}
}

func removeIndex(tableId int, docid string, keywords []string) {
	w := getPendingDelete(tableId)
	for _, kw := range keywords {
		// Add to delete cache to merge with other documents and flush later
		w.InvertedIndex[kw] = RelatedDocs{
			DocIds:    append(w.InvertedIndex[kw].DocIds, docid),
			UpdatedAt: time.Now(),
		}
	}
}

// writeInvertedIndex writes a keyword to the database
var writeInvertedIndex = func(batch pebble.Batch, tableId int, kw string, docids []string, key []byte) {
	content := encodeInvertedValue(docids)
	if len(key) == 0 {
		key = encodeInvertedKey(tableId, kw, len(docids))
	}
	batch.Put(key, content)
}

// removeDocumentsFromInvertedIndex removes a document from the keywords index
// It will remove the document from the keywords index and rewrite the keyword with new docids
func removeDocumentsFromInvertedIndex(batch pebble.Batch, tableId int, kw string, removingDocids []string,
	maxKeywordIndexSize int) {
	if len(kw) == 0 {
		log.Println("[Inverted] Warning: Removing document from keywords index, but keyword is empty")
		return
	}

	removings := map[string]struct{}{}
	for _, id := range removingDocids {
		if id != "" {
			removings[id] = struct{}{}
		}
	}

	if len(removings) == 0 {
		log.Println("[Inverted] Warning: Removing document from keywords index, but docid is empty")
		return
	}

	keys := []string{}
	docids := map[string]struct{}{}
	db.Scan(encodeInvertedKeyPrefix(tableId, kw), func(key, value []byte) bool {
		changed := false
		tmpids := []string{}

		ids := decodeInvertedValue(value)
		for _, id := range ids {
			if _, ok := removings[id]; ok {
				// remove the document from the keyword index
				changed = true
				continue
			}
			if id != "" {
				tmpids = append(tmpids, id)
			}
		}

		if changed || len(tmpids) < maxKeywordIndexSize/2 {
			keys = append(keys, string(key))
			for _, id := range tmpids {
				docids[id] = struct{}{}
			}
		}
		return true
	})

	count := 0
	for len(docids) > 0 {
		docs := []string{}
		for id := range docids {
			if len(docs) >= maxKeywordIndexSize {
				break
			}
			docs = append(docs, id)
			delete(docids, id)
		}

		var key string
		if len(keys) > 0 {
			key = keys[0]
			keys = keys[1:]
		}

		writeInvertedIndex(batch, tableId, kw, docs, []byte(key))
		count++
	}

	for _, key := range keys {
		batch.Delete([]byte(key))
	}
}
