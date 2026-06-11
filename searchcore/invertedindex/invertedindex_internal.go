package invertedindex

import (
	"log"
	"time"

	"github.com/codetrek/haystack/searchcore/kv"
)

// updateIndex updates the keyword index in write cache.
// It will add the document to the keyword index cache to merge with other
// documents and flush later.
func (idx *Index) updateIndex(tableId int, docid string, keywords []string) {
	cache := idx.getPendingWrite(tableId)
	for _, kw := range keywords {
		// Add to write cache to merge with other documents and flush later. docid may be
		// duplicated; however for performance we don't check for duplicates here — all
		// duplicates will be merged later in the background.
		cache.InvertedIndex[kw] = RelatedDocs{
			DocIds:    append(cache.InvertedIndex[kw].DocIds, docid),
			UpdatedAt: time.Now(),
		}
	}
}

func (idx *Index) removeIndex(tableId int, docid string, keywords []string) {
	w := idx.getPendingDelete(tableId)
	for _, kw := range keywords {
		// Add to delete cache to merge with other documents and flush later.
		w.InvertedIndex[kw] = RelatedDocs{
			DocIds:    append(w.InvertedIndex[kw].DocIds, docid),
			UpdatedAt: time.Now(),
		}
	}
}

// writeInvertedIndex writes a keyword to the database.
// When key is nil the key is encoded using the default key-type byte
// (DefaultKeyTypeRow = 20).  Callers with non-default key-type bytes must
// pre-compute the key via idx.encodeInvertedKey and pass it explicitly.
var writeInvertedIndex = func(batch kv.Batch, tableId int, kw string, docids []string, key []byte) {
	// Remove duplicates to ensure the data stored is clean
	uniqueDocids := removeDuplicatesEfficiently(docids)
	content := encodeInvertedValue(uniqueDocids)
	if len(key) == 0 {
		key = encodeInvertedKey(tableId, kw, len(docids))
	}
	batch.Put(key, content)
}

// removeDocumentsFromInvertedIndex removes a document from the keywords index.
// It will remove the document from the keywords index and rewrite the keyword with new docids.
func (idx *Index) removeDocumentsFromInvertedIndex(batch kv.Batch, tableId int, kw string, removingDocids []string,
	maxKeywordIndexSize int) error {
	if len(kw) == 0 {
		log.Println("[Inverted] Warning: Removing document from keywords index, but keyword is empty")
		return nil
	}

	removings := map[string]struct{}{}
	for _, id := range removingDocids {
		if id != "" {
			removings[id] = struct{}{}
		}
	}

	if len(removings) == 0 {
		log.Println("[Inverted] Warning: Removing document from keywords index, but docid is empty")
		return nil
	}

	keys := []string{}
	docids := map[string]struct{}{}
	err := idx.db.Scan(idx.encodeInvertedKeyPrefix(tableId, kw), func(key, value []byte) bool {
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

	if err != nil {
		return err
	}

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

	return nil
}

// removeDuplicatesEfficiently removes duplicates from the docids slice.
// It returns a new slice with duplicates removed, preserving first-occurrence order.
func removeDuplicatesEfficiently(docids []string) []string {
	if len(docids) <= 1 {
		return docids
	}

	seen := make(map[string]bool, len(docids))
	result := make([]string, 0, len(docids))

	for _, docid := range docids {
		if !seen[docid] {
			seen[docid] = true
			result = append(result, docid)
		}
	}
	return result
}
