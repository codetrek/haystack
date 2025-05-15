package fulltext

type KeywordResult struct {
	Keyword string              `json:"-"`
	DocIds  map[string]struct{} `json:"-"`
}

type SearchResult struct {
	//	Keywords map[string]*KeywordResult `json:"keywords"`
	DocIds map[string]struct{} `json:"docIds"`
}

func Search(workspaceid int, query string, limit int) SearchResult {
	results := SearchResult{
		// Keywords: make(map[string]*KeywordResult),
		DocIds: make(map[string]struct{}),
	}

	db.Scan(EncodeInvertedSearchKey(workspaceid, query), func(key, value []byte) bool {
		// _, keyword, _, _ := DecodeKeywordIndexKey(string(key))
		docids := DecodeInvertedValue(value)
		if len(docids) > 0 {
			/*
				kr, ok := results.Keywords[keyword]
				if !ok {
					kr = &KeywordResult{
						Keyword: keyword,
						DocIds:  make(map[string]struct{}),
					}
					results.Keywords[keyword] = kr
				}
			*/

			for _, docid := range docids {
				// kr.DocIds[docid] = struct{}{}
				results.DocIds[docid] = struct{}{}
			}
		}

		if limit > 0 && len(results.DocIds) >= limit {
			return false
		}

		return true
	})
	return results
}

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
