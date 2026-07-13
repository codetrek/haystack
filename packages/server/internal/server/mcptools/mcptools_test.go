package mcptools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codetrek/haystack/packages/core/collection"
	"github.com/codetrek/haystack/packages/core/documents"
	"github.com/codetrek/haystack/packages/core/idtable"
	"github.com/codetrek/haystack/packages/core/invertedindex"
	"github.com/codetrek/haystack/packages/core/queue"
	"github.com/codetrek/haystack/server/internal/conf"
	"github.com/codetrek/haystack/server/internal/core/storage"
	"github.com/codetrek/haystack/server/internal/core/symbols"
	"github.com/codetrek/haystack/server/internal/core/workspace"
	"github.com/codetrek/haystack/server/internal/server/indexer"
	"github.com/codetrek/haystack/server/internal/server/searcher"
	"github.com/codetrek/haystack/server/internal/shared/running"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
)

// --- Test infrastructure ---

var (
	testWorkspacePath string
	testSetupOnce     sync.Once
	testCleanup       func()
)

func setupMCPTestEnv(t *testing.T) {
	t.Helper()
	testSetupOnce.Do(func() {
		tempDir := t.TempDir()
		testWorkspacePath = filepath.Join(tempDir, "mcp_test_workspace")
		if !assert.NoError(t, os.MkdirAll(testWorkspacePath, 0755)) {
			return
		}

		// Configure
		conf.Get().Global.DataPath = filepath.Join(tempDir, "mcp_test_data")
		conf.Get().Server.CacheSize = 8 * 1024 * 1024
		iiOpts := invertedindex.Options{
			FlushTicker:        50 * time.Millisecond,
			FlushWaitTimeout:   1 * time.Microsecond,
			FlushWaitBatchSize: 10,
			FlushCooldown:      50 * time.Millisecond,
		}

		// Create test files
		testFiles := map[string]string{
			"main.go": `package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}

func add(a, b int) int {
	return a + b
}`,
			"utils.go": `package main

func processString(input string) string {
	return input
}`,
			"README.md": `# Test Project
This is a test project.`,
			"sub/nested.js": `function calculateSum(numbers) {
	return numbers.reduce((sum, num) => sum + num, 0);
}`,
		}

		for filename, content := range testFiles {
			fullPath := filepath.Join(testWorkspacePath, filename)
			if !assert.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0755)) {
				return
			}
			if !assert.NoError(t, os.WriteFile(fullPath, []byte(content), 0644)) {
				return
			}
		}

		// Start engine
		wg := &sync.WaitGroup{}
		running.InitShutdown(wg)

		db, err := storage.Open(filepath.Join(conf.Get().Global.DataPath, "data"), conf.Get().Server.CacheSize)
		if !assert.NoError(t, err) {
			return
		}
		indexdb, err := storage.Open(filepath.Join(conf.Get().Global.DataPath, "index"), conf.Get().Server.CacheSize)
		if !assert.NoError(t, err) {
			return
		}

		mpsc := queue.NewMpsc("MCPTestDBQueue")
		mpsc.Start()

		idx, err := invertedindex.New(indexdb, mpsc, iiOpts)
		if !assert.NoError(t, err) {
			return
		}
		st, stErr := documents.New(db, mpsc, idx, documents.Options{})
		if !assert.NoError(t, stErr) {
			return
		}
		indexer.SetDocStore(st)
		workspace.SetDocStore(st)
		cat, catErr := collection.New(db, st, collection.Options{})
		if !assert.NoError(t, catErr) {
			return
		}
		if !assert.NoError(t, workspace.Init(cat)) {
			return
		}
		if !assert.NoError(t, symbols.Init(db, mpsc, idx)) {
			return
		}

		alloc, allocErr := idtable.New(db, idtable.Options{})
		if !assert.NoError(t, allocErr) {
			return
		}
		indexer.SetIdAllocator(alloc)

		indexer.Run(wg)
		searcher.Run(wg, idx, st)

		// Create and index workspace
		_, err = indexer.CreateWorkspace(testWorkspacePath, true, nil)
		if !assert.NoError(t, err) {
			return
		}

		// Poll until indexing completes instead of a fixed sleep.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			ws, wsErr := workspace.GetByPath(testWorkspacePath)
			if wsErr == nil && !ws.GetLastFullSync().IsZero() && ws.GetIndexingProgress() == nil {
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		// Wait for inverted index flush ticker + cooldown to commit.
		time.Sleep(200 * time.Millisecond)

		testCleanup = func() {
			running.Shutdown()
			wg.Wait()
			st.CloseAndWait()
			workspace.SetDocStore(nil)
			indexer.SetDocStore(nil)
			idx.CloseAndWait()
			symbols.CloseAndWait()
			mpsc.Stop()
			alloc.Close()
			db.Close()
			indexdb.Close()
		}
	})

	t.Cleanup(func() {
		// Only run after all tests in the package
		// testCleanup will be called at package exit via TestMain if needed
	})
}

func makeMCPRequest(args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "HaystackSearch",
			Arguments: args,
		},
	}
}

// --- B Group: SearchContent MCP Tests ---

func TestMCPSearchContent(t *testing.T) {
	setupMCPTestEnv(t)
	defer func() {
		if testCleanup != nil {
			testCleanup()
			testCleanup = nil
		}
	}()

	t.Run("ValidQuery_HasResults", func(t *testing.T) {
		req := makeMCPRequest(map[string]any{
			"query":     "fmt.Println",
			"workspace": testWorkspacePath,
		})

		result, err := SearchContent(context.Background(), req)
		if !assert.NoError(t, err) {
			return
		}
		if !assert.NotNil(t, result) {
			return
		}
		assert.True(t, len(result.Content) > 0)

		text := result.Content[0].(mcp.TextContent).Text
		assert.Contains(t, text, "Found")
		assert.NotContains(t, text, "No results found")
	})

	t.Run("ValidQuery_NoMatch", func(t *testing.T) {
		req := makeMCPRequest(map[string]any{
			"query":     "thisStringDefinitelyDoesNotExistAnywhere",
			"workspace": testWorkspacePath,
		})

		result, err := SearchContent(context.Background(), req)
		if !assert.NoError(t, err) {
			return
		}
		if !assert.NotNil(t, result) {
			return
		}

		text := result.Content[0].(mcp.TextContent).Text
		assert.Contains(t, text, "Found 0 results")
	})

	t.Run("WorkspaceNotFound", func(t *testing.T) {
		req := makeMCPRequest(map[string]any{
			"query":     "test",
			"workspace": "/nonexistent/workspace/path",
		})

		_, err := SearchContent(context.Background(), req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "workspace")
	})

	t.Run("MissingQuery", func(t *testing.T) {
		req := makeMCPRequest(map[string]any{
			"workspace": testWorkspacePath,
		})

		_, err := SearchContent(context.Background(), req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid arguments")
	})

	t.Run("MissingWorkspace", func(t *testing.T) {
		req := makeMCPRequest(map[string]any{
			"query": "test",
		})

		_, err := SearchContent(context.Background(), req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid arguments")
	})

	t.Run("RelativeWorkspacePath", func(t *testing.T) {
		req := makeMCPRequest(map[string]any{
			"query":     "test",
			"workspace": "relative/path",
		})

		_, err := SearchContent(context.Background(), req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not absolute")
	})

	t.Run("AbsolutePathFilter_Rejected", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Name: "HaystackSearch",
				Arguments: map[string]any{
					"query":     "fmt",
					"workspace": testWorkspacePath,
					"path":      "/absolute/not/allowed",
				},
			},
		}

		_, err := SearchContent(context.Background(), req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "absolute")
	})

	t.Run("FilterByExtension", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Name: "HaystackSearch",
				Arguments: map[string]any{
					"query":     "func",
					"workspace": testWorkspacePath,
					"filter":    "*.js",
				},
			},
		}

		result, err := SearchContent(context.Background(), req)
		if !assert.NoError(t, err) {
			return
		}
		if !assert.NotNil(t, result) {
			return
		}

		text := result.Content[0].(mcp.TextContent).Text
		// If there are results, they should only be from .js files
		if !strings.Contains(text, "Found 0 results") {
			assert.Contains(t, text, ".js")
			assert.NotContains(t, text, "main.go")
			assert.NotContains(t, text, "utils.go")
		}
	})

	t.Run("ContextCancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		req := makeMCPRequest(map[string]any{
			"query":     "fmt",
			"workspace": testWorkspacePath,
		})

		// Should not panic, may return partial/empty results
		result, err := SearchContent(ctx, req)
		if err == nil {
			assert.NotNil(t, result)
		}
	})

	t.Run("LimitResults", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Name: "HaystackSearch",
				Arguments: map[string]any{
					"query":     "func",
					"workspace": testWorkspacePath,
					"limit":     float64(1),
				},
			},
		}

		result, err := SearchContent(context.Background(), req)
		if !assert.NoError(t, err) {
			return
		}
		if !assert.NotNil(t, result) {
			return
		}
		// With limit=1, results should be limited
		assert.True(t, len(result.Content) > 0)
	})

	// --- C Group: SearchFiles MCP Tests ---

	t.Run("SearchFiles_FuzzyMatch", func(t *testing.T) {
		req := makeMCPRequest(map[string]any{
			"query":     "main",
			"workspace": testWorkspacePath,
		})

		result, err := SearchFiles(context.Background(), req)
		if !assert.NoError(t, err) {
			return
		}
		if !assert.NotNil(t, result) {
			return
		}

		// Should find main.go
		allText := ""
		for _, c := range result.Content {
			if tc, ok := c.(mcp.TextContent); ok {
				allText += tc.Text + "\n"
			}
		}
		assert.Contains(t, allText, "main.go")
	})

	t.Run("SearchFiles_NoMatch", func(t *testing.T) {
		req := makeMCPRequest(map[string]any{
			"query":     "zzzznonexistentfilename",
			"workspace": testWorkspacePath,
		})

		result, err := SearchFiles(context.Background(), req)
		if !assert.NoError(t, err) {
			return
		}
		if !assert.NotNil(t, result) {
			return
		}

		text := result.Content[0].(mcp.TextContent).Text
		assert.Contains(t, text, "Found 0 files")
	})

	t.Run("SearchFiles_WorkspaceNotFound", func(t *testing.T) {
		req := makeMCPRequest(map[string]any{
			"query":     "main",
			"workspace": "/nonexistent/path",
		})

		_, err := SearchFiles(context.Background(), req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "workspace")
	})

	t.Run("SearchFiles_MissingArgs", func(t *testing.T) {
		req := makeMCPRequest(map[string]any{
			"query": "test",
		})

		_, err := SearchFiles(context.Background(), req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid arguments")
	})

	t.Run("SearchFiles_LimitTruncation", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Name: "HaystackFiles",
				Arguments: map[string]any{
					"query":     ".go",
					"workspace": testWorkspacePath,
					"limit":     float64(1),
				},
			},
		}

		result, err := SearchFiles(context.Background(), req)
		if !assert.NoError(t, err) {
			return
		}
		if !assert.NotNil(t, result) {
			return
		}

		// First content is "Found N files.", file entries follow
		// With limit=1, should have at most 1 file entry after the header
		fileCount := 0
		for _, c := range result.Content {
			if tc, ok := c.(mcp.TextContent); ok {
				if !strings.HasPrefix(tc.Text, "Found") && !strings.HasPrefix(tc.Text, "No results") {
					fileCount++
				}
			}
		}
		assert.LessOrEqual(t, fileCount, 1, "should respect limit=1")
	})
}
