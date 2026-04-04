package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/codetrek/haystack/shared/types"
	"github.com/stretchr/testify/assert"
)

// --- Search Content Handler Validation ---

func TestHandleSearchContent_EmptyWorkspace(t *testing.T) {
	request := types.SearchContentRequest{
		Workspace: "",
		Query:     "test",
	}
	body, _ := json.Marshal(request)

	req := httptest.NewRequest("POST", "/api/v1/search/content", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handleSearchContent(w, req)

	var resp types.SearchContentResponse
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotEqual(t, 0, resp.Code, "should reject empty workspace")
	assert.Contains(t, resp.Message, "Workspace")
}

func TestHandleSearchContent_RelativeWorkspace(t *testing.T) {
	request := types.SearchContentRequest{
		Workspace: "relative/path",
		Query:     "test",
	}
	body, _ := json.Marshal(request)

	req := httptest.NewRequest("POST", "/api/v1/search/content", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handleSearchContent(w, req)

	var resp types.SearchContentResponse
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotEqual(t, 0, resp.Code, "should reject relative workspace path")
}

func TestHandleSearchContent_EmptyQuery(t *testing.T) {
	// Note: handler validates workspace before query, so with a non-existent
	// workspace path, we get "workspace not found" before hitting query validation.
	// This test verifies the workspace-not-found path is handled correctly.
	request := types.SearchContentRequest{
		Workspace: "/nonexistent/workspace/path",
		Query:     "",
	}
	body, _ := json.Marshal(request)

	req := httptest.NewRequest("POST", "/api/v1/search/content", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handleSearchContent(w, req)

	var resp types.SearchContentResponse
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotEqual(t, 0, resp.Code, "should reject when workspace not found")
}

func TestHandleSearchContent_InvalidJSON(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/search/content", bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()

	handleSearchContent(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- Search Files Handler Validation ---

func TestHandleSearchFiles_EmptyWorkspace(t *testing.T) {
	request := types.SearchFilesRequest{
		Workspace: "",
		Query:     "test",
	}
	body, _ := json.Marshal(request)

	req := httptest.NewRequest("POST", "/api/v1/search/files", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handleSearchFiles(w, req)

	var resp types.SearchFilesResponse
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotEqual(t, 0, resp.Code, "should reject empty workspace")
}

func TestHandleSearchFiles_RelativeWorkspace(t *testing.T) {
	request := types.SearchFilesRequest{
		Workspace: "relative/path",
		Query:     "test",
	}
	body, _ := json.Marshal(request)

	req := httptest.NewRequest("POST", "/api/v1/search/files", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handleSearchFiles(w, req)

	var resp types.SearchFilesResponse
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotEqual(t, 0, resp.Code, "should reject relative workspace path")
}

func TestHandleSearchFiles_EmptyQuery(t *testing.T) {
	request := types.SearchFilesRequest{
		Workspace: "/some/path",
		Query:     "",
	}
	body, _ := json.Marshal(request)

	req := httptest.NewRequest("POST", "/api/v1/search/files", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handleSearchFiles(w, req)

	var resp types.SearchFilesResponse
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotEqual(t, 0, resp.Code, "should reject empty query")
}

func TestHandleSearchFiles_InvalidJSON(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/search/files", bytes.NewReader([]byte("{bad")))
	w := httptest.NewRecorder()

	handleSearchFiles(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- Search Symbols Handler Validation ---

func TestHandleSearchSymbols_EmptyWorkspace(t *testing.T) {
	request := types.SearchSymbolsRequest{
		Workspace: "",
		Query:     "test",
	}
	body, _ := json.Marshal(request)

	req := httptest.NewRequest("POST", "/api/v1/search/symbols", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handleSearchSymbols(w, req)

	var resp types.SearchSymbolsResponse
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotEqual(t, 0, resp.Code, "should reject empty workspace")
}

func TestHandleSearchSymbols_RelativeWorkspace(t *testing.T) {
	request := types.SearchSymbolsRequest{
		Workspace: "relative/path",
		Query:     "test",
	}
	body, _ := json.Marshal(request)

	req := httptest.NewRequest("POST", "/api/v1/search/symbols", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handleSearchSymbols(w, req)

	var resp types.SearchSymbolsResponse
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotEqual(t, 0, resp.Code, "should reject relative workspace path")
}

func TestHandleSearchSymbols_EmptyQuery(t *testing.T) {
	request := types.SearchSymbolsRequest{
		Workspace: "/some/path",
		Query:     "",
	}
	body, _ := json.Marshal(request)

	req := httptest.NewRequest("POST", "/api/v1/search/symbols", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handleSearchSymbols(w, req)

	var resp types.SearchSymbolsResponse
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotEqual(t, 0, resp.Code, "should reject empty query")
}

// --- Delete Workspace Handler Validation ---

func TestHandleDeleteWorkspace_EmptyPath(t *testing.T) {
	request := types.DeleteWorkspaceRequest{
		Workspace: "",
	}
	body, _ := json.Marshal(request)

	req := httptest.NewRequest("POST", "/api/v1/workspace/delete", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handleDeleteWorkspace(w, req)

	var resp types.CommonResponse
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotEqual(t, 0, resp.Code, "should reject empty workspace path")
	assert.Contains(t, resp.Message, "required")
}

func TestHandleDeleteWorkspace_RelativePath(t *testing.T) {
	request := types.DeleteWorkspaceRequest{
		Workspace: "relative/path",
	}
	body, _ := json.Marshal(request)

	req := httptest.NewRequest("POST", "/api/v1/workspace/delete", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handleDeleteWorkspace(w, req)

	var resp types.CommonResponse
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotEqual(t, 0, resp.Code, "should reject relative workspace path")
	assert.Contains(t, resp.Message, "absolute")
}

func TestHandleDeleteWorkspace_InvalidJSON(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/workspace/delete", bytes.NewReader([]byte("garbage")))
	w := httptest.NewRecorder()

	handleDeleteWorkspace(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- Create Workspace Handler Validation ---

func TestHandleCreateWorkspace_InvalidJSON(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/workspace/create", bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()

	handleCreateWorkspace(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- MCP Args Validation (mcptools) ---
// Note: MCP tools validation is tested indirectly via the parseAndValidateSearchArgs
// function in mcptools package. Direct handler tests require full MCP server setup.
