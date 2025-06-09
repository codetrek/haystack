package prompts

import (
	"encoding/json"
	"fmt"
	"sort"

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
