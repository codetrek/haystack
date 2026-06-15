package documents

import (
	"time"

	"github.com/codetrek/haystack/core/kv"
)

// saveDocument persists a document's metadata, words, and path to the batch.
func (s *Store) saveDocument(batch kv.Batch, collectionID int, doc *Document) {
	doc.LastSyncTime = time.Now().UnixNano()
	meta, err := encodeDocumentMetaValue(doc)
	if err != nil {
		return
	}

	// Save the document meta and words
	batch.Put(s.encodeDocumentMetaKey(collectionID, doc.ID), meta)
	batch.Put(s.encodeDocumentWordsKey(collectionID, doc.ID), encodeDocumentWordsValue(doc.Words))
	batch.Put(s.encodeDocumentPathKey(collectionID, doc.ID), []byte(doc.RelPath))
}
