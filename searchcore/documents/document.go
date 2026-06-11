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
	RelPath      string `json:"rel_path"`
	Size         int64  `json:"size"`
	Hash         string `json:"hash"`
	ModifiedTime int64  `json:"modified_time"`
	LastSyncTime int64  `json:"last_sync_time"`

	Words     []string `json:"-"` // words in the document content
	PathWords []string `json:"-"` // words in the document relative-path
}

// GetDocument returns a document from the store.
// Returns nil, nil if the document does not exist.
// If includeWords is true, the document's Words field is populated.
func (s *Store) GetDocument(workspaceid int, docid string, includeWords bool) (*Document, error) {
	data, err := s.db.Get(s.encodeDocumentMetaKey(workspaceid, docid))
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

	if includeWords {
		words, err := s.getDocumentWords(workspaceid, docid)
		if err != nil {
			return nil, err
		}

		doc.Words = words
	}

	return doc, nil
}

// getDocumentWords returns the words of a document.
// Returns an empty slice if the document does not exist.
func (s *Store) getDocumentWords(workspaceid int, docid string) ([]string, error) {
	words, err := s.db.Get(s.encodeDocumentWordsKey(workspaceid, docid))
	if err != nil {
		return nil, err
	}

	if len(words) == 0 {
		return []string{}, nil
	}

	return decodeDocumentWordsValue(string(words)), nil
}

// SaveNewDocuments persists a batch of new documents and updates the
// in-memory document counter and the inverted index.
func (s *Store) SaveNewDocuments(workspaceid int, docs []*Document) error {
	return s.q.RunFunc(func() error {
		if s.db.IsClosed() {
			log.Println("[Documents] Database is closed, skip saving new documents")
			return nil
		}

		if s.isWorkspaceDeleted(workspaceid) {
			log.Println("[Documents] Error: workspace is deleted, skip updating documents")
			return fmt.Errorf("workspace is deleted")
		}

		ft, err := s.GetWorkspace(workspaceid)
		if err != nil {
			log.Println("[Documents] Error: failed to get workspace:", err)
			return err
		}

		batch := newBatch(s.db)
		for _, doc := range docs {
			s.indexDocument(ft.InvertedId, doc.ID, doc.Words, nil)
			s.saveDocument(batch, workspaceid, doc)
		}
		err = batch.Commit()
		if err != nil {
			log.Println("[Documents] Error: failed to save new documents:", err)
			return err
		}

		s.docCountMu.Lock()
		s.docCount[workspaceid] += len(docs)
		s.docCountMu.Unlock()

		return nil
	})
}

// UpdateDocuments updates words and metadata for a batch of existing documents.
// It also updates the inverted index with the diff between old and new words.
func (s *Store) UpdateDocuments(workspaceid int, updatedDocs []*Document) error {
	return s.q.RunFunc(func() error {
		if s.db.IsClosed() {
			log.Println("[Documents] Database is closed, skip updating documents")
			return fmt.Errorf("database is closed")
		}

		if s.isWorkspaceDeleted(workspaceid) {
			log.Println("[Documents] Error: workspace is deleted, skip updating documents")
			return fmt.Errorf("workspace is deleted")
		}

		ft, err := s.GetWorkspace(workspaceid)
		if err != nil {
			log.Println("[Documents] Error: failed to get workspace:", err)
			return err
		}

		batch := newBatch(s.db)
		for _, updatedDoc := range updatedDocs {
			// Get the current document words from the database
			oldWords, err := s.getDocumentWords(workspaceid, updatedDoc.ID)
			if err != nil {
				continue
			}

			s.indexDocument(ft.InvertedId, updatedDoc.ID, updatedDoc.Words, oldWords)

			// Save the updated document
			s.saveDocument(batch, workspaceid, updatedDoc)
		}
		err = batch.Commit()
		if err != nil {
			log.Println("[Documents] Error: failed to update documents:", err)
		}

		return err
	})
}

// DeleteDocument removes a document, its words, and its path entry from the
// store, and notifies the inverted index of the removal.
func (s *Store) DeleteDocument(workspaceId int, docId string) error {
	return s.q.RunFunc(func() error {
		if s.db.IsClosed() {
			log.Println("[Documents] Database is closed, skip deleting document")
			return nil
		}

		ft, err := s.GetWorkspace(workspaceId)
		if err != nil {
			log.Println("[Documents] Error: failed to get workspace:", err)
			return err
		}

		doc, err := s.GetDocument(workspaceId, docId, true)
		if err != nil {
			log.Println("[Documents] Error: failed to get document:", err)
			return err
		}

		if doc == nil {
			return fmt.Errorf("document not found")
		}

		defer log.Printf("[Documents] Document `%s` deleted from workspace `%d`", doc.RelPath, workspaceId)

		s.indexDocument(ft.InvertedId, docId, []string{}, doc.Words)

		// delete the document meta and words
		batch := newBatch(s.db)
		batch.Delete(s.encodeDocumentMetaKey(workspaceId, docId))
		batch.Delete(s.encodeDocumentWordsKey(workspaceId, docId))
		batch.Delete(s.encodeDocumentPathKey(workspaceId, docId))
		err = batch.Commit()
		if err != nil {
			log.Println("[Documents] Failed to delete document:", err)
			return err
		}

		s.docCountMu.Lock()
		s.docCount[workspaceId] -= 1
		s.docCountMu.Unlock()

		return nil
	})
}
