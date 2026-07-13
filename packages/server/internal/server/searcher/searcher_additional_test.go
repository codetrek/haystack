package searcher

import (
	"strings"
	"testing"

	"github.com/codetrek/haystack/packages/core/engine"
	"github.com/stretchr/testify/assert"
)

func newTestEngine(maxWild, maxKwDist int, wholeWord bool) *engine.Engine {
	return engine.New(nil, nil, 0, engine.Options{
		MaxWildcardLength:  maxWild,
		MaxKeywordDistance: maxKwDist,
		WholeWord:          wholeWord,
	})
}

func TestFuzzyMatchWithScore_ExactContains(t *testing.T) {
	matched, score := fuzzyMatchWithScore("hello", "say hello world")
	assert.True(t, matched)
	assert.Equal(t, 100, score)
}

func TestFuzzyMatchWithScore_CaseInsensitive(t *testing.T) {
	matched, score := fuzzyMatchWithScore("HELLO", "say hello world")
	assert.True(t, matched)
	assert.Equal(t, 100, score)
}

func TestFuzzyMatchWithScore_FilePath(t *testing.T) {
	matched, score := fuzzyMatchWithScore("main", "src/pkg/main.go")
	assert.True(t, matched)
	assert.Equal(t, 100, score) // exact contains
}

func TestFuzzyMatchWithScore_FuzzyPath(t *testing.T) {
	matched, score := fuzzyMatchWithScore("mgo", "src/main.go")
	assert.True(t, matched)
	assert.True(t, score > 0)
}

func TestSearchInContent_BasicMatch(t *testing.T) {
	eng := newTestEngine(24, 32, false)
	err := eng.Compile("hello", false)
	assert.NoError(t, err)

	content := "line1\nhello world\nline3\nhello again\n"
	result, err := searchInContent("test.txt", strings.NewReader(content), eng, 0, nil, new(int))
	assert.NoError(t, err)
	assert.Equal(t, 2, len(result.Lines))
	assert.Equal(t, 2, result.Lines[0].Line.LineNumber)
	assert.Equal(t, 4, result.Lines[1].Line.LineNumber)
}

func TestSearchInContent_NoMatch(t *testing.T) {
	eng := newTestEngine(24, 32, false)
	eng.Compile("nonexistent", false) //nolint:errcheck

	content := "line1\nline2\nline3\n"
	result, err := searchInContent("test.txt", strings.NewReader(content), eng, 0, nil, new(int))
	assert.NoError(t, err)
	assert.Equal(t, 0, len(result.Lines))
}

func TestSearchInContent_BeforeAfter(t *testing.T) {
	eng := newTestEngine(24, 32, false)
	eng.Compile("target", false) //nolint:errcheck

	content := "before1\nbefore2\ntarget line\nafter1\nafter2\n"
	result, err := searchInContent("test.txt", strings.NewReader(content), eng, 2, nil, new(int))
	assert.NoError(t, err)
	assert.Equal(t, 1, len(result.Lines))
	assert.Equal(t, 2, len(result.Lines[0].Before))
	assert.Equal(t, 2, len(result.Lines[0].After))
}

func TestTokenizeWithQuotes_Basic(t *testing.T) {
	tokens := engine.TokenizeWithQuotes(`word1 "exact phrase" word2`)
	assert.Equal(t, []string{"word1", `"exact phrase"`, "word2"}, tokens)
}

func TestTokenizeWithQuotes_WithPipe(t *testing.T) {
	tokens := engine.TokenizeWithQuotes(`hello | world`)
	assert.Equal(t, []string{"hello", "|", "world"}, tokens)
}

func TestTokenizeWithQuotes_EscapedQuote(t *testing.T) {
	tokens := engine.TokenizeWithQuotes(`word1 "say \"hi\"" word2`)
	assert.Equal(t, 3, len(tokens))
}

func TestTokenizeWithQuotes_Empty(t *testing.T) {
	tokens := engine.TokenizeWithQuotes("")
	assert.Equal(t, 0, len(tokens))
}

func TestIsQuotedPhrase(t *testing.T) {
	assert.True(t, engine.IsQuotedPhrase(`"hello world"`))
	assert.False(t, engine.IsQuotedPhrase(`hello`))
	assert.False(t, engine.IsQuotedPhrase(`"`))
	assert.False(t, engine.IsQuotedPhrase(`"a`))
}

func TestUnwrapQuotes(t *testing.T) {
	assert.Equal(t, "hello world", engine.UnwrapQuotes(`"hello world"`))
}

func TestSimpleContentSearchEngine_String(t *testing.T) {
	eng := newTestEngine(24, 32, false)
	eng.Compile("hello world", false) //nolint:errcheck
	s := eng.String()
	assert.NotEmpty(t, s)
}

func TestSimpleContentSearchEngine_CompileEmpty(t *testing.T) {
	eng := newTestEngine(24, 32, false)
	err := eng.Compile("", false)
	assert.Error(t, err)
}

func TestSimpleContentSearchEngine_CompileOR(t *testing.T) {
	eng := newTestEngine(24, 32, false)
	err := eng.Compile("hello | world", false)
	assert.NoError(t, err)
	// Verify 2 OR clauses by checking IsLineMatch behavior
	assert.True(t, len(eng.IsLineMatch("hello")) > 0)
	assert.True(t, len(eng.IsLineMatch("world")) > 0)
	assert.Equal(t, 0, len(eng.IsLineMatch("nothing")))
}

func TestSimpleContentSearchEngine_WholeWord(t *testing.T) {
	eng := newTestEngine(24, 32, true)
	err := eng.Compile("test", false)
	assert.NoError(t, err)

	// "test" should match as whole word
	matches := eng.IsLineMatch("this is a test line")
	assert.True(t, len(matches) > 0)

	// "testing" should NOT match as whole word
	matches = eng.IsLineMatch("this is testing")
	assert.Equal(t, 0, len(matches))
}
