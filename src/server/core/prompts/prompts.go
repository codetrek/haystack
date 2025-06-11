package prompts

import (
	"fmt"
	"log"
)

// Process embeddings outside of mpsc
type PromptEmbeddingData struct {
	Key   []byte
	Value []byte
}

func ScanPromptFiles(workspaceId int, path string, callback func(promptKey string, value []byte) bool) {
	db.Scan(EncodePromptPathKey(workspaceId, path), func(key, value []byte) bool {
		return callback(string(key), value)
	})
}

/**
 * Saves the prompts from the specified paths into the database.
 * It uses a batch operation to save multiple prompts at once.
 * The prompts are encoded with their workspace ID and relative path.
 *
 * @param promptsToSave - a slice of PromptEmbeddingData containing the prompts to save.
 */
func SavePrompts(promptsToSave []PromptEmbeddingData) {
	mpsc.RunFunc(func() error {
		if db.IsClosed() {
			log.Println("[Prompts] Database is closed, skip saving new documents")
			return fmt.Errorf("database is closed")
		}

		batch := NewBatch(db)
		for _, prompt := range promptsToSave {
			batch.Put(prompt.Key, prompt.Value)
		}

		err := batch.Commit()
		if err != nil {
			log.Println("[Prompts] Error: failed to save new documents:", err)
		}

		return err
	})
}
