// Package engine provides a content query engine for full-text search.
// It compiles a query string into a set of OR/AND clauses, collects matching
// document IDs from an inverted index, and tests individual lines for matches.
package engine

import (
	"errors"
	"log"
	"regexp"
	"strconv"
	"strings"

	"github.com/codetrek/haystack/searchcore/documents"
	"github.com/codetrek/haystack/searchcore/invertedindex"
	"github.com/codetrek/haystack/searchcore/tokenizer"
)

// Options controls the matching behaviour of an Engine.
type Options struct {
	// MaxWildcardLength is the maximum number of characters a wildcard (*) may
	// expand to in the generated regular expression.
	MaxWildcardLength int

	// MaxKeywordDistance is the maximum number of characters allowed between
	// adjacent AND terms in the generated regular expression.
	MaxKeywordDistance int

	// WholeWord, when true, wraps each term's regex with word boundaries (\b).
	WholeWord bool
}

// Engine compiles a query string and matches content lines against it. It also
// uses an inverted index and document store to pre-filter candidate documents
// before performing per-line matching.
type Engine struct {
	opts         Options
	collectionID int
	idx          *invertedindex.Index
	docs         *documents.Store

	orClauses []*andClause
}

// New constructs a content Engine backed by the supplied index and document
// store. idx and docs may be nil (e.g. in unit tests that only call
// Compile/IsLineMatch without CollectDocuments).
func New(idx *invertedindex.Index, docs *documents.Store, collectionID int, opts Options) *Engine {
	return &Engine{
		opts:         opts,
		collectionID: collectionID,
		idx:          idx,
		docs:         docs,
	}
}

// andClause represents one OR branch of a compiled query. It contains one or
// more AND terms that must all appear in a document (index phase) and a regex
// that must match a line (line-match phase).
type andClause struct {
	engine   *Engine
	regex    *regexp.Regexp
	andTerms []*term
}

// term represents a single token within an AND clause. It carries the raw
// pattern, its regex rendering, the index keywords, and any wildcard keywords.
type term struct {
	engine     *Engine
	Pattern    string
	RegPattern string
	Keywords   []string // First element serves as main prefix
	Wildcards  []string // Wildcard patterns, if any
}

// CollectDocuments uses the inverted index to collect the set of document IDs
// that are candidate matches for the compiled query.
func (e *Engine) CollectDocuments() (*invertedindex.SearchResult, error) {
	rs := []*invertedindex.SearchResult{}
	for _, clause := range e.orClauses {
		r, err := clause.collectDocuments(e.collectionID)
		if err != nil {
			continue
		}
		rs = append(rs, r)
	}

	if len(rs) == 0 {
		return &invertedindex.SearchResult{}, nil
	}

	// Merge results — union across OR clauses.
	result := rs[0]
	for _, r := range rs[1:] {
		for docid := range r.DocIds {
			result.DocIds[docid] = struct{}{}
		}
		for docid := range r.WildDocIds {
			result.WildDocIds[docid] = struct{}{}
		}
	}

	if len(e.orClauses) > 1 {
		log.Printf("[Engine] Merged Documents: ==>`%s` found %d documents", e.String(), len(result.DocIds))
	}

	return result, nil
}

func (c *andClause) collectDocuments(collectionID int) (*invertedindex.SearchResult, error) {
	rs := []*invertedindex.SearchResult{}
	for _, t := range c.andTerms {
		if len(t.Keywords) > 0 {
			r := t.collectDocuments(collectionID)
			rs = append(rs, &r)
		}
	}

	if len(rs) == 0 {
		return &invertedindex.SearchResult{
			DocIds: make(map[string]struct{}),
		}, nil
	}

	// Intersection across AND terms.
	result := rs[0]
	for _, r := range rs[1:] {
		for docid := range result.DocIds {
			if _, ok := r.DocIds[docid]; !ok {
				delete(result.DocIds, docid)
			}
		}
		for docid := range r.WildDocIds {
			if _, ok := result.DocIds[docid]; !ok {
				delete(result.WildDocIds, docid)
			}
		}
	}

	log.Printf("[Engine] Merged Documents: =>`%s` found %d documents, %d wildcard documents",
		c.String(), len(result.DocIds), len(result.WildDocIds))

	return result, nil
}

func (t *term) collectDocuments(collectionID int) invertedindex.SearchResult {
	if t.engine.docs == nil {
		log.Printf("[Engine] collectDocuments: docs store not initialised")
		return invertedindex.SearchResult{}
	}

	ws, err := t.engine.docs.GetWorkspace(collectionID)
	if err != nil {
		log.Printf("[Engine] collectDocuments: failed to get workspace: %v", err)
		return invertedindex.SearchResult{}
	}

	if len(t.Keywords) == 0 {
		return invertedindex.SearchResult{}
	}

	result := invertedindex.SearchResult{
		DocIds:     t.collectWithKeywords(ws.InvertedId, t.Keywords),
		WildDocIds: t.collectWithKeywords(ws.InvertedId, t.Wildcards),
	}

	for docID := range result.WildDocIds {
		if _, exists := result.DocIds[docID]; !exists {
			delete(result.WildDocIds, docID)
		}
	}

	return result
}

func (t *term) collectWithKeywords(invertedID int, kws []string) map[string]struct{} {
	if len(kws) == 0 {
		return map[string]struct{}{}
	}

	if t.engine.idx == nil {
		log.Printf("[Engine] collectWithKeywords: inverted index not initialised")
		return map[string]struct{}{}
	}

	rs := t.engine.idx.Search(invertedID, kws[0], -1, nil)
	if len(kws) == 1 {
		log.Printf("[Engine] CollectDocuments: |--`%s` found %d documents using keyword `%s`",
			t.String(), len(rs.DocIds), kws[0])
		return rs.DocIds
	}

	result := rs.DocIds
	log.Printf("[Engine] CollectDocuments: |----`%s` of `%s` found %d documents", kws[0], t.String(), len(result))

	for _, prefix := range kws[1:] {
		r := t.engine.idx.Search(invertedID, prefix, -1, nil)
		log.Printf("[Engine] CollectDocuments: |----`%s` of `%s` found %d documents", prefix, t.String(), len(r.DocIds))

		if len(r.DocIds) < len(result) {
			result, r.DocIds = r.DocIds, result
		}

		for docID := range result {
			if _, exists := r.DocIds[docID]; !exists {
				delete(result, docID)
			}
		}

		if len(result) == 0 {
			break
		}
	}

	log.Printf("[Engine] CollectDocuments: |--`%s` found %d documents using %d keywords",
		t.String(), len(result), len(kws))
	return result
}

// IsLineMatch returns the byte offsets of all matches of the compiled query in
// line. Each element is a [start, end] pair (as returned by
// regexp.FindAllSubmatchIndex).
func (e *Engine) IsLineMatch(line string) [][]int {
	for _, clause := range e.orClauses {
		matches := clause.isLineMatch(line)
		if len(matches) > 0 {
			return matches
		}
	}
	return [][]int{}
}

func (c *andClause) isLineMatch(line string) [][]int {
	if len(c.andTerms) == 0 {
		return [][]int{}
	}
	if c.regex == nil {
		return [][]int{}
	}

	results := [][]int{}
	matches := c.regex.FindAllSubmatchIndex([]byte(line), -1)
	for _, match := range matches {
		if len(match) == 0 {
			continue
		}
		results = append(results, match[2:4])
	}
	return results
}

// Compile parses query into OR/AND/term clauses and compiles per-clause regexes.
func (e *Engine) Compile(query string, caseSensitive bool) error {
	query = strings.TrimSpace(query)
	if query == "" {
		return errors.New("query is empty")
	}

	maxWildcardLength := strconv.Itoa(e.opts.MaxWildcardLength)
	maxKeywordDistance := strconv.Itoa(e.opts.MaxKeywordDistance)

	allTokens := TokenizeWithQuotes(query)
	if len(allTokens) == 0 {
		return errors.New("query is empty")
	}

	var orClauses []*andClause
	var currentTerms []*term
	var currentRegPatterns []string

	for _, token := range allTokens {
		if token == "|" {
			if len(currentTerms) > 0 {
				clause, err := e.finalizeOrClause(currentTerms, currentRegPatterns, caseSensitive, maxKeywordDistance)
				if err != nil {
					return err
				}
				orClauses = append(orClauses, clause)
				currentTerms = nil
				currentRegPatterns = nil
			}
			continue
		}

		t := e.processToken(token, maxWildcardLength)
		if t != nil {
			currentTerms = append(currentTerms, t)
			currentRegPatterns = append(currentRegPatterns, t.RegPattern)
		}
	}

	if len(currentTerms) > 0 {
		clause, err := e.finalizeOrClause(currentTerms, currentRegPatterns, caseSensitive, maxKeywordDistance)
		if err != nil {
			return err
		}
		orClauses = append(orClauses, clause)
	}

	if len(orClauses) == 0 {
		return errors.New("query is empty")
	}

	e.orClauses = orClauses
	return nil
}

func (e *Engine) processToken(token string, maxWildcardLength string) *term {
	token = strings.TrimSpace(token)
	if token == "" || token == "AND" {
		return nil
	}

	if IsQuotedPhrase(token) {
		unwrapped := UnwrapQuotes(token)
		keywords, wildcards := tokenizer.TokenizeForSearch(unwrapped, true)

		if strings.Contains(unwrapped, "\\\"") {
			unwrapped = strings.ReplaceAll(unwrapped, "\\\"", "\"")
		}

		escapedPhrase := regexp.QuoteMeta(unwrapped)
		return &term{
			engine:     e,
			Pattern:    token,
			RegPattern: escapedPhrase,
			Keywords:   keywords,
			Wildcards:  wildcards,
		}
	}

	regPattern := token
	regPattern = strings.ReplaceAll(regPattern, "\\", "\\\\")
	regPattern = strings.ReplaceAll(regPattern, ".", "\\.")
	regPattern = strings.ReplaceAll(regPattern, "{", "\\{")
	regPattern = strings.ReplaceAll(regPattern, "}", "\\}")
	regPattern = strings.ReplaceAll(regPattern, "*", ".{0,"+maxWildcardLength+"}")
	regPattern = strings.ReplaceAll(regPattern, "?", "\\?")
	regPattern = strings.ReplaceAll(regPattern, "(", "\\(")
	regPattern = strings.ReplaceAll(regPattern, ")", "\\)")
	regPattern = strings.ReplaceAll(regPattern, "[", "\\[")
	regPattern = strings.ReplaceAll(regPattern, "]", "\\]")
	regPattern = strings.ReplaceAll(regPattern, "^", "\\^")
	regPattern = strings.ReplaceAll(regPattern, "$", "\\$")
	regPattern = strings.ReplaceAll(regPattern, ":", "\\:")
	regPattern = strings.ReplaceAll(regPattern, "+", "\\+")

	keywords, wildcards := tokenizer.TokenizeForSearch(token, false)
	return &term{
		engine:     e,
		Pattern:    token,
		RegPattern: regPattern,
		Keywords:   keywords,
		Wildcards:  wildcards,
	}
}

func (e *Engine) finalizeOrClause(
	andTerms []*term,
	regPatterns []string,
	caseSensitive bool,
	maxKeywordDistance string,
) (*andClause, error) {
	if len(andTerms) == 0 {
		return nil, errors.New("empty OR clause")
	}

	casePrefix := ""
	if !caseSensitive {
		casePrefix = "(?i)"
	}

	finalPatterns := regPatterns
	if e.opts.WholeWord {
		finalPatterns = make([]string, len(regPatterns))
		for i, p := range regPatterns {
			finalPatterns[i] = "\\b" + p + "\\b"
		}
	}

	regexPattern := casePrefix + "(" + strings.Join(finalPatterns, ".{0,"+maxKeywordDistance+"}") + ")"
	reg, err := regexp.Compile(regexPattern)
	if err != nil {
		return nil, err
	}

	return &andClause{
		engine:   e,
		regex:    reg,
		andTerms: andTerms,
	}, nil
}

// String returns a human-readable representation of the compiled query.
func (e *Engine) String() string {
	parts := make([]string, 0, len(e.orClauses))
	for _, c := range e.orClauses {
		parts = append(parts, c.String())
	}
	return strings.Join(parts, " | ")
}

func (c *andClause) String() string {
	parts := make([]string, 0, len(c.andTerms))
	for _, t := range c.andTerms {
		parts = append(parts, t.String())
	}
	return strings.Join(parts, " AND ")
}

func (t *term) String() string {
	return t.Pattern
}

// TokenizeWithQuotes splits a string into tokens while preserving quoted
// phrases as single tokens. For example:
//
//	`word1 "phrase with spaces" word2` → ["word1", `"phrase with spaces"`, "word2"]
func TokenizeWithQuotes(s string) []string {
	var tokens []string
	var current strings.Builder
	inQuotes := false

	for i := 0; i < len(s); i++ {
		r := s[i]

		if r == '\\' && i+1 < len(s) && s[i+1] == '"' {
			current.WriteByte('\\')
			current.WriteByte('"')
			i++
			continue
		}

		if r == '"' {
			inQuotes = !inQuotes
			current.WriteByte(r)
			continue
		}

		if r == '|' && !inQuotes {
			if current.Len() > 0 {
				tokens = append(tokens, strings.TrimSpace(current.String()))
				current.Reset()
			}
			tokens = append(tokens, "|")
			continue
		}

		if r == ' ' && !inQuotes {
			if current.Len() > 0 {
				tokens = append(tokens, strings.TrimSpace(current.String()))
				current.Reset()
			}
			continue
		}

		current.WriteByte(r)
	}

	if current.Len() > 0 {
		tokens = append(tokens, strings.TrimSpace(current.String()))
	}

	result := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		if tok != "" {
			result = append(result, tok)
		}
	}
	return result
}

// IsQuotedPhrase reports whether token is a double-quoted phrase.
func IsQuotedPhrase(token string) bool {
	return len(token) >= 2 && token[0] == '"' && token[len(token)-1] == '"'
}

// UnwrapQuotes removes the surrounding double quotes from a quoted phrase.
func UnwrapQuotes(token string) string {
	return token[1 : len(token)-1]
}
