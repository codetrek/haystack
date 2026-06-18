package invertedindex

import (
	"log"
	"time"

	"github.com/codetrek/haystack/core/kv"
)

// updateIndex updates the keyword index in write cache.
// It will add the document to the keyword index cache to merge with other
// documents and flush later.
func (idx *Index) updateIndex(tableId int, docid string, keywords []string) {
	// Row values are fixed docIDSize-byte docids concatenated with no delimiter,
	// so a docid of any other length (including the empty string) corrupts the
	// value chunking on decode — fabricating/dropping docids that can never be
	// searched or deleted. Reject at ingress, symmetric with the delete path.
	if len(docid) != docIDSize {
		log.Printf("[Inverted] Warning: ignoring add for table %d: docid must be %d bytes, got %d", tableId, docIDSize, len(docid))
		return
	}
	cache := idx.getPendingWrite(tableId)
	now := time.Now() // one timestamp for the whole update (per-keyword time.Now is hot)
	for _, kw := range keywords {
		// Add to write cache to merge with other documents and flush later. docid may be
		// duplicated; however for performance we don't check for duplicates here — all
		// duplicates will be merged later in the background.
		cache.InvertedIndex[kw] = relatedDocs{
			DocIds:    append(cache.InvertedIndex[kw].DocIds, docid),
			UpdatedAt: now,
		}
	}
}

func (idx *Index) removeIndex(tableId int, docid string, keywords []string) {
	if len(docid) != docIDSize {
		log.Printf("[Inverted] Warning: ignoring delete for table %d: docid must be %d bytes, got %d", tableId, docIDSize, len(docid))
		return
	}
	w := idx.getPendingDelete(tableId)
	now := time.Now()
	for _, kw := range keywords {
		// Add to delete cache to merge with other documents and flush later.
		w.InvertedIndex[kw] = relatedDocs{
			DocIds:    append(w.InvertedIndex[kw].DocIds, docid),
			UpdatedAt: now,
		}
	}
}

// writeInvertedIndex writes a keyword to the database.
// Callers must pass the pre-computed key via idx.encodeInvertedKey so that the
// configured key-type bytes are honoured.
var writeInvertedIndex = func(batch kv.Batch, tableId int, kw string, docids []string, key []byte) {
	// Remove duplicates to ensure the data stored is clean
	uniqueDocids := removeDuplicatesEfficiently(docids)
	content := encodeInvertedValue(uniqueDocids)
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
		// The prefix "<tid>|<kw>|" also matches rows of any keyword "kw|..." (the
		// keyword may contain '|'), so skip rows whose decoded keyword is not
		// exactly kw — otherwise deleting from "a" would rewrite/destroy "a|x".
		if _, k, _, _ := idx.decodeInvertedKey(string(key)); k != kw {
			return true
		}
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

	for len(docids) > 0 {
		docs := []string{}
		for id := range docids {
			if len(docs) >= maxKeywordIndexSize {
				break
			}
			docs = append(docs, id)
			delete(docids, id)
		}

		// Always re-encode under a fresh key carrying the TRUE doccount. Reusing
		// an original key (keys[0]) would keep its stale, inflated doccount, which
		// the merger's `doccount > maxSize/2` guard then quarantines from
		// compaction forever. encodeInvertedKey's seq suffix keeps the new key
		// distinct from the originals, all of which are deleted below.
		key := idx.encodeInvertedKey(tableId, kw, len(docs))

		writeInvertedIndex(batch, tableId, kw, docs, key)
	}

	// Delete every original row we collected; their surviving docids were
	// rewritten under fresh, correctly-counted keys above.
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
