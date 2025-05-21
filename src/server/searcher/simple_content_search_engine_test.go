package searcher

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseQuerySimple(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		want    *SimpleContentSearchEngine
		wantErr bool
	}{
		{name: "empty query", query: "", wantErr: true},
		{name: "whitespace only", query: "   ", wantErr: true},
		{
			name:  "single pattern",
			query: "test",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "test", Prefixes: []string{"test"}},
						},
					},
				},
			},
		},
		{
			name:  "multiple AND patterns",
			query: "test1 test2",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "test1", Prefixes: []string{"test1"}},
							{Pattern: "test2", Prefixes: []string{"test2"}},
						},
					},
				},
			},
		},
		{
			name:  "OR clauses",
			query: "test1 test2 | test3",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "test1", Prefixes: []string{"test1"}},
							{Pattern: "test2", Prefixes: []string{"test2"}},
						},
					},
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "test3", Prefixes: []string{"test3"}},
						},
					},
				},
			},
		},
		{
			name:  "pattern with prefix",
			query: "prefix:value",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "prefix:value", Prefixes: []string{"prefix"}},
						},
					},
				},
			},
		},
		{
			name:  "complex query with prefixes and OR",
			query: "field1:value1 field2:value2 | field3:value3",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "field1:value1", Prefixes: []string{"field1"}},
							{Pattern: "field2:value2", Prefixes: []string{"field2"}},
						},
					},
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "field3:value3", Prefixes: []string{"field3"}},
						},
					},
				},
			},
		},
		{
			name:  "quoted query for exact matching",
			query: "\"test1 test2\"",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "\"test1 test2\"", Prefixes: []string{"test1", "test2"}}, // We expect all valid prefixes to be used
						},
					},
				},
			},
		},
		{
			name:  "mixed regular and quoted terms",
			query: "regular \"quoted phrase\" another",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "regular", Prefixes: []string{"regular"}},
							{Pattern: "\"quoted phrase\"", Prefixes: []string{"quoted", "phrase"}},
							{Pattern: "another", Prefixes: []string{"another"}},
						},
					},
				},
			},
		},
		{
			name:  "quoted term with OR clause",
			query: "\"exact phrase\" | regular term",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "\"exact phrase\"", Prefixes: []string{"exact", "phrase"}},
						},
					},
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "regular", Prefixes: []string{"regular"}},
							{Pattern: "term", Prefixes: []string{"term"}},
						},
					},
				},
			},
		},
		{
			name:  "quoted term with special characters",
			query: "\"test.with[special]chars\"",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "\"test.with[special]chars\"", Prefixes: []string{"test"}}, // First word before special chars
						},
					},
				},
			},
		},
		{
			name:  "single word quoted term",
			query: "\"singleword\"",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "\"singleword\"", Prefixes: []string{"singleword"}},
						},
					},
				},
			},
		},
		{
			name:  "quoted phrase with multiple words and punctuation",
			query: "\"hello, world! how are you?\"",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "\"hello, world! how are you?\"", Prefixes: []string{"hello", "world", "how", "are", "you"}},
						},
					},
				},
			},
		},
		{
			name:  "quoted phrase with numbers and symbols",
			query: "\"version 1.2.3-beta+build.456\"",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "\"version 1.2.3-beta+build.456\"", Prefixes: []string{"version"}},
						},
					},
				},
			},
		},
		{
			name:  "mixed quoted phrases with AND and OR operators",
			query: "\"first phrase\" second | third \"fourth phrase\"",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "\"first phrase\"", Prefixes: []string{"first", "phrase"}},
							{Pattern: "second", Prefixes: []string{"second"}},
						},
					},
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "third", Prefixes: []string{"third"}},
							{Pattern: "\"fourth phrase\"", Prefixes: []string{"fourth", "phrase"}},
						},
					},
				},
			},
		},
		{
			name:  "quoted phrase with escaped quotes",
			query: "\"code with \\\"quoted\\\" text\"",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "\"code with \\\"quoted\\\" text\"", Prefixes: []string{"code", "with", "text"}},
						},
					},
				},
			},
		},
		{
			name:  "term with dash",
			query: "exactly-with-dash another",
			want: &SimpleContentSearchEngine{
				OrClauses: []*SimpleContentSearchEngineAndClause{
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "exactly-with-dash", Prefixes: []string{"exactly-with-dash"}},
							{Pattern: "another", Prefixes: []string{"another"}},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := &SimpleContentSearchEngine{}
			err := got.Compile(tt.query, true)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseQuerySimple() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}

			if len(got.OrClauses) != len(tt.want.OrClauses) {
				t.Errorf("ParseQuerySimple() got %d OR clauses, want %d", len(got.OrClauses), len(tt.want.OrClauses))
				return
			}

			for i, orClause := range got.OrClauses {
				wantOrClause := tt.want.OrClauses[i]
				if len(orClause.AndTerms) != len(wantOrClause.AndTerms) {
					t.Errorf("OR clause %d: got %d AND patterns, want %d", i, len(orClause.AndTerms), len(wantOrClause.AndTerms))
					continue
				}

				for j, pattern := range orClause.AndTerms {
					wantPattern := wantOrClause.AndTerms[j]
					if pattern.Pattern != wantPattern.Pattern {
						t.Errorf("pattern %d in OR clause %d: got pattern %q, want %q", j, i, pattern.Pattern, wantPattern.Pattern)
					}
					if len(pattern.Prefixes) != len(wantPattern.Prefixes) {
						t.Errorf("pattern %d in OR clause %d: got %d prefixes, want %d", j, i, len(pattern.Prefixes), len(wantPattern.Prefixes))
						continue
					}
					for k, prefix := range pattern.Prefixes {
						if prefix != wantPattern.Prefixes[k] {
							t.Errorf("pattern %d in OR clause %d: prefix %d got %q, want %q",
								j, i, k, prefix, wantPattern.Prefixes[k])
						}
					}
				}
			}
		})
	}
}

// Add a new test function to verify TokenizeWithQuotes works correctly
func TestTokenizeWithQuotes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "simple space-separated terms",
			input: "term1 term2 term3",
			want:  []string{"term1", "term2", "term3"},
		},
		{
			name:  "pattern with prefix",
			input: "prefix:value",
			want:  []string{"prefix:value"},
		},
		{
			name:  "quoted query for exact matching",
			input: "\"test1 test2\"",
			want:  []string{"\"test1 test2\""},
		},
		{
			name:  "mixed regular and quoted terms",
			input: "term1 \"quoted phrase\" term2",
			want:  []string{"term1", "\"quoted phrase\"", "term2"},
		},
		{
			name:  "quoted term with OR clause",
			input: "\"exact phrase\" | regular term",
			want:  []string{"\"exact phrase\"", "|", "regular", "term"},
		},
		{
			name:  "quoted term with special characters",
			input: "\"test.with[special]chars\"",
			want:  []string{"\"test.with[special]chars\""},
		},
		{
			name:  "single word quoted term",
			input: "\"singleword\"",
			want:  []string{"\"singleword\""},
		},
		{
			name:  "multiple quoted phrases",
			input: "\"first phrase\" regular \"second phrase\"",
			want:  []string{"\"first phrase\"", "regular", "\"second phrase\""},
		},
		{
			name:  "quoted phrase with internal quotes",
			input: "before \"phrase with \\\"internal quotes\\\"\" after",
			want:  []string{"before", "\"phrase with \\\"internal quotes\\\"\"", "after"},
		},
		{
			name:  "unclosed quote",
			input: "term1 \"unclosed quote",
			want:  []string{"term1", "\"unclosed quote"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TokenizeWithQuotes(tt.input)

			if len(got) != len(tt.want) {
				t.Errorf("TokenizeWithQuotes() got %d tokens, want %d tokens", len(got), len(tt.want))
				return
			}

			for i, token := range got {
				if token != tt.want[i] {
					t.Errorf("TokenizeWithQuotes() token %d: got %q, want %q", i, token, tt.want[i])
				}
			}
		})
	}
}

// Test helper functions for quoted phrases
func TestQuoteHelpers(t *testing.T) {
	quotedTests := []struct {
		input string
		want  bool
	}{
		{"\"quoted\"", true},
		{"notquoted", false},
		{"\"multiple words\"", true},
		{"\"", false},
		{"\"\"", true},
		{"\"partial", false},
		{"partial\"", false},
	}

	for _, tt := range quotedTests {
		t.Run("IsQuotedPhrase_"+tt.input, func(t *testing.T) {
			got := IsQuotedPhrase(tt.input)
			if got != tt.want {
				t.Errorf("IsQuotedPhrase(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}

	// Test UnwrapQuotes
	unwrapTests := []struct {
		input string
		want  string
	}{
		{"\"quoted\"", "quoted"},
		{"notquoted", "notquoted"},
		{"\"multiple words\"", "multiple words"},
		{"\"\"", ""},
	}

	for _, tt := range unwrapTests {
		t.Run("UnwrapQuotes_"+tt.input, func(t *testing.T) {
			got := UnwrapQuotes(tt.input)
			if got != tt.want {
				t.Errorf("UnwrapQuotes(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func CreateSimpleEngine(t *testing.T, query string, caseSensitive bool, maxWildLen, maxKwDist int) *SimpleContentSearchEngine {
	eng := NewSimpleContentSearchEngine(nil, maxWildLen, maxKwDist)
	assert.NoError(t, eng.Compile(query, caseSensitive))
	return eng
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
}
