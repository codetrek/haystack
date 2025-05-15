package fulltext

func DeleteDatabase(id int) error {
	batch := db.NewBatch(0)
	batch.DeletePrefix(EncodeDocumentMetaKey(id, ""))
	batch.DeletePrefix(EncodeDocumentWordsKey(id, ""))
	batch.DeletePrefix(EncodeInvertedSearchKey(id, ""))
	return batch.Commit()
}
