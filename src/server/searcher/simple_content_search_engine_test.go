package searcher

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func CreateSimpleEngine(t *testing.T, query string, caseSensitive bool, maxWildLen, maxKwDist int) *SimpleContentSearchEngine {
	eng := NewSimpleContentSearchEngine(nil, maxWildLen, maxKwDist)
	assert.NoError(t, eng.Compile(query, caseSensitive))
	return eng
}

func TestParseQuerySimple(t *testing.T) {
	type OneCase struct {
		query   string
		want    *SimpleContentSearchEngine
		wantErr bool
	}

	runCase := func(t *testing.T, tt OneCase) {
		got := NewSimpleContentSearchEngine(nil, 4, 4)
		err := got.Compile(tt.query, true)

		if tt.wantErr {
			assert.Error(t, err, "Compile() expected an error, but got none for query: %s", tt.query)
			return
		}
		assert.NoError(t, err, "Compile() returned an unexpected error: %v for query: %s", err, tt.query)
		if err != nil { // If assert.NoError marked a failure, err might be non-nil.
			return
		}

		// Use require for critical assertions that should stop the test if they fail.
		// Use assert for non-critical assertions where the test can continue.
		if !assert.Equal(t, len(tt.want.OrClauses), len(got.OrClauses), "Number of OR clauses mismatch for query: %s", tt.query) {
			return // Stop further checks if the number of OR clauses is different.
		}

		for i, orClause := range got.OrClauses {
			wantOrClause := tt.want.OrClauses[i]
			if !assert.Equal(t, len(wantOrClause.AndTerms), len(orClause.AndTerms), "OR clause %d: number of AND patterns mismatch for query: %s", i, tt.query) {
				continue // Continue to the next OR clause if AND terms count differs for this one.
			}

			for j, pattern := range orClause.AndTerms {
				wantPattern := wantOrClause.AndTerms[j]
				assert.Equal(t, wantPattern.Pattern, pattern.Pattern, "Pattern %d in OR clause %d: pattern mismatch for query: %s", j, i, tt.query)
				assert.Equal(t, wantPattern.RegPattern, pattern.RegPattern, "RegPattern %d in OR clause %d: pattern mismatch for query: %s", j, i, tt.query)
				assert.Equal(t, wantPattern.Keywords, pattern.Keywords, "Keywords %d in OR clause %d: pattern mismatch for query: %s", j, i, tt.query)
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
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "test", RegPattern: "test", Keywords: []string{"test"}},
						},
					},
				},
			},
		})
	})

	t.Run("multiple AND patterns", func(t *testing.T) {
		runCase(t, OneCase{
			query: "test1 test2",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "test1", RegPattern: "test1", Keywords: []string{"test1"}},
							{Pattern: "test2", RegPattern: "test2", Keywords: []string{"test2"}},
						},
					},
				},
			},
		})
	})

	t.Run("OR clauses", func(t *testing.T) {
		runCase(t, OneCase{
			query: "test1 test2 | test3",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "test1", RegPattern: "test1", Keywords: []string{"test1"}},
							{Pattern: "test2", RegPattern: "test2", Keywords: []string{"test2"}},
						},
					},
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "test3", RegPattern: "test3", Keywords: []string{"test3"}},
						},
					},
				},
			},
		})
	})

	t.Run("pattern with prefix", func(t *testing.T) {
		runCase(t, OneCase{
			query: "prefix:value",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "prefix:value", RegPattern: "prefix\\:value", Keywords: []string{"prefix", "value"}},
						},
					},
				},
			},
		})
	})

	t.Run("complex query with prefixes and OR", func(t *testing.T) {
		runCase(t, OneCase{
			query: "field1:value1 field2:value2 | field3:value3",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "field1:value1", RegPattern: "field1\\:value1", Keywords: []string{"field1", "value1"}},
							{Pattern: "field2:value2", RegPattern: "field2\\:value2", Keywords: []string{"field2", "value2"}},
						},
					},
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "field3:value3", RegPattern: "field3\\:value3", Keywords: []string{"field3", "value3"}},
						},
					},
				},
			},
		})
	})

	t.Run("quoted query for exact matching", func(t *testing.T) {
		runCase(t, OneCase{
			query: "\"test1 test2\"",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "\"test1 test2\"", RegPattern: "test1 test2", Keywords: []string{"test1", "test2"}}, // We expect all valid prefixes to be used
						},
					},
				},
			},
		})
	})

	t.Run("mixed regular and quoted terms", func(t *testing.T) {
		runCase(t, OneCase{
			query: "regular \"quoted phrase\" another",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "regular", RegPattern: "regular", Keywords: []string{"regular"}},
							{Pattern: "\"quoted phrase\"", RegPattern: "quoted phrase", Keywords: []string{"quoted", "phrase"}},
							{Pattern: "another", RegPattern: "another", Keywords: []string{"another"}},
						},
					},
				},
			},
		})
	})

	t.Run("quoted term with OR clause", func(t *testing.T) {
		runCase(t, OneCase{
			query: "\"exact phrase\" | regular term",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "\"exact phrase\"", RegPattern: "exact phrase", Keywords: []string{"exact", "phrase"}},
						},
					},
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "regular", RegPattern: "regular", Keywords: []string{"regular"}},
							{Pattern: "term", RegPattern: "term", Keywords: []string{"term"}},
						},
					},
				},
			},
		})
	})

	t.Run("quoted term with special characters", func(t *testing.T) {
		runCase(t, OneCase{
			query: "\"test.with[special]chars\"",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{
								Pattern:    "\"test.with[special]chars\"",
								RegPattern: "test\\.with\\[special\\]chars",
								Keywords:   []string{"test", "with", "special", "chars"},
							},
						},
					},
				},
			},
		})
	})

	t.Run("single word quoted term", func(t *testing.T) {
		runCase(t, OneCase{
			query: "\"singleword\"",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "\"singleword\"", RegPattern: "singleword", Keywords: []string{"singleword"}},
						},
					},
				},
			},
		})
	})

	t.Run("quoted phrase with multiple words and punctuation", func(t *testing.T) {
		runCase(t, OneCase{
			query: "\"hello, world! how are you?\"",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{
								Pattern:    "\"hello, world! how are you?\"",
								RegPattern: "hello, world! how are you\\?",
								Keywords:   []string{"hello", "world", "how", "are", "you"}},
						},
					},
				},
			},
		})
	})

	t.Run("quoted phrase with numbers and symbols", func(t *testing.T) {
		runCase(t, OneCase{
			query: "\"version 1.2.3-beta+build.456\"",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{
								Pattern:    "\"version 1.2.3-beta+build.456\"",
								RegPattern: "version 1\\.2\\.3-beta\\+build\\.456",
								Keywords:   []string{"version", "1.2.3", "beta", "build", "456"}},
						},
					},
				},
			},
		})
	})

	t.Run("mixed quoted phrases with AND and OR operators", func(t *testing.T) {
		runCase(t, OneCase{
			query: "\"first phrase\" second | third \"fourth phrase\"",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "\"first phrase\"", RegPattern: "first phrase", Keywords: []string{"first", "phrase"}},
							{Pattern: "second", RegPattern: "second", Keywords: []string{"second"}},
						},
					},
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "third", RegPattern: "third", Keywords: []string{"third"}},
							{Pattern: "\"fourth phrase\"", RegPattern: "fourth phrase", Keywords: []string{"fourth", "phrase"}},
						},
					},
				},
			},
		})
	})

	t.Run("quoted phrase with escaped quotes", func(t *testing.T) {
		runCase(t, OneCase{
			query: "\"code with \\\"quoted\\\" text\"",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{
								Pattern:    "\"code with \\\"quoted\\\" text\"",
								RegPattern: "code with \"quoted\" text",
								Keywords:   []string{"code", "with", "quoted", "text"}},
						},
					},
				},
			},
		})
	})

	t.Run("with wild match", func(t *testing.T) {
		runCase(t, OneCase{
			query: "test*abc?defg-hij.efg",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{
								Pattern:    "test*abc?defg-hij.efg",
								RegPattern: "test.{0,4}abc\\?defg-hij\\.efg",
								Keywords:   []string{"test", "defg", "hij", "efg"}},
						},
					},
				},
			},
		})

		runCase(t, OneCase{
			query: "abc?--defg*--hij",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "abc?--defg*--hij", RegPattern: "abc\\?--defg.{0,4}--hij", Keywords: []string{"abc", "defg", "hij"}},
						},
					},
				},
			},
		})

		runCase(t, OneCase{
			query: "abc?..defg",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "abc?..defg", RegPattern: "abc\\?\\.\\.defg", Keywords: []string{"abc", "defg"}},
						},
					},
				},
			},
		})
	})

	t.Run("term with dash", func(t *testing.T) {
		runCase(t, OneCase{
			query: "exactly-with-dash another",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "exactly-with-dash", RegPattern: "exactly-with-dash", Keywords: []string{"exactly", "with", "dash"}},
							{Pattern: "another", RegPattern: "another", Keywords: []string{"another"}},
						},
					},
				},
			},
		})
	})

	t.Run("leading special chars", func(t *testing.T) {
		runCase(t, OneCase{
			query: "->with-pointer",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "->with-pointer", RegPattern: "->with-pointer", Keywords: []string{"with", "pointer"}},
						},
					},
				},
			},
		})

		runCase(t, OneCase{
			query: "$with-pointer",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "$with-pointer", RegPattern: "\\$with-pointer", Keywords: []string{"with", "pointer"}},
						},
					},
				},
			},
		})
	})

	t.Run("trailing special chars", func(t *testing.T) {
		runCase(t, OneCase{
			query: "For this, u",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "For", RegPattern: "For", Keywords: []string{"For"}},
							{Pattern: "this,", RegPattern: "this,", Keywords: []string{"this"}},
							{Pattern: "u", RegPattern: "u", Keywords: []string{}},
						},
					},
				},
			},
		})
	})
}

// Add a new test function to verify TokenizeWithQuotes works correctly
func TestTokenizeWithQuotes(t *testing.T) {
	type OneCase struct {
		input string
		want  []string
	}

	runCase := func(t *testing.T, tt OneCase) {
		got := TokenizeWithQuotes(tt.input)

		if !assert.Equal(t, len(tt.want), len(got), "TokenizeWithQuotes() token count mismatch for input: %s", tt.input) {
			return
		}

		for i, token := range got {
			assert.Equal(t, tt.want[i], token, "TokenizeWithQuotes() token %d mismatch for input: %s", i, tt.input)
		}
	}

	t.Run("simple space-separated terms", func(t *testing.T) {
		runCase(t, OneCase{
			input: "term1 term2 term3",
			want:  []string{"term1", "term2", "term3"},
		})
	})
	t.Run("pattern with prefix", func(t *testing.T) {
		runCase(t, OneCase{
			input: "prefix:value",
			want:  []string{"prefix:value"},
		})
	})
	t.Run("quoted query for exact matching", func(t *testing.T) {
		runCase(t, OneCase{
			input: "\"test1 test2\"",
			want:  []string{"\"test1 test2\""},
		})
	})
	t.Run("mixed regular and quoted terms", func(t *testing.T) {
		runCase(t, OneCase{
			input: "term1 \"quoted phrase\" term2",
			want:  []string{"term1", "\"quoted phrase\"", "term2"},
		})
	})
	t.Run("quoted term with OR clause", func(t *testing.T) {
		runCase(t, OneCase{
			input: "\"exact phrase\" | regular term",
			want:  []string{"\"exact phrase\"", "|", "regular", "term"},
		})
	})
	t.Run("quoted term with special characters", func(t *testing.T) {
		runCase(t, OneCase{
			input: "\"test.with[special]chars\"",
			want:  []string{"\"test.with[special]chars\""},
		})
	})
	t.Run("single word quoted term", func(t *testing.T) {
		runCase(t, OneCase{
			input: "\"singleword\"",
			want:  []string{"\"singleword\""},
		})
	})
	t.Run("multiple quoted phrases", func(t *testing.T) {
		runCase(t, OneCase{
			input: "\"first phrase\" regular \"second phrase\"",
			want:  []string{"\"first phrase\"", "regular", "\"second phrase\""},
		})
	})
	t.Run("quoted phrase with internal quotes", func(t *testing.T) {
		runCase(t, OneCase{
			input: "before \"phrase with \\\"internal quotes\\\"\" after",
			want:  []string{"before", "\"phrase with \\\"internal quotes\\\"\"", "after"},
		})
	})
	t.Run("unclosed quote", func(t *testing.T) {
		runCase(t, OneCase{
			input: "term1 \"unclosed quote",
			want:  []string{"term1", "\"unclosed quote"},
		})
	})
}

// Test helper functions for quoted phrases
func TestQuoteHelpers(t *testing.T) {
	// Test cases for IsQuotedPhrase
	t.Run("IsQuotedPhrase_empty string", func(t *testing.T) {
		input := ""
		want := false
		got := IsQuotedPhrase(input)
		assert.Equal(t, want, got, "IsQuotedPhrase(%q)", input)
	})
	t.Run("IsQuotedPhrase_\"quoted\"", func(t *testing.T) {
		input := "\"quoted\""
		want := true
		got := IsQuotedPhrase(input)
		assert.Equal(t, want, got, "IsQuotedPhrase(%q)", input)
	})
	t.Run("IsQuotedPhrase_notquoted", func(t *testing.T) {
		input := "notquoted"
		want := false
		got := IsQuotedPhrase(input)
		assert.Equal(t, want, got, "IsQuotedPhrase(%q)", input)
	})
	t.Run("IsQuotedPhrase_\"multiple words\"", func(t *testing.T) {
		input := "\"multiple words\""
		want := true
		got := IsQuotedPhrase(input)
		assert.Equal(t, want, got, "IsQuotedPhrase(%q)", input)
	})
	t.Run("IsQuotedPhrase_\"", func(t *testing.T) {
		input := "\""
		want := false
		got := IsQuotedPhrase(input)
		assert.Equal(t, want, got, "IsQuotedPhrase(%q)", input)
	})
	t.Run("IsQuotedPhrase_\"\"", func(t *testing.T) {
		input := "\"\""
		want := true
		got := IsQuotedPhrase(input)
		assert.Equal(t, want, got, "IsQuotedPhrase(%q)", input)
	})
	t.Run("IsQuotedPhrase_\"partial", func(t *testing.T) {
		input := "\"partial"
		want := false
		got := IsQuotedPhrase(input)
		assert.Equal(t, want, got, "IsQuotedPhrase(%q)", input)
	})
	t.Run("IsQuotedPhrase_partial\"", func(t *testing.T) {
		input := "partial\""
		want := false
		got := IsQuotedPhrase(input)
		assert.Equal(t, want, got, "IsQuotedPhrase(%q)", input)
	})

	// Test UnwrapQuotes
	t.Run("UnwrapQuotes_\"quoted\"", func(t *testing.T) {
		input := "\"quoted\""
		want := "quoted"
		got := UnwrapQuotes(input)
		assert.Equal(t, want, got, "UnwrapQuotes(%q)", input)
	})
	t.Run("UnwrapQuotes_\"multiple words\"", func(t *testing.T) {
		input := "\"multiple words\""
		want := "multiple words"
		got := UnwrapQuotes(input)
		assert.Equal(t, want, got, "UnwrapQuotes(%q)", input)
	})
	t.Run("UnwrapQuotes_\"\"", func(t *testing.T) {
		input := "\"\""
		want := ""
		got := UnwrapQuotes(input)
		assert.Equal(t, want, got, "UnwrapQuotes(%q)", input)
	})
}

func TestContentLineMatch(t *testing.T) {
	t.Run("Test with special characters", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "special characters", true, 4, 4)

		actual := eng.IsLineMatch("with special characters: !@#$%^&*()")
		assert.Equal(t, [][]int{{5, 23}}, actual)
	})

	t.Run("Test single keyword", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "ContentSearch", true, 4, 4)

		var actual [][]int
		actual = eng.IsLineMatch("ContentSearchEngineAndClause")
		assert.Equal(t, [][]int{{0, 13}}, actual)

		actual = eng.IsLineMatch("here@ContentSearchEngineAndClause")
		assert.Equal(t, [][]int{{5, 18}}, actual)

		actual = eng.IsLineMatch(
			"func (q *SimpleContentSearchEngineAndClause) CollectDocuments(workspaceId string)")
		assert.Equal(t, [][]int{{15, 28}}, actual)
	})

	t.Run("Test with multiple keywords", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "ContentSearch Engine", true, 4, 4)

		var actual [][]int
		actual = eng.IsLineMatch("ContentSearchEngineAndClause")
		assert.Equal(t, [][]int{{0, 19}}, actual)

		actual = eng.IsLineMatch("ContentSearch EngineAndClause")
		assert.Equal(t, [][]int{{0, 20}}, actual)

		actual = eng.IsLineMatch("ContentSearch@EngineAndClause")
		assert.Equal(t, [][]int{{0, 20}}, actual)

		actual = eng.IsLineMatch("here@ContentSearchEngineAndClause")
		assert.Equal(t, [][]int{{5, 24}}, actual)

		actual = eng.IsLineMatch(
			"func (q *SimpleContentSearchEngineAndClause) CollectDocuments")
		assert.Equal(t, [][]int{{15, 34}}, actual)
	})

	t.Run("Test with multiple matches", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "ContentSearch Engine", true, 4, 4)

		var actual [][]int
		actual = eng.IsLineMatch("ContentSearchEngineAndClause ContentSearchEngineAndClause")
		assert.Equal(t, [][]int{{0, 19}, {29, 48}}, actual)

		actual = eng.IsLineMatch("ContentSearch EngineAndClause ContentSearch EngineAndClause")
		assert.Equal(t, [][]int{{0, 20}, {30, 50}}, actual)
	})

	t.Run("Test with OR", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "Content | Clause", true, 4, 4)

		var actual [][]int
		actual = eng.IsLineMatch("ContentSearchEngineAnd  Clause")
		assert.Equal(t, [][]int{{0, 7}}, actual)

		actual = eng.IsLineMatch("SearchEngineAnd Clause")
		assert.Equal(t, [][]int{{16, 22}}, actual)
	})

	t.Run("Test with dot", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "Content.Search", true, 4, 4)

		var actual [][]int
		actual = eng.IsLineMatch("Content.SearchEngineAndClause")
		assert.Equal(t, [][]int{{0, 14}}, actual)

		actual = eng.IsLineMatch("Content-SearchEngineAndClause")
		assert.Equal(t, [][]int{}, actual)
	})

	t.Run("Test with hyphen", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "Content-Search", true, 4, 4)

		var actual [][]int
		actual = eng.IsLineMatch("Content-SearchEngineAndClause")
		assert.Equal(t, [][]int{{0, 14}}, actual)

		actual = eng.IsLineMatch("Content.SearchEngineAndClause")
		assert.Equal(t, [][]int{}, actual)
	})

	t.Run("Test with underscore", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "Content_Search", true, 4, 4)

		var actual [][]int
		actual = eng.IsLineMatch("Content_SearchEngineAndClause")
		assert.Equal(t, [][]int{{0, 14}}, actual)

		actual = eng.IsLineMatch("Content-SearchEngineAndClause")
		assert.Equal(t, [][]int{}, actual)
	})

	t.Run("Test with colon", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "Content::Search", true, 4, 4)

		actual := eng.IsLineMatch("func Content::SearchEngineAndClause")
		assert.Equal(t, [][]int{{5, 20}}, actual)
	})

	t.Run("Test case insensitivity", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "contentsearch", false, 4, 4)

		var actual [][]int
		actual = eng.IsLineMatch("ContentSearchEngineAndClause")
		assert.Equal(t, [][]int{{0, 13}}, actual)

		actual = eng.IsLineMatch("ContentsearchEngineAndClause")
		assert.Equal(t, [][]int{{0, 13}}, actual)
	})

	t.Run("Test keyword distance", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "Content Search", true, 4, 4)

		var actual [][]int
		actual = eng.IsLineMatch("ContentWithSearch")
		assert.Equal(t, [][]int{{0, 17}}, actual)

		// Distance is farther then 4 chars, so this should not match
		actual = eng.IsLineMatch("ContentWithoutSearch")
		assert.Equal(t, [][]int{}, actual)
	})

	t.Run("Test wild match length", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "Content*Search", true, 4, 4)

		var actual [][]int
		actual = eng.IsLineMatch("ContentWithSearch")
		assert.Equal(t, [][]int{{0, 17}}, actual)

		// len(wildcard) is greater than 4 chars, so this should not match
		actual = eng.IsLineMatch("ContentWithoutSearch")
		assert.Equal(t, [][]int{}, actual)

	})

	t.Run("Test empty line", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "Content", true, 4, 4)
		actual := eng.IsLineMatch("")
		assert.Equal(t, [][]int{}, actual)
	})

	t.Run("Test numbers and alphanumeric", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "test123 abc456", true, 4, 4)

		var actual [][]int
		actual = eng.IsLineMatch("test123_abc456_function")
		assert.Equal(t, [][]int{{0, 14}}, actual)

		actual = eng.IsLineMatch("prefix_test123_abc456")
		assert.Equal(t, [][]int{{7, 21}}, actual)
	})

	t.Run("Test mixed case with OR", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "content | SEARCH", false, 4, 4)

		var actual [][]int
		actual = eng.IsLineMatch("ContentSearchEngine")
		assert.Equal(t, [][]int{{0, 7}}, actual)

		actual = eng.IsLineMatch("CONTENT_SEARCH")
		assert.Equal(t, [][]int{{0, 7}}, actual)
	})

	t.Run("Test keyword distance edge cases", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "test func", true, 4, 4)

		var actual [][]int
		// Distance exactly 4 chars - should match
		actual = eng.IsLineMatch("test____func")
		assert.Equal(t, [][]int{{0, 12}}, actual)

		// Distance 5 chars - should not match
		actual = eng.IsLineMatch("test_____func")
		assert.Equal(t, [][]int{}, actual)
	})

	t.Run("Test mixed separators", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "test.func-call_method", true, 4, 4)

		var actual [][]int
		actual = eng.IsLineMatch("test.func-call_method()")
		assert.Equal(t, [][]int{{0, 21}}, actual)

		// Different separator order should not match
		actual = eng.IsLineMatch("test-func.call_method()")
		assert.Equal(t, [][]int{}, actual)
	})

	t.Run("Regular pattern matching", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "test", true, 4, 4)
		actual := eng.IsLineMatch("This is a test line.")
		assert.Equal(t, [][]int{{10, 14}}, actual)

		eng = CreateSimpleEngine(t, "missing", true, 4, 4)
		actual = eng.IsLineMatch("This is a test line.")
		assert.Equal(t, [][]int{}, actual)

		eng = CreateSimpleEngine(t, "test", true, 4, 4)
		actual = eng.IsLineMatch("This test is a test line for testing.")
		assert.Equal(t, [][]int{{5, 9}, {15, 19}, {29, 33}}, actual)

		eng = CreateSimpleEngine(t, "test", true, 4, 4)
		actual = eng.IsLineMatch("This is a testing line.")
		assert.Equal(t, [][]int{{10, 14}}, actual)

		eng = CreateSimpleEngine(t, "test", true, 4, 4)
		actual = eng.IsLineMatch("This is a attest line.")
		assert.Equal(t, [][]int{{12, 16}}, actual)
	})

	t.Run("Leading special characters", func(t *testing.T) {
		eng := CreateSimpleEngine(t, ".test", true, 4, 4)
		actual := eng.IsLineMatch(".test line")
		assert.Equal(t, [][]int{{0, 5}}, actual)
	})

	t.Run("Exact phrase matching", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "\"test line\"", true, 4, 4)
		actual := eng.IsLineMatch("This is a test line for verification.")
		assert.Equal(t, [][]int{{10, 19}}, actual)

		eng = CreateSimpleEngine(t, "\"func\"", true, 4, 4)
		actual = eng.IsLineMatch("The function is called regularly.")
		assert.Equal(t, [][]int{{4, 8}}, actual)

		eng = CreateSimpleEngine(t, "\"test line\"", true, 4, 4)
		actual = eng.IsLineMatch("This is a test linear for verification.")
		assert.Equal(t, [][]int{{10, 19}}, actual)

		eng = CreateSimpleEngine(t, "\"Second Third\"", true, 4, 4)
		actual = eng.IsLineMatch("This is a firstSecond Third line for verification.")
		assert.Equal(t, [][]int{{15, 27}}, actual)

		eng = CreateSimpleEngine(t, "first \"second phrase\" | third \"fourth phrase\"", true, 4, 8)
		actual = eng.IsLineMatch("This line contains first and second phrase words, but not the other clause.")
		assert.Equal(t, [][]int{{19, 42}}, actual)

		eng = CreateSimpleEngine(t, "first \"second phrase\" | third \"fourth phrase\"", true, 4, 8)
		actual = eng.IsLineMatch("This line doesn't have the first clause, but has third and fourth phrase words.")
		assert.Equal(t, [][]int{{49, 72}}, actual)

		eng = CreateSimpleEngine(t, "\"test line\"", true, 4, 4)
		actual = eng.IsLineMatch("This is a testing line for verification.")
		assert.Equal(t, [][]int{}, actual)

		eng = CreateSimpleEngine(t, "\"line test\"", true, 4, 4)
		actual = eng.IsLineMatch("This is a test line for verification.")
		assert.Equal(t, [][]int{}, actual)

		eng = CreateSimpleEngine(t, "\"is verification\"", true, 4, 4)
		actual = eng.IsLineMatch("This is a test line for verification.")
		assert.Equal(t, [][]int{}, actual)

		eng = CreateSimpleEngine(t, "\"test\"", false, 4, 4)
		actual = eng.IsLineMatch("This is a Test line for verification.")
		assert.Equal(t, [][]int{{10, 14}}, actual)

		eng = CreateSimpleEngine(t, "\"test\"", true, 4, 4)
		actual = eng.IsLineMatch("This is a Test line for verification.")
		assert.Equal(t, [][]int{}, actual)
	})

	t.Run("Mixed regular and exact phrase matching", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "this \"test line\"", false, 4, 8)
		actual := eng.IsLineMatch("This is a test line for verification.")
		assert.Equal(t, [][]int{{0, 19}}, actual)

		eng = CreateSimpleEngine(t, "this \"line test\"", false, 4, 4)
		actual = eng.IsLineMatch("This is a test line for verification.")
		assert.Equal(t, [][]int{}, actual)
	})

	t.Run("Special characters in quoted phrase", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "\"test.line\"", true, 4, 4)
		actual := eng.IsLineMatch("This is a test.line for verification.")
		assert.Equal(t, [][]int{{10, 19}}, actual)

		eng = CreateSimpleEngine(t, "\"Hello, world!\"", true, 4, 4)
		actual = eng.IsLineMatch("The program outputs Hello, world! when run.")
		assert.Equal(t, [][]int{{20, 33}}, actual)

		eng = CreateSimpleEngine(t, "\"version 1.2.3-beta\"", true, 4, 4)
		actual = eng.IsLineMatch("We're currently using version 1.2.3-beta of the software.")
		assert.Equal(t, [][]int{{22, 40}}, actual)

		eng = CreateSimpleEngine(t, "\"version 1.2.3-beta\"", true, 4, 4)
		actual = eng.IsLineMatch("We're currently using version 1.2.3-beta+build.123 of the software.")
		assert.Equal(t, [][]int{{22, 40}}, actual)
	})

	t.Run("Test with short keywords", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "For this, u", true, 4, 4)
		actual := eng.IsLineMatch("For this, use Visual studio.")
		assert.Equal(t, [][]int{{0, 11}}, actual)
	})
}
