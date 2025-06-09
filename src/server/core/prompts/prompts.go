package prompts

import (
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ai-microsoft/haystack/server/core/pebble"
	"github.com/ai-microsoft/haystack/server/core/workspace"
)

func ScanPromptFiles(workspaceId int, path string, callback func(promptKey string, value []byte) bool) {
	db.Scan(EncodePromptPathKey(workspaceId, path), func(key, value []byte) bool {
		return callback(string(key), value)
	})
}

/**
 * Saves the prompts from the specified paths into the database.
 * It reads each prompt file, extracts the description part from the content,
 * computes its embedding, and stores it in the database under the corresponding key.
 *
 * @param workspace The workspace where the prompts are located.
 * @param realPathArr An array of file paths relative to the workspace.
 *                    Each path should end with ".prompt.md" to be processed.
 *
 * This function runs in a separate goroutine to avoid blocking the main thread.
 * It uses a batch operation to save all prompts at once for efficiency.
 * If the database is closed, it logs a message and skips saving.
 * If any error occurs during reading the file, computing the embedding,
 * or saving to the database, it logs the error but continues processing other files.
 */
func SavePrompts(workspace *workspace.Workspace, relPathArr []string) {
	mpsc.RunFunc(func() error {
		if db.IsClosed() {
			log.Println("[Prompts] Database is closed, skip saving new documents")
			return fmt.Errorf("database is closed")
		}

		batch := NewBatch(db)
		for _, relPath := range relPathArr {
			savePrompt(batch, workspace, relPath)
		}

		err := batch.Commit()
		if err != nil {
			log.Println("[Prompts] Error: failed to save new documents:", err)
		}

		return err
	})
}

func savePrompt(batch pebble.Batch, workspace *workspace.Workspace, relPath string) {
	if !strings.HasSuffix(relPath, ".prompt.md") {
		return
	}

	fullPath := filepath.Join(workspace.Path, relPath)
	// Prepare JSON data for the API request
	fileContent, err := os.ReadFile(fullPath)
	if err != nil {
		log.Printf("[Prompts] Error: Failed to read file %s: %v", relPath, err)
		return
	}

	embedding_value := string(fileContent)

	if workspace.EnablePromptSearch {
		description := extractDescriptionFromPrompt(string(fileContent))
		if description != "" {
			embedding_value = description
		}

		embedding, err := EmbeddingText(string(embedding_value))
		if err != nil {
			log.Printf("[Prompts] Error: Failed to get embedding for file %s: %v", relPath, err)
			return
		}

		value, err := EncodeFloat32Vector(embedding)
		if err != nil {
			log.Printf("[Prompts] Error: Failed to encode embedding for file %s: %v", relPath, err)
			return
		}

		batch.Put(EncodePromptPathKey(workspace.Id, relPath), value)
	}
}

func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) {
		panic("vectors must be the same length")
	}

	var dotProduct, normA, normB float32
	for i := 0; i < len(a); i++ {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}

/**
 * Returns a threshold value based on the number of words in a query.
 * The threshold is used to determine the similarity score required for a match.
 */
func GetThresholdByQueryLength(wordCount int) float32 {
	switch {
	case wordCount <= 2:
		return 0.5
	case wordCount <= 5:
		return 0.65
	default:
		return 0.75
	}
}

func extractDescriptionFromPrompt(content string) string {
	// Regular expression to match front matter section
	frontMatterRegex := regexp.MustCompile(`(?s)(?:^|\n)---\s*(.*?)\s*---`)
	matches := frontMatterRegex.FindStringSubmatch(content)

	if len(matches) < 2 {
		return "" // No front matter found
	}

	// Extract front matter content
	frontMatter := matches[1]

	// Look for description field with different formats
	// First try with quotes (single or double)
	descRegexQuoted := regexp.MustCompile(`(?m)^description:\s*['"](.+?)['"]`)
	descMatchesQuoted := descRegexQuoted.FindStringSubmatch(frontMatter)

	if len(descMatchesQuoted) >= 2 {
		return descMatchesQuoted[1]
	}

	// Then try without quotes (matches until end of line)
	descRegexPlain := regexp.MustCompile(`(?m)^description:\s*([^'\"\n].+?)$`)
	descMatchesPlain := descRegexPlain.FindStringSubmatch(frontMatter)

	if len(descMatchesPlain) >= 2 {
		return strings.TrimSpace(descMatchesPlain[1])
	}

	// Finally try multiline format with > or |
	descRegexMulti := regexp.MustCompile(`(?m)^description:\s*[>|]\n(\s+.+)$`)
	descMatchesMulti := descRegexMulti.FindStringSubmatch(frontMatter)

	if len(descMatchesMulti) >= 2 {
		return strings.TrimSpace(descMatchesMulti[1])
	}

	return ""
}
