package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/shared/types"
)

type result struct {
	Body       *types.CommonResponse
	StatusCode int
}

func serverRequest(api string, postData []byte) (*result, error) {
	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	apiURL := fmt.Sprintf("http://127.0.0.1:%d/api/v1%s", conf.Get().Global.Port, api)

	// Send request
	resp, err := client.Post(
		apiURL,
		"application/json",
		bytes.NewBuffer(postData),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to API: %v", err)
	}
	defer resp.Body.Close()

	result := &result{
		StatusCode: resp.StatusCode,
	}

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %v", err)
	}

	var response types.CommonResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %v", err)
	}

	if response.Code != 0 {
		return nil, fmt.Errorf("error code: %d, message: %s", response.Code, response.Message)
	}

	result.Body = &response

	return result, nil
}
