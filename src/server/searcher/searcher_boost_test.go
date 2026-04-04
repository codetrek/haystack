package searcher

import (
	"fmt"
	"strings"
	"testing"

	"github.com/codetrek/haystack/server/core/invertedindex"
	"github.com/codetrek/haystack/shared/types"
	"github.com/stretchr/testify/assert"
)

// =========================================================================
// Unit tests that do NOT require DB/indexer
// =========================================================================

// --- sortDocuments: filter-based removal path ---

func TestSortDocuments_FilterRejectsDocs(t *testing.T) {
	sr := &invertedindex.SearchResult{
		DocIds:     map[string]struct{}{},
		WildDocIds: map[string]struct{}{},
	}
	result := sortDocuments(0, nil, sr, func(_ string) bool { return false })
	assert.NotNil(t, result)
	assert.Equal(t, 0, len(result))
}

func TestSortDocuments_EditorEmpty(t *testing.T) {
	sr := &invertedindex.SearchResult{
		DocIds:     map[string]struct{}{},
		WildDocIds: map[string]struct{}{},
	}
	editor := &types.Editor{}
	result := sortDocuments(0, editor, sr, func(_ string) bool { return true })
	assert.NotNil(t, result)
}

// --- searchInContent: scanner error path ---

func TestSearchInContent_ScannerError(t *testing.T) {
	engine := NewSimpleContentSearchEngine(nil, 24, 32, false)
	err := engine.Compile("hello", false)
	assert.NoError(t, err)

	totalHits := 0
	_, err = searchInContent("test.txt", &errorReader{}, engine, 0, nil, &totalHits)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error scanning content")
}

type errorReader struct{}

func (e *errorReader) Read(p []byte) (n int, err error) {
	return 0, fmt.Errorf("simulated read error")
}

// --- fuzzyMatchWithScore edge cases ---

func TestFuzzyMatchWithScore_PositionAfterUnderscore(t *testing.T) {
	matched, score := fuzzyMatchWithScore("abcdef", "x_abcdef")
	assert.True(t, matched)
	assert.Equal(t, 100, score)
}

func TestFuzzyMatchWithScore_FuzzyStartNearBeginning(t *testing.T) {
	matched, score := fuzzyMatchWithScore("xz", "xyz")
	assert.True(t, matched)
	assert.True(t, score > 0)
}

func TestFuzzyMatchWithScore_FuzzyAfterDelimiter(t *testing.T) {
	matched, score := fuzzyMatchWithScore("bx", "aaaa.bx")
	assert.True(t, matched)
	assert.Equal(t, 100, score)
}

func TestFuzzyMatchWithScore_FuzzyNoDelimiter(t *testing.T) {
	matched, score := fuzzyMatchWithScore("zq", "abcdzefghijklmnoq")
	assert.True(t, matched)
	assert.True(t, score > 0)
}

func TestFuzzyMatchWithScore_FilePathBonus(t *testing.T) {
	matched, score := fuzzyMatchWithScore("mgo", "a/b/c/mgo.txt")
	assert.True(t, matched)
	assert.Equal(t, 100, score)
}

func TestFuzzyMatchWithScore_FilePathFuzzyFilename(t *testing.T) {
	matched, score := fuzzyMatchWithScore("mf", "src/deep/dir/mainfile.go")
	assert.True(t, matched)
	assert.True(t, score > 0)
}

// --- Compile edge cases ---

func TestCompile_OnlyANDTokensSkipped(t *testing.T) {
	eng := NewSimpleContentSearchEngine(nil, 4, 4, false)
	err := eng.Compile("AND AND", false)
	assert.Error(t, err)
}

// --- IsLineMatch with multiple OR clauses ---

func TestIsLineMatch_MultipleOrClauses_SecondMatches(t *testing.T) {
	eng := NewSimpleContentSearchEngine(nil, 24, 32, false)
	err := eng.Compile("nonexistent | hello", false)
	assert.NoError(t, err)

	matches := eng.IsLineMatch("hello world")
	assert.True(t, len(matches) > 0)
}

func TestIsLineMatch_MultipleOrClauses_NoneMatch(t *testing.T) {
	eng := NewSimpleContentSearchEngine(nil, 24, 32, false)
	err := eng.Compile("alpha | beta", false)
	assert.NoError(t, err)

	matches := eng.IsLineMatch("hello world")
	assert.Equal(t, 0, len(matches))
}

// --- searchInContent edge cases ---

func TestSearchInContent_ContextClampedEnd(t *testing.T) {
	engine := NewSimpleContentSearchEngine(nil, 24, 32, false)
	engine.Compile("last", false)

	content := "line1\nline2\nline3\nlast"
	totalHits := 0
	result, err := searchInContent("test.txt", strings.NewReader(content), engine, 3, nil, &totalHits)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(result.Lines))
	assert.Equal(t, 3, len(result.Lines[0].Before))
	assert.Equal(t, 0, len(result.Lines[0].After))
}

func TestSearchInContent_MaxResultsHitMidFile(t *testing.T) {
	engine := NewSimpleContentSearchEngine(nil, 24, 32, false)
	engine.Compile("item", false)

	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "item is here")
	}
	content := strings.Join(lines, "\n")

	limit := &types.SearchLimit{MaxResults: 3, MaxResultsPerFile: 100}
	totalHits := 0
	result, err := searchInContent("test.txt", strings.NewReader(content), engine, 0, limit, &totalHits)
	assert.NoError(t, err)
	assert.Equal(t, 3, len(result.Lines))
}

// --- CollectDocuments unit tests ---

func TestCollectDocuments_NoOrClauses(t *testing.T) {
	eng := &SimpleContentSearchEngine{
		OrClauses: []*SimpleContentSearchEngineAndClause{},
	}
	result, err := eng.CollectDocuments()
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, 0, len(result.DocIds))
}

func TestAndClauseCollectDocuments_NoKeywordTerms(t *testing.T) {
	clause := &SimpleContentSearchEngineAndClause{
		AndTerms: []*SimpleContentSearchEngineTerm{
			{Pattern: "test", Keywords: []string{}},
		},
	}
	result, err := clause.CollectDocuments(0)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, 0, len(result.DocIds))
}

// --- TokenizeWithQuotes edge cases ---

func TestTokenizeWithQuotes_EscapedQuoteAtEnd(t *testing.T) {
	tokens := TokenizeWithQuotes(`hello\"`)
	assert.Equal(t, 1, len(tokens))
	assert.Contains(t, tokens[0], `\"`)
}

func TestTokenizeWithQuotes_PipeNoSpaces(t *testing.T) {
	tokens := TokenizeWithQuotes("a|b")
	assert.Equal(t, []string{"a", "|", "b"}, tokens)
}

func TestTokenizeWithQuotes_OnlyQuoted(t *testing.T) {
	tokens := TokenizeWithQuotes(`"hello world"`)
	assert.Equal(t, []string{`"hello world"`}, tokens)
}
