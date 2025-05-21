package searcher

import (
	"errors"
	"log"
	"regexp"
	"strconv"
	"strings"

	"github.com/ai-microsoft/haystack/server/core/documents"
	"github.com/ai-microsoft/haystack/server/core/invertedindex"
	"github.com/ai-microsoft/haystack/server/core/workspace"
)

var rePrefix = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_-]+`)

type SimpleContentSearchEngine struct {
	MaxWildcardLength  int
	MaxKeywordDistance int
	Workspace          *workspace.Workspace
	OrClauses          []*SimpleContentSearchEngineAndClause
}

type SimpleContentSearchEngineAndClause struct {
	Regex    *regexp.Regexp
	AndTerms []*SimpleContentSearchEngineTerm
}

type SimpleContentSearchEngineTerm struct {
	Pattern  string
	Prefixes []string // First element serves as main prefix
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
		r := term.CollectDocuments(workspaceId)
		rs = append(rs, &r)
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
	}

	if len(q.AndTerms) > 1 {
		log.Printf("[Searcher] Merged Documents: =>`%s` found %d documents", q.String(), len(result.DocIds))
	}

	return result, nil
}

func (q *SimpleContentSearchEngineTerm) CollectDocuments(workspaceId int) invertedindex.SearchResult {
	ft, err := documents.GetWorkspace(workspaceId)
	if err != nil {
		log.Printf("[Searcher] CollectDocuments: failed to get fulltext index: %v", err)
		return invertedindex.SearchResult{}
	}
	// If no prefixes, return empty result
	if len(q.Prefixes) == 0 {
		return invertedindex.SearchResult{}
	}

	// Get results for first prefix
	result := invertedindex.Search(ft.InvertedId, q.Prefixes[0], -1)
	if len(q.Prefixes) == 1 {
		log.Printf("[Searcher] CollectDocuments: |--`%s` found %d documents using prefix `%s`", q.String(), len(result.DocIds), q.Prefixes[0])
		return result
	}

	log.Printf("[Searcher] CollectDocuments: |----`%s` of `%s` found %d documents", q.Prefixes[0], q.String(), len(result.DocIds))
	// Intersect with results from other prefixes
	for _, prefix := range q.Prefixes[1:] {
		r := invertedindex.Search(ft.InvertedId, prefix, -1)
		log.Printf("[Searcher] CollectDocuments: |----`%s` of `%s` found %d documents", prefix, q.String(), len(r.DocIds))
		// Keep only documents that exist in both results
		for docId := range result.DocIds {
			if _, exists := r.DocIds[docId]; !exists {
				delete(result.DocIds, docId)
			}
		}
	}

	log.Printf("[Searcher] CollectDocuments: |--`%s` found %d documents using %d prefixes",
		q.String(), len(result.DocIds), len(q.Prefixes))
	return result
}

func NewSimpleContentSearchEngine(workspace *workspace.Workspace, maxWildLen, maxKwDist int) *SimpleContentSearchEngine {
	return &SimpleContentSearchEngine{
		MaxWildcardLength:  maxWildLen,
		MaxKeywordDistance: maxKwDist,
		Workspace:          workspace,
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
	if IsQuotedPhrase(token) {
		return token[1 : len(token)-1]
	}
	return token
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
		pattern, regPattern := q.processToken(token, maxWildcardLength)
		if pattern != nil {
			currentOrClause = append(currentOrClause, pattern)
			currentRegPatterns = append(currentRegPatterns, regPattern)
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
func (q *SimpleContentSearchEngine) processToken(token string, maxWildcardLength string) (*SimpleContentSearchEngineTerm, string) {
	token = strings.TrimSpace(token)
	if token == "" || token == "AND" {
		return nil, ""
	}

	// Handle quoted phrases specially for exact matching
	if IsQuotedPhrase(token) {
		unwrappedPhrase := UnwrapQuotes(token)
		words := strings.Fields(unwrappedPhrase)

		// Collect all valid prefixes
		var prefixes []string
		for _, word := range words {
			if matches := rePrefix.FindAllString(word, 1); len(matches) > 0 {
				prefixes = append(prefixes, strings.ToLower(matches[0]))
			}
		}

		// If no valid prefixes found, use first word or entire phrase
		if len(prefixes) == 0 {
			prefixWord := unwrappedPhrase
			if spaceIdx := strings.IndexByte(unwrappedPhrase, ' '); spaceIdx > 0 {
				prefixWord = unwrappedPhrase[:spaceIdx]
			}
			if matches := rePrefix.FindAllString(prefixWord, 1); len(matches) > 0 {
				prefixes = append(prefixes, strings.ToLower(matches[0]))
			} else {
				prefixes = append(prefixes, unwrappedPhrase)
			}
		}

		// Handle escaped quotes
		if strings.Contains(unwrappedPhrase, "\\\"") {
			textPattern := strings.ReplaceAll(unwrappedPhrase, "\\\"", "\"")
			escapedPhrase := regexp.QuoteMeta(textPattern)
			return &SimpleContentSearchEngineTerm{
				Pattern:  token,
				Prefixes: prefixes,
			}, escapedPhrase
		}

		escapedPhrase := regexp.QuoteMeta(unwrappedPhrase)
		return &SimpleContentSearchEngineTerm{
			Pattern:  token,
			Prefixes: prefixes,
		}, escapedPhrase
	}

	// Handle regular patterns (non-quoted)
	prefixes := rePrefix.FindAllString(token, 1)
	if len(prefixes) > 0 {
		regPattern := token
		regPattern = strings.ReplaceAll(regPattern, ".", "\\.")
		regPattern = strings.ReplaceAll(regPattern, "*", ".{0,"+maxWildcardLength+"}")
		regPattern = strings.ReplaceAll(regPattern, "?", ".?")
		regPattern = strings.ReplaceAll(regPattern, "[", "\\[")
		regPattern = strings.ReplaceAll(regPattern, "]", "\\]")
		regPattern = strings.ReplaceAll(regPattern, "^", "\\^")
		regPattern = strings.ReplaceAll(regPattern, "$", "\\$")
		regPattern = strings.ReplaceAll(regPattern, ":", "\\:")

		return &SimpleContentSearchEngineTerm{
			Pattern:  token,
			Prefixes: []string{strings.ToLower(prefixes[0])},
		}, regPattern
	}

	return nil, ""
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

	reg, err := regexp.Compile(casePattern + "(" + strings.Join(regPatterns, ".{0,"+maxKeywordDistance+"}") + ")")

	if err != nil {
		return nil, err
	}

	return &SimpleContentSearchEngineAndClause{
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
