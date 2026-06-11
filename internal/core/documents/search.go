package documents

func (s *Store) GetDocumentPath(workspaceId int, docid string) string {
	key := EncodeDocumentPathKey(workspaceId, docid)
	value, _ := s.db.Get(key)
	if value == nil {
		return ""
	}

	return string(value)
}

func (s *Store) ScanFiles(workspaceId int, callback func(docid, relPath string) bool) {
	s.db.Scan(EncodeDocumentPathKey(workspaceId, ""), func(key, value []byte) bool {
		_, docid := DecodeDocumentPathKey(string(key))
		if docid == "" {
			return true
		}
		return callback(docid, string(value))
	})
}
