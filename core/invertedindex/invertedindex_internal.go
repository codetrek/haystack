package invertedindex

import (
	"log"
	"time"

	"github.com/codetrek/haystack/core/kv"
)

// updateIndex updates the keyword index in write cache.
// It will add the document to the keyword index cache to merge with other
// documents and flush later.
func (idx *Index) updateIndex(tableId int, docid int64, keywords []string) {
	// docids are fixed-width 8-byte big-endian int64s on disk; an int64 is always
	// exactly that width by construction, so the value codec can never be
	// corrupted by a malformed docid (the previous string-based ingress guard is
	// no longer representable and has been removed).
	cache := idx.getPendingWrite(tableId)
	now := time.Now().UnixNano() // one timestamp for the whole update (per-keyword time.Now is hot)
	for _, kw := range keywords {
		// Add to write cache to merge with other documents and flush later. docid may be
		// duplicated; however for performance we don't check for duplicates here — all
		// duplicates will be merged later in the background.
		cache.InvertedIndex[kw] = relatedDocs{
			DocIds:    append(cache.InvertedIndex[kw].DocIds, docid),
			UpdatedAt: now,
		}
	}
	idx.pendingWritePostings += len(keywords)
	idx.maybeFlushOnPressure()
}

func (idx *Index) removeIndex(tableId int, docid int64, keywords []string) {
	w := idx.getPendingDelete(tableId)
	now := time.Now().UnixNano()
	for _, kw := range keywords {
		// Add to delete cache to merge with other documents and flush later.
		w.InvertedIndex[kw] = relatedDocs{
			DocIds:    append(w.InvertedIndex[kw].DocIds, docid),
			UpdatedAt: now,
		}
	}
	idx.pendingDeletePostings += len(keywords)
	idx.maybeFlushOnPressure()
}

// writeInvertedIndex writes a keyword to the database.
// Callers must pass the pre-computed key via idx.encodeInvertedKey so that the
// configured key-type bytes are honoured.
var writeInvertedIndex = func(batch kv.Batch, tableId int, kw string, docids []int64, key []byte) {
	// encodeInvertedValue sorts AND dedups, so the raw docids (which the flush
	// path intentionally leaves with duplicates) can be handed straight to it.
	content := encodeInvertedValue(docids)
	batch.Put(key, content)
}

// removeDocumentsFromInvertedIndex removes a document from the keywords index.
// It will remove the document from the keywords index and rewrite the keyword with new docids.
func (idx *Index) removeDocumentsFromInvertedIndex(batch kv.Batch, tableId int, kw string, removingDocids []int64,
	maxKeywordIndexSize int) error {
	if len(kw) == 0 {
		log.Println("[Inverted] Warning: Removing document from keywords index, but keyword is empty")
		return nil
	}

	removings := map[int64]struct{}{}
	for _, id := range removingDocids {
		removings[id] = struct{}{}
	}

	if len(removings) == 0 {
		log.Println("[Inverted] Warning: Removing document from keywords index, but docid is empty")
		return nil
	}

	keys := []string{}
	docids := map[int64]struct{}{}
	err := idx.db.Scan(idx.encodeInvertedKeyPrefix(tableId, kw), func(key, value []byte) bool {
		// The prefix "<tid>|<kw>|" also matches rows of any keyword "kw|..." (the
		// keyword may contain '|'), so skip rows whose decoded keyword is not
		// exactly kw — otherwise deleting from "a" would rewrite/destroy "a|x".
		if _, k, _, _ := idx.decodeInvertedKey(string(key)); k != kw {
			return true
		}
		changed := false
		tmpids := []int64{}

		ids := decodeInvertedValue(value)
		for _, id := range ids {
			if _, ok := removings[id]; ok {
				// remove the document from the keyword index
				changed = true
				continue
			}
			tmpids = append(tmpids, id)
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

	// Sample the opaque key tick ONCE for every rewritten row of this keyword
	// (uniqueness is guaranteed by keySeq, not the tick), instead of reading the
	// clock per row.
	tick := time.Now().UnixMicro()
	for len(docids) > 0 {
		docs := []int64{}
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
		key := idx.encodeInvertedKey(tableId, kw, len(docs), tick)

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
func removeDuplicatesEfficiently(docids []int64) []int64 {
	if len(docids) <= 1 {
		return docids
	}

	seen := make(map[int64]bool, len(docids))
	result := make([]int64, 0, len(docids))

	for _, docid := range docids {
		if !seen[docid] {
			seen[docid] = true
			result = append(result, docid)
		}
	}
	return result
}
