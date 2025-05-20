package fulltext

import (
	"time"

	"github.com/ai-microsoft/haystack/server/core/pebble"
)

// saveDocument saves a document to the database
func saveDocument(batch pebble.Batch, workspaceid int, doc *Document) {
	doc.LastSyncTime = time.Now().UnixNano()
	meta, err := EncodeDocumentMetaValue(doc)
	if err != nil {
		return
	}

	// Save the document meta and words
	batch.Put(EncodeDocumentMetaKey(workspaceid, doc.ID), meta)
	batch.Put(EncodeDocumentWordsKey(workspaceid, doc.ID), EncodeDocumentWordsValue(doc.Words))
	batch.Put(EncodeDocumentPathKey(workspaceid, doc.ID), []byte(doc.RelPath))
}
