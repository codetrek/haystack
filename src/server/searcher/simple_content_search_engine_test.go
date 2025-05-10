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
							{Pattern: "test", Prefix: "test"},
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
							{Pattern: "test1", Prefix: "test1"},
							{Pattern: "test2", Prefix: "test2"},
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
							{Pattern: "test1", Prefix: "test1"},
							{Pattern: "test2", Prefix: "test2"},
						},
					},
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "test3", Prefix: "test3"},
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
							{Pattern: "prefix:value", Prefix: "prefix"},
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
							{Pattern: "field1:value1", Prefix: "field1"},
							{Pattern: "field2:value2", Prefix: "field2"},
						},
					},
					{
						AndTerms: []*SimpleContentSearchEngineTerm{
							{Pattern: "field3:value3", Prefix: "field3"},
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
					if pattern.Prefix != wantPattern.Prefix {
						t.Errorf("pattern %d in OR clause %d: got prefix %q, want %q", j, i, pattern.Prefix, wantPattern.Prefix)
					}
				}
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

		line := "with special characters: !@#$%^&*()"
		actual := eng.IsLineMatch(line)
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
}
