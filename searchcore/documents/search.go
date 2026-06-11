package documents

// GetDocumentPath returns the relative file path stored for a given document.
// Returns an empty string if the document does not exist.
func (s *Store) GetDocumentPath(workspaceId int, docid string) string {
	key := s.encodeDocumentPathKey(workspaceId, docid)
	value, _ := s.db.Get(key)
	if value == nil {
		return ""
	}

	return string(value)
}

// ScanFiles iterates over all document path entries for the given workspace,
// calling callback(docid, relPath) for each one. Returning false from callback
// stops the scan early.
func (s *Store) ScanFiles(workspaceId int, callback func(docid, relPath string) bool) {
	s.db.Scan(s.encodeDocumentPathKey(workspaceId, ""), func(key, value []byte) bool {
		_, docid := s.decodeDocumentPathKey(string(key))
		if docid == "" {
			return true
		}
		return callback(docid, string(value))
	})
}
