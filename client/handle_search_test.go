package client

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/codetrek/haystack/shared/types"
)

func TestHandleSearch_Help(t *testing.T) {
	output := captureStdout(t, func() {
		handleSearch([]string{"-h"})
	})
	if !strings.Contains(output, "Usage:") {
		t.Error("handleSearch -h should show usage")
	}
	if !strings.Contains(output, "search") {
		t.Error("handleSearch -h should mention search")
	}
}

func TestHandleSearch_NoArgs(t *testing.T) {
	output := captureStdout(t, func() {
		handleSearch(nil)
	})
	if !strings.Contains(output, "Usage:") {
		t.Error("handleSearch with no args should show usage")
	}
}

func TestHandleSearch_EmptyQuery(t *testing.T) {
	output := captureStdout(t, func() {
		handleSearch([]string{})
	})
	// With no args, wantsHelp returns true so this shows usage
	if !strings.Contains(output, "Usage:") {
		t.Error("handleSearch with empty args should show usage")
	}
}

func TestHandleSearch_WithQuery_Success(t *testing.T) {
	results := types.SearchContentResults{
		Results: []types.SearchContentResult{
			{
				File: "test.go",
				Lines: []types.LineMatch{
					{
						Line: types.SearchContentLine{
							LineNumber: 10,
							Content:    "func main() {",
							Match:      []int{5, 9},
						},
					},
				},
				Truncate: false,
			},
		},
		Truncate: false,
	}

	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", results))
	})

	output := captureStdout(t, func() {
		handleSearch([]string{"main"})
	})
	if !strings.Contains(output, "Searching for: main") {
		t.Errorf("expected search message, got: %s", output)
	}
	if !strings.Contains(output, "test.go") {
		t.Errorf("expected file result, got: %s", output)
	}
}

func TestHandleSearch_WithFilters(t *testing.T) {
	results := types.SearchContentResults{
		Results: []types.SearchContentResult{},
	}

	var receivedReq types.SearchContentRequest
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedReq)
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", results))
	})

	output := captureStdout(t, func() {
		handleSearch([]string{"-path", "/src", "-include", "*.go", "-exclude", "vendor", "-case-sensitive", "-whole-word", "query"})
	})
	if !strings.Contains(output, "Searching for: query") {
		t.Errorf("expected search message, got: %s", output)
	}
	// Verify filters were sent
	if receivedReq.Filters == nil {
		t.Error("expected filters to be set")
	}
	if receivedReq.Filters != nil {
		if receivedReq.Filters.Path != "/src" {
			t.Errorf("expected path /src, got %s", receivedReq.Filters.Path)
		}
		if receivedReq.Filters.Include != "*.go" {
			t.Errorf("expected include *.go, got %s", receivedReq.Filters.Include)
		}
		if receivedReq.Filters.Exclude != "vendor" {
			t.Errorf("expected exclude vendor, got %s", receivedReq.Filters.Exclude)
		}
	}
	if !receivedReq.CaseSensitive {
		t.Error("expected case_sensitive to be true")
	}
	if !receivedReq.WholeWord {
		t.Error("expected whole_word to be true")
	}
}

func TestHandleSearch_ServerError(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 1, "internal error", nil))
	})

	output := captureStdout(t, func() {
		handleSearch([]string{"test"})
	})
	if !strings.Contains(output, "Error") {
		t.Errorf("expected error message, got: %s", output)
	}
}

func TestDisplaySearchResults_Empty(t *testing.T) {
	output := captureStdout(t, func() {
		displaySearchResults(&types.SearchContentResults{})
	})
	if !strings.Contains(output, "No results found") {
		t.Errorf("expected 'No results found', got: %s", output)
	}
}

func TestDisplaySearchResults_WithResults(t *testing.T) {
	results := &types.SearchContentResults{
		Results: []types.SearchContentResult{
			{
				File: "main.go",
				Lines: []types.LineMatch{
					{
						Line: types.SearchContentLine{
							LineNumber: 5,
							Content:    "package main",
							Match:      []int{8, 12},
						},
					},
					{
						Line: types.SearchContentLine{
							LineNumber: 10,
							Content:    "func main() {",
							Match:      []int{5, 9},
						},
					},
				},
				Truncate: false,
			},
			{
				File: "util.go",
				Lines: []types.LineMatch{
					{
						Line: types.SearchContentLine{
							LineNumber: 1,
							Content:    "package main",
							Match:      []int{8, 12},
						},
					},
				},
				Truncate: true,
			},
		},
		Truncate: false,
	}

	output := captureStdout(t, func() {
		displaySearchResults(results)
	})

	if !strings.Contains(output, "Found 3 results in 2 files") {
		t.Errorf("expected result count, got: %s", output)
	}
	if !strings.Contains(output, "File: main.go") {
		t.Error("expected main.go file")
	}
	if !strings.Contains(output, "File: util.go") {
		t.Error("expected util.go file")
	}
	if !strings.Contains(output, "(Results truncated...)") {
		t.Error("expected truncation notice for util.go")
	}
}

func TestDisplaySearchResults_GlobalTruncation(t *testing.T) {
	results := &types.SearchContentResults{
		Results: []types.SearchContentResult{
			{
				File: "a.go",
				Lines: []types.LineMatch{
					{
						Line: types.SearchContentLine{
							LineNumber: 1,
							Content:    "line content",
							Match:      []int{0, 4},
						},
					},
				},
			},
		},
		Truncate: true,
	}

	output := captureStdout(t, func() {
		displaySearchResults(results)
	})
	if !strings.Contains(output, "Search results were truncated") {
		t.Error("expected global truncation notice")
	}
}

// --- File search tests ---

func TestHandleSearchFiles_Help(t *testing.T) {
	output := captureStdout(t, func() {
		handleSearchFiles([]string{"-h"})
	})
	if !strings.Contains(output, "Usage:") {
		t.Error("handleSearchFiles -h should show usage")
	}
}

func TestHandleSearchFiles_NoArgs(t *testing.T) {
	output := captureStdout(t, func() {
		handleSearchFiles(nil)
	})
	if !strings.Contains(output, "Usage:") {
		t.Error("handleSearchFiles with no args should show usage")
	}
}

func TestHandleSearchFiles_EmptyQueryAfterParse(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		// This should not be called since query is empty
		t.Error("server should not be called with empty query")
	})
	output := captureStdout(t, func() {
		handleSearchFiles([]string{"-limit", "10"})
	})
	if !strings.Contains(output, "Error: Search query cannot be empty") {
		t.Errorf("expected empty query error, got: %s", output)
	}
}

func TestHandleSearchFiles_WithQuery_Success(t *testing.T) {
	result := types.SearchFilesResult{
		Query: "main",
		Files: []string{"main.go", "cmd/main.go"},
	}

	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", result))
	})

	output := captureStdout(t, func() {
		handleSearchFiles([]string{"main"})
	})
	if !strings.Contains(output, "Searching for: main") {
		t.Errorf("expected search message, got: %s", output)
	}
	if !strings.Contains(output, "main.go") {
		t.Errorf("expected file result, got: %s", output)
	}
}

func TestHandleSearchFiles_ServerError(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 1, "error", nil))
	})

	output := captureStdout(t, func() {
		handleSearchFiles([]string{"test"})
	})
	if !strings.Contains(output, "Error") {
		t.Errorf("expected error message, got: %s", output)
	}
}

func TestDisplaySearchFilesResults_Empty(t *testing.T) {
	output := captureStdout(t, func() {
		displaySearchFilesResults(&types.SearchFilesResult{})
	})
	if !strings.Contains(output, "No results found") {
		t.Errorf("expected 'No results found', got: %s", output)
	}
}

func TestDisplaySearchFilesResults_WithFiles(t *testing.T) {
	result := &types.SearchFilesResult{
		Files: []string{"a.go", "b.go", "c.go"},
	}
	output := captureStdout(t, func() {
		displaySearchFilesResults(result)
	})
	if !strings.Contains(output, "Found 3 files") {
		t.Errorf("expected file count, got: %s", output)
	}
	if !strings.Contains(output, "File: a.go") {
		t.Error("expected a.go")
	}
	if !strings.Contains(output, "File: b.go") {
		t.Error("expected b.go")
	}
}

func TestSendSearchRequest_Success(t *testing.T) {
	expected := types.SearchContentResults{
		Results: []types.SearchContentResult{
			{File: "test.go"},
		},
	}

	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/search/content" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", expected))
	})

	result, err := sendSearchRequest(types.SearchContentRequest{Query: "test"})
	if err != nil {
		t.Fatalf("sendSearchRequest error: %v", err)
	}
	if len(result.Results) != 1 {
		t.Errorf("expected 1 result, got %d", len(result.Results))
	}
}

func TestHandleSearch_EmptyQueryAfterParse(t *testing.T) {
	// Pass only flags, no positional query arg
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called with empty query")
	})
	output := captureStdout(t, func() {
		handleSearch([]string{"-limit", "10"})
	})
	if !strings.Contains(output, "Error: Search query cannot be empty") {
		t.Errorf("expected empty query error, got: %s", output)
	}
}

func TestSendSearchRequest_UnmarshalError(t *testing.T) {
	// Server returns a valid CommonResponse but with Data that cannot be
	// unmarshalled into SearchContentResults (a JSON string instead of object).
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Build a CommonResponse where Data is a JSON string literal, which
		// will fail json.Unmarshal into a struct.
		raw := json.RawMessage(`"not a valid struct"`)
		resp := types.CommonResponse{
			Code:    0,
			Message: "ok",
			Data:    &raw,
		}
		b, _ := json.Marshal(resp)
		w.WriteHeader(http.StatusOK)
		w.Write(b)
	})

	_, err := sendSearchRequest(types.SearchContentRequest{Query: "test"})
	if err == nil {
		t.Fatal("expected error from sendSearchRequest when response data is invalid")
	}
	if !strings.Contains(err.Error(), "failed to parse response") {
		t.Errorf("expected 'failed to parse response' error, got: %v", err)
	}
}

func TestSendSearchFilesRequest_Success(t *testing.T) {
	expected := types.SearchFilesResult{
		Files: []string{"a.go"},
	}

	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/search/files" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", expected))
	})

	result, err := sendSearchFilesRequest(types.SearchFilesRequest{Query: "a"})
	if err != nil {
		t.Fatalf("sendSearchFilesRequest error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Errorf("expected 1 file, got %d", len(result.Files))
	}
}

func TestSendSearchFilesRequest_UnmarshalError(t *testing.T) {
	// Server returns a valid CommonResponse but with Data that cannot be
	// unmarshalled into SearchFilesResult (a JSON string instead of object).
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		raw := json.RawMessage(`"not a valid struct"`)
		resp := types.CommonResponse{
			Code:    0,
			Message: "ok",
			Data:    &raw,
		}
		b, _ := json.Marshal(resp)
		w.WriteHeader(http.StatusOK)
		w.Write(b)
	})

	_, err := sendSearchFilesRequest(types.SearchFilesRequest{Query: "test"})
	if err == nil {
		t.Fatal("expected error from sendSearchFilesRequest when response data is invalid")
	}
	if !strings.Contains(err.Error(), "failed to parse response") {
		t.Errorf("expected 'failed to parse response' error, got: %v", err)
	}
}
