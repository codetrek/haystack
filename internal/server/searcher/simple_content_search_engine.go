package searcher

import (
	"errors"
	"log"
	"regexp"
	"strconv"
	"strings"

	"github.com/codetrek/haystack/internal/core/documents"
	"github.com/codetrek/haystack/internal/core/invertedindex"
	"github.com/codetrek/haystack/searchcore/tokenizer"
	"github.com/codetrek/haystack/internal/core/workspace"
)

// SimpleContentSearchEngine is a simple search engine that uses regex to find documents

type SimpleContentSearchEngine struct {
	MaxWildcardLength  int
	MaxKeywordDistance int
	Workspace          *workspace.Workspace
	OrClauses          []*SimpleContentSearchEngineAndClause
	WholeWord          bool
}

type SimpleContentSearchEngineAndClause struct {
	Engine   *SimpleContentSearchEngine // Reference to parent engine
	Regex    *regexp.Regexp
	AndTerms []*SimpleContentSearchEngineTerm
}

type SimpleContentSearchEngineTerm struct {
	Engine     *SimpleContentSearchEngine // Reference to parent engine
	Pattern    string
	RegPattern string
	Keywords   []string // First element serves as main prefix
	Wildcards  []string // Wildcard patterns, if any
}

func (q *SimpleContentSearchEngine) CollectDocuments() (*invertedindex.SearchResult, error) {
	rs := []*invertedindex.SearchResult{}
	// Collect the documents for each or clause
	for _, orClause := range q.OrClauses {
		r, err := orClause.CollectDocuments(q.Workspace.Id)
		if err != nil {
			continue
		}

		rs = append(rs, r)
	}

	if len(rs) == 0 {
		return &invertedindex.SearchResult{}, nil
	}

	// Merge the results, we use the first result as the base and merge all other results into it
	result := rs[0]
	for _, r := range rs[1:] {
		for docid := range r.DocIds {
			result.DocIds[docid] = struct{}{}
		}
		for docid := range r.WildDocIds {
			result.WildDocIds[docid] = struct{}{}
		}
	}

	if len(q.OrClauses) > 1 {
		log.Printf("[Searcher] Merged Documents: ==>`%s` found %d documents", q.String(), len(result.DocIds))
	}

	return result, nil
}

func (q *SimpleContentSearchEngineAndClause) CollectDocuments(workspaceId int) (*invertedindex.SearchResult, error) {
	// Collect the documents for each term
	rs := []*invertedindex.SearchResult{}
	for _, term := range q.AndTerms {
		if len(term.Keywords) > 0 {
			r := term.CollectDocuments(workspaceId)
			rs = append(rs, &r)
		}
	}

	if len(rs) == 0 {
		return &invertedindex.SearchResult{
			DocIds: make(map[string]struct{}),
		}, nil
	}

	// Merge the results, the documents should match all "AND" terms
	// We use the first result as the base and remove documents that don't match the other results
	result := rs[0]
	for _, r := range rs[1:] {
		for docid := range result.DocIds {
			if _, ok := r.DocIds[docid]; !ok {
				delete(result.DocIds, docid)
			}
		}

		for docid := range r.WildDocIds {
			if _, ok := result.DocIds[docid]; !ok {
				// If the document is in wildcards but not in keywords, we remove it from the result
				delete(result.WildDocIds, docid)
			}
		}
	}

	log.Printf("[Searcher] Merged Documents: =>`%s` found %d documents, %d wildcard documents", q.String(), len(result.DocIds), len(result.WildDocIds))

	return result, nil
}

func (q *SimpleContentSearchEngineTerm) CollectDocuments(workspaceId int) invertedindex.SearchResult {
	ft, err := documents.GetWorkspace(workspaceId)
	if err != nil {
		log.Printf("[Searcher] CollectDocuments: failed to get fulltext index: %v", err)
		return invertedindex.SearchResult{}
	}
	// If no prefixes, return empty result
	if len(q.Keywords) == 0 {
		return invertedindex.SearchResult{}
	}

	result := invertedindex.SearchResult{
		DocIds:     q.collectWithKeywords(ft.InvertedId, q.Keywords),
		WildDocIds: q.collectWithKeywords(ft.InvertedId, q.Wildcards),
	}

	for docId := range result.WildDocIds {
		if _, exists := result.DocIds[docId]; !exists {
			// If the document is in wildcards but not in keywords, we remove it from the result
			delete(result.WildDocIds, docId)
		}
	}

	return result
}

func (q *SimpleContentSearchEngineTerm) collectWithKeywords(invertedId int, kws []string) map[string]struct{} {
	// If no prefixes, return empty result
	if len(kws) == 0 {
		return map[string]struct{}{}
	}

	// Get results for first prefix
	rs := invertedindex.Search(invertedId, kws[0], -1, nil)
	if len(kws) == 1 {
		log.Printf("[Searcher] CollectDocuments: |--`%s` found %d documents using keyword `%s`", q.String(), len(rs.DocIds), kws[0])
		return rs.DocIds
	}

	result := rs.DocIds
	log.Printf("[Searcher] CollectDocuments: |----`%s` of `%s` found %d documents", kws[0], q.String(), len(result))
	// Intersect with results from other prefixes
	for _, prefix := range kws[1:] {
		r := invertedindex.Search(invertedId, prefix, -1, nil)
		log.Printf("[Searcher] CollectDocuments: |----`%s` of `%s` found %d documents", prefix, q.String(), len(r.DocIds))

		if len(r.DocIds) < len(result) {
			// Swap result and r to ensure result is always the smaller set
			result, r.DocIds = r.DocIds, result
		}

		// Keep only documents that exist in both results
		for docId := range result {
			if _, exists := r.DocIds[docId]; !exists {
				delete(result, docId)
			}
		}

		if len(result) == 0 {
			break
		}
	}

	log.Printf("[Searcher] CollectDocuments: |--`%s` found %d documents using %d keywords",
		q.String(), len(result), len(kws))
	return result
}

func NewSimpleContentSearchEngine(workspace *workspace.Workspace, maxWildLen, maxKwDist int, wholeWord bool) *SimpleContentSearchEngine {
	return &SimpleContentSearchEngine{
		MaxWildcardLength:  maxWildLen,
		MaxKeywordDistance: maxKwDist,
		Workspace:          workspace,
		WholeWord:          wholeWord,
	}
}

func (q *SimpleContentSearchEngine) IsLineMatch(line string) [][]int {
	for _, orClause := range q.OrClauses {
		matches := orClause.IsLineMatch(line)
		if len(matches) > 0 {
			return matches
		}
	}

	return [][]int{}
}

func (q *SimpleContentSearchEngineAndClause) IsLineMatch(line string) [][]int {
	if len(q.AndTerms) == 0 {
		return [][]int{}
	}

	if q.Regex == nil {
		return [][]int{}
	}

	results := [][]int{}
	matches := q.Regex.FindAllSubmatchIndex([]byte(line), -1)
	for _, match := range matches {
		if len(match) == 0 {
			continue
		}
		results = append(results, match[2:4]) // match[2] is the start of the match, match[3] is the end of the match
	}

	return results
}

// TokenizeWithQuotes splits a string into tokens while preserving quoted phrases as single tokens.
// This ensures that text inside quotes is treated as a single entity and not split by spaces.
// For example: 'word1 "phrase with spaces" word2' -> ['word1', '"phrase with spaces"', 'word2']
func TokenizeWithQuotes(s string) []string {
	var tokens []string
	var currentToken strings.Builder
	inQuotes := false
	escaped := false

	for i := 0; i < len(s); i++ {
		r := s[i]

		if r == '\\' && i+1 < len(s) && s[i+1] == '"' {
			currentToken.WriteByte('\\') // Keep the backslash
			currentToken.WriteByte('"')  // Add the quote character
			i++                          // Skip the next quote character since we've handled it
			continue
		}

		if r == '"' && !escaped {
			inQuotes = !inQuotes
			currentToken.WriteByte(r)
			continue
		}

		// Handle OR operator '|' when not in quotes
		if r == '|' && !inQuotes {
			if currentToken.Len() > 0 {
				tokens = append(tokens, strings.TrimSpace(currentToken.String()))
				currentToken.Reset()
			}
			// Add the pipe as a separate token
			tokens = append(tokens, "|")
			continue
		}

		if r == ' ' && !inQuotes {
			if currentToken.Len() > 0 {
				tokens = append(tokens, strings.TrimSpace(currentToken.String()))
				currentToken.Reset()
			}
			continue
		}

		currentToken.WriteByte(r)
	}

	if currentToken.Len() > 0 {
		tokens = append(tokens, strings.TrimSpace(currentToken.String()))
	}

	// Remove empty tokens
	result := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token != "" {
			result = append(result, token)
		}
	}

	return result
}

// IsQuotedPhrase checks if a token is a quoted phrase
func IsQuotedPhrase(token string) bool {
	return len(token) >= 2 && token[0] == '"' && token[len(token)-1] == '"'
}

// UnwrapQuotes removes surrounding quotes from a quoted phrase
func UnwrapQuotes(token string) string {
	return token[1 : len(token)-1]
}

func (q *SimpleContentSearchEngine) Compile(query string, caseSensitive bool) error {
	query = strings.TrimSpace(query)
	if query == "" {
		return errors.New("query is empty")
	}

	maxWildcardLength := strconv.Itoa(q.MaxWildcardLength)
	maxKeywordDistance := strconv.Itoa(q.MaxKeywordDistance)

	// First tokenize the entire query to properly handle quoted phrases containing '|'
	allTokens := TokenizeWithQuotes(query)
	if len(allTokens) == 0 {
		return errors.New("query is empty")
	}

	// Now group tokens into OR clauses
	orClauses := []*SimpleContentSearchEngineAndClause{}
	currentOrClause := []*SimpleContentSearchEngineTerm{}
	currentRegPatterns := []string{}

	for _, token := range allTokens {
		// Check if this is an OR separator outside of quoted phrases
		if token == "|" {
			// If we have any terms in the current OR clause, finalize it
			if len(currentOrClause) > 0 {
				orClause, err := q.finalizeOrClause(currentOrClause, currentRegPatterns, caseSensitive, maxKeywordDistance)
				if err != nil {
					return err
				}
				orClauses = append(orClauses, orClause)

				// Reset for next OR clause
				currentOrClause = []*SimpleContentSearchEngineTerm{}
				currentRegPatterns = []string{}
			}
			continue
		}

		// Process the token
		pattern := q.processToken(token, maxWildcardLength)
		if pattern != nil {
			currentOrClause = append(currentOrClause, pattern)
			currentRegPatterns = append(currentRegPatterns, pattern.RegPattern)
		}
	}

	// Add the final OR clause if there are any terms
	if len(currentOrClause) > 0 {
		orClause, err := q.finalizeOrClause(currentOrClause, currentRegPatterns, caseSensitive, maxKeywordDistance)
		if err != nil {
			return err
		}
		orClauses = append(orClauses, orClause)
	}

	if len(orClauses) == 0 {
		return errors.New("query is empty")
	}

	q.OrClauses = orClauses
	return nil
}

// processToken handles a single token (quoted phrase or regular term) and returns
// the corresponding SimpleContentSearchEngineTerm and regex pattern
func (q *SimpleContentSearchEngine) processToken(token string, maxWildcardLength string) *SimpleContentSearchEngineTerm {
	token = strings.TrimSpace(token)
	if token == "" || token == "AND" {
		return nil
	}

	// Handle quoted phrases specially for exact matching
	if IsQuotedPhrase(token) {
		unwrappedPhrase := UnwrapQuotes(token)
		keywords, wildcards := tokenizer.TokenizeForSearch(unwrappedPhrase, true)

		// Handle escaped quotes
		if strings.Contains(unwrappedPhrase, "\\\"") {
			unwrappedPhrase = strings.ReplaceAll(unwrappedPhrase, "\\\"", "\"")
		}

		escapedPhrase := regexp.QuoteMeta(unwrappedPhrase)
		return &SimpleContentSearchEngineTerm{
			Engine:     q,
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
	// Handle regular patterns (non-quoted)
	return &SimpleContentSearchEngineTerm{
		Engine:     q,
		Pattern:    token,
		RegPattern: regPattern,
		Keywords:   keywords,
		Wildcards:  wildcards,
	}
}

// finalizeOrClause creates a SimpleContentSearchEngineAndClause from the given terms and patterns
func (q *SimpleContentSearchEngine) finalizeOrClause(
	andPatterns []*SimpleContentSearchEngineTerm,
	regPatterns []string,
	caseSensitive bool,
	maxKeywordDistance string) (*SimpleContentSearchEngineAndClause, error) {

	if len(andPatterns) == 0 {
		return nil, errors.New("empty OR clause")
	}

	casePattern := ""
	if !caseSensitive {
		casePattern = "(?i)"
	}

	// Choose patterns based on WholeWord flag
	var finalPatterns []string
	if q.WholeWord {
		// Add word boundaries for whole word matching
		finalPatterns = make([]string, len(regPatterns))
		for i, pattern := range regPatterns {
			finalPatterns[i] = "\\b" + pattern + "\\b"
		}
	} else {
		finalPatterns = regPatterns
	}

	// Compile the appropriate regex
	regexPattern := casePattern + "(" + strings.Join(finalPatterns, ".{0,"+maxKeywordDistance+"}") + ")"
	reg, err := regexp.Compile(regexPattern)
	if err != nil {
		return nil, err
	}

	return &SimpleContentSearchEngineAndClause{
		Engine:   q,
		Regex:    reg,
		AndTerms: andPatterns,
	}, nil
}

func (q *SimpleContentSearchEngine) String() string {
	orClauses := []string{}
	for _, orClause := range q.OrClauses {
		orClauses = append(orClauses, orClause.String())
	}

	return strings.Join(orClauses, " | ")
}

func (t *SimpleContentSearchEngineAndClause) String() string {
	terms := []string{}
	for _, term := range t.AndTerms {
		terms = append(terms, term.String())
	}

	return strings.Join(terms, " AND ")
}

func (t *SimpleContentSearchEngineTerm) String() string {
	return t.Pattern
}
