package documents

import (
	"fmt"
	"log"
)

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
func (s *Store) SaveNewDocuments(collectionID int, docs []*Document) error {
	return s.q.RunFunc(func() error {
		if s.db.IsClosed() {
			log.Println("[Documents] Database is closed, skip saving new documents")
			return nil
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

		batch := newBatch(s.db)
		for _, doc := range docs {
			s.indexAddDocument(ft.InvertedId, doc.ID, doc.Words)
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
}

// UpdateDocuments updates words and metadata for a batch of existing documents.
// It also updates the inverted index with the diff between old and new words.
func (s *Store) UpdateDocuments(collectionID int, updatedDocs []*Document) error {
	return s.q.RunFunc(func() error {
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

		batch := newBatch(s.db)
		for _, updatedDoc := range updatedDocs {
			s.indexUpdateDocument(ft.InvertedId, updatedDoc.ID, updatedDoc.Words)
			s.saveDocument(batch, collectionID, updatedDoc)
		}
		err = batch.Commit()
		if err != nil {
			log.Println("[Documents] Error: failed to update documents:", err)
		}

		return err
	})
}

// DeleteDocument removes a document and its path entry from the store, and
// notifies the inverted index of the removal.
func (s *Store) DeleteDocument(collectionID int, docId string) error {
	return s.q.RunFunc(func() error {
		if s.db.IsClosed() {
			log.Println("[Documents] Database is closed, skip deleting document")
			return nil
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

		s.indexDeleteDocument(ft.InvertedId, docId)

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

		return nil
	})
}
