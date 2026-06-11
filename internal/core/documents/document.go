package documents

import (
	"fmt"
	"log"
)

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

// As the Document already breakdown into keywords, we can use the document full-path as the document id
// and store the document id and its keywords in the storage, below is the process:
//   - Create a reading snapshot of the storage to allow concurrent read operations
//   - Document full-path is converted to a md5 hash value, and used as the document id
//   - A new entry is created in the storage:
//       key: "doc:<workspace_id>|<document_id>"
//       value: <Document>
//   - For each keyword in the document, a new entry is created in the storage:
//       key: "kw:<workspace_id>|<keyword>|<document_count>|<document_hash>"
//       value: <document_ids>

// GetDocument returns a document from the database
// It returns nil if the document does not exist
func (s *Store) GetDocument(workspaceid int, docid string, includeWords bool) (*Document, error) {
	data, err := s.db.Get(EncodeDocumentMetaKey(workspaceid, docid))
	if err != nil {
		return nil, err
	}

	if data == nil {
		return nil, nil
	}

	doc, err := DecodeDocumentMetaValue(data)
	if err != nil {
		return nil, err
	}

	doc.ID = docid

	if includeWords {
		words, err := s.GetDocumentWords(workspaceid, docid)
		if err != nil {
			return nil, err
		}

		doc.Words = words
	}

	return doc, nil
}

// GetDocumentWords returns the words of a document
// It returns an empty array if the document does not exist
func (s *Store) GetDocumentWords(workspaceid int, docid string) ([]string, error) {
	words, err := s.db.Get(EncodeDocumentWordsKey(workspaceid, docid))
	if err != nil {
		return nil, err
	}

	if len(words) == 0 {
		return []string{}, nil
	}

	return DecodeDocumentWordsValue(string(words)), nil
}

// SaveNewDocuments saves new documents to the database
// It also updates the pending writes cache to merge with other documents and flush later
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

		batch := NewBatch(s.db)
		for _, doc := range docs {
			s.indexDocument(ft.InvertedId, doc.ID, doc.Words, nil)
			saveDocument(batch, workspaceid, doc)
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

// UpdateDocuments updates the words of a document
// It also updates the pending writes cache to merge with other documents and flush later
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

		batch := NewBatch(s.db)
		for _, updatedDoc := range updatedDocs {
			// Get the current document words from the database
			oldWords, err := s.GetDocumentWords(workspaceid, updatedDoc.ID)
			if err != nil {
				continue
			}

			s.indexDocument(ft.InvertedId, updatedDoc.ID, updatedDoc.Words, oldWords)

			// Save the updated document
			saveDocument(batch, workspaceid, updatedDoc)
		}
		err = batch.Commit()
		if err != nil {
			log.Println("[Documents] Error: failed to update documents:", err)
		}

		return err
	})
}

// DeleteDocument deletes a document from the database
// It will delete the document from the keywords index and the document meta
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
		batch := NewBatch(s.db)
		batch.Delete(EncodeDocumentMetaKey(workspaceId, docId))
		batch.Delete(EncodeDocumentWordsKey(workspaceId, docId))
		batch.Delete(EncodeDocumentPathKey(workspaceId, docId))
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
