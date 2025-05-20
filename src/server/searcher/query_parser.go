package searcher

import (
	"github.com/ai-microsoft/haystack/server/core/invertedindex"
	"github.com/alecthomas/participle/v2"
	"github.com/alecthomas/participle/v2/lexer"
)

// Query represents the complete search query
type Query struct {
	Expression *OrExpression `parser:"@@"`
}

// OrExpression handles OR operations with lower precedence
type OrExpression struct {
	Left  *AndExpression   `parser:"@@"`
	Right []*OrRightClause `parser:"@@*"`
}

// OrRightClause represents the right side of an OR operation
type OrRightClause struct {
	Op    string         `parser:"@(\"OR\" | \"|\")"`
	Right *AndExpression `parser:"@@"`
}

// AndExpression handles AND operations with higher precedence
type AndExpression struct {
	Left  *Term             `parser:"@@"`
	Right []*AndRightClause `parser:"@@*"`
}

// AndRightClause represents the right side of an AND operation
type AndRightClause struct {
	Op    string `parser:"@\"AND\"?"`
	Right *Term  `parser:"@@"`
}

// Term represents a basic search term
type Term struct {
	Not      bool    `parser:"@\"NOT\"?"`
	Quoted   *string `parser:"( @String"`
	Word     *string `parser:"| @Ident )"`
	Wildcard bool    `parser:"@\"*\"?"`
}

func ParseQuery(query string) (*Query, error) {
	// Define the lexer for our grammar
	queryLexer := lexer.MustSimple([]lexer.SimpleRule{
		{Name: "AND", Pattern: `AND`},
		{Name: "OR", Pattern: `OR`},
		{Name: "NOT", Pattern: `NOT`},
		{Name: "Ident", Pattern: `[a-zA-Z0-9_]+`},
		{Name: "String", Pattern: `"(\\.|[^"])*"`},
		{Name: "Whitespace", Pattern: `\s+`},
		{Name: "Punct", Pattern: `[*|]`},
	})

	// Create the parser with our grammar and lexer
	parser := participle.MustBuild[Query](
		participle.Lexer(queryLexer),
		participle.Unquote("String"),
		participle.Elide("Whitespace"),
	)

	// Parse the query string
	result, err := parser.ParseString("", query)
	if err != nil {
		return nil, err
	}

	// Process quoted terms to remove quotes
	processQuery(result)

	return result, nil
}

// processQuery post-processes the parsed query
func processQuery(q *Query) {
	// Post-processing logic if needed
}

// SearchFiles searches the files in the workspace
// input is the search result from the previous query
// returns the search result for the current query
func (q *Query) SearchFiles() (*invertedindex.SearchResult, error) {
	result := &invertedindex.SearchResult{}
	return q.Expression.SearchFiles(result)
}

func (o *OrExpression) SearchFiles(input *invertedindex.SearchResult) (*invertedindex.SearchResult, error) {
	return input, nil
}

func (a *AndExpression) SearchFiles(input *invertedindex.SearchResult) *invertedindex.SearchResult {
	return nil
}

func (t *Term) SearchFiles(input *invertedindex.SearchResult) *invertedindex.SearchResult {
	return nil
}

// String returns the string representation of the query
func (q *Query) String() string {
	if q.Expression == nil {
		return ""
	}
	return q.Expression.String()
}

// String returns the string representation of an OR expression
func (o *OrExpression) String() string {
	if o == nil {
		return ""
	}

	result := o.Left.String()
	for _, right := range o.Right {
		result += " " + right.Op + " " + right.Right.String()
	}
	return result
}

// String returns the string representation of an AND expression
func (a *AndExpression) String() string {
	if a == nil {
		return ""
	}

	result := a.Left.String()
	for _, right := range a.Right {
		if right.Op != "" {
			result += " " + right.Op + " " + right.Right.String()
		} else {
			result += " " + right.Right.String()
		}
	}
	return result
}

// String returns the string representation of a term
func (t *Term) String() string {
	if t == nil {
		return ""
	}

	prefix := ""
	if t.Not {
		prefix = "NOT "
	}

	var value string
	if t.Quoted != nil {
		value = "\"" + *t.Quoted + "\""
	} else if t.Word != nil {
		value = *t.Word
	}

	suffix := ""
	if t.Wildcard {
		suffix = "*"
	}

	return prefix + value + suffix
}
