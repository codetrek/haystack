package symbols

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/shared/types"
)

type EmbeddingResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type SymbolScore struct {
	Symbol string  `json:"symbol"`
	Score  float64 `json:"score"`
}

type QueryResponse struct {
	Code    int           `json:"code"`
	Message string        `json:"message"`
	Data    []SymbolScore `json:"data,omitempty"`
}

func getEmbeddingDBPath(workspaceId int) string {
	return filepath.Join(conf.Get().Global.DataPath, "data", "embedding", fmt.Sprintf("%d", workspaceId))
}

func getOrCreateEmbeddingDBPath(workspaceId int) (string, error) {
	dbPath := getEmbeddingDBPath(workspaceId)

	if _, statErr := os.Stat(dbPath); os.IsNotExist(statErr) {
		// Directory doesn't exist, create it
		err := os.MkdirAll(dbPath, 0755)
		if err != nil {
			return "", fmt.Errorf("failed to create embedding dbPath: %w", err)
		}
	}

	return dbPath, nil
}

func PostMessage(api string, jsonData []byte) (string, error) {
	req, err := http.NewRequest("POST", fmt.Sprintf("http://localhost:%d/%s", conf.Get().Symbols.EmbeddingPort, api), bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: 60 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return string(body), fmt.Errorf("request failed with status code: %d", resp.StatusCode)
	}

	return string(body), nil
}

func EmbeddingStop() (QueryResponse, error) {
	response, err := PostMessage("stop", nil)
	if err != nil {
		return QueryResponse{}, err
	}

	var result QueryResponse
	err = json.Unmarshal([]byte(response), &result)
	if err != nil {
		return QueryResponse{}, fmt.Errorf("EmbeddingStop, failed to parse JSON: %v", err)
	}

	return result, nil
}

func EmbeddingHealth() (QueryResponse, error) {
	response, err := PostMessage("health", nil)
	if err != nil {
		return QueryResponse{}, err
	}

	var result QueryResponse
	err = json.Unmarshal([]byte(response), &result)
	if err != nil {
		return QueryResponse{}, fmt.Errorf("EmbeddingHealth, failed to parse JSON: %v", err)
	}

	return result, nil
}

func EmbeddingAddSymbolsToDB(workspace2Functions map[int][]string) (int, error) {
	count := 0
	for workspaceId, functions := range workspace2Functions {
		dbPath, err := getOrCreateEmbeddingDBPath(workspaceId)
		if err != nil {
			log.Printf("[Symbols] Error: Failed to get embedding dbPath for workspace %d: %v", workspaceId, err)
			return 0, err
		}

		// Prepare JSON data for the API request
		requestData := map[string]interface{}{
			"functions": functions,
			"dbPath":    dbPath,
		}

		jsonData, err := json.Marshal(requestData)
		if err != nil {
			log.Printf("[Symbols] Error: Failed to marshal functions for embedding: %v", err)
			return 0, err
		}

		// Send to the API endpoint
		response, err := PostMessage("embeddingSymbolToDB", jsonData)
		if err != nil {
			log.Printf("[Symbols] Error: Failed to send functions for embedding: %v", err)
			return 0, err
		}

		var result EmbeddingResponse
		err = json.Unmarshal([]byte(response), &result)
		if err != nil {
			return 0, err
		}

		if result.Code != 0 {
			return 0, fmt.Errorf("failed to embed functions: %s", result.Message)
		}

		count += len(functions)
	}

	return count, nil
}

func EmbeddingSearch(workspaceId int, query string, limit types.SearchLimit) (QueryResponse, error) {
	dbPath, err := getOrCreateEmbeddingDBPath(workspaceId)
	if err != nil {
		log.Printf("[Symbols] Error: Failed to get embedding dbPath for workspace %d: %v", workspaceId, err)
		return QueryResponse{}, err
	}

	requestData := map[string]interface{}{
		"query":  query,
		"dbPath": dbPath,
		"limit":  limit.MaxResults,
	}

	jsonData, err := json.Marshal(requestData)
	if err != nil {
		log.Printf("[Symbols] Error: Failed to marshal query for embedding: %v", err)
		return QueryResponse{}, err
	}

	// Send to the API endpoint
	response, err := PostMessage("query", jsonData)
	if err != nil {
		log.Printf("[Symbols] Error: Failed to send query for embedding: %v", err)
		return QueryResponse{}, err
	}

	var result QueryResponse
	err = json.Unmarshal([]byte(response), &result)
	if err != nil {
		log.Printf("[Symbols] Error: Failed to parse JSON:%v", err)
		return QueryResponse{}, err
	}

	return result, nil
}

func EmbeddingBuildIndex() (EmbeddingResponse, error) {
	// Send to the API endpoint
	response, err := PostMessage("buildIndexIfNeeded", nil)

	if err != nil {
		log.Printf("[Symbols] Error: Failed to send query for embedding: %v", err)
		return EmbeddingResponse{}, err
	}

	var result EmbeddingResponse
	err = json.Unmarshal([]byte(response), &result)
	if err != nil {
		log.Printf("[Symbols] Error: Failed to parse JSON: %v", err)
		return EmbeddingResponse{}, err
	}

	return result, nil
}

func EmbeddingRemoveDB(workspaceId int) (EmbeddingResponse, error) {
	dbPath := getEmbeddingDBPath(workspaceId)
	if _, statErr := os.Stat(dbPath); os.IsNotExist(statErr) {
		log.Printf("[Symbols] Error: Embedding DB for workspace %d does not exist", workspaceId)
		return EmbeddingResponse{}, nil
	}

	requestData := map[string]interface{}{
		"dbPath": dbPath,
	}

	jsonData, err := json.Marshal(requestData)
	if err != nil {
		log.Printf("[Symbols] Error: Failed to marshal query for embedding: %v", err)
		return EmbeddingResponse{}, err
	}

	// Send to the API endpoint
	response, err := PostMessage("removeDB", jsonData)
	if err != nil {
		log.Printf("[Symbols] Error: Failed to send query for embedding: %v", err)
		return EmbeddingResponse{}, err
	}

	var result EmbeddingResponse
	err = json.Unmarshal([]byte(response), &result)
	if err != nil {
		log.Printf("[Symbols] Error: Failed to parse JSON: %v", err)
		return EmbeddingResponse{}, err
	}

	return result, nil
}
