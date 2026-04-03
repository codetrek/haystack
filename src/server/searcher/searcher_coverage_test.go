package searcher

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codetrek/haystack/conf"
	"github.com/codetrek/haystack/internal/testutil"
	"github.com/codetrek/haystack/server/core/documents"
	"github.com/codetrek/haystack/server/core/idtable"
	"github.com/codetrek/haystack/server/core/invertedindex"
	"github.com/codetrek/haystack/server/core/symbols"
	"github.com/codetrek/haystack/server/core/workspace"
	"github.com/codetrek/haystack/server/indexer"
	"github.com/codetrek/haystack/shared/running"
	"github.com/codetrek/haystack/shared/types"
	"github.com/stretchr/testify/assert"
)

// =========================================================================
// query_parser.go – uncovered paths (no DB needed)
// =========================================================================

func TestQuerySearchFiles(t *testing.T) {
	q, err := ParseQuery("hello world")
	assert.NoError(t, err)
	assert.NotNil(t, q)

	sr, err := q.SearchFiles()
	assert.NoError(t, err)
	assert.NotNil(t, sr)
}

func TestOrExpressionSearchFiles(t *testing.T) {
	q, err := ParseQuery("a OR b")
	assert.NoError(t, err)

	input := &invertedindex.SearchResult{}
	result, err := q.Expression.SearchFiles(input)
	assert.NoError(t, err)
	assert.Equal(t, input, result)
}

func TestAndExpressionSearchFiles(t *testing.T) {
	q, err := ParseQuery("a AND b")
	assert.NoError(t, err)

	input := &invertedindex.SearchResult{}
	result := q.Expression.Left.SearchFiles(input)
	assert.Nil(t, result)
}

func TestTermSearchFiles(t *testing.T) {
	q, err := ParseQuery("hello")
	assert.NoError(t, err)

	input := &invertedindex.SearchResult{}
	result := q.Expression.Left.Left.SearchFiles(input)
	assert.Nil(t, result)
}

func TestProcessQuery(t *testing.T) {
	q, err := ParseQuery("hello")
	assert.NoError(t, err)
	processQuery(q)
}

func TestQueryString_Nil(t *testing.T) {
	q := &Query{}
	assert.Equal(t, "", q.String())
}

func TestOrExpressionString_Nil(t *testing.T) {
	var o *OrExpression
	assert.Equal(t, "", o.String())
}

func TestAndExpressionString_Nil(t *testing.T) {
	var a *AndExpression
	assert.Equal(t, "", a.String())
}

func TestTermString_Nil(t *testing.T) {
	var term *Term
	assert.Equal(t, "", term.String())
}

func TestTermString_WithNot(t *testing.T) {
	q, err := ParseQuery("NOT hello")
	assert.NoError(t, err)
	s := q.String()
	assert.Contains(t, s, "NOT")
	assert.Contains(t, s, "hello")
}

func TestTermString_WithWildcard(t *testing.T) {
	q, err := ParseQuery("hello*")
	assert.NoError(t, err)
	s := q.String()
	assert.Contains(t, s, "hello*")
}

func TestTermString_WithQuoted(t *testing.T) {
	q, err := ParseQuery(`"hello world"`)
	assert.NoError(t, err)
	s := q.String()
	assert.Contains(t, s, `"hello world"`)
}

func TestAndExpressionString_WithOp(t *testing.T) {
	q, err := ParseQuery("a AND b")
	assert.NoError(t, err)
	assert.Equal(t, "a AND b", q.String())
}

func TestAndExpressionString_WithoutOp(t *testing.T) {
	q, err := ParseQuery("a b")
	assert.NoError(t, err)
	assert.Equal(t, "a b", q.String())
}

func TestOrExpressionString_WithRight(t *testing.T) {
	q, err := ParseQuery("a OR b")
	assert.NoError(t, err)
	assert.Equal(t, "a OR b", q.String())
}

// =========================================================================
// simple_content_search_engine.go – uncovered edge cases (no DB needed)
// =========================================================================

func TestAndClauseIsLineMatch_EmptyAndTerms(t *testing.T) {
	clause := &SimpleContentSearchEngineAndClause{
		AndTerms: []*SimpleContentSearchEngineTerm{},
	}
	result := clause.IsLineMatch("hello world")
	assert.Equal(t, [][]int{}, result)
}

func TestAndClauseIsLineMatch_NilRegex(t *testing.T) {
	clause := &SimpleContentSearchEngineAndClause{
		AndTerms: []*SimpleContentSearchEngineTerm{{Pattern: "test"}},
		Regex:    nil,
	}
	result := clause.IsLineMatch("hello world")
	assert.Equal(t, [][]int{}, result)
}

func TestAndClauseIsLineMatch_NoSubmatchIndex(t *testing.T) {
	eng := NewSimpleContentSearchEngine(nil, 24, 32, false)
	err := eng.Compile("testpattern", false)
	assert.NoError(t, err)
	result := eng.IsLineMatch("nothing here")
	assert.Equal(t, [][]int{}, result)
}

func TestCompile_WhitespaceOnly(t *testing.T) {
	eng := NewSimpleContentSearchEngine(nil, 4, 4, false)
	err := eng.Compile("   ", false)
	assert.Error(t, err)
}

func TestCompile_OnlyPipe(t *testing.T) {
	eng := NewSimpleContentSearchEngine(nil, 4, 4, false)
	err := eng.Compile("|", false)
	assert.Error(t, err)
}

func TestCompile_PipePipe(t *testing.T) {
	eng := NewSimpleContentSearchEngine(nil, 4, 4, false)
	err := eng.Compile("| |", false)
	assert.Error(t, err)
}

func TestCompile_AndToken(t *testing.T) {
	eng := NewSimpleContentSearchEngine(nil, 4, 4, false)
	err := eng.Compile("AND", false)
	assert.Error(t, err)
}

func TestFinalizeOrClause_EmptyPatterns(t *testing.T) {
	eng := NewSimpleContentSearchEngine(nil, 4, 4, false)
	_, err := eng.finalizeOrClause(nil, nil, false, "4")
	assert.Error(t, err)
}

func TestEngineString_MultipleOrClauses(t *testing.T) {
	eng := NewSimpleContentSearchEngine(nil, 24, 32, false)
	err := eng.Compile("alpha | beta gamma", false)
	assert.NoError(t, err)
	s := eng.String()
	assert.Contains(t, s, "alpha")
	assert.Contains(t, s, " | ")
	assert.Contains(t, s, "beta")
}

func TestTermString_Simple(t *testing.T) {
	term := &SimpleContentSearchEngineTerm{Pattern: "hello"}
	assert.Equal(t, "hello", term.String())
}

func TestProcessToken_EmptyString(t *testing.T) {
	eng := NewSimpleContentSearchEngine(nil, 4, 4, false)
	result := eng.processToken("", "4")
	assert.Nil(t, result)
}

func TestProcessToken_ANDKeyword(t *testing.T) {
	eng := NewSimpleContentSearchEngine(nil, 4, 4, false)
	result := eng.processToken("AND", "4")
	assert.Nil(t, result)
}

func TestProcessToken_WhitespaceOnly2(t *testing.T) {
	eng := NewSimpleContentSearchEngine(nil, 4, 4, false)
	result := eng.processToken("  ", "4")
	assert.Nil(t, result)
}

// =========================================================================
// searcher.go – searchInContent edge cases (no DB needed)
// =========================================================================

func TestSearchInContent_MaxResultsPerFile(t *testing.T) {
	engine := NewSimpleContentSearchEngine(nil, 24, 32, false)
	engine.Compile("match", false)

	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "this is a match line")
	}
	content := strings.Join(lines, "\n")

	limit := &types.SearchLimit{MaxResultsPerFile: 3, MaxResults: 100}
	totalHits := 0
	result, err := searchInContent("test.txt", strings.NewReader(content), engine, 0, limit, &totalHits)
	assert.NoError(t, err)
	assert.Equal(t, 3, len(result.Lines))
	assert.True(t, result.Truncate)
}

func TestSearchInContent_MaxResults(t *testing.T) {
	engine := NewSimpleContentSearchEngine(nil, 24, 32, false)
	engine.Compile("match", false)

	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "this is a match line")
	}
	content := strings.Join(lines, "\n")

	limit := &types.SearchLimit{MaxResults: 5, MaxResultsPerFile: 1000}
	totalHits := 0
	result, err := searchInContent("test.txt", strings.NewReader(content), engine, 0, limit, &totalHits)
	assert.NoError(t, err)
	assert.Equal(t, 5, len(result.Lines))
}

func TestSearchInContent_NilLimit(t *testing.T) {
	engine := NewSimpleContentSearchEngine(nil, 24, 32, false)
	engine.Compile("hello", false)

	totalHits := 0
	result, err := searchInContent("test.txt", strings.NewReader("hello world\n"), engine, 0, nil, &totalHits)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(result.Lines))
	assert.Equal(t, 1, totalHits)
}

func TestSearchInContent_BeforeContextEdge(t *testing.T) {
	engine := NewSimpleContentSearchEngine(nil, 24, 32, false)
	engine.Compile("target", false)

	content := "target line\nafter1\nafter2\n"
	totalHits := 0
	result, err := searchInContent("test.txt", strings.NewReader(content), engine, 2, nil, &totalHits)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(result.Lines))
	assert.Equal(t, 0, len(result.Lines[0].Before))
	assert.Equal(t, 2, len(result.Lines[0].After))
}

func TestSearchInContent_AfterContextEdge(t *testing.T) {
	engine := NewSimpleContentSearchEngine(nil, 24, 32, false)
	engine.Compile("target", false)

	content := "before1\nbefore2\ntarget line"
	totalHits := 0
	result, err := searchInContent("test.txt", strings.NewReader(content), engine, 2, nil, &totalHits)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(result.Lines))
	assert.Equal(t, 2, len(result.Lines[0].Before))
	assert.Equal(t, 0, len(result.Lines[0].After))
}

func TestSearchInContent_CleanPath(t *testing.T) {
	engine := NewSimpleContentSearchEngine(nil, 24, 32, false)
	engine.Compile("hello", false)

	totalHits := 0
	result, err := searchInContent("./dir/../test.txt", strings.NewReader("hello\n"), engine, 0, nil, &totalHits)
	assert.NoError(t, err)
	assert.Equal(t, "test.txt", result.File)
}

func TestSearchInContent_MultipleMatchesOnOneLine(t *testing.T) {
	engine := NewSimpleContentSearchEngine(nil, 24, 32, false)
	engine.Compile("ab", false)

	content := "ab cd ab ef ab\n"
	totalHits := 0
	result, err := searchInContent("test.txt", strings.NewReader(content), engine, 0, nil, &totalHits)
	assert.NoError(t, err)
	assert.Equal(t, 3, len(result.Lines))
	assert.Equal(t, 3, totalHits)
}

func TestSearchInContent_TotalHitsPreserved(t *testing.T) {
	engine := NewSimpleContentSearchEngine(nil, 24, 32, false)
	engine.Compile("word", false)

	content := "word one\nword two\nword three\n"
	totalHits := 5
	result, err := searchInContent("test.txt", strings.NewReader(content), engine, 0, nil, &totalHits)
	assert.NoError(t, err)
	assert.Equal(t, 3, len(result.Lines))
	assert.Equal(t, 8, totalHits)
}

func TestSearchInContent_LimitFromConf(t *testing.T) {
	savedLimit := conf.Get().Server.Search.Limit
	conf.Get().Server.Search.Limit.MaxResultsPerFile = 2
	conf.Get().Server.Search.Limit.MaxResults = 2
	defer func() { conf.Get().Server.Search.Limit = savedLimit }()

	engine := NewSimpleContentSearchEngine(nil, 24, 32, false)
	engine.Compile("item", false)

	content := "item 1\nitem 2\nitem 3\nitem 4\n"
	totalHits := 0
	result, err := searchInContent("test.txt", strings.NewReader(content), engine, 0, nil, &totalHits)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(result.Lines))
	assert.True(t, result.Truncate)
}

func TestSearchInContent_ZeroBeforeAfter(t *testing.T) {
	engine := NewSimpleContentSearchEngine(nil, 24, 32, false)
	engine.Compile("target", false)

	content := "before\ntarget line\nafter\n"
	totalHits := 0
	result, err := searchInContent("test.txt", strings.NewReader(content), engine, 0, nil, &totalHits)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(result.Lines))
	assert.Nil(t, result.Lines[0].Before)
	assert.Nil(t, result.Lines[0].After)
}

func TestSearchInContent_TruncateMultipleMatchesSameLine(t *testing.T) {
	engine := NewSimpleContentSearchEngine(nil, 24, 32, false)
	engine.Compile("x", false)

	content := "x x x x x x x x x x\n"
	limit := &types.SearchLimit{MaxResultsPerFile: 3, MaxResults: 100}
	totalHits := 0
	result, err := searchInContent("test.txt", strings.NewReader(content), engine, 0, limit, &totalHits)
	assert.NoError(t, err)
	assert.Equal(t, 3, len(result.Lines))
	assert.True(t, result.Truncate)
}

// =========================================================================
// fuzzyMatchWithScore edge cases (no DB needed)
// =========================================================================

func TestFuzzyMatchWithScore_PositionBonus_Delimiter(t *testing.T) {
	matched, score := fuzzyMatchWithScore("test", "my_test_func")
	assert.True(t, matched)
	assert.Equal(t, 100, score)
}

func TestFuzzyMatchWithScore_NoPathSep(t *testing.T) {
	matched, score := fuzzyMatchWithScore("abc", "xyzabc")
	assert.True(t, matched)
	assert.Equal(t, 100, score)
}

func TestFuzzyMatchWithScore_ScoreCapping(t *testing.T) {
	matched, score := fuzzyMatchWithScore("main", "src/main.go")
	assert.True(t, matched)
	assert.LessOrEqual(t, score, 100)
}

func TestFuzzyMatchWithScore_FuzzyWithDelimiter(t *testing.T) {
	matched, score := fuzzyMatchWithScore("mgo", "src/main.go")
	assert.True(t, matched)
	assert.True(t, score > 0)
}

func TestFuzzyMatchWithScore_PathWithBackslash(t *testing.T) {
	matched, score := fuzzyMatchWithScore("test", `src\pkg\test.go`)
	assert.True(t, matched)
	assert.Equal(t, 100, score)
}

func TestFuzzyMatchWithScore_FuzzyLongGap(t *testing.T) {
	matched, score := fuzzyMatchWithScore("ag", "abcdefg")
	assert.True(t, matched)
	assert.True(t, score > 0 && score < 100)
}

func TestFuzzyMatchWithScore_SingleChar(t *testing.T) {
	matched, score := fuzzyMatchWithScore("a", "a")
	assert.True(t, matched)
	assert.Equal(t, 100, score)
}

// =========================================================================
// simple_content_search_engine.go – more compile paths (no DB needed)
// =========================================================================

func TestCompile_WholeWordMultipleTerms(t *testing.T) {
	eng := NewSimpleContentSearchEngine(nil, 24, 32, true)
	err := eng.Compile("test func", false)
	assert.NoError(t, err)

	matches := eng.IsLineMatch("testfunc")
	assert.Equal(t, 0, len(matches))

	matches = eng.IsLineMatch("the test func is here")
	assert.True(t, len(matches) > 0)
}

func TestCompile_CaseSensitive(t *testing.T) {
	eng := NewSimpleContentSearchEngine(nil, 24, 32, false)
	err := eng.Compile("Hello", true)
	assert.NoError(t, err)

	matches := eng.IsLineMatch("hello world")
	assert.Equal(t, 0, len(matches))

	matches = eng.IsLineMatch("Hello world")
	assert.True(t, len(matches) > 0)
}

func TestTokenizeWithQuotes_LeadingTrailingSpaces(t *testing.T) {
	tokens := TokenizeWithQuotes("  hello  world  ")
	assert.Equal(t, []string{"hello", "world"}, tokens)
}

func TestTokenizeWithQuotes_MultiplePipes(t *testing.T) {
	tokens := TokenizeWithQuotes("a | b | c")
	assert.Equal(t, []string{"a", "|", "b", "|", "c"}, tokens)
}

func TestTokenizeWithQuotes_PipeInQuotes(t *testing.T) {
	tokens := TokenizeWithQuotes(`"a | b" c`)
	assert.Equal(t, []string{`"a | b"`, "c"}, tokens)
}

// =========================================================================
// Integration: Run() (no DB needed, just needs running context)
// =========================================================================

func TestRun(t *testing.T) {
	var shutdownWg sync.WaitGroup
	running.InitShutdown(&shutdownWg)

	var wg sync.WaitGroup
	Run(&wg)

	running.Shutdown()
	shutdownWg.Wait()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run goroutine did not stop")
	}
}

// =========================================================================
// SINGLE integration test for the full SearchContent/SearchFiles/
// sortDocuments/SearchSymbols pipeline.
//
// We use a single test function to avoid the singleton indexer problem
// (indexer.Run can only be called once because it uses package-level vars).
// =========================================================================

func TestFullIntegration(t *testing.T) {
	env := testutil.SetupEnv(t, "searcher-integ")

	var shutdownWg sync.WaitGroup
	running.InitShutdown(&shutdownWg)

	if err := idtable.Init(env.DB); err != nil {
		t.Fatalf("idtable.Init: %v", err)
	}
	if err := invertedindex.Init(env.DB, env.Mpsc); err != nil {
		t.Fatalf("invertedindex.Init: %v", err)
	}
	if err := documents.Init(env.DB, env.Mpsc); err != nil {
		t.Fatalf("documents.Init: %v", err)
	}
	if err := symbols.Init(env.DB, env.Mpsc); err != nil {
		t.Fatalf("symbols.Init: %v", err)
	}
	if err := workspace.Init(env.DB); err != nil {
		t.Fatalf("workspace.Init: %v", err)
	}

	// Start the indexer pipeline ONCE.
	var indexerWg sync.WaitGroup
	indexer.Run(&indexerWg)

	// Helper to create a workspace and index files.
	makeWS := func(t *testing.T, files map[string]string) *workspace.Workspace {
		t.Helper()
		wsDir := t.TempDir()
		for relPath, content := range files {
			full := filepath.Join(wsDir, relPath)
			os.MkdirAll(filepath.Dir(full), 0755)
			os.WriteFile(full, []byte(content), 0644)
		}
		ws, err := workspace.Create(wsDir)
		if err != nil {
			t.Fatalf("workspace.Create: %v", err)
		}
		indexer.Sync(ws, false)
		// Poll until the indexing pipeline finishes instead of a fixed sleep.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if !ws.LastFullSync.IsZero() {
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		if ws.LastFullSync.IsZero() {
			t.Fatal("indexing did not complete within timeout")
		}
		// Small buffer for the writer to flush the last batch.
		time.Sleep(30 * time.Millisecond)
		return ws
	}

	// --- Sub-tests that share the same indexer instance ---

	t.Run("SearchContent basic", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go": "package main\nimport \"fmt\"\nfunc main() {\n\tfmt.Println(\"hello world\")\n}\n",
			"util.go": "package main\nfunc helper() string {\n\treturn \"hello world\"\n}\n",
		})

		req := &types.SearchContentRequest{Query: "hello", BeforeAfter: 1}
		ctx := context.Background()
		results, truncated := SearchContent(ws, req, nil, ctx, 10*time.Second)
		assert.False(t, truncated)
		_ = results
	})

	t.Run("SearchContent with editor", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go":     "package main\nfunc main() { hello() }\n",
			"pkg/util.go": "package pkg\nfunc hello() { }\n",
		})

		req := &types.SearchContentRequest{
			Query: "hello",
			Editor: &types.Editor{
				ActiveFile: "pkg/util.go",
				OpenFiles:  []string{"main.go"},
			},
		}
		ctx := context.Background()
		SearchContent(ws, req, nil, ctx, 10*time.Second)
	})

	t.Run("SearchContent with callback", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"test.go": "package main\nfunc test() { hello() }\n",
		})

		callbackCount := 0
		req := &types.SearchContentRequest{Query: "hello"}
		ctx := context.Background()
		SearchContent(ws, req, func(r types.SearchContentResult) { callbackCount++ }, ctx, 10*time.Second)
	})

	t.Run("SearchContent unsaved only", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go": "package main\nfunc main() { }\n",
		})

		req := &types.SearchContentRequest{
			Query:            "unsaved_marker",
			UnsavedFilesOnly: true,
			UnsavedFiles:     []types.UnsavedFile{{Path: "main.go", Content: "unsaved_marker is here\n"}},
		}
		ctx := context.Background()
		results, _ := SearchContent(ws, req, nil, ctx, 10*time.Second)
		assert.Equal(t, 1, len(results))
		assert.Equal(t, 1, len(results[0].Lines))
	})

	t.Run("SearchContent unsaved with callback", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go": "package main\n",
		})

		callbackCount := 0
		req := &types.SearchContentRequest{
			Query:            "findme",
			UnsavedFilesOnly: true,
			UnsavedFiles:     []types.UnsavedFile{{Path: "main.go", Content: "findme is here\n"}},
		}
		ctx := context.Background()
		results, _ := SearchContent(ws, req, func(r types.SearchContentResult) { callbackCount++ }, ctx, 10*time.Second)
		assert.Equal(t, 1, len(results))
		assert.Equal(t, 1, callbackCount)
	})

	t.Run("SearchContent with filters", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go":           "package main\nfunc hello() {}\n",
			"test/util_test.go": "package test\nfunc TestHello() { hello() }\n",
		})

		req := &types.SearchContentRequest{
			Query:   "hello",
			Filters: &types.SearchFilters{Include: "*.go", Exclude: "*_test.go"},
		}
		ctx := context.Background()
		results, _ := SearchContent(ws, req, nil, ctx, 10*time.Second)
		for _, r := range results {
			assert.False(t, strings.HasSuffix(r.File, "_test.go"))
		}
	})

	t.Run("SearchContent with path filter", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"src/main.go":  "package main\nfunc hello() {}\n",
			"test/test.go": "package test\nfunc hello() {}\n",
		})

		req := &types.SearchContentRequest{
			Query:   "hello",
			Filters: &types.SearchFilters{Path: "src"},
		}
		ctx := context.Background()
		results, _ := SearchContent(ws, req, nil, ctx, 10*time.Second)
		for _, r := range results {
			assert.True(t, strings.HasPrefix(r.File, "src"))
		}
	})

	t.Run("SearchContent with limit", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go": "package main\nfunc hello() {}\nfunc hello2() {}\n",
		})

		req := &types.SearchContentRequest{
			Query: "hello",
			Limit: &types.SearchLimit{MaxResults: 1, MaxResultsPerFile: 1},
		}
		ctx := context.Background()
		SearchContent(ws, req, nil, ctx, 10*time.Second)
	})

	t.Run("SearchContent empty query", func(t *testing.T) {
		ws := makeWS(t, map[string]string{"main.go": "package main\n"})

		req := &types.SearchContentRequest{Query: ""}
		ctx := context.Background()
		results, _ := SearchContent(ws, req, nil, ctx, 10*time.Second)
		assert.Equal(t, 0, len(results))
	})

	t.Run("SearchContent cancelled context", func(t *testing.T) {
		ws := makeWS(t, map[string]string{"main.go": "package main\nfunc hello() {}\n"})

		req := &types.SearchContentRequest{Query: "hello"}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		SearchContent(ws, req, nil, ctx, 10*time.Second)
	})

	t.Run("SearchContent case sensitive", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go": "package main\nfunc Hello() {}\nfunc hello() {}\n",
		})

		req := &types.SearchContentRequest{Query: "Hello", CaseSensitive: true}
		ctx := context.Background()
		SearchContent(ws, req, nil, ctx, 10*time.Second)
	})

	t.Run("SearchContent beforeAfter clamping", func(t *testing.T) {
		ws := makeWS(t, map[string]string{"main.go": "package main\nfunc hello() {}\n"})
		ctx := context.Background()

		req1 := &types.SearchContentRequest{Query: "hello", BeforeAfter: -5}
		SearchContent(ws, req1, nil, ctx, 10*time.Second)

		req2 := &types.SearchContentRequest{Query: "hello", BeforeAfter: 100}
		SearchContent(ws, req2, nil, ctx, 10*time.Second)
	})

	t.Run("SearchContent whole word", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go": "package main\nvar test = 1\nfunc testing() {}\n",
		})

		req := &types.SearchContentRequest{Query: "test", WholeWord: true}
		ctx := context.Background()
		SearchContent(ws, req, nil, ctx, 10*time.Second)
	})

	t.Run("SearchContent unsaved file filtered out", func(t *testing.T) {
		ws := makeWS(t, map[string]string{"main.go": "package main\n"})

		req := &types.SearchContentRequest{
			Query:        "findme",
			Filters:      &types.SearchFilters{Exclude: "*.go"},
			UnsavedFiles: []types.UnsavedFile{{Path: "main.go", Content: "findme\n"}},
		}
		ctx := context.Background()
		results, _ := SearchContent(ws, req, nil, ctx, 10*time.Second)
		assert.Equal(t, 0, len(results))
	})

	t.Run("SearchFiles basic", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go":        "package main\n",
			"pkg/util.go":    "package pkg\n",
			"pkg/handler.go": "package pkg\n",
		})

		req := &types.SearchFilesRequest{Query: "main", Limit: 10}
		result, err := SearchFiles(ws, req)
		assert.NoError(t, err)
		assert.Equal(t, "main", result.Query)
	})

	t.Run("SearchFiles fuzzy", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go":          "package main\n",
			"pkg/myhandler.go": "package pkg\n",
		})

		req := &types.SearchFilesRequest{Query: "mhandler", Limit: 10}
		SearchFiles(ws, req)
	})

	t.Run("sortDocuments nil editor", func(t *testing.T) {
		ws := makeWS(t, map[string]string{"main.go": "package main\n"})

		sr := &invertedindex.SearchResult{
			DocIds:     map[string]struct{}{},
			WildDocIds: map[string]struct{}{},
		}
		result := sortDocuments(ws.Id, nil, sr, func(p string) bool { return true })
		assert.NotNil(t, result)
	})

	t.Run("sortDocuments with editor", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go":     "package main\n",
			"pkg/util.go": "package pkg\n",
		})

		sr := &invertedindex.SearchResult{
			DocIds:     map[string]struct{}{},
			WildDocIds: map[string]struct{}{},
		}
		editor := &types.Editor{
			ActiveFile: "main.go",
			OpenFiles:  []string{"pkg/util.go"},
		}
		result := sortDocuments(ws.Id, editor, sr, func(p string) bool { return true })
		assert.NotNil(t, result)
	})

	t.Run("SearchSymbols", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go": "package main\nfunc calculateTotal(items []int) int {\n\ttotal := 0\n\tfor _, item := range items {\n\t\ttotal += item\n\t}\n\treturn total\n}\n",
		})

		req := &types.SearchSymbolsRequest{
			Query: "calculate",
			Limit: &types.SearchLimit{MaxResults: 10, MaxResultsPerFile: 10},
		}
		SearchSymbols(ws, req)
	})

	// --- Teardown ---
	running.Shutdown()
	shutdownWg.Wait()
	indexerWg.Wait()
	symbols.CloseAndWait()
	documents.CloseAndWait()
	invertedindex.CloseAndWait()
	idtable.Close()
	env.TeardownBase()
}
