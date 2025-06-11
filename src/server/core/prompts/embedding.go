package prompts

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/ai-microsoft/haystack/server/core/symbols"
)

type EmbeddingResponse struct {
	Code      int                `json:"code"`
	Embedding map[string]float32 `json:"embedding"`
}

func EmbeddingText(text string) ([]float32, error) {
	requestData := map[string]interface{}{
		"text": text,
	}

	jsonData, err := json.Marshal(requestData)
	if err != nil {
		return nil, err
	}

	response, err := symbols.PostMessage("embedding", jsonData)
	if err != nil {
		return nil, err
	}

	var es EmbeddingResponse
	err = json.Unmarshal([]byte(response), &es)
	if err != nil {
		return nil, err
	}

	embeddingMap := es.Embedding
	size := len(embeddingMap)
	result := make([]float32, size)

	keys := make([]int, 0, size)
	for k := range embeddingMap {
		var i int
		fmt.Sscanf(k, "%d", &i)
		keys = append(keys, i)
	}
	sort.Ints(keys)

	for i, k := range keys {
		result[i] = embeddingMap[fmt.Sprintf("%d", k)]
	}

	return result, nil
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

func ExtractDescriptionFromPrompt(content string) string {
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
