package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// createEngine is a test helper that creates and compiles an engine.
func createEngine(t *testing.T, query string, caseSensitive bool, maxWildLen, maxKwDist int) *Engine {
	t.Helper()
	eng := New(nil, nil, 0, Options{
		MaxWildcardLength:  maxWildLen,
		MaxKeywordDistance: maxKwDist,
	})
	assert.NoError(t, eng.Compile(query, caseSensitive))
	return eng
}

// createWholeWordEngine is a test helper that creates a whole-word engine.
func createWholeWordEngine(t *testing.T, query string, caseSensitive bool, maxWildLen, maxKwDist int) *Engine {
	t.Helper()
	eng := New(nil, nil, 0, Options{
		MaxWildcardLength:  maxWildLen,
		MaxKeywordDistance: maxKwDist,
		WholeWord:          true,
	})
	assert.NoError(t, eng.Compile(query, caseSensitive))
	return eng
}

func TestParseQuerySimple(t *testing.T) {
	type wantTerm struct {
		Pattern    string
		RegPattern string
		Keywords   []string
	}
	type wantClause struct {
		Terms []wantTerm
	}
	type OneCase struct {
		query       string
		wantErr     bool
		wantClauses []wantClause
	}

	runCase := func(t *testing.T, tt OneCase) {
		t.Helper()
		eng := New(nil, nil, 0, Options{MaxWildcardLength: 4, MaxKeywordDistance: 4})
		err := eng.Compile(tt.query, true)

		if tt.wantErr {
			assert.Error(t, err, "Compile() expected an error for query: %s", tt.query)
			return
		}
		assert.NoError(t, err, "Compile() returned unexpected error: %v for query: %s", err, tt.query)
		if err != nil {
			return
		}

		if !assert.Equal(t, len(tt.wantClauses), len(eng.orClauses), "OR clause count mismatch for query: %s", tt.query) {
			return
		}

		for i, gotClause := range eng.orClauses {
			wantC := tt.wantClauses[i]
			if !assert.Equal(t, len(wantC.Terms), len(gotClause.andTerms), "OR clause %d: AND term count mismatch for query: %s", i, tt.query) {
				continue
			}
			for j, gotTerm := range gotClause.andTerms {
				wantT := wantC.Terms[j]
				assert.Equal(t, wantT.Pattern, gotTerm.Pattern, "Pattern %d in clause %d mismatch for query: %s", j, i, tt.query)
				assert.Equal(t, wantT.RegPattern, gotTerm.RegPattern, "RegPattern %d in clause %d mismatch for query: %s", j, i, tt.query)
				assert.Equal(t, wantT.Keywords, gotTerm.Keywords, "Keywords %d in clause %d mismatch for query: %s", j, i, tt.query)
			}
		}
	}

	t.Run("empty query", func(t *testing.T) {
		runCase(t, OneCase{query: "", wantErr: true})
	})

	t.Run("whitespace only", func(t *testing.T) {
		runCase(t, OneCase{query: "   ", wantErr: true})
	})

	t.Run("single pattern", func(t *testing.T) {
		runCase(t, OneCase{
			query: "test",
			wantClauses: []wantClause{
				{Terms: []wantTerm{{Pattern: "test", RegPattern: "test", Keywords: []string{"test"}}}},
			},
		})
	})

	t.Run("multiple AND patterns", func(t *testing.T) {
		runCase(t, OneCase{
			query: "test1 test2",
			wantClauses: []wantClause{
				{Terms: []wantTerm{
					{Pattern: "test1", RegPattern: "test1", Keywords: []string{"test1"}},
					{Pattern: "test2", RegPattern: "test2", Keywords: []string{"test2"}},
				}},
			},
		})
	})

	t.Run("OR clauses", func(t *testing.T) {
		runCase(t, OneCase{
			query: "test1 test2 | test3",
			wantClauses: []wantClause{
				{Terms: []wantTerm{
					{Pattern: "test1", RegPattern: "test1", Keywords: []string{"test1"}},
					{Pattern: "test2", RegPattern: "test2", Keywords: []string{"test2"}},
				}},
				{Terms: []wantTerm{
					{Pattern: "test3", RegPattern: "test3", Keywords: []string{"test3"}},
				}},
			},
		})
	})

	t.Run("pattern with prefix", func(t *testing.T) {
		runCase(t, OneCase{
			query: "prefix:value",
			wantClauses: []wantClause{
				{Terms: []wantTerm{
					{Pattern: "prefix:value", RegPattern: "prefix\\:value", Keywords: []string{"prefix", "value"}},
				}},
			},
		})
	})

	t.Run("complex query with prefixes and OR", func(t *testing.T) {
		runCase(t, OneCase{
			query: "field1:value1 field2:value2 | field3:value3",
			wantClauses: []wantClause{
				{Terms: []wantTerm{
					{Pattern: "field1:value1", RegPattern: "field1\\:value1", Keywords: []string{"field1", "value1"}},
					{Pattern: "field2:value2", RegPattern: "field2\\:value2", Keywords: []string{"field2", "value2"}},
				}},
				{Terms: []wantTerm{
					{Pattern: "field3:value3", RegPattern: "field3\\:value3", Keywords: []string{"field3", "value3"}},
				}},
			},
		})
	})

	t.Run("quoted query for exact matching", func(t *testing.T) {
		runCase(t, OneCase{
			query: "\"test1 test2\"",
			wantClauses: []wantClause{
				{Terms: []wantTerm{
					{Pattern: "\"test1 test2\"", RegPattern: "test1 test2", Keywords: []string{"test1", "test2"}},
				}},
			},
		})
	})

	t.Run("mixed regular and quoted terms", func(t *testing.T) {
		runCase(t, OneCase{
			query: "regular \"quoted phrase\" another",
			wantClauses: []wantClause{
				{Terms: []wantTerm{
					{Pattern: "regular", RegPattern: "regular", Keywords: []string{"regular"}},
					{Pattern: "\"quoted phrase\"", RegPattern: "quoted phrase", Keywords: []string{"quoted", "phrase"}},
					{Pattern: "another", RegPattern: "another", Keywords: []string{"another"}},
				}},
			},
		})
	})

	t.Run("quoted term with OR clause", func(t *testing.T) {
		runCase(t, OneCase{
			query: "\"exact phrase\" | regular term",
			wantClauses: []wantClause{
				{Terms: []wantTerm{
					{Pattern: "\"exact phrase\"", RegPattern: "exact phrase", Keywords: []string{"exact", "phrase"}},
				}},
				{Terms: []wantTerm{
					{Pattern: "regular", RegPattern: "regular", Keywords: []string{"regular"}},
					{Pattern: "term", RegPattern: "term", Keywords: []string{"term"}},
				}},
			},
		})
	})

	t.Run("quoted term with special characters", func(t *testing.T) {
		runCase(t, OneCase{
			query: "\"test.with[special]chars\"",
			wantClauses: []wantClause{
				{Terms: []wantTerm{
					{
						Pattern:    "\"test.with[special]chars\"",
						RegPattern: "test\\.with\\[special\\]chars",
						Keywords:   []string{"test", "with", "special", "chars"},
					},
				}},
			},
		})
	})

	t.Run("single word quoted term", func(t *testing.T) {
		runCase(t, OneCase{
			query: "\"singleword\"",
			wantClauses: []wantClause{
				{Terms: []wantTerm{
					{Pattern: "\"singleword\"", RegPattern: "singleword", Keywords: []string{"singleword"}},
				}},
			},
		})
	})

	t.Run("quoted phrase with multiple words and punctuation", func(t *testing.T) {
		runCase(t, OneCase{
			query: "\"hello, world! how are you?\"",
			wantClauses: []wantClause{
				{Terms: []wantTerm{
					{
						Pattern:    "\"hello, world! how are you?\"",
						RegPattern: "hello, world! how are you\\?",
						Keywords:   []string{"hello", "world", "how", "are", "you"},
					},
				}},
			},
		})
	})

	t.Run("quoted phrase with numbers and symbols", func(t *testing.T) {
		runCase(t, OneCase{
			query: "\"version 1.2.3-beta+build.456\"",
			wantClauses: []wantClause{
				{Terms: []wantTerm{
					{
						Pattern:    "\"version 1.2.3-beta+build.456\"",
						RegPattern: "version 1\\.2\\.3-beta\\+build\\.456",
						Keywords:   []string{"version", "1.2.3", "beta", "build", "456"},
					},
				}},
			},
		})
	})

	t.Run("mixed quoted phrases with AND and OR operators", func(t *testing.T) {
		runCase(t, OneCase{
			query: "\"first phrase\" second | third \"fourth phrase\"",
			wantClauses: []wantClause{
				{Terms: []wantTerm{
					{Pattern: "\"first phrase\"", RegPattern: "first phrase", Keywords: []string{"first", "phrase"}},
					{Pattern: "second", RegPattern: "second", Keywords: []string{"second"}},
				}},
				{Terms: []wantTerm{
					{Pattern: "third", RegPattern: "third", Keywords: []string{"third"}},
					{Pattern: "\"fourth phrase\"", RegPattern: "fourth phrase", Keywords: []string{"fourth", "phrase"}},
				}},
			},
		})
	})

	t.Run("quoted phrase with escaped quotes", func(t *testing.T) {
		runCase(t, OneCase{
			query: "\"code with \\\"quoted\\\" text\"",
			wantClauses: []wantClause{
				{Terms: []wantTerm{
					{
						Pattern:    "\"code with \\\"quoted\\\" text\"",
						RegPattern: "code with \"quoted\" text",
						Keywords:   []string{"code", "with", "quoted", "text"},
					},
				}},
			},
		})
	})

	t.Run("with wild match", func(t *testing.T) {
		runCase(t, OneCase{
			query: "test*abc?defg-hij.efg",
			wantClauses: []wantClause{
				{Terms: []wantTerm{
					{
						Pattern:    "test*abc?defg-hij.efg",
						RegPattern: "test.{0,4}abc\\?defg-hij\\.efg",
						Keywords:   []string{"test", "defg", "hij", "efg"},
					},
				}},
			},
		})

		runCase(t, OneCase{
			query: "abc?--defg*--hij",
			wantClauses: []wantClause{
				{Terms: []wantTerm{
					{Pattern: "abc?--defg*--hij", RegPattern: "abc\\?--defg.{0,4}--hij", Keywords: []string{"abc", "defg", "hij"}},
				}},
			},
		})

		runCase(t, OneCase{
			query: "abc?..defg",
			wantClauses: []wantClause{
				{Terms: []wantTerm{
					{Pattern: "abc?..defg", RegPattern: "abc\\?\\.\\.defg", Keywords: []string{"abc", "defg"}},
				}},
			},
		})
	})

	t.Run("term with dash", func(t *testing.T) {
		runCase(t, OneCase{
			query: "exactly-with-dash another",
			wantClauses: []wantClause{
				{Terms: []wantTerm{
					{Pattern: "exactly-with-dash", RegPattern: "exactly-with-dash", Keywords: []string{"exactly", "with", "dash"}},
					{Pattern: "another", RegPattern: "another", Keywords: []string{"another"}},
				}},
			},
		})
	})

	t.Run("leading special chars", func(t *testing.T) {
		runCase(t, OneCase{
			query: "->with-pointer",
			wantClauses: []wantClause{
				{Terms: []wantTerm{
					{Pattern: "->with-pointer", RegPattern: "->with-pointer", Keywords: []string{"with", "pointer"}},
				}},
			},
		})

		runCase(t, OneCase{
			query: "$with-pointer",
			wantClauses: []wantClause{
				{Terms: []wantTerm{
					{Pattern: "$with-pointer", RegPattern: "\\$with-pointer", Keywords: []string{"with", "pointer"}},
				}},
			},
		})
	})

	t.Run("trailing special chars", func(t *testing.T) {
		runCase(t, OneCase{
			query: "For this, u",
			wantClauses: []wantClause{
				{Terms: []wantTerm{
					{Pattern: "For", RegPattern: "For", Keywords: []string{"For"}},
					{Pattern: "this,", RegPattern: "this,", Keywords: []string{"this"}},
					{Pattern: "u", RegPattern: "u", Keywords: []string{}},
				}},
			},
		})
	})
}

// TestTokenizeWithQuotes exercises the exported TokenizeWithQuotes function.
func TestTokenizeWithQuotes(t *testing.T) {
	type OneCase struct {
		input string
		want  []string
	}

	runCase := func(t *testing.T, tt OneCase) {
		t.Helper()
		got := TokenizeWithQuotes(tt.input)
		if !assert.Equal(t, len(tt.want), len(got), "token count mismatch for input: %s", tt.input) {
			return
		}
		for i, tok := range got {
			assert.Equal(t, tt.want[i], tok, "token %d mismatch for input: %s", i, tt.input)
		}
	}

	t.Run("simple space-separated terms", func(t *testing.T) {
		runCase(t, OneCase{input: "term1 term2 term3", want: []string{"term1", "term2", "term3"}})
	})
	t.Run("pattern with prefix", func(t *testing.T) {
		runCase(t, OneCase{input: "prefix:value", want: []string{"prefix:value"}})
	})
	t.Run("quoted query for exact matching", func(t *testing.T) {
		runCase(t, OneCase{input: "\"test1 test2\"", want: []string{"\"test1 test2\""}})
	})
	t.Run("mixed regular and quoted terms", func(t *testing.T) {
		runCase(t, OneCase{input: "term1 \"quoted phrase\" term2", want: []string{"term1", "\"quoted phrase\"", "term2"}})
	})
	t.Run("quoted term with OR clause", func(t *testing.T) {
		runCase(t, OneCase{input: "\"exact phrase\" | regular term", want: []string{"\"exact phrase\"", "|", "regular", "term"}})
	})
	t.Run("quoted term with special characters", func(t *testing.T) {
		runCase(t, OneCase{input: "\"test.with[special]chars\"", want: []string{"\"test.with[special]chars\""}})
	})
	t.Run("single word quoted term", func(t *testing.T) {
		runCase(t, OneCase{input: "\"singleword\"", want: []string{"\"singleword\""}})
	})
	t.Run("multiple quoted phrases", func(t *testing.T) {
		runCase(t, OneCase{input: "\"first phrase\" regular \"second phrase\"", want: []string{"\"first phrase\"", "regular", "\"second phrase\""}})
	})
	t.Run("quoted phrase with internal quotes", func(t *testing.T) {
		runCase(t, OneCase{
			input: "before \"phrase with \\\"internal quotes\\\"\" after",
			want:  []string{"before", "\"phrase with \\\"internal quotes\\\"\"", "after"},
		})
	})
	t.Run("unclosed quote", func(t *testing.T) {
		runCase(t, OneCase{input: "term1 \"unclosed quote", want: []string{"term1", "\"unclosed quote"}})
	})
}

// TestQuoteHelpers exercises IsQuotedPhrase and UnwrapQuotes.
func TestQuoteHelpers(t *testing.T) {
	t.Run("IsQuotedPhrase empty", func(t *testing.T) {
		assert.False(t, IsQuotedPhrase(""))
	})
	t.Run("IsQuotedPhrase quoted", func(t *testing.T) {
		assert.True(t, IsQuotedPhrase("\"quoted\""))
	})
	t.Run("IsQuotedPhrase not quoted", func(t *testing.T) {
		assert.False(t, IsQuotedPhrase("notquoted"))
	})
	t.Run("IsQuotedPhrase multiple words", func(t *testing.T) {
		assert.True(t, IsQuotedPhrase("\"multiple words\""))
	})
	t.Run("IsQuotedPhrase single quote", func(t *testing.T) {
		assert.False(t, IsQuotedPhrase("\""))
	})
	t.Run("IsQuotedPhrase empty quotes", func(t *testing.T) {
		assert.True(t, IsQuotedPhrase("\"\""))
	})
	t.Run("IsQuotedPhrase partial left", func(t *testing.T) {
		assert.False(t, IsQuotedPhrase("\"partial"))
	})
	t.Run("IsQuotedPhrase partial right", func(t *testing.T) {
		assert.False(t, IsQuotedPhrase("partial\""))
	})
	t.Run("UnwrapQuotes quoted", func(t *testing.T) {
		assert.Equal(t, "quoted", UnwrapQuotes("\"quoted\""))
	})
	t.Run("UnwrapQuotes multiple words", func(t *testing.T) {
		assert.Equal(t, "multiple words", UnwrapQuotes("\"multiple words\""))
	})
	t.Run("UnwrapQuotes empty", func(t *testing.T) {
		assert.Equal(t, "", UnwrapQuotes("\"\""))
	})
}

// TestAndClauseIsLineMatch_Empty tests that an empty andClause returns no matches.
func TestAndClauseIsLineMatch_Empty(t *testing.T) {
	c := &andClause{andTerms: []*term{}}
	assert.Equal(t, [][]int{}, c.isLineMatch("hello world"))
}

// TestAndClauseIsLineMatch_NilRegex tests that a nil regex returns no matches.
func TestAndClauseIsLineMatch_NilRegex(t *testing.T) {
	c := &andClause{
		andTerms: []*term{{Pattern: "test"}},
		regex:    nil,
	}
	assert.Equal(t, [][]int{}, c.isLineMatch("hello world"))
}

// TestAndClauseCollectDocuments_NoKeywordTerms tests that terms with no keywords return empty.
func TestAndClauseCollectDocuments_NoKeywordTerms(t *testing.T) {
	eng := New(nil, nil, 0, Options{})
	c := &andClause{
		engine:   eng,
		andTerms: []*term{{engine: eng, Pattern: "test", Keywords: []string{}}},
	}
	result, err := c.collectDocuments(0)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(result.DocIds))
}

// TestCollectDocuments_NoOrClauses tests engine.CollectDocuments with no compiled clauses.
func TestCollectDocuments_NoOrClauses(t *testing.T) {
	eng := New(nil, nil, 0, Options{})
	result, err := eng.CollectDocuments()
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, 0, len(result.DocIds))
}

func TestContentLineMatch(t *testing.T) {
	t.Run("Test with special characters", func(t *testing.T) {
		eng := createEngine(t, "special characters", true, 4, 4)
		actual := eng.IsLineMatch("with special characters: !@#$%^&*()")
		assert.Equal(t, [][]int{{5, 23}}, actual)
	})

	t.Run("Test single keyword", func(t *testing.T) {
		eng := createEngine(t, "ContentSearch", true, 4, 4)
		assert.Equal(t, [][]int{{0, 13}}, eng.IsLineMatch("ContentSearchEngineAndClause"))
		assert.Equal(t, [][]int{{5, 18}}, eng.IsLineMatch("here@ContentSearchEngineAndClause"))
		assert.Equal(t, [][]int{{15, 28}}, eng.IsLineMatch("func (q *SimpleContentSearchEngineAndClause) CollectDocuments(workspaceId string)"))
	})

	t.Run("Test with multiple keywords", func(t *testing.T) {
		eng := createEngine(t, "ContentSearch Engine", true, 4, 4)
		assert.Equal(t, [][]int{{0, 19}}, eng.IsLineMatch("ContentSearchEngineAndClause"))
		assert.Equal(t, [][]int{{0, 20}}, eng.IsLineMatch("ContentSearch EngineAndClause"))
		assert.Equal(t, [][]int{{0, 20}}, eng.IsLineMatch("ContentSearch@EngineAndClause"))
		assert.Equal(t, [][]int{{5, 24}}, eng.IsLineMatch("here@ContentSearchEngineAndClause"))
		assert.Equal(t, [][]int{{15, 34}}, eng.IsLineMatch("func (q *SimpleContentSearchEngineAndClause) CollectDocuments"))
	})

	t.Run("Test with multiple matches", func(t *testing.T) {
		eng := createEngine(t, "ContentSearch Engine", true, 4, 4)
		assert.Equal(t, [][]int{{0, 19}, {29, 48}}, eng.IsLineMatch("ContentSearchEngineAndClause ContentSearchEngineAndClause"))
		assert.Equal(t, [][]int{{0, 20}, {30, 50}}, eng.IsLineMatch("ContentSearch EngineAndClause ContentSearch EngineAndClause"))
	})

	t.Run("Test with OR", func(t *testing.T) {
		eng := createEngine(t, "Content | Clause", true, 4, 4)
		assert.Equal(t, [][]int{{0, 7}}, eng.IsLineMatch("ContentSearchEngineAnd  Clause"))
		assert.Equal(t, [][]int{{16, 22}}, eng.IsLineMatch("SearchEngineAnd Clause"))
	})

	t.Run("Test with dot", func(t *testing.T) {
		eng := createEngine(t, "Content.Search", true, 4, 4)
		assert.Equal(t, [][]int{{0, 14}}, eng.IsLineMatch("Content.SearchEngineAndClause"))
		assert.Equal(t, [][]int{}, eng.IsLineMatch("Content-SearchEngineAndClause"))
	})

	t.Run("Test with hyphen", func(t *testing.T) {
		eng := createEngine(t, "Content-Search", true, 4, 4)
		assert.Equal(t, [][]int{{0, 14}}, eng.IsLineMatch("Content-SearchEngineAndClause"))
		assert.Equal(t, [][]int{}, eng.IsLineMatch("Content.SearchEngineAndClause"))
	})

	t.Run("Test with underscore", func(t *testing.T) {
		eng := createEngine(t, "Content_Search", true, 4, 4)
		assert.Equal(t, [][]int{{0, 14}}, eng.IsLineMatch("Content_SearchEngineAndClause"))
		assert.Equal(t, [][]int{}, eng.IsLineMatch("Content-SearchEngineAndClause"))
	})

	t.Run("Test with colon", func(t *testing.T) {
		eng := createEngine(t, "Content::Search", true, 4, 4)
		assert.Equal(t, [][]int{{5, 20}}, eng.IsLineMatch("func Content::SearchEngineAndClause"))
	})

	t.Run("Test case insensitivity", func(t *testing.T) {
		eng := createEngine(t, "contentsearch", false, 4, 4)
		assert.Equal(t, [][]int{{0, 13}}, eng.IsLineMatch("ContentSearchEngineAndClause"))
		assert.Equal(t, [][]int{{0, 13}}, eng.IsLineMatch("ContentsearchEngineAndClause"))
	})

	t.Run("Test keyword distance", func(t *testing.T) {
		eng := createEngine(t, "Content Search", true, 4, 4)
		assert.Equal(t, [][]int{{0, 17}}, eng.IsLineMatch("ContentWithSearch"))
		assert.Equal(t, [][]int{}, eng.IsLineMatch("ContentWithoutSearch"))
	})

	t.Run("Test wild match length", func(t *testing.T) {
		eng := createEngine(t, "Content*Search", true, 4, 4)
		assert.Equal(t, [][]int{{0, 17}}, eng.IsLineMatch("ContentWithSearch"))
		assert.Equal(t, [][]int{}, eng.IsLineMatch("ContentWithoutSearch"))
	})

	t.Run("Test empty line", func(t *testing.T) {
		eng := createEngine(t, "Content", true, 4, 4)
		assert.Equal(t, [][]int{}, eng.IsLineMatch(""))
	})

	t.Run("Test numbers and alphanumeric", func(t *testing.T) {
		eng := createEngine(t, "test123 abc456", true, 4, 4)
		assert.Equal(t, [][]int{{0, 14}}, eng.IsLineMatch("test123_abc456_function"))
		assert.Equal(t, [][]int{{7, 21}}, eng.IsLineMatch("prefix_test123_abc456"))
	})

	t.Run("Test mixed case with OR", func(t *testing.T) {
		eng := createEngine(t, "content | SEARCH", false, 4, 4)
		assert.Equal(t, [][]int{{0, 7}}, eng.IsLineMatch("ContentSearchEngine"))
		assert.Equal(t, [][]int{{0, 7}}, eng.IsLineMatch("CONTENT_SEARCH"))
	})

	t.Run("Test keyword distance edge cases", func(t *testing.T) {
		eng := createEngine(t, "test func", true, 4, 4)
		assert.Equal(t, [][]int{{0, 12}}, eng.IsLineMatch("test____func"))
		assert.Equal(t, [][]int{}, eng.IsLineMatch("test_____func"))
	})

	t.Run("Test mixed separators", func(t *testing.T) {
		eng := createEngine(t, "test.func-call_method", true, 4, 4)
		assert.Equal(t, [][]int{{0, 21}}, eng.IsLineMatch("test.func-call_method()"))
		assert.Equal(t, [][]int{}, eng.IsLineMatch("test-func.call_method()"))
	})

	t.Run("Regular pattern matching", func(t *testing.T) {
		eng := createEngine(t, "test", true, 4, 4)
		assert.Equal(t, [][]int{{10, 14}}, eng.IsLineMatch("This is a test line."))

		eng = createEngine(t, "missing", true, 4, 4)
		assert.Equal(t, [][]int{}, eng.IsLineMatch("This is a test line."))

		eng = createEngine(t, "test", true, 4, 4)
		assert.Equal(t, [][]int{{5, 9}, {15, 19}, {29, 33}}, eng.IsLineMatch("This test is a test line for testing."))

		eng = createEngine(t, "test", true, 4, 4)
		assert.Equal(t, [][]int{{10, 14}}, eng.IsLineMatch("This is a testing line."))

		eng = createEngine(t, "test", true, 4, 4)
		assert.Equal(t, [][]int{{12, 16}}, eng.IsLineMatch("This is a attest line."))
	})

	t.Run("Leading special characters", func(t *testing.T) {
		eng := createEngine(t, ".test", true, 4, 4)
		assert.Equal(t, [][]int{{0, 5}}, eng.IsLineMatch(".test line"))
	})

	t.Run("Exact phrase matching", func(t *testing.T) {
		eng := createEngine(t, "\"test line\"", true, 4, 4)
		assert.Equal(t, [][]int{{10, 19}}, eng.IsLineMatch("This is a test line for verification."))

		eng = createEngine(t, "\"func\"", true, 4, 4)
		assert.Equal(t, [][]int{{4, 8}}, eng.IsLineMatch("The function is called regularly."))

		eng = createEngine(t, "\"test line\"", true, 4, 4)
		assert.Equal(t, [][]int{{10, 19}}, eng.IsLineMatch("This is a test linear for verification."))

		eng = createEngine(t, "\"Second Third\"", true, 4, 4)
		assert.Equal(t, [][]int{{15, 27}}, eng.IsLineMatch("This is a firstSecond Third line for verification."))

		eng = createEngine(t, "first \"second phrase\" | third \"fourth phrase\"", true, 4, 8)
		assert.Equal(t, [][]int{{19, 42}}, eng.IsLineMatch("This line contains first and second phrase words, but not the other clause."))

		eng = createEngine(t, "first \"second phrase\" | third \"fourth phrase\"", true, 4, 8)
		assert.Equal(t, [][]int{{49, 72}}, eng.IsLineMatch("This line doesn't have the first clause, but has third and fourth phrase words."))

		eng = createEngine(t, "\"test line\"", true, 4, 4)
		assert.Equal(t, [][]int{}, eng.IsLineMatch("This is a testing line for verification."))

		eng = createEngine(t, "\"line test\"", true, 4, 4)
		assert.Equal(t, [][]int{}, eng.IsLineMatch("This is a test line for verification."))

		eng = createEngine(t, "\"is verification\"", true, 4, 4)
		assert.Equal(t, [][]int{}, eng.IsLineMatch("This is a test line for verification."))

		eng = createEngine(t, "\"test\"", false, 4, 4)
		assert.Equal(t, [][]int{{10, 14}}, eng.IsLineMatch("This is a Test line for verification."))

		eng = createEngine(t, "\"test\"", true, 4, 4)
		assert.Equal(t, [][]int{}, eng.IsLineMatch("This is a Test line for verification."))
	})

	t.Run("Mixed regular and exact phrase matching", func(t *testing.T) {
		eng := createEngine(t, "this \"test line\"", false, 4, 8)
		assert.Equal(t, [][]int{{0, 19}}, eng.IsLineMatch("This is a test line for verification."))

		eng = createEngine(t, "this \"line test\"", false, 4, 4)
		assert.Equal(t, [][]int{}, eng.IsLineMatch("This is a test line for verification."))
	})

	t.Run("Special characters in quoted phrase", func(t *testing.T) {
		eng := createEngine(t, "\"test.line\"", true, 4, 4)
		assert.Equal(t, [][]int{{10, 19}}, eng.IsLineMatch("This is a test.line for verification."))

		eng = createEngine(t, "\"Hello, world!\"", true, 4, 4)
		assert.Equal(t, [][]int{{20, 33}}, eng.IsLineMatch("The program outputs Hello, world! when run."))

		eng = createEngine(t, "\"version 1.2.3-beta\"", true, 4, 4)
		assert.Equal(t, [][]int{{22, 40}}, eng.IsLineMatch("We're currently using version 1.2.3-beta of the software."))

		eng = createEngine(t, "\"version 1.2.3-beta\"", true, 4, 4)
		assert.Equal(t, [][]int{{22, 40}}, eng.IsLineMatch("We're currently using version 1.2.3-beta+build.123 of the software."))
	})

	t.Run("Test with short keywords", func(t *testing.T) {
		eng := createEngine(t, "For this, u", true, 4, 4)
		assert.Equal(t, [][]int{{0, 11}}, eng.IsLineMatch("For this, use Visual studio."))
	})

	t.Run("Whole word matching behavior comparison", func(t *testing.T) {
		testLine := "This is a test line with testing and test_func keywords"

		eng1 := createEngine(t, "test", true, 4, 4)
		matches1 := eng1.IsLineMatch(testLine)
		assert.Equal(t, 3, len(matches1))
		assert.Equal(t, [][]int{{10, 14}, {25, 29}, {37, 41}}, matches1)

		eng2 := createEngine(t, "line", true, 4, 4)
		matches2 := eng2.IsLineMatch(testLine)
		assert.Equal(t, 1, len(matches2))
		assert.Equal(t, [][]int{{15, 19}}, matches2)

		testLine2 := "function func_call func() my_func"
		eng3 := createEngine(t, "func", true, 4, 4)
		matches3 := eng3.IsLineMatch(testLine2)
		assert.Equal(t, 4, len(matches3))
		assert.Equal(t, [][]int{{0, 4}, {9, 13}, {19, 23}, {29, 33}}, matches3)

		testLine3 := "class MyClass method classmethod my_method"
		eng4 := createEngine(t, "class method", true, 4, 8)
		matches4 := eng4.IsLineMatch(testLine3)
		assert.Greater(t, len(matches4), 0)

		falsePositiveTests := []struct {
			query string
			line  string
		}{
			{"in", "int main() { return 0; }"},
			{"or", "for (int i = 0; i < 10; i++) {}"},
			{"if", "diff --git a/file.txt b/file.txt"},
			{"id", "void method() { ... }"},
			{"new", "renew subscription"},
		}

		for _, tt := range falsePositiveTests {
			substrEng := createEngine(t, tt.query, true, 4, 4)
			assert.Greater(t, len(substrEng.IsLineMatch(tt.line)), 0)

			wwEng := createWholeWordEngine(t, tt.query, true, 4, 4)
			assert.Equal(t, 0, len(wwEng.IsLineMatch(tt.line)))
		}

		validWholeWordTests := []struct {
			query string
			line  string
		}{
			{"test", "run test suite"},
			{"func", "func main() {"},
			{"class", "public class MyClass {"},
			{"var", "var x = 10;"},
		}

		for _, tt := range validWholeWordTests {
			substrEng := createEngine(t, tt.query, true, 4, 4)
			assert.Greater(t, len(substrEng.IsLineMatch(tt.line)), 0)

			wwEng := createWholeWordEngine(t, tt.query, true, 4, 4)
			assert.Greater(t, len(wwEng.IsLineMatch(tt.line)), 0)
		}
	})
}
