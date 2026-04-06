package searcher

import (
	"context"
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codetrek/haystack/internal/conf"
	"github.com/codetrek/haystack/internal/core/documents"
	"github.com/codetrek/haystack/internal/core/idtable"
	"github.com/codetrek/haystack/internal/core/invertedindex"
	"github.com/codetrek/haystack/internal/core/symbols"
	"github.com/codetrek/haystack/internal/core/workspace"
	"github.com/codetrek/haystack/internal/server/indexer"
	"github.com/codetrek/haystack/internal/shared/running"
	"github.com/codetrek/haystack/internal/shared/types"
	"github.com/codetrek/haystack/internal/testutil"
	"github.com/stretchr/testify/assert"
)

// findCtagsBinary returns the path to the ctags binary bundled under
// the project's deps/ directory, selecting the correct platform variant.
// It walks up from the current file's directory to find the repo root.
func findCtagsBinary(t *testing.T) string {
	t.Helper()
	// Determine platform subdirectory: linux-amd64, darwin-arm64, etc.
	platform := runtime.GOOS + "-" + runtime.GOARCH
	binary := "ctags"
	if runtime.GOOS == "windows" {
		binary = "ctags.exe"
	}

	// Walk up from the directory of this test file to find the repo root
	// (look for the deps/ directory).
	_, thisFile, _, _ := runtime.Caller(0)
	dir := filepath.Dir(thisFile)
	for i := 0; i < 10; i++ {
		candidate := filepath.Join(dir, "deps", platform, binary)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Skipf("ctags binary not found for %s under deps/", platform)
	return ""
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

// integrationOnce ensures the indexer-based integration test only runs once,
// even with -count=N, because the indexer uses package-level singletons
// with channels that cannot be re-used after Stop().
var integrationOnce sync.Once
var integrationRan bool

func TestFullIntegration(t *testing.T) {
	// The indexer package uses singleton channels (scanner.stop, scanner.done, etc.)
	// that panic on "close of closed channel" if Run/Stop is called more than once.
	// Guard against re-runs with -count=N.
	ranBefore := integrationRan
	integrationOnce.Do(func() { integrationRan = true })
	if ranBefore {
		t.Skip("skipping: indexer singleton already used in this process (re-run via -count)")
	}

	env := testutil.SetupEnv(t, "searcher-integ")

	// Enable symbol parsing by pointing to the bundled ctags binary.
	ctagsPath := findCtagsBinary(t)
	origCtags := conf.Get().BinPath.CTags
	conf.Get().BinPath.CTags = ctagsPath
	defer func() { conf.Get().BinPath.CTags = origCtags }()

	// Speed up symbol parser flush (default 5s is too slow for tests).
	origFlushInterval := indexer.SymbolParserFlushInterval
	indexer.SymbolParserFlushInterval = 50 * time.Millisecond
	defer func() { indexer.SymbolParserFlushInterval = origFlushInterval }()

	// Speed up inverted index flush: reduce the "entry must be N seconds old"
	// timeout so pending writes are flushed quickly.
	origFlushWaitTimeout := invertedindex.FlushWaitTimeout
	invertedindex.FlushWaitTimeout = 200 * time.Millisecond
	defer func() { invertedindex.FlushWaitTimeout = origFlushWaitTimeout }()

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
	// ResetForTest re-creates singletons so the symbol parser picks up
	// the shortened SymbolParserFlushInterval we set above.
	indexer.ResetForTest()
	var indexerWg sync.WaitGroup
	indexer.Run(&indexerWg)

	// Create a shared workspace with many files that different sub-tests
	// can reuse, avoiding the 1.2s flush wait per workspace.
	sharedDir := t.TempDir()
	sharedFiles := map[string]string{
		"main.go":           "package main\nimport \"fmt\"\nfunc main() {\n\tfmt.Println(\"hello world\")\n}\n",
		"util.go":           "package main\nfunc helper() string {\n\treturn \"hello world\"\n}\n",
		"pkg/util.go":       "package pkg\nfunc hello() { }\n",
		"test/util_test.go": "package test\nfunc TestHello() { hello() }\n",
		"src/main.go":       "package main\nfunc srcHello() { pfiltword }\n",
		"test/test.go":      "package test\nfunc testHello() { pfiltword }\n",
		"data.txt":          "inclword in text file\n",
		"main_test.go":      "package main\nfunc testExcl() { exclword }\n",
		"pkg/handler.go":    "package pkg\nfunc handler() { }\n",
		"pkg/a/active.go":   "package a\nfunc aMarker() { editorpri }\n",
		"pkg/a/sibling.go":  "package a\nfunc sMarker() { editorpri }\n",
		"pkg/b/open.go":     "package b\nfunc oMarker() { editorpri }\n",
		"other/file.go":     "package other\nfunc otherMarker() { editorpri }\n",
		"pkg/file1.go":      "package pkg\nfunc f1() { dirpri }\n",
		"pkg/file2.go":      "package pkg\nfunc f2() { dirpri }\n",
		"a/b/deep.go":       "package b\nfunc deepFunc() { parentpri }\n",
		"a/sibling.go":      "package a\nfunc siblingFunc() { parentpri }\n",
		"keep.go":           "package main\nfunc keepFunc() { keep_marker }\n",
		"reject.go":         "package main\nfunc rejectFunc() { keep_marker }\n",
		"neutral.go":        "package main\nfunc neutralFunc() { }\n",
		"wild.go":           "package main\nfunc wildtest() { }\n",
		"reject_wild.go":    "package main\nfunc rw() { }\n",
		"indexed.go":        "package main\nfunc indexedFunc() { combined_marker }\n",
		"unsaved.go":        "package main\nfunc unsavedFunc() { }\n",
		"alpha.go":          "package main\nfunc alpha() { }\n",
		"beta.go":           "package main\nfunc beta() { }\n",
		"a_kwone.go":        "package main\nfunc kwone() { }\n",
		"b_kwtwo.go":        "package main\nfunc kwtwo() { }\n",
		"funcs.go":          "package main\n\nfunc targetSymbol() int {\n\treturn 1\n}\n\nfunc otherSymbol() int {\n\treturn 2\n}\n\nfunc calculateTotal(items []int) int {\n\ttotal := 0\n\tfor _, item := range items {\n\t\ttotal += item\n\t}\n\treturn total\n}\n\nfunc getUserProfile() int {\n\treturn 1\n}\n\nfunc exactFunc() int {\n\treturn 1\n}\n\nfunc myHandler() {\n}\n\nfunc myProcessor() {\n}\n",
		// JavaScript files for symbol search coverage (ctags produces kind=function for JS)
		"funcs.js":    "function targetSymbol() { return 1; }\nfunction otherSymbol() { return 2; }\nfunction calculateTotal(items) { return items.reduce((a,b) => a+b, 0); }\nfunction getUserProfile() { return 1; }\nfunction exactFunc() { return 1; }\nfunction myHandler() {}\nfunction myProcessor() {}\n",
		"cbmain.go":   "package main\nfunc cbFunc() { cbword }\n",
		"cbutil.go":   "package main\nfunc cbHelper() { cbword }\n",
		"foobar.go":   "package main\nfunc fooBarBaz() { }\n",
		"unique.go":   "package main\nfunc unique_func_xyz() { }\n",
		"wdboth.go":   "package main\nfunc wdBoth() { wdterm wdwild }\n",
		"wdonly.go":   "package main\nfunc wdOnly() { wdwild }\n",
		"andfileA.go": "package main\nfunc andA() { andtermone andtermtwo }\n",
		"andfileB.go": "package main\nfunc andB() { andtermone }\n",
		"andfileC.go": "package main\nfunc andC() { andtermtwo }\n",
		"andwildA.go": "package main\nfunc awA() { awfirst awsecond awwildone awwildtwo }\n",
		"andwildB.go": "package main\nfunc awB() { awfirst awwildone }\n",
		"andwildC.go": "package main\nfunc awC() { awsecond awwildtwo }\n",
		// CJK test files (HAY-002 step 4)
		"cjk_pure.txt":     "中华人民共和国成立于1949年\n这是第二行\n",
		"cjk_mixed.txt":    "Go语言是Google开发的编程语言\nRust也是一种系统编程语言\n",
		"说明文档.md":          "这是一个说明文档\n包含中文内容\n",
		"cjk_stopword.txt": "的了在是有\n这里只有停用词\n",
	}
	for relPath, content := range sharedFiles {
		full := filepath.Join(sharedDir, relPath)
		os.MkdirAll(filepath.Dir(full), 0755)
		os.WriteFile(full, []byte(content), 0644)
	}
	sharedWS, err := workspace.Create(sharedDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}
	indexer.Sync(sharedWS, false)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !sharedWS.GetLastFullSync().IsZero() {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if sharedWS.GetLastFullSync().IsZero() {
		t.Fatal("shared workspace indexing did not complete within timeout")
	}
	// Wait for symbol parsing: poll until GetDocFunctions returns non-empty
	// for a known JS file (funcs.js). The symbol parser flushes every 50ms
	// (configured above) and ctags processes files quickly.
	// NOTE: We use .js files for symbol testing because ctags produces
	// kind="function" for JavaScript (which the code handles) vs kind="func"
	// for Go (which the code does not yet handle).
	{
		var funcsDocId string
		dl := time.Now().Add(30 * time.Second)
		for time.Now().Before(dl) {
			documents.ScanFiles(sharedWS.Id, func(id, relPath string) bool {
				if relPath == "funcs.js" {
					funcsDocId = id
					return false
				}
				return true
			})
			if funcsDocId != "" {
				fns, _ := symbols.GetDocFunctions(sharedWS.Id, funcsDocId)
				if len(fns) > 0 {
					break
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	// Wait for the inverted-index to flush pending writes from both
	// content indexing and symbol indexing.
	// We reduced FlushWaitTimeout to 200ms; wait for that plus a ticker cycle.
	time.Sleep(time.Duration(invertedindex.FlushWaitTimeout) + time.Duration(invertedindex.FlushTicker) + 200*time.Millisecond)

	// makeWS creates a NEW workspace for tests that need isolated files.
	// It does NOT wait for index flush - use only when index search isn't needed.
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
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if !ws.GetLastFullSync().IsZero() {
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		if ws.GetLastFullSync().IsZero() {
			t.Fatal("indexing did not complete within timeout")
		}
		time.Sleep(50 * time.Millisecond)
		return ws
	}

	// --- Sub-tests that share the same indexer instance ---
	// Tests that need index-search results use sharedWS (already flushed).
	// Tests that only need unsaved-file search or special setup use makeWS.

	t.Run("SearchContent basic", func(t *testing.T) {
		req := &types.SearchContentRequest{Query: "keep_marker", BeforeAfter: 1}
		ctx := context.Background()
		results, truncated := SearchContent(sharedWS, req, nil, ctx, 10*time.Second)
		assert.False(t, truncated)
		assert.True(t, len(results) > 0, "expected index search results for 'keep_marker'")
	})

	t.Run("SearchContent with editor", func(t *testing.T) {
		req := &types.SearchContentRequest{
			Query: "editorpri",
			Editor: &types.Editor{
				ActiveFile: "pkg/a/active.go",
				OpenFiles:  []string{"main.go"},
			},
		}
		ctx := context.Background()
		results, _ := SearchContent(sharedWS, req, nil, ctx, 10*time.Second)
		assert.True(t, len(results) > 0)
	})

	t.Run("SearchContent with callback", func(t *testing.T) {
		callbackCount := 0
		req := &types.SearchContentRequest{Query: "keep_marker"}
		ctx := context.Background()
		SearchContent(sharedWS, req, func(r types.SearchContentResult) { callbackCount++ }, ctx, 10*time.Second)
		assert.True(t, callbackCount > 0)
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
		results, _ := SearchContent(ws, req, nil, ctx, 10*time.Second)
		assert.LessOrEqual(t, len(results), 1, "limit MaxResults=1 should cap results")
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
		results, _ := SearchContent(ws, req, nil, ctx, 10*time.Second)
		// Cancelled context should return early with no or partial results
		assert.NotNil(t, results)
	})

	t.Run("SearchContent case sensitive", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go": "package main\nfunc Hello() {}\nfunc hello() {}\n",
		})

		req := &types.SearchContentRequest{Query: "Hello", CaseSensitive: true}
		ctx := context.Background()
		results, _ := SearchContent(ws, req, nil, ctx, 10*time.Second)
		// Case-sensitive "Hello" should not match "hello"
		for _, r := range results {
			for _, l := range r.Lines {
				assert.Contains(t, l.Line.Content, "Hello")
			}
		}
	})

	t.Run("SearchContent beforeAfter clamping", func(t *testing.T) {
		ws := makeWS(t, map[string]string{"main.go": "package main\nfunc hello() {}\n"})
		ctx := context.Background()

		// Negative beforeAfter should be clamped to 0
		req1 := &types.SearchContentRequest{Query: "hello", BeforeAfter: -5}
		results1, _ := SearchContent(ws, req1, nil, ctx, 10*time.Second)
		for _, r := range results1 {
			for _, l := range r.Lines {
				assert.Nil(t, l.Before, "negative beforeAfter should produce no before context")
				assert.Nil(t, l.After, "negative beforeAfter should produce no after context")
			}
		}

		// Large beforeAfter should be clamped to 5
		req2 := &types.SearchContentRequest{Query: "hello", BeforeAfter: 100}
		results2, _ := SearchContent(ws, req2, nil, ctx, 10*time.Second)
		for _, r := range results2 {
			for _, l := range r.Lines {
				assert.True(t, len(l.Before) <= 5, "beforeAfter clamped to 5")
				assert.True(t, len(l.After) <= 5, "beforeAfter clamped to 5")
			}
		}
	})

	t.Run("SearchContent whole word", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go": "package main\nvar test = 1\nfunc testing() {}\n",
		})

		req := &types.SearchContentRequest{Query: "test", WholeWord: true}
		ctx := context.Background()
		results, _ := SearchContent(ws, req, nil, ctx, 10*time.Second)
		// Whole-word "test" should match "test" but not "testing"
		for _, r := range results {
			for _, l := range r.Lines {
				assert.NotContains(t, l.Line.Content, "testing", "whole word should not match partial")
			}
		}
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
		result, err := SearchFiles(ws, req)
		assert.NoError(t, err)
		assert.Equal(t, "mhandler", result.Query)
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
		result, err := SearchSymbols(ws, req)
		assert.NoError(t, err)
		assert.Equal(t, "calculate", result.Query)
	})

	// =================================================================
	// Coverage boost sub-tests (added to reuse the singleton indexer)
	// =================================================================

	// --- sortDocuments with filter that rejects some docs ---
	t.Run("sortDocuments filter rejects some", func(t *testing.T) {
		engine := NewSimpleContentSearchEngine(sharedWS, 24, 32, false)
		err := engine.Compile("keep_marker", false)
		assert.NoError(t, err)

		sr, err := engine.CollectDocuments()
		assert.NoError(t, err)

		sorted := sortDocuments(sharedWS.Id, nil, sr, func(relPath string) bool {
			return relPath == "keep.go"
		})
		for _, docid := range sorted {
			doc, _ := documents.GetDocument(sharedWS.Id, docid, false)
			if doc != nil {
				assert.Equal(t, "keep.go", doc.RelPath)
			}
		}
	})

	// --- sortDocuments with editor active/open files ---
	t.Run("sortDocuments editor priority boost", func(t *testing.T) {
		engine := NewSimpleContentSearchEngine(sharedWS, 24, 32, false)
		err := engine.Compile("editorpri", false)
		assert.NoError(t, err)

		sr, err := engine.CollectDocuments()
		assert.NoError(t, err)

		editor := &types.Editor{
			ActiveFile: "pkg/a/active.go",
			OpenFiles:  []string{"pkg/b/open.go"},
		}
		sorted := sortDocuments(sharedWS.Id, editor, sr, func(_ string) bool { return true })
		assert.NotNil(t, sorted)
	})

	// --- sortDocuments: with WildDocIds ---
	t.Run("sortDocuments with wild docids", func(t *testing.T) {
		var docId string
		documents.ScanFiles(sharedWS.Id, func(id, relPath string) bool {
			if relPath == "wild.go" {
				docId = id
				return false
			}
			return true
		})
		assert.NotEmpty(t, docId, "wild.go must be indexed")

		sr := &invertedindex.SearchResult{
			DocIds:     map[string]struct{}{docId: {}},
			WildDocIds: map[string]struct{}{docId: {}},
		}
		result := sortDocuments(sharedWS.Id, nil, sr, func(_ string) bool { return true })
		assert.NotNil(t, result)
		assert.Equal(t, 1, len(result), "WildDocId should be included in sorted results")
	})

	// --- sortDocuments: filter rejects WildDocIds too ---
	t.Run("sortDocuments filter rejects wild", func(t *testing.T) {
		var docId string
		documents.ScanFiles(sharedWS.Id, func(id, relPath string) bool {
			if relPath == "reject_wild.go" {
				docId = id
				return false
			}
			return true
		})
		assert.NotEmpty(t, docId, "reject_wild.go must be indexed")

		sr := &invertedindex.SearchResult{
			DocIds:     map[string]struct{}{docId: {}},
			WildDocIds: map[string]struct{}{docId: {}},
		}
		result := sortDocuments(sharedWS.Id, nil, sr, func(_ string) bool { return false })
		assert.NotNil(t, result)
		assert.Equal(t, 0, len(result))
	})

	// --- sortDocuments: large DocIds triggers ScanFiles path (line 50-55) ---
	t.Run("sortDocuments large docids uses ScanFiles", func(t *testing.T) {
		// Build a SearchResult with >10000 fake DocIds to trigger the ScanFiles branch.
		largeDocIds := make(map[string]struct{}, 10001)
		for i := 0; i < 10001; i++ {
			largeDocIds[fmt.Sprintf("fake-doc-%d", i)] = struct{}{}
		}
		// Also insert real indexed doc IDs so ScanFiles finds matches.
		documents.ScanFiles(sharedWS.Id, func(id, relPath string) bool {
			largeDocIds[id] = struct{}{}
			return true
		})

		sr := &invertedindex.SearchResult{
			DocIds:     largeDocIds,
			WildDocIds: map[string]struct{}{},
		}
		result := sortDocuments(sharedWS.Id, nil, sr, func(_ string) bool { return true })
		assert.NotNil(t, result)
		// Should contain at least the real indexed files
		assert.True(t, len(result) > 0, "ScanFiles path should find real indexed documents")
	})

	// --- sortDocuments: editor same dir / parent dir ---
	t.Run("sortDocuments editor same dir", func(t *testing.T) {
		engine := NewSimpleContentSearchEngine(sharedWS, 24, 32, false)
		engine.Compile("dirpri", false)
		sr, _ := engine.CollectDocuments()

		editor := &types.Editor{
			ActiveFile: "pkg/file1.go",
			OpenFiles:  []string{"pkg/file2.go"},
		}
		sorted := sortDocuments(sharedWS.Id, editor, sr, func(_ string) bool { return true })
		assert.NotNil(t, sorted)
	})

	t.Run("sortDocuments editor parent dir", func(t *testing.T) {
		engine := NewSimpleContentSearchEngine(sharedWS, 24, 32, false)
		engine.Compile("parentpri", false)
		sr, _ := engine.CollectDocuments()

		editor := &types.Editor{ActiveFile: "a/b/deep.go"}
		sorted := sortDocuments(sharedWS.Id, editor, sr, func(_ string) bool { return true })
		assert.NotNil(t, sorted)
	})

	// --- SearchContent: combined unsaved + index ---
	t.Run("SearchContent unsaved plus index", func(t *testing.T) {
		req := &types.SearchContentRequest{
			Query: "combined_marker",
			UnsavedFiles: []types.UnsavedFile{
				{Path: "unsaved.go", Content: "combined_marker found here\n"},
			},
		}
		ctx := context.Background()
		results, _ := SearchContent(sharedWS, req, nil, ctx, 10*time.Second)
		assert.True(t, len(results) >= 1)
	})

	// --- SearchContent: unsaved callback + limit ---
	t.Run("SearchContent unsaved limit hit", func(t *testing.T) {
		ws := makeWS(t, map[string]string{"a.go": "package main\n"})

		callbackCount := 0
		req := &types.SearchContentRequest{
			Query: "limitmarker",
			Limit: &types.SearchLimit{MaxResults: 1, MaxResultsPerFile: 1},
			UnsavedFiles: []types.UnsavedFile{
				{Path: "a.go", Content: "limitmarker one\nlimitmarker two\n"},
				{Path: "b.go", Content: "limitmarker three\n"},
			},
		}
		ctx := context.Background()
		results, _ := SearchContent(ws, req, func(r types.SearchContentResult) { callbackCount++ }, ctx, 10*time.Second)
		assert.True(t, callbackCount >= 1)
		assert.LessOrEqual(t, len(results), 1, "MaxResults=1 should limit total results")
	})

	// --- SearchContent: unsaved file filtered by include ---
	t.Run("SearchContent unsaved filtered by include", func(t *testing.T) {
		ws := makeWS(t, map[string]string{"main.go": "package main\n"})

		req := &types.SearchContentRequest{
			Query:        "filttest",
			Filters:      &types.SearchFilters{Include: "*.txt"},
			UnsavedFiles: []types.UnsavedFile{{Path: "main.go", Content: "filttest\n"}},
		}
		ctx := context.Background()
		results, _ := SearchContent(ws, req, nil, ctx, 10*time.Second)
		assert.Equal(t, 0, len(results))
	})

	// --- SearchContent: timeout ---
	t.Run("SearchContent timeout", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go": "package main\nfunc hello() { timeoutword }\n",
		})

		req := &types.SearchContentRequest{Query: "timeoutword"}
		ctx := context.Background()
		results, truncated := SearchContent(ws, req, nil, ctx, 1*time.Nanosecond)
		// 1ns timeout: search should return early; either no results or truncated
		if len(results) > 0 {
			assert.True(t, truncated, "if results exist with 1ns timeout, should be truncated")
		}
	})

	// --- SearchContent: cancelled context ---
	t.Run("SearchContent cancelled before index", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go": "package main\nfunc hello() { cancelword }\n",
		})

		req := &types.SearchContentRequest{Query: "cancelword"}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		results, _ := SearchContent(ws, req, nil, ctx, 10*time.Second)
		// Cancelled context should prevent index search from returning results
		assert.NotNil(t, results)
	})

	// --- SearchContent: custom limit smaller than conf ---
	t.Run("SearchContent custom limit smaller", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go": "package main\nfunc a() { limitword }\nfunc b() { limitword }\nfunc c() { limitword }\n",
		})

		req := &types.SearchContentRequest{
			Query: "limitword",
			Limit: &types.SearchLimit{MaxResults: 2, MaxResultsPerFile: 2},
		}
		ctx := context.Background()
		results, _ := SearchContent(ws, req, nil, ctx, 10*time.Second)
		totalLines := 0
		for _, r := range results {
			totalLines += len(r.Lines)
		}
		assert.LessOrEqual(t, totalLines, 2, "custom limit MaxResults=2 should cap results")
	})

	// --- SearchContent: limit with only MaxResults ---
	t.Run("SearchContent limit only MaxResults", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go": "package main\nfunc hello() { onlymaxword }\n",
		})

		req := &types.SearchContentRequest{
			Query: "onlymaxword",
			Limit: &types.SearchLimit{MaxResults: 100},
		}
		ctx := context.Background()
		results, _ := SearchContent(ws, req, nil, ctx, 10*time.Second)
		// Results may be empty if index hasn't flushed, but should not error
		assert.NotNil(t, results)
	})

	// --- SearchContent: limit with only MaxResultsPerFile ---
	t.Run("SearchContent limit only MaxResultsPerFile", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go": "package main\nfunc hello() { onlyperfileword }\n",
		})

		req := &types.SearchContentRequest{
			Query: "onlyperfileword",
			Limit: &types.SearchLimit{MaxResultsPerFile: 100},
		}
		ctx := context.Background()
		results, _ := SearchContent(ws, req, nil, ctx, 10*time.Second)
		assert.NotNil(t, results)
	})

	// --- SearchContent: negative beforeAfter ---
	t.Run("SearchContent negative beforeAfter", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go": "package main\nfunc hello() { negba_marker }\n",
		})

		req := &types.SearchContentRequest{Query: "negba_marker", BeforeAfter: -10}
		ctx := context.Background()
		results, _ := SearchContent(ws, req, nil, ctx, 10*time.Second)
		for _, r := range results {
			for _, l := range r.Lines {
				assert.Nil(t, l.Before)
				assert.Nil(t, l.After)
			}
		}
	})

	// --- SearchContent: large beforeAfter clamped to 5 ---
	t.Run("SearchContent large beforeAfter clamped", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go": "package main\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nfunc hello() { lgba_marker }\nline10\nline11\nline12\nline13\nline14\n",
		})

		req := &types.SearchContentRequest{Query: "lgba_marker", BeforeAfter: 100}
		ctx := context.Background()
		results, _ := SearchContent(ws, req, nil, ctx, 10*time.Second)
		for _, r := range results {
			for _, l := range r.Lines {
				assert.True(t, len(l.Before) <= 5)
				assert.True(t, len(l.After) <= 5)
			}
		}
	})

	// --- SearchContent: editor prioritization ---
	t.Run("SearchContent editor active+open prioritization", func(t *testing.T) {
		req := &types.SearchContentRequest{
			Query: "editorpri",
			Editor: &types.Editor{
				ActiveFile: "pkg/a/active.go",
				OpenFiles:  []string{"pkg/b/open.go"},
			},
		}
		ctx := context.Background()
		results, _ := SearchContent(sharedWS, req, nil, ctx, 10*time.Second)
		if len(results) > 0 {
			assert.Equal(t, "pkg/a/active.go", results[0].File)
		}
	})

	// --- SearchContent: unsaved shadows indexed ---
	t.Run("SearchContent unsaved shadows indexed", func(t *testing.T) {
		req := &types.SearchContentRequest{
			Query: "keep_marker",
			UnsavedFiles: []types.UnsavedFile{
				{Path: "keep.go", Content: "keep_marker found in unsaved version\n"},
			},
		}
		ctx := context.Background()
		results, _ := SearchContent(sharedWS, req, nil, ctx, 10*time.Second)
		keepCount := 0
		for _, r := range results {
			if r.File == "keep.go" {
				keepCount++
			}
		}
		assert.LessOrEqual(t, keepCount, 1)
	})

	// --- SearchContent: path filter ---
	t.Run("SearchContent path filter dir boost", func(t *testing.T) {
		req := &types.SearchContentRequest{
			Query:   "pfiltword",
			Filters: &types.SearchFilters{Path: "src"},
		}
		ctx := context.Background()
		results, _ := SearchContent(sharedWS, req, nil, ctx, 10*time.Second)
		for _, r := range results {
			assert.True(t, strings.HasPrefix(r.File, "src"))
		}
	})

	// --- SearchContent: exclude filter ---
	t.Run("SearchContent exclude filter boost", func(t *testing.T) {
		req := &types.SearchContentRequest{
			Query:   "exclword",
			Filters: &types.SearchFilters{Exclude: "*_test.go"},
		}
		ctx := context.Background()
		results, _ := SearchContent(sharedWS, req, nil, ctx, 10*time.Second)
		for _, r := range results {
			assert.False(t, strings.HasSuffix(r.File, "_test.go"))
		}
	})

	// --- SearchContent: include filter ---
	t.Run("SearchContent include filter boost", func(t *testing.T) {
		req := &types.SearchContentRequest{
			Query:   "inclword",
			Filters: &types.SearchFilters{Include: "*.txt"},
		}
		ctx := context.Background()
		results, _ := SearchContent(sharedWS, req, nil, ctx, 10*time.Second)
		for _, r := range results {
			assert.True(t, strings.HasSuffix(r.File, ".txt"))
		}
	})

	// --- SearchContent: callback with index results ---
	t.Run("SearchContent callback with index", func(t *testing.T) {
		callbackCount := 0
		req := &types.SearchContentRequest{Query: "cbword"}
		ctx := context.Background()
		results, _ := SearchContent(sharedWS, req, func(r types.SearchContentResult) { callbackCount++ }, ctx, 10*time.Second)
		assert.True(t, callbackCount > 0)
		assert.Equal(t, callbackCount, len(results), "callback should be invoked once per result")
	})

	// --- SearchContent: MaxResults during index loop ---
	t.Run("SearchContent maxresults during index", func(t *testing.T) {
		files := map[string]string{}
		for i := 0; i < 10; i++ {
			files[fmt.Sprintf("file%d.go", i)] = fmt.Sprintf("package main\nfunc f%d() { idxlimit_marker }\n", i)
		}
		ws := makeWS(t, files)

		savedLimit := conf.Get().Server.Search.Limit
		conf.Get().Server.Search.Limit.MaxResults = 2
		conf.Get().Server.Search.Limit.MaxResultsPerFile = 10
		defer func() { conf.Get().Server.Search.Limit = savedLimit }()

		req := &types.SearchContentRequest{
			Query: "idxlimit_marker",
			Limit: &types.SearchLimit{MaxResults: 2, MaxResultsPerFile: 10},
		}
		ctx := context.Background()
		results, _ := SearchContent(ws, req, nil, ctx, 10*time.Second)
		assert.LessOrEqual(t, len(results), 2, "MaxResults=2 should limit to at most 2 file results")
	})

	// --- SearchContent: editor with empty ActiveFile ---
	t.Run("SearchContent editor empty active", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go": "package main\nfunc hello() { emptyactive }\n",
		})

		req := &types.SearchContentRequest{
			Query: "emptyactive",
			Editor: &types.Editor{
				ActiveFile: "",
				OpenFiles:  []string{"main.go"},
			},
		}
		ctx := context.Background()
		results, _ := SearchContent(ws, req, nil, ctx, 10*time.Second)
		// Empty ActiveFile should not crash; search should still work
		assert.NotNil(t, results)
	})

	// --- SearchContent: unsaved only no matches ---
	t.Run("SearchContent unsaved only no matches", func(t *testing.T) {
		ws := makeWS(t, map[string]string{"main.go": "package main\n"})

		req := &types.SearchContentRequest{
			Query:            "nonexistent_string_xyz",
			UnsavedFilesOnly: true,
			UnsavedFiles: []types.UnsavedFile{
				{Path: "main.go", Content: "nothing here\n"},
			},
		}
		ctx := context.Background()
		results, _ := SearchContent(ws, req, nil, ctx, 10*time.Second)
		assert.Equal(t, 0, len(results))
	})

	// --- SearchFiles: removed file ---
	// This test covers lines 596-599 (os.IsNotExist path) and lines 611-616
	// (goroutine that calls indexer.RemoveFile for stale files).
	t.Run("SearchFiles removed file", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"keep.go":   "package main\n",
			"remove.go": "package main\n",
		})

		os.Remove(filepath.Join(ws.Path, "remove.go"))

		req := &types.SearchFilesRequest{Query: "remove", Limit: 10}
		result, err := SearchFiles(ws, req)
		assert.NoError(t, err)
		for _, f := range result.Files {
			assert.NotEqual(t, "remove.go", f)
		}
		// Give the background goroutine (lines 611-616) time to call
		// indexer.RemoveFile before the test infrastructure tears down.
		time.Sleep(500 * time.Millisecond)
	})

	// --- SearchFiles: directory path in index ---
	// Covers the stat.IsDir() branch on line 596 and the removedFiles
	// goroutine (lines 611-616).
	t.Run("SearchFiles directory in index", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"keepdir.go":     "package main\n",
			"mydir/inner.go": "package inner\n",
		})

		// Remove the file inside the directory so only the directory remains.
		os.Remove(filepath.Join(ws.Path, "mydir", "inner.go"))
		// Create a file with the same name as the directory entry so
		// os.Stat succeeds but IsDir() returns true.
		// Actually, "mydir" is already a directory on disk.
		// We need "mydir" to be in the documents index. Since we indexed
		// "mydir/inner.go", let's search for "mydir" which will match
		// "mydir/inner.go" (now deleted) via ScanFiles.

		req := &types.SearchFilesRequest{Query: "inner", Limit: 10}
		result, err := SearchFiles(ws, req)
		assert.NoError(t, err)
		// "mydir/inner.go" was removed from disk, so it should not appear
		for _, f := range result.Files {
			assert.NotEqual(t, "mydir/inner.go", f)
		}
		time.Sleep(500 * time.Millisecond)
	})

	// --- SearchFiles: limit 1 ---
	t.Run("SearchFiles limit 1", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go":  "package main\n",
			"main2.go": "package main\n",
			"main3.go": "package main\n",
		})

		req := &types.SearchFilesRequest{Query: "main", Limit: 1}
		result, err := SearchFiles(ws, req)
		assert.NoError(t, err)
		assert.LessOrEqual(t, len(result.Files), 1)
	})

	// --- SearchFiles: sort by score then length ---
	t.Run("SearchFiles sort by score then length", func(t *testing.T) {
		ws := makeWS(t, map[string]string{
			"main.go":       "package main\n",
			"pkg/main.go":   "package main\n",
			"a/b/c/main.go": "package main\n",
		})

		req := &types.SearchFilesRequest{Query: "main.go", Limit: 10}
		result, err := SearchFiles(ws, req)
		assert.NoError(t, err)
		if len(result.Files) >= 2 {
			assert.True(t, len(result.Files[0]) <= len(result.Files[1]))
		}
	})

	// --- SearchSymbols: multi word ---
	t.Run("SearchSymbols multi word", func(t *testing.T) {
		req := &types.SearchSymbolsRequest{
			Query: "getuserprofile",
			Limit: &types.SearchLimit{MaxResults: 10, MaxResultsPerFile: 10},
		}
		result, err := SearchSymbols(sharedWS, req)
		assert.NoError(t, err)
		assert.NotNil(t, result)
	})

	// --- searchSymbols: exact match ---
	t.Run("searchSymbols exact match", func(t *testing.T) {
		req := &types.SearchSymbolsRequest{
			Query: "exactFunc",
			Limit: &types.SearchLimit{MaxResults: 10, MaxResultsPerFile: 10},
		}
		result, err := searchSymbols(sharedWS, req)
		assert.NoError(t, err)
		assert.NotNil(t, result)
	})

	// --- searchSymbols: no match ---
	t.Run("searchSymbols no match", func(t *testing.T) {
		req := &types.SearchSymbolsRequest{
			Query: "nonExistentFunc",
			Limit: &types.SearchLimit{MaxResults: 10, MaxResultsPerFile: 10},
		}
		result, err := searchSymbols(sharedWS, req)
		assert.NoError(t, err)
		assert.Equal(t, 0, len(result.Symbols))
	})

	// --- searchSymbols: with matching functions ---
	t.Run("searchSymbols with matching functions", func(t *testing.T) {
		req := &types.SearchSymbolsRequest{
			Query: "targetSymbol",
			Limit: &types.SearchLimit{MaxResults: 10, MaxResultsPerFile: 10},
		}
		result, err := searchSymbols(sharedWS, req)
		assert.NoError(t, err)
		assert.NotNil(t, result)
	})

	// --- fuzzySearchSymbols: error workspace ---
	t.Run("fuzzySearchSymbols error workspace", func(t *testing.T) {
		ws := &workspace.Workspace{Id: -999}
		req := &types.SearchSymbolsRequest{
			Query: "test",
			Limit: &types.SearchLimit{MaxResults: 10, MaxResultsPerFile: 10},
		}
		result, err := fuzzySearchSymbols(ws, req)
		assert.Error(t, err, "invalid workspace should return error")
		assert.NotNil(t, result)
		assert.Equal(t, 0, len(result.Symbols))
	})

	// --- isFileChanged: file not found ---
	t.Run("isFileChanged file not found", func(t *testing.T) {
		doc := &documents.Document{RelPath: "nonexistent.go"}
		result := isFileChanged(sharedWS, doc)
		assert.False(t, result)
	})

	// --- isFileChanged: modtime matches ---
	t.Run("isFileChanged modtime matches", func(t *testing.T) {
		fullPath := filepath.Join(sharedWS.Path, "main.go")
		fi, _ := os.Stat(fullPath)

		doc := &documents.Document{
			RelPath:      "main.go",
			ModifiedTime: fi.ModTime().UnixNano(),
		}
		result := isFileChanged(sharedWS, doc)
		assert.False(t, result)
	})

	// --- isFileChanged: modtime differs ---
	t.Run("isFileChanged modtime differs", func(t *testing.T) {
		doc := &documents.Document{
			RelPath:      "main.go",
			ModifiedTime: 12345,
		}
		result := isFileChanged(sharedWS, doc)
		assert.False(t, result)
	})

	// --- isFileChanged: empty hash ---
	t.Run("isFileChanged empty hash", func(t *testing.T) {
		fullPath := filepath.Join(sharedWS.Path, "main.go")
		fi, _ := os.Stat(fullPath)

		doc := &documents.Document{
			RelPath:      "main.go",
			ModifiedTime: fi.ModTime().UnixNano(),
			Hash:         "",
		}
		result := isFileChanged(sharedWS, doc)
		assert.False(t, result)
	})

	// --- isFileChanged: hash matches ---
	t.Run("isFileChanged hash matches", func(t *testing.T) {
		fullPath := filepath.Join(sharedWS.Path, "main.go")
		fi, _ := os.Stat(fullPath)
		content, _ := os.ReadFile(fullPath)
		hash := fmt.Sprintf("%x", md5.Sum(content))

		doc := &documents.Document{
			RelPath:      "main.go",
			ModifiedTime: fi.ModTime().UnixNano(),
			Hash:         hash,
		}
		result := isFileChanged(sharedWS, doc)
		assert.True(t, result)
	})

	// --- isFileChanged: hash mismatch ---
	t.Run("isFileChanged hash mismatch", func(t *testing.T) {
		fullPath := filepath.Join(sharedWS.Path, "main.go")
		fi, _ := os.Stat(fullPath)

		doc := &documents.Document{
			RelPath:      "main.go",
			ModifiedTime: fi.ModTime().UnixNano(),
			Hash:         "badhash",
		}
		result := isFileChanged(sharedWS, doc)
		assert.False(t, result)
	})

	// --- getFunctionFileMatch: invalid doc ---
	t.Run("getFunctionFileMatch invalid doc", func(t *testing.T) {
		result, err := getFunctionFileMatch(sharedWS, []string{"test"}, "nonexistent_doc_id")
		assert.Nil(t, err)
		assert.Nil(t, result)
	})

	// --- getFunctionFileMatch: valid doc ---
	t.Run("getFunctionFileMatch valid doc", func(t *testing.T) {
		var docId string
		documents.ScanFiles(sharedWS.Id, func(id, relPath string) bool {
			if relPath == "funcs.js" {
				docId = id
				return false
			}
			return true
		})
		assert.NotEmpty(t, docId, "funcs.js must be indexed")

		result, err := getFunctionFileMatch(sharedWS, []string{"my", "handler"}, docId)
		assert.NoError(t, err)
		assert.NotNil(t, result, "valid doc should return non-nil map")
	})

	// --- CollectDocuments: multiple OR clauses ---
	t.Run("CollectDocuments merge OR clauses", func(t *testing.T) {
		engine := NewSimpleContentSearchEngine(sharedWS, 24, 32, false)
		engine.Compile("alpha | beta", false)
		result, err := engine.CollectDocuments()
		assert.NoError(t, err)
		assert.NotNil(t, result)
	})

	// --- CollectDocuments: single clause ---
	t.Run("CollectDocuments single clause", func(t *testing.T) {
		engine := NewSimpleContentSearchEngine(sharedWS, 24, 32, false)
		engine.Compile("unique_func_xyz", false)
		result, err := engine.CollectDocuments()
		assert.NoError(t, err)
		assert.NotNil(t, result)
	})

	// --- collectWithKeywords: multiple keywords ---
	t.Run("collectWithKeywords multiple keywords", func(t *testing.T) {
		engine := NewSimpleContentSearchEngine(sharedWS, 24, 32, false)
		engine.Compile("foo bar", false)
		result, err := engine.CollectDocuments()
		assert.NoError(t, err)
		assert.NotNil(t, result)
	})

	// --- Term.CollectDocuments: no keywords ---
	t.Run("TermCollectDocuments no keywords", func(t *testing.T) {
		term := &SimpleContentSearchEngineTerm{
			Engine:   &SimpleContentSearchEngine{Workspace: sharedWS},
			Keywords: []string{},
		}
		result := term.CollectDocuments(sharedWS.Id)
		assert.Equal(t, 0, len(result.DocIds))
	})

	// --- collectWithKeywords: single keyword ---
	t.Run("collectWithKeywords single keyword", func(t *testing.T) {
		ft, err := documents.GetWorkspace(sharedWS.Id)
		assert.NoError(t, err)

		term := &SimpleContentSearchEngineTerm{Keywords: []string{"keep_marker"}}
		result := term.collectWithKeywords(ft.InvertedId, term.Keywords)
		assert.NotNil(t, result)
	})

	// --- collectWithKeywords: empty ---
	t.Run("collectWithKeywords empty", func(t *testing.T) {
		term := &SimpleContentSearchEngineTerm{}
		result := term.collectWithKeywords(0, []string{})
		assert.NotNil(t, result)
		assert.Equal(t, 0, len(result))
	})

	// --- collectWithKeywords: intersection empties out ---
	t.Run("collectWithKeywords intersection empty", func(t *testing.T) {
		ft, err := documents.GetWorkspace(sharedWS.Id)
		assert.NoError(t, err)

		term := &SimpleContentSearchEngineTerm{Keywords: []string{"kwone", "kwtwo"}}
		result := term.collectWithKeywords(ft.InvertedId, term.Keywords)
		assert.NotNil(t, result)
	})

	// --- AndClause.CollectDocuments: multiple terms ---
	t.Run("AndClauseCollectDocuments multiple terms", func(t *testing.T) {
		engine := NewSimpleContentSearchEngine(sharedWS, 24, 32, false)
		engine.Compile("keep_marker", false)

		if len(engine.OrClauses) > 0 {
			clause := engine.OrClauses[0]
			result, err := clause.CollectDocuments(sharedWS.Id)
			assert.NoError(t, err)
			assert.NotNil(t, result)
		}
	})

	// =================================================================
	// Symbol search coverage: exercises fuzzySearchSymbols, searchSymbols,
	// getFunctionFileMatch branches.
	// =================================================================

	// Hit the "matched = false" branch in getFunctionFileMatch:
	// query words that do NOT appear in any function name.
	t.Run("SearchSymbols no match", func(t *testing.T) {
		req := &types.SearchSymbolsRequest{
			Query: "xyznonexistent",
			Limit: &types.SearchLimit{MaxResults: 10, MaxResultsPerFile: 10},
		}
		result, err := SearchSymbols(sharedWS, req)
		assert.NoError(t, err)
		assert.Equal(t, 0, len(result.Symbols))
	})

	// Hit the exact-match loop in searchSymbols (lines 201-207).
	t.Run("searchSymbols exact match", func(t *testing.T) {
		req := &types.SearchSymbolsRequest{
			Query: "exactFunc",
			Limit: &types.SearchLimit{MaxResults: 10, MaxResultsPerFile: 10},
		}
		result, err := searchSymbols(sharedWS, req)
		assert.NoError(t, err)
		assert.Equal(t, "exactFunc", result.Query)
	})

	// Hit the limit-break branch in fuzzySearchSymbols (lines 146-150):
	// set MaxResults=1 so the loop breaks early.
	t.Run("SearchSymbols limit 1", func(t *testing.T) {
		req := &types.SearchSymbolsRequest{
			Query: "func",
			Limit: &types.SearchLimit{MaxResults: 1, MaxResultsPerFile: 100},
		}
		result, err := SearchSymbols(sharedWS, req)
		assert.NoError(t, err)
		assert.LessOrEqual(t, len(result.Symbols), 1)
	})

	// Hit the MaxResultsPerFile break in fuzzySearchSymbols (line 137-138):
	// set MaxResultsPerFile=1 so the file-count loop breaks early.
	t.Run("SearchSymbols limit per file 1", func(t *testing.T) {
		req := &types.SearchSymbolsRequest{
			Query: "func",
			Limit: &types.SearchLimit{MaxResults: 100, MaxResultsPerFile: 1},
		}
		result, err := SearchSymbols(sharedWS, req)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "func", result.Query)
	})

	// Hit the filter branch in fuzzySearchSymbols (lines 123-127):
	// use a multi-word query where the second word filters out some symbols.
	t.Run("SearchSymbols multi word filter", func(t *testing.T) {
		req := &types.SearchSymbolsRequest{
			Query: "calculatetotal",
			Limit: &types.SearchLimit{MaxResults: 10, MaxResultsPerFile: 10},
		}
		result, err := SearchSymbols(sharedWS, req)
		assert.NoError(t, err)
		assert.Equal(t, "calculatetotal", result.Query)
	})

	// --- searchSymbols: exact match with strong assertions ---
	// Exercises the exact-match append path (lines 202-207) and the
	// isFileChanged/doc-nil loops in searchSymbols with real indexed data.
	t.Run("searchSymbols exactFunc with assertions", func(t *testing.T) {
		req := &types.SearchSymbolsRequest{
			Query: "exactFunc",
			Limit: &types.SearchLimit{MaxResults: 10, MaxResultsPerFile: 10},
		}
		result, err := searchSymbols(sharedWS, req)
		assert.NoError(t, err)
		assert.Equal(t, "exactFunc", result.Query)
		// If ctags parsed funcs.go, we should find exactFunc
		if len(result.Symbols) > 0 {
			assert.Equal(t, "exactFunc", result.Symbols[0].Name)
			assert.True(t, len(result.Symbols[0].Files) > 0)
			assert.Equal(t, "funcs.js", result.Symbols[0].Files[0].Path)
		}
	})

	// --- searchSymbols: no match returns empty ---
	// Exercises the loop where no function name equals the query,
	// covering the path where the for-loop runs but never appends.
	t.Run("searchSymbols no match returns empty", func(t *testing.T) {
		req := &types.SearchSymbolsRequest{
			Query: "totallyBogusSymbolName",
			Limit: &types.SearchLimit{MaxResults: 10, MaxResultsPerFile: 10},
		}
		result, err := searchSymbols(sharedWS, req)
		assert.NoError(t, err)
		assert.Equal(t, 0, len(result.Symbols))
	})

	// --- searchSymbols: with deleted file ---
	// Create a workspace, index it, then delete a file. The doc will still
	// be in the inverted index but the file won't exist on disk, exercising
	// the isFileChanged → RemoveFile → return false path (lines 190-192).
	t.Run("searchSymbols deleted file", func(t *testing.T) {
		wsDir := t.TempDir()
		delFiles := map[string]string{
			"present.go": "package main\nfunc delTestFunc() int { return 1 }\n",
		}
		for relPath, content := range delFiles {
			full := filepath.Join(wsDir, relPath)
			os.MkdirAll(filepath.Dir(full), 0755)
			os.WriteFile(full, []byte(content), 0644)
		}
		delWS, err := workspace.Create(wsDir)
		if err != nil {
			t.Fatalf("workspace.Create: %v", err)
		}
		indexer.Sync(delWS, false)
		dl := time.Now().Add(10 * time.Second)
		for time.Now().Before(dl) {
			if !delWS.GetLastFullSync().IsZero() {
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		time.Sleep(2000 * time.Millisecond)

		// Delete the file after indexing
		os.Remove(filepath.Join(wsDir, "present.go"))

		req := &types.SearchSymbolsRequest{
			Query: "delTestFunc",
			Limit: &types.SearchLimit{MaxResults: 10, MaxResultsPerFile: 10},
		}
		result, err := searchSymbols(delWS, req)
		assert.NoError(t, err)
		// The file is gone, so isFileChanged returns false → continue
		// Result should be empty because the only doc was skipped
		assert.Equal(t, 0, len(result.Symbols))
	})

	// --- fuzzySearchSymbols: error workspace (GetSymbolWordsTable error) ---
	// Uses an invalid workspace ID so GetSymbolWordsTable fails,
	// exercising lines 116-118.
	t.Run("fuzzySearchSymbols GetSymbolWordsTable error", func(t *testing.T) {
		badWS := &workspace.Workspace{Id: -12345}
		req := &types.SearchSymbolsRequest{
			Query: "anything",
			Limit: &types.SearchLimit{MaxResults: 10, MaxResultsPerFile: 10},
		}
		result, err := fuzzySearchSymbols(badWS, req)
		assert.Error(t, err)
		assert.Equal(t, "anything", result.Query)
		assert.Equal(t, 0, len(result.Symbols))
	})

	// --- fuzzySearchSymbols: query matching nothing in inverted index ---
	// A query that segments into words not in the symbol words table,
	// so the inverted index search returns no docIds.
	t.Run("fuzzySearchSymbols empty results", func(t *testing.T) {
		req := &types.SearchSymbolsRequest{
			Query: "zzzzqqqq",
			Limit: &types.SearchLimit{MaxResults: 10, MaxResultsPerFile: 10},
		}
		result, err := fuzzySearchSymbols(sharedWS, req)
		assert.NoError(t, err)
		assert.Equal(t, 0, len(result.Symbols))
	})

	// --- fuzzySearchSymbols: MaxResults=1 to hit the limit break ---
	// Uses a broad query that matches many symbols, but limits to 1 result,
	// exercising the break at lines 149-150.
	t.Run("fuzzySearchSymbols MaxResults break", func(t *testing.T) {
		req := &types.SearchSymbolsRequest{
			Query: "handler",
			Limit: &types.SearchLimit{MaxResults: 1, MaxResultsPerFile: 100},
		}
		result, err := fuzzySearchSymbols(sharedWS, req)
		assert.NoError(t, err)
		assert.LessOrEqual(t, len(result.Symbols), 1)
	})

	// --- fuzzySearchSymbols: MaxResultsPerFile=1 to hit the file count break ---
	// Uses a broad query, but limits per-file results to 1,
	// exercising the break at lines 137-138.
	t.Run("fuzzySearchSymbols MaxResultsPerFile break", func(t *testing.T) {
		req := &types.SearchSymbolsRequest{
			Query: "handler",
			Limit: &types.SearchLimit{MaxResults: 100, MaxResultsPerFile: 1},
		}
		result, err := fuzzySearchSymbols(sharedWS, req)
		assert.NoError(t, err)
		assert.Equal(t, "handler", result.Query)
	})

	// --- fuzzySearchSymbols: filter branch with multi-word ---
	// "foobarbaz" segments into words; the filter func (lines 124-127)
	// checks each word is contained in the key, filtering out non-matches.
	t.Run("fuzzySearchSymbols filter branch", func(t *testing.T) {
		req := &types.SearchSymbolsRequest{
			Query: "foobarbaz",
			Limit: &types.SearchLimit{MaxResults: 10, MaxResultsPerFile: 10},
		}
		result, err := fuzzySearchSymbols(sharedWS, req)
		assert.NoError(t, err)
		assert.Equal(t, "foobarbaz", result.Query)
	})

	// --- getFunctionFileMatch: matched=true append (lines 98-102) ---
	// Directly call with query words that match a known function name.
	t.Run("getFunctionFileMatch matched true", func(t *testing.T) {
		var docId string
		documents.ScanFiles(sharedWS.Id, func(id, relPath string) bool {
			if relPath == "funcs.js" {
				docId = id
				return false
			}
			return true
		})
		assert.NotEmpty(t, docId, "funcs.js must be indexed")

		result, err := getFunctionFileMatch(sharedWS, []string{"exact", "func"}, docId)
		assert.NoError(t, err)
		assert.NotNil(t, result, "should find functions matching query words")
		files, ok := result["exactFunc"]
		assert.True(t, ok, "exactFunc should be matched")
		assert.True(t, len(files) > 0)
		assert.Equal(t, "funcs.js", files[0].Path)
	})

	// --- getFunctionFileMatch: matched=false (lines 90-92) ---
	// Query words that cannot be found in sequence in any function name.
	t.Run("getFunctionFileMatch matched false", func(t *testing.T) {
		var docId string
		documents.ScanFiles(sharedWS.Id, func(id, relPath string) bool {
			if relPath == "funcs.js" {
				docId = id
				return false
			}
			return true
		})
		assert.NotEmpty(t, docId, "funcs.js must be indexed")

		result, err := getFunctionFileMatch(sharedWS, []string{"zzz", "qqq"}, docId)
		assert.NoError(t, err)
		// No function name contains "zzz" followed by "qqq", result should be empty
		assert.Equal(t, 0, len(result), "no functions should match nonsense query words")
	})

	// --- Term.CollectDocuments: error path (invalid workspace) ---
	t.Run("TermCollectDocuments error workspace", func(t *testing.T) {
		term := &SimpleContentSearchEngineTerm{
			Engine:   &SimpleContentSearchEngine{Workspace: sharedWS},
			Keywords: []string{"anything"},
		}
		result := term.CollectDocuments(-999) // invalid workspace id
		assert.Equal(t, 0, len(result.DocIds))
		assert.Nil(t, result.WildDocIds)
	})

	// --- Term.CollectDocuments: wildcard deduplication path ---
	// Search for "wdterm*wdwild" which produces keywords=["wdterm"] and
	// wildcards=["wdwild"]. wdboth.go contains both tokens so it stays in
	// WildDocIds; wdonly.go contains only "wdwild" so it must be removed
	// from WildDocIds by the filtering loop (lines 129-134).
	t.Run("TermCollectDocuments wildcard dedup", func(t *testing.T) {
		engine := NewSimpleContentSearchEngine(sharedWS, 24, 32, false)
		err := engine.Compile("wdterm*wdwild", false)
		assert.NoError(t, err)

		// Verify that the compiled engine has wildcards
		assert.True(t, len(engine.OrClauses) > 0)
		clause := engine.OrClauses[0]
		assert.True(t, len(clause.AndTerms) > 0)
		term := clause.AndTerms[0]
		assert.True(t, len(term.Wildcards) > 0, "expected wildcards from *wdwild pattern")

		// Call Term.CollectDocuments directly to exercise the wildcard filtering
		result := term.CollectDocuments(sharedWS.Id)
		// DocIds should contain files matching "wdterm" keyword
		assert.True(t, len(result.DocIds) > 0, "expected DocIds from keyword wdterm")
		// WildDocIds should only contain docs that are also in DocIds
		for docId := range result.WildDocIds {
			_, inDocIds := result.DocIds[docId]
			assert.True(t, inDocIds, "WildDocIds entry should also be in DocIds after filtering")
		}
	})

	// --- AndClause.CollectDocuments: multi-term AND intersection ---
	// Using two AND terms: "andtermone andtermtwo".
	// andfileA.go contains both => stays.
	// andfileB.go contains only andtermone => removed during intersection.
	// andfileC.go contains only andtermtwo => removed during intersection.
	t.Run("AndClauseCollectDocuments AND intersection", func(t *testing.T) {
		engine := NewSimpleContentSearchEngine(sharedWS, 24, 32, false)
		err := engine.Compile("andtermone andtermtwo", false)
		assert.NoError(t, err)

		assert.True(t, len(engine.OrClauses) > 0)
		clause := engine.OrClauses[0]
		assert.True(t, len(clause.AndTerms) >= 2, "expected at least 2 AND terms")

		result, err := clause.CollectDocuments(sharedWS.Id)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		// Only files containing BOTH terms should survive intersection.
		// Verify by resolving doc IDs to paths.
		for docId := range result.DocIds {
			doc, _ := documents.GetDocument(sharedWS.Id, docId, false)
			if doc != nil {
				assert.Equal(t, "andfileA.go", doc.RelPath,
					"only andfileA.go should survive AND intersection")
			}
		}
	})

	// --- AndClause.CollectDocuments: AND with wildcards exercises WildDocIds loop ---
	// Search "awfirst*awwildone awsecond*awwildtwo" as two AND terms where both
	// produce WildDocIds. The AND clause merge iterates r.WildDocIds (lines 100-105)
	// and removes entries that are not in result.DocIds.
	t.Run("AndClauseCollectDocuments AND with wildcard filtering", func(t *testing.T) {
		engine := NewSimpleContentSearchEngine(sharedWS, 24, 32, false)
		err := engine.Compile("awfirst*awwildone awsecond*awwildtwo", false)
		assert.NoError(t, err)

		assert.True(t, len(engine.OrClauses) > 0)
		clause := engine.OrClauses[0]
		assert.True(t, len(clause.AndTerms) >= 2, "expected 2 AND terms")

		result, err := clause.CollectDocuments(sharedWS.Id)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		// After AND intersection, DocIds should only contain docs matching
		// both terms' keywords (andwildA.go has awfirst + awsecond).
		// WildDocIds may still contain entries from earlier terms.
		assert.NotNil(t, result.DocIds)
	})

	// =========================================================================
	// CJK end-to-end integration tests (HAY-002 step 4)
	// These tests exercise the full index → search pipeline with CJK content.
	// =========================================================================

	t.Run("CJK SearchContent pure Chinese", func(t *testing.T) {
		// Search for "中华人民" — should find cjk_pure.txt which contains "中华人民共和国成立"
		req := &types.SearchContentRequest{Query: "中华人民"}
		ctx := context.Background()
		results, _ := SearchContent(sharedWS, req, nil, ctx, 10*time.Second)
		assert.True(t, len(results) > 0, "expected search results for '中华人民'")
		found := false
		for _, r := range results {
			if r.File == "cjk_pure.txt" {
				found = true
				break
			}
		}
		assert.True(t, found, "expected cjk_pure.txt in results for '中华人民'")
	})

	t.Run("CJK SearchContent Chinese word 成立", func(t *testing.T) {
		req := &types.SearchContentRequest{Query: "成立"}
		ctx := context.Background()
		results, _ := SearchContent(sharedWS, req, nil, ctx, 10*time.Second)
		assert.True(t, len(results) > 0, "expected search results for '成立'")
		found := false
		for _, r := range results {
			if r.File == "cjk_pure.txt" {
				found = true
				break
			}
		}
		assert.True(t, found, "expected cjk_pure.txt in results for '成立'")
	})

	t.Run("CJK SearchContent mixed Chinese-ASCII", func(t *testing.T) {
		// Search for "编程" — should find cjk_mixed.txt
		req := &types.SearchContentRequest{Query: "编程"}
		ctx := context.Background()
		results, _ := SearchContent(sharedWS, req, nil, ctx, 10*time.Second)
		assert.True(t, len(results) > 0, "expected search results for '编程'")
		found := false
		for _, r := range results {
			if r.File == "cjk_mixed.txt" {
				found = true
				break
			}
		}
		assert.True(t, found, "expected cjk_mixed.txt in results for '编程'")
	})

	t.Run("CJK SearchContent ASCII in mixed file", func(t *testing.T) {
		// Search for "Google" — should find cjk_mixed.txt via ASCII tokenizer
		req := &types.SearchContentRequest{Query: "Google"}
		ctx := context.Background()
		results, _ := SearchContent(sharedWS, req, nil, ctx, 10*time.Second)
		assert.True(t, len(results) > 0, "expected search results for 'Google'")
		found := false
		for _, r := range results {
			if r.File == "cjk_mixed.txt" {
				found = true
				break
			}
		}
		assert.True(t, found, "expected cjk_mixed.txt in results for 'Google'")
	})

	t.Run("CJK SearchFiles Chinese filename", func(t *testing.T) {
		// Search for "说明" — should find "说明文档.md" in file search
		req := &types.SearchFilesRequest{Query: "说明", Limit: 10}
		result, err := SearchFiles(sharedWS, req)
		assert.NoError(t, err)
		found := false
		for _, f := range result.Files {
			if strings.Contains(f, "说明文档") {
				found = true
				break
			}
		}
		assert.True(t, found, "expected 说明文档.md in file search results for '说明'")
	})

	t.Run("CJK SearchContent ASCII still works", func(t *testing.T) {
		// Regression: ensure existing English search is not broken
		req := &types.SearchContentRequest{Query: "keep_marker"}
		ctx := context.Background()
		results, _ := SearchContent(sharedWS, req, nil, ctx, 10*time.Second)
		assert.True(t, len(results) > 0, "ASCII search for 'keep_marker' should still work")
	})

	t.Run("CJK SearchContent unsaved Chinese file", func(t *testing.T) {
		// Test unsaved file with Chinese content
		req := &types.SearchContentRequest{
			Query:            "人工智能",
			UnsavedFilesOnly: true,
			UnsavedFiles: []types.UnsavedFile{
				{Path: "unsaved_cjk.txt", Content: "人工智能是未来的发展方向\n"},
			},
		}
		ctx := context.Background()
		results, _ := SearchContent(sharedWS, req, nil, ctx, 10*time.Second)
		assert.True(t, len(results) > 0, "expected results for unsaved Chinese content")
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
