package documents

import (
	"errors"
	"fmt"
	"log"

	"github.com/codetrek/haystack/core/idtable"
)

// errSkip is an internal sentinel returned by a worker task that bailed for a
// benign reason (e.g. the db is closed) and whose CALLER must report success
// (return nil) — the historical contract of SaveNewDocuments/DeleteDocument on a
// closed db. It is never surfaced to callers (ignoreSkip strips it); it only lets
// the post-task index notification be skipped without conflating "skip" with a
// real error.
var errSkip = errors.New("documents: skip (benign)")

// ignoreSkip maps the errSkip sentinel back to nil (benign skip), passing any
// other error through unchanged.
func ignoreSkip(err error) error {
	if errors.Is(err, errSkip) {
		return nil
	}
	return err
}

// indexDocuments notifies the inverted index of a batch of doc mutations in ONE
// Indexer batch (so N docs collapse into a single enqueued apply). Each doc's
// CURRENT full keyword set is sent (empty/nil ⇒ delete). It MUST be called
// OUTSIDE any s.q worker task: a Batch.Commit enqueues onto the shared queue, so
// calling it from within the worker would deadlock once the channel buffer fills.
func (s *Store) indexDocuments(tableId int, docs []*Document) {
	if s.idx == nil || len(docs) == 0 {
		return
	}
	b := s.idx.NewBatch()
	for _, doc := range docs {
		// The inverted index keys postings by the docid's int64 value; doc.ID here
		// is its canonical 8-byte string form (idtable.GetId), so decode it at this
		// boundary. doc.Words is the doc's CURRENT keyword set; the index diffs it
		// against its own forward map (no oldWords).
		b.Update(tableId, idtable.DecodeId(doc.ID), doc.Words)
	}
	b.Commit()
}

// Document represents an indexed source file with its metadata and keywords.
// ID is the caller-supplied document identifier (the key suffix used to store
// the document). It is not persisted in the value; GetDocument populates it
// from the lookup key after a successful read.
type Document struct {
	ID           string `json:"-"`
	RelPath      string `json:"rel_path,omitempty"`
	Size         int64  `json:"size"`
	Hash         string `json:"hash,omitempty"`
	ModifiedTime int64  `json:"modified_time"`
	LastSyncTime int64  `json:"last_sync_time"`

	Words     []string `json:"-"` // words in the document content
	PathWords []string `json:"-"` // words in the document relative-path
}

// GetDocument returns a document from the store.
// Returns nil, nil if the document does not exist.
func (s *Store) GetDocument(collectionID int, docid string) (*Document, error) {
	data, err := s.db.Get(s.encodeDocumentMetaKey(collectionID, docid))
	if err != nil {
		return nil, err
	}

	if data == nil {
		return nil, nil
	}

	doc, err := decodeDocumentMetaValue(data)
	if err != nil {
		return nil, err
	}

	doc.ID = docid
	return doc, nil
}

// SaveNewDocuments persists a batch of new documents and updates the
// in-memory document counter and the inverted index.
//
// The kv writes + count update are serialized on s.q (the worker); the inverted
// index notification is built into ONE Indexer batch and committed OUTSIDE that
// worker task. This is mandatory, not cosmetic: under the storage-agnostic seam
// an Indexer.Update/Batch.Commit ENQUEUES onto the same shared queue (a channel
// send). Calling it from INSIDE s.q.RunFunc — i.e. while this goroutine occupies
// the single worker — would block forever once the channel buffer fills (the
// worker cannot drain what it is itself trying to send). So we collect the
// per-doc updates and commit the index batch only after the worker task returns.
func (s *Store) SaveNewDocuments(collectionID int, docs []*Document) error {
	var invertedId int
	err := s.q.RunFunc(func() error {
		if s.db.IsClosed() {
			log.Println("[Documents] Database is closed, skip saving new documents")
			return errSkip
		}

		if s.isCollectionDeleted(collectionID) {
			log.Println("[Documents] Error: collection is deleted, skip updating documents")
			return fmt.Errorf("workspace is deleted")
		}

		ft, err := s.GetCollection(collectionID)
		if err != nil {
			log.Println("[Documents] Error: failed to get collection:", err)
			return err
		}
		invertedId = ft.InvertedId

		batch := newBatch(s.db)
		for _, doc := range docs {
			s.saveDocument(batch, collectionID, doc)
		}
		err = batch.Commit()
		if err != nil {
			log.Println("[Documents] Error: failed to save new documents:", err)
			return err
		}

		s.docCountMu.Lock()
		s.docCount[collectionID] += len(docs)
		s.docCountMu.Unlock()

		return nil
	})
	if err != nil {
		return ignoreSkip(err)
	}

	// Notify the index OUTSIDE the worker task (see the method doc): one batch for
	// the whole save so N docs collapse into a single enqueued apply.
	s.indexDocuments(invertedId, docs)
	return nil
}

// UpdateDocuments updates words and metadata for a batch of existing documents.
// It also updates the inverted index with the diff between old and new words.
//
// As in SaveNewDocuments, the inverted-index notification is committed OUTSIDE
// the worker task (one Indexer batch) so an Indexer whose Update enqueues onto
// the shared queue can never deadlock the worker mid-task.
func (s *Store) UpdateDocuments(collectionID int, updatedDocs []*Document) error {
	var invertedId int
	err := s.q.RunFunc(func() error {
		if s.db.IsClosed() {
			log.Println("[Documents] Database is closed, skip updating documents")
			return fmt.Errorf("database is closed")
		}

		if s.isCollectionDeleted(collectionID) {
			log.Println("[Documents] Error: collection is deleted, skip updating documents")
			return fmt.Errorf("workspace is deleted")
		}

		ft, err := s.GetCollection(collectionID)
		if err != nil {
			log.Println("[Documents] Error: failed to get collection:", err)
			return err
		}
		invertedId = ft.InvertedId

		batch := newBatch(s.db)
		for _, updatedDoc := range updatedDocs {
			// Save the updated document. The inverted index diffs the doc's current
			// keyword set against its own forward map; the notification happens below,
			// outside this worker task.
			s.saveDocument(batch, collectionID, updatedDoc)
		}
		err = batch.Commit()
		if err != nil {
			log.Println("[Documents] Error: failed to update documents:", err)
		}

		return err
	})
	if err != nil {
		return err
	}

	// Notify the index OUTSIDE the worker task: one batch carrying each doc's
	// CURRENT keyword set, which the index diffs against its forward map.
	s.indexDocuments(invertedId, updatedDocs)
	return nil
}

// DeleteDocument removes a document and its path entry from the store, and
// notifies the inverted index of the removal.
//
// The inverted-index removal (Update with empty keywords) is hoisted OUTSIDE the
// worker task for the same reason as the batch paths: an Indexer.Update enqueues
// onto the shared queue, which would deadlock if called while this goroutine
// holds the single worker.
func (s *Store) DeleteDocument(collectionID int, docId string) error {
	var (
		invertedId int
		doIndex    bool
	)
	err := s.q.RunFunc(func() error {
		if s.db.IsClosed() {
			log.Println("[Documents] Database is closed, skip deleting document")
			return errSkip
		}

		ft, err := s.GetCollection(collectionID)
		if err != nil {
			log.Println("[Documents] Error: failed to get collection:", err)
			return err
		}

		doc, err := s.GetDocument(collectionID, docId)
		if err != nil {
			log.Println("[Documents] Error: failed to get document:", err)
			return err
		}

		if doc == nil {
			return fmt.Errorf("document not found")
		}

		defer log.Printf("[Documents] Document `%s` deleted from collection `%d`", doc.RelPath, collectionID)

		// delete the document meta and path
		batch := newBatch(s.db)
		batch.Delete(s.encodeDocumentMetaKey(collectionID, docId))
		batch.Delete(s.encodeDocumentPathKey(collectionID, docId))
		err = batch.Commit()
		if err != nil {
			log.Println("[Documents] Failed to delete document:", err)
			return err
		}

		s.docCountMu.Lock()
		s.docCount[collectionID] -= 1
		s.docCountMu.Unlock()

		invertedId = ft.InvertedId
		doIndex = true
		return nil
	})
	if err != nil {
		return ignoreSkip(err)
	}

	// Notify the index OUTSIDE the worker task: empty/nil keywords ⇒ delete the
	// doc from the index; the index tombstones it against its own forward map
	// (no caller-supplied old words needed).
	if doIndex {
		s.indexDocument(invertedId, docId, nil)
	}
	return nil
}
