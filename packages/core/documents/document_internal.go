package documents

import (
	"time"

	"github.com/codetrek/haystack/packages/core/kv"
)

// saveDocument persists a document's metadata and path to the batch.
func (s *Store) saveDocument(batch kv.Batch, collectionID int, doc *Document) {
	doc.LastSyncTime = time.Now().UnixNano()
	meta, err := encodeDocumentMetaValue(doc)
	if err != nil {
		return
	}

	// Save the document meta and path. Keywords are owned by the inverted index's
	// forward map, not persisted here.
	batch.Put(s.encodeDocumentMetaKey(collectionID, doc.ID), meta)
	batch.Put(s.encodeDocumentPathKey(collectionID, doc.ID), []byte(doc.RelPath))
}
