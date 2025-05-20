package fulltext

func GetDocumentPath(workspaceId int, docid string) string {
	key := EncodeDocumentPathKey(workspaceId, docid)
	value, _ := db.Get(key)
	if value == nil {
		return ""
	}

	return string(value)
}

func ScanFiles(workspaceId int, callback func(docid, relPath string) bool) {
	db.Scan(EncodeDocumentPathKey(workspaceId, ""), func(key, value []byte) bool {
		_, docid := DecodeDocumentPathKey(string(key))
		if docid == "" {
			return true
		}
		return callback(docid, string(value))
	})
}
