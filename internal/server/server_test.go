package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/codetrek/haystack/core/collection"
	"github.com/codetrek/haystack/core/documents"
	"github.com/codetrek/haystack/core/idtable"
	"github.com/codetrek/haystack/core/invertedindex"
	"github.com/codetrek/haystack/core/queue"
	"github.com/codetrek/haystack/internal/conf"
	"github.com/codetrek/haystack/internal/core/storage"
	"github.com/codetrek/haystack/internal/core/symbols"
	"github.com/codetrek/haystack/internal/core/workspace"
	"github.com/codetrek/haystack/internal/server/httpapi"
	"github.com/codetrek/haystack/internal/server/indexer"
	"github.com/codetrek/haystack/internal/server/searcher"
	"github.com/codetrek/haystack/internal/shared/running"
	"github.com/codetrek/haystack/internal/shared/types"
)

const (
	testPort     = 18888
	testDataPath = "test_data"
)

var (
	testWorkspacePath string
	testServerURL     string

	// testInvertedIndexOptions holds the fast-flush options used by the test
	// server. Set in setupTestEnvironment, consumed in startTestServer.
	testInvertedIndexOptions invertedindex.Options
)

func TestServerEndToEnd(t *testing.T) {
	setupTestEnvironment(t)
	createTestFiles(t)
	cleanup := startTestServer(t)
	defer cleanup()

	t.Run("HealthCheck", testHealthCheck)
	t.Run("WorkspaceOperations", testWorkspaceOperations)
	t.Run("EdgeCases", testServerEdgeCases)

	waitForIndexingDone(t, testWorkspacePath, 5*time.Second)
	t.Run("SearchBeforeUpdate", testSearchBeforeUpdate)
	t.Run("SearchUnsavedFiles", testSearchUnsavedFiles)

	t.Run("DocumentOperations", testDocumentOperations)

	waitForIndexingDone(t, testWorkspacePath, 5*time.Second)
	t.Run("SearchOperations", testSearchOperations)
	t.Run("WorkspaceIndexing", testWorkspaceIndexing)
}

func setupTestEnvironment(t *testing.T) {
	// Create temporary test workspace
	tempDir := t.TempDir()
	testWorkspacePath = filepath.Join(tempDir, "test_workspace")
	err := os.MkdirAll(testWorkspacePath, 0755)
	assert.NoError(t, err)

	testServerURL = fmt.Sprintf("http://127.0.0.1:%d", testPort)

	// Configure test settings directly
	conf.Get().Global.Port = testPort
	conf.Get().Global.DataPath = filepath.Join(tempDir, testDataPath)
	conf.Get().Server.CacheSize = 8 * 1024 * 1024 // 8MB for tests

	testInvertedIndexOptions = invertedindex.Options{
		FlushTicker:        50 * time.Millisecond,
		FlushWaitTimeout:   1 * time.Microsecond,
		FlushWaitBatchSize: 10,
		FlushCooldown:      50 * time.Millisecond,
	}
}

// waitForServerReady polls the health endpoint until the server responds.
func waitForServerReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(testServerURL + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server did not become ready within timeout")
}

// waitForIndexingDone polls until the workspace's indexing is complete and
// documents are searchable.
func waitForIndexingDone(t *testing.T, wsPath string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ws, err := workspace.GetByPath(wsPath)
		if err == nil && !ws.GetLastFullSync().IsZero() && ws.GetIndexingProgress() == nil {
			// Wait for inverted index flush ticker + cooldown to commit.
			time.Sleep(200 * time.Millisecond)
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("indexing did not complete within timeout")
}

func createTestFiles(t *testing.T) {
	// Create test files with various content
	testFiles := map[string]string{
		"main.go": `package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
	fmt.Println("Hello, The World!)
	result := add(1, 2)
	fmt.Printf("Result: %d\n", result)
	fmt.Println("This is original main.go file.")
}

func add(a, b int) int {
	return a + b
}`,
		"utils.go": `package main

import "strings"

func processString(input string) string {
	return strings.ToUpper(input)
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}`,
		"README.md": `# Test Project

This is a test project for haystack server testing.

## Features

- File indexing
- Content search
- Workspace management

## Usage

Run the main function to see output.`,
		"config.yaml": `server:
  version: 1.2.3.4
  port: 8080
  host: localhost
database:
  path: "./data"
  cache_size: 1000
`,
		"subdirectory/nested.js": `function calculateSum(numbers) {
    return numbers.reduce((sum, num) => sum + num, 0);
}

function findMax(numbers) {
    return Math.max(...numbers);
}

module.exports = { calculateSum, findMax };`,
	}

	for filename, content := range testFiles {
		fullPath := filepath.Join(testWorkspacePath, filename)
		dir := filepath.Dir(fullPath)

		err := os.MkdirAll(dir, 0755)
		assert.NoError(t, err)

		err = os.WriteFile(fullPath, []byte(content), 0644)
		assert.NoError(t, err)
	}
}

func startTestServer(t *testing.T) func() {
	wg := &sync.WaitGroup{}
	running.InitShutdown(wg)

	db, err := storage.Open(filepath.Join(conf.Get().Global.DataPath, "data"), conf.Get().Server.CacheSize)
	assert.NoError(t, err)

	indexdb, err := storage.Open(filepath.Join(conf.Get().Global.DataPath, "index"), conf.Get().Server.CacheSize)
	assert.NoError(t, err)

	mpsc := queue.NewMpsc("TestDBQueue")
	mpsc.Start()

	alloc, err := idtable.New(db, idtable.Options{})
	assert.NoError(t, err)
	indexer.SetIdAllocator(alloc)

	idx, err := invertedindex.New(indexdb, mpsc, testInvertedIndexOptions)
	assert.NoError(t, err)

	st, err := documents.New(db, mpsc, idx, documents.Options{})
	assert.NoError(t, err)
	indexer.SetDocStore(st)
	workspace.SetDocStore(st)

	cat, err := collection.New(db, st, collection.Options{})
	assert.NoError(t, err)

	err = workspace.Init(cat)
	assert.NoError(t, err)

	err = symbols.Init(db, mpsc, idx)
	assert.NoError(t, err)

	indexer.Run(wg)
	searcher.Run(wg, idx, st)

	go httpapi.StartServer(wg, fmt.Sprintf("127.0.0.1:%d", testPort), "")

	// Wait for server to start
	waitForServerReady(t)

	// Cleanup function
	return func() {
		running.Shutdown()
		wg.Wait()
		st.CloseAndWait()
		idx.CloseAndWait()
		mpsc.Stop()
		alloc.Close()
		db.Close()
		indexdb.Close()
		workspace.SetDocStore(nil)
		indexer.SetDocStore(nil)
	}
}

func makeRequest(t *testing.T, method, endpoint string, body interface{}) *http.Response {
	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		assert.NoError(t, err)
		bodyReader = bytes.NewBuffer(jsonBody)
	}

	req, err := http.NewRequest(method, testServerURL+endpoint, bodyReader)
	assert.NoError(t, err)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)

	return resp
}

func testHealthCheck(t *testing.T) {
	resp := makeRequest(t, "GET", "/health", nil)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func testWorkspaceOperations(t *testing.T) {
	t.Run("CreateWorkspace", func(t *testing.T) {
		request := types.CreateWorkspaceRequest{
			Workspace:        testWorkspacePath,
			UseGlobalFilters: true,
		}

		resp := makeRequest(t, "POST", "/api/v1/workspace/create", request)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response types.CreateWorkspaceResponse
		err := json.NewDecoder(resp.Body).Decode(&response)
		assert.NoError(t, err)

		assert.Equal(t, 0, response.Code)
		assert.Equal(t, testWorkspacePath, response.Data.Path)
		assert.True(t, response.Data.Indexing, "workspace should be indexing")
	})

	t.Run("ListWorkspaces", func(t *testing.T) {
		resp := makeRequest(t, "GET", "/api/v1/workspace/list", nil)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response types.ListWorkspaceResponse
		err := json.NewDecoder(resp.Body).Decode(&response)
		assert.NoError(t, err)

		assert.Equal(t, 0, response.Code)
		assert.True(t, len(response.Data.Workspaces) > 0, "should have workspaces")

		found := false
		for _, ws := range response.Data.Workspaces {
			if ws.Path == testWorkspacePath {
				found = true
				break
			}
		}
		assert.True(t, found, "Test workspace should be in the list")
	})

	t.Run("GetWorkspace", func(t *testing.T) {
		request := types.GetWorkspaceRequest{
			Workspace: testWorkspacePath,
		}

		resp := makeRequest(t, "POST", "/api/v1/workspace/get", request)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response types.GetWorkspaceResponse
		err := json.NewDecoder(resp.Body).Decode(&response)
		assert.NoError(t, err)

		assert.Equal(t, 0, response.Code)
		assert.NotNil(t, response.Data)
		assert.Equal(t, testWorkspacePath, response.Data.Path)
	})

	t.Run("SyncWorkspace", func(t *testing.T) {
		request := types.SyncWorkspaceRequest{
			Workspace: testWorkspacePath,
		}

		resp := makeRequest(t, "POST", "/api/v1/workspace/sync", request)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response types.CommonResponse
		err := json.NewDecoder(resp.Body).Decode(&response)
		assert.NoError(t, err)

		assert.Equal(t, 0, response.Code)
		assert.Contains(t, response.Message, "Sync in progress")
	})

	t.Run("UpdateWorkspace", func(t *testing.T) {
		request := types.UpdateWorkspaceRequest{
			Workspace:        testWorkspacePath,
			UseGlobalFilters: false,
		}

		resp := makeRequest(t, "POST", "/api/v1/workspace/update", request)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response types.UpdateWorkspaceResponse
		err := json.NewDecoder(resp.Body).Decode(&response)
		assert.NoError(t, err)

		assert.Equal(t, 0, response.Code)
	})
}

func testSearchBeforeUpdate(t *testing.T) {
	var runCase = func(query string, matches map[string]int) {
		t.Helper()

		request := types.SearchContentRequest{
			Workspace: testWorkspacePath,
			Query:     query,
			Limit: &types.SearchLimit{
				MaxResults: 10,
			},
		}

		resp := makeRequest(t, "POST", "/api/v1/search/content", request)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response types.SearchContentResponse
		err := json.NewDecoder(resp.Body).Decode(&response)
		assert.NoError(t, err)
		assert.Equal(t, 0, response.Code)

		assert.Equal(t, len(matches), len(response.Data.Results))
		for _, result := range response.Data.Results {
			name := filepath.ToSlash(result.File)
			assert.Contains(t, matches, name)
			assert.Equal(t, matches[name], len(result.Lines), "file `%s`", result.File)
		}
	}

	runCase("original main", map[string]int{"main.go": 1})
	runCase(`Hello World`, map[string]int{"main.go": 2})
	runCase(`"Hello World"`, map[string]int{})
	runCase(`"Hello, World"`, map[string]int{"main.go": 1})
	runCase("fmt.Println", map[string]int{"main.go": 3})
	runCase("main", map[string]int{"main.go": 3, "README.md": 1, "utils.go": 1})
	runCase("func", map[string]int{"main.go": 2, "README.md": 1, "utils.go": 2, "subdirectory/nested.js": 2})
	runCase("This is not exists", map[string]int{})
	runCase("return numbers.reduce((sum", map[string]int{"subdirectory/nested.js": 1})
	runCase(`fmt.Println("Hello, World!")`, map[string]int{"main.go": 1})
	runCase("func divide(", map[string]int{})
	runCase("1.2.3", map[string]int{"config.yaml": 1})
	runCase("2.3", map[string]int{"config.yaml": 1})
}

func testDocumentOperations(t *testing.T) {
	t.Run("UpdateDocument", func(t *testing.T) {
		// Create a new test file
		newFilePath := filepath.Join(testWorkspacePath, "new_file.txt")
		err := os.WriteFile(newFilePath, []byte("This is a new test file content for indexing."), 0644)
		assert.NoError(t, err)

		request := types.DocumentUpdateRequest{
			Workspace: testWorkspacePath,
			Path:      "new_file.txt",
		}

		resp := makeRequest(t, "POST", "/api/v1/document/update", request)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response types.CommonResponse
		err = json.NewDecoder(resp.Body).Decode(&response)
		assert.NoError(t, err)

		assert.Equal(t, 0, response.Code)
		assert.Equal(t, "Ok", response.Message)
	})

	t.Run("ModifyAndUpdateDocument", func(t *testing.T) {
		// Modify existing file
		modifiedContent := `package main

import "fmt"

func main() {
	fmt.Println("Hello, Modified World!")
	result := multiply(3, 4)
	fmt.Printf("Result: %d\n", result)
}

func multiply(a, b int) int {
	return a * b
}

func divide(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}`

		err := os.WriteFile(filepath.Join(testWorkspacePath, "main.go"), []byte(modifiedContent), 0644)
		assert.NoError(t, err)

		request := types.DocumentUpdateRequest{
			Workspace: testWorkspacePath,
			Path:      "main.go",
		}

		resp := makeRequest(t, "POST", "/api/v1/document/update", request)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response types.CommonResponse
		err = json.NewDecoder(resp.Body).Decode(&response)
		assert.NoError(t, err)

		assert.Equal(t, 0, response.Code)
	})

	t.Run("DeleteDocument", func(t *testing.T) {
		request := types.DocumentDeleteRequest{
			Workspace: testWorkspacePath,
			Path:      "new_file.txt",
		}

		resp := makeRequest(t, "POST", "/api/v1/document/delete", request)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response types.CommonResponse
		err := json.NewDecoder(resp.Body).Decode(&response)
		assert.NoError(t, err)

		assert.Equal(t, 0, response.Code)
		assert.Equal(t, "Ok", response.Message)
	})
}

func testSearchOperations(t *testing.T) {
	t.Run("SearchContent", func(t *testing.T) {
		testCases := []struct {
			name          string
			query         string
			expectedFiles int
			expectedTerms []string
		}{
			{
				name:          "SimpleSearch",
				query:         "fmt.Println",
				expectedFiles: 1,
				expectedTerms: []string{"fmt.Println"},
			},
			{
				name:          "FunctionSearch",
				query:         "multiply",
				expectedFiles: 1,
				expectedTerms: []string{"multiply"},
			},
			{
				name:          "MultiWordSearch",
				query:         "package main",
				expectedFiles: 2,
				expectedTerms: []string{"package", "main"},
			},
			{
				name:          "StringSearch",
				query:         "strings.ToUpper",
				expectedFiles: 1,
				expectedTerms: []string{"strings", "ToUpper"},
			},
			{
				name:          "MarkdownSearch",
				query:         "Features",
				expectedFiles: 1,
				expectedTerms: []string{"Features"},
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				request := types.SearchContentRequest{
					Workspace: testWorkspacePath,
					Query:     tc.query,
					Limit: &types.SearchLimit{
						MaxResults:        100,
						MaxResultsPerFile: 10,
					},
				}

				resp := makeRequest(t, "POST", "/api/v1/search/content", request)
				defer resp.Body.Close()

				assert.Equal(t, http.StatusOK, resp.StatusCode)

				var response types.SearchContentResponse
				err := json.NewDecoder(resp.Body).Decode(&response)
				assert.NoError(t, err)

				assert.Equal(t, 0, response.Code)
				if tc.expectedFiles > 0 {
					assert.True(t, len(response.Data.Results) >= tc.expectedFiles,
						fmt.Sprintf("Expected at least %d files, got %d", tc.expectedFiles, len(response.Data.Results)))
				}
			})
		}
	})

	t.Run("SearchContentWithFilters", func(t *testing.T) {
		request := types.SearchContentRequest{
			Workspace: testWorkspacePath,
			Query:     "function",
			Filters: &types.SearchFilters{
				Include: "*.js",
			},
			Limit: &types.SearchLimit{
				MaxResults:        50,
				MaxResultsPerFile: 5,
			},
		}

		resp := makeRequest(t, "POST", "/api/v1/search/content", request)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response types.SearchContentResponse
		err := json.NewDecoder(resp.Body).Decode(&response)
		assert.NoError(t, err)

		assert.Equal(t, 0, response.Code)

		// Should find results in JavaScript files
		for _, result := range response.Data.Results {
			assert.True(t, strings.HasSuffix(result.File, ".js"),
				fmt.Sprintf("File %s should be a JavaScript file", result.File))
		}
	})

	t.Run("SearchContentCaseSensitive", func(t *testing.T) {
		request := types.SearchContentRequest{
			Workspace:     testWorkspacePath,
			Query:         "MAIN",
			CaseSensitive: true,
			Limit: &types.SearchLimit{
				MaxResults: 50,
			},
		}

		resp := makeRequest(t, "POST", "/api/v1/search/content", request)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response types.SearchContentResponse
		err := json.NewDecoder(resp.Body).Decode(&response)
		assert.NoError(t, err)

		assert.Equal(t, 0, response.Code)
		// Should have fewer results than case-insensitive search
	})

	t.Run("SearchContentAfterUpdate", func(t *testing.T) {
		var runCase = func(query string, matches map[string]int) {
			t.Helper()

			request := types.SearchContentRequest{
				Workspace: testWorkspacePath,
				Query:     query,
				Limit: &types.SearchLimit{
					MaxResults: 10,
				},
			}

			resp := makeRequest(t, "POST", "/api/v1/search/content", request)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)

			var response types.SearchContentResponse
			err := json.NewDecoder(resp.Body).Decode(&response)
			assert.NoError(t, err)
			assert.Equal(t, 0, response.Code)

			assert.Equal(t, len(matches), len(response.Data.Results))
			for _, result := range response.Data.Results {
				name := filepath.ToSlash(result.File)
				assert.Contains(t, matches, name)
				assert.Equal(t, matches[name], len(result.Lines), "file `%s`", result.File)
			}
		}

		runCase("original main", map[string]int{})
		runCase("fmt.Println", map[string]int{"main.go": 1})
		runCase("main", map[string]int{"main.go": 2, "README.md": 1, "utils.go": 1})
		runCase("func", map[string]int{"main.go": 3, "README.md": 1, "utils.go": 2, "subdirectory/nested.js": 2})
		runCase("This is not exists", map[string]int{})
		runCase("return numbers.reduce((sum", map[string]int{"subdirectory/nested.js": 1})
		runCase(`fmt.Println("Hello, World!")`, map[string]int{})
		runCase("func divide(", map[string]int{"main.go": 1})
	})

	t.Run("SearchFiles", func(t *testing.T) {
		testCases := []struct {
			name          string
			query         string
			expectedFiles []string
		}{
			{
				name:          "GoFiles",
				query:         ".go",
				expectedFiles: []string{"main.go", "utils.go"},
			},
			{
				name:          "ConfigFiles",
				query:         "config",
				expectedFiles: []string{"config.yaml"},
			},
			{
				name:          "MarkdownFiles",
				query:         ".md",
				expectedFiles: []string{"README.md"},
			},
			{
				name:          "JavaScriptFiles",
				query:         ".js",
				expectedFiles: []string{"nested.js"},
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				request := types.SearchFilesRequest{
					Workspace: testWorkspacePath,
					Query:     tc.query,
					Limit:     50,
				}

				resp := makeRequest(t, "POST", "/api/v1/search/files", request)
				defer resp.Body.Close()

				assert.Equal(t, http.StatusOK, resp.StatusCode)

				var response types.SearchFilesResponse
				err := json.NewDecoder(resp.Body).Decode(&response)
				assert.NoError(t, err)
				assert.Equal(t, 0, response.Code)

				// Check if expected files are in results
				for _, expectedFile := range tc.expectedFiles {
					found := false
					for _, resultFile := range response.Data.Files {
						if strings.Contains(resultFile, expectedFile) {
							found = true
							break
						}
					}
					assert.True(t, found, fmt.Sprintf("Expected file %s not found in results", expectedFile))
				}
			})
		}
	})

	t.Run("SearchContentStreaming", func(t *testing.T) {
		request := types.SearchContentRequest{
			Workspace: testWorkspacePath,
			Query:     "fmt",
			Limit: &types.SearchLimit{
				MaxResults: 50,
			},
		}

		jsonBody, err := json.Marshal(request)
		assert.NoError(t, err)

		req, err := http.NewRequest("POST", testServerURL+"/api/v1/search/content", bytes.NewBuffer(jsonBody))
		assert.NoError(t, err)

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")

		resp, err := http.DefaultClient.Do(req)
		assert.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

		// Read streaming response
		body, err := io.ReadAll(resp.Body)
		assert.NoError(t, err)

		content := string(body)
		assert.Contains(t, content, "event:result")
		assert.Contains(t, content, "event:done")
	})
}

func testWorkspaceIndexing(t *testing.T) {
	t.Run("SyncAllWorkspaces", func(t *testing.T) {
		resp := makeRequest(t, "POST", "/api/v1/workspace/sync-all", nil)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response types.CommonResponse
		err := json.NewDecoder(resp.Body).Decode(&response)
		assert.NoError(t, err)

		assert.Equal(t, 0, response.Code)
		assert.Contains(t, response.Message, "Sync all in progress")
		waitForIndexingDone(t, testWorkspacePath, 5*time.Second)
	})

	t.Run("VerifyIndexingAfterFileOperations", func(t *testing.T) {
		// Create a new file with specific content
		testContent := "This is a unique test content for indexing verification"
		testFile := filepath.Join(testWorkspacePath, "indexing_test.txt")
		err := os.WriteFile(testFile, []byte(testContent), 0644)
		assert.NoError(t, err)

		// Update the document in index
		updateRequest := types.DocumentUpdateRequest{
			Workspace: testWorkspacePath,
			Path:      "indexing_test.txt",
		}

		resp := makeRequest(t, "POST", "/api/v1/document/update", updateRequest)
		resp.Body.Close()

		// Poll until the document appears in search results.
		searchRequest := types.SearchContentRequest{
			Workspace: testWorkspacePath,
			Query:     "unique test content",
			Limit: &types.SearchLimit{
				MaxResults: 10,
			},
		}

		var searchResponse types.SearchContentResponse
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			resp = makeRequest(t, "POST", "/api/v1/search/content", searchRequest)
			err = json.NewDecoder(resp.Body).Decode(&searchResponse)
			resp.Body.Close()
			assert.NoError(t, err)
			if len(searchResponse.Data.Results) > 0 {
				break
			}
			time.Sleep(30 * time.Millisecond)
		}

		assert.Equal(t, 0, searchResponse.Code)
		assert.True(t, len(searchResponse.Data.Results) > 0, "Should find the indexed content")

		// Verify the correct file is found
		found := false
		for _, result := range searchResponse.Data.Results {
			if strings.Contains(result.File, "indexing_test.txt") {
				found = true
				break
			}
		}
		assert.True(t, found, "Should find the test file in search results")
	})

	t.Run("DeleteWorkspace", func(t *testing.T) {
		request := types.DeleteWorkspaceRequest{
			Workspace: testWorkspacePath,
		}

		resp := makeRequest(t, "POST", "/api/v1/workspace/delete", request)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var response types.DeleteWorkspaceResponse
		err := json.NewDecoder(resp.Body).Decode(&response)
		assert.NoError(t, err)

		assert.Equal(t, 0, response.Code)
		assert.Equal(t, testWorkspacePath, response.Data.Path)
		assert.False(t, response.Data.Indexing, "workspace should not be indexing after deletion")
	})

	t.Run("VerifyWorkspaceDeleted", func(t *testing.T) {
		request := types.GetWorkspaceRequest{
			Workspace: testWorkspacePath,
		}

		resp := makeRequest(t, "POST", "/api/v1/workspace/get", request)
		defer resp.Body.Close()

		var response types.GetWorkspaceResponse
		err := json.NewDecoder(resp.Body).Decode(&response)
		assert.NoError(t, err)

		// Should return error code indicating workspace not found
		assert.NotEqual(t, 0, response.Code)
	})
}

func testSearchUnsavedFiles(t *testing.T) {
	// 1. Modify main.go in memory without saving
	unsavedContent := `package main

import "fmt"

func main() {
	fmt.Println("This is the unsaved version of main.go")
}
`
	// Use a path with a different separator to test normalization
	unsavedFilePath := "main.go"
	if os.PathSeparator == '\\' {
		unsavedFilePath = strings.ReplaceAll(unsavedFilePath, "/", "\\")
	}

	searchReq := types.SearchContentRequest{
		Workspace: testWorkspacePath,
		Query:     "unsaved version",
		UnsavedFiles: []types.UnsavedFile{
			{
				Path:    unsavedFilePath,
				Content: unsavedContent,
			},
		},
	}

	searchResp := makeRequest(t, http.MethodPost, "/api/v1/search/content", searchReq)
	assert.Equal(t, http.StatusOK, searchResp.StatusCode)

	var searchResponse types.SearchContentResponse
	err := json.NewDecoder(searchResp.Body).Decode(&searchResponse)
	assert.NoError(t, err)

	searchResults := searchResponse.Data.Results
	// We should find a match in the unsaved content
	assert.Len(t, searchResults, 1, "Should find one matching file")
	foundUnsavedMatch := false
	for _, result := range searchResults {
		if filepath.ToSlash(result.File) == "main.go" {
			foundUnsavedMatch = true
			assert.Len(t, result.Lines, 1, "Should find one matching line in unsaved file")
			if len(result.Lines) > 0 {
				assert.Contains(t, result.Lines[0].Line.Content, "unsaved version", "The content should be from the unsaved version")
			}
		}
	}
	assert.True(t, foundUnsavedMatch, "Did not find match in unsaved main.go")

	// 2. Now search for content that only exists in the original file on disk
	searchReqDisk := types.SearchContentRequest{
		Workspace: testWorkspacePath,
		Query:     "original main.go",
		UnsavedFiles: []types.UnsavedFile{
			{
				Path:    unsavedFilePath,
				Content: unsavedContent,
			},
		},
	}

	searchRespDisk := makeRequest(t, http.MethodPost, "/api/v1/search/content", searchReqDisk)
	assert.Equal(t, http.StatusOK, searchRespDisk.StatusCode)

	var searchResponseDisk types.SearchContentResponse
	err = json.NewDecoder(searchRespDisk.Body).Decode(&searchResponseDisk)
	assert.NoError(t, err)

	// We should NOT find a match, because the unsaved version should take precedence
	assert.Len(t, searchResponseDisk.Data.Results, 0, "Should not find matches in the on-disk version of a file that is unsaved")
}

func testServerEdgeCases(t *testing.T) {
	t.Run("InvalidWorkspacePath", func(t *testing.T) {
		request := types.CreateWorkspaceRequest{
			Workspace: "invalid/relative/path",
		}

		resp := makeRequest(t, "POST", "/api/v1/workspace/create", request)
		defer resp.Body.Close()

		var response types.CreateWorkspaceResponse
		err := json.NewDecoder(resp.Body).Decode(&response)
		assert.NoError(t, err)

		assert.NotEqual(t, 0, response.Code)
	})

	t.Run("NonExistentWorkspaceSearch", func(t *testing.T) {
		request := types.SearchContentRequest{
			Workspace: "/non/existent/path",
			Query:     "test",
		}

		resp := makeRequest(t, "POST", "/api/v1/search/content", request)
		defer resp.Body.Close()

		var response types.SearchContentResponse
		err := json.NewDecoder(resp.Body).Decode(&response)
		assert.NoError(t, err)

		assert.NotEqual(t, 0, response.Code)
	})

	t.Run("EmptySearchQuery", func(t *testing.T) {
		request := types.SearchContentRequest{
			Workspace: testWorkspacePath,
			Query:     "",
		}

		resp := makeRequest(t, "POST", "/api/v1/search/content", request)
		defer resp.Body.Close()

		var response types.SearchContentResponse
		err := json.NewDecoder(resp.Body).Decode(&response)
		assert.NoError(t, err)

		assert.NotEqual(t, 0, response.Code)
		assert.Contains(t, response.Message, "Query is required")
	})
}
