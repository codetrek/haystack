package fulltext

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
func GetDocument(workspaceid int, docid string, includeWords bool) (*Document, error) {
	data, err := db.Get(EncodeDocumentMetaKey(workspaceid, docid))
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
		words, err := GetDocumentWords(workspaceid, docid)
		if err != nil {
			return nil, err
		}

		doc.Words = words
	}

	return doc, nil
}

// GetDocumentWords returns the words of a document
// It returns an empty array if the document does not exist
func GetDocumentWords(workspaceid int, docid string) ([]string, error) {
	words, err := db.Get(EncodeDocumentWordsKey(workspaceid, docid))
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
func SaveNewDocuments(workspaceid int, docs []*Document) error {
	t := &saveNewDocumentsTask{
		WorkspaceID: workspaceid,
		Docs:        docs,
		done:        make(chan error),
	}

	writeQueue <- t
	return t.Wait()
}

// UpdateDocuments updates the words of a document
// It also updates the pending writes cache to merge with other documents and flush later
func UpdateDocuments(workspaceid int, updatedDocs []*Document) error {
	t := &updateDocumentsTask{
		WorkspaceID: workspaceid,
		Docs:        updatedDocs,
		done:        make(chan error),
	}

	writeQueue <- t
	return t.Wait()
}

// DeleteDocument deletes a document from the database
// It will delete the document from the keywords index and the document meta
func DeleteDocument(workspaceid int, docid string) error {
	t := &deleteDocumentTask{
		WorkspaceID: workspaceid,
		DocId:       docid,
		done:        make(chan error),
	}

	writeQueue <- t
	return t.Wait()
}
