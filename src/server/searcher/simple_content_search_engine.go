package searcher

import (
	"errors"
	"log"
	"regexp"
	"strconv"
	"strings"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/server/core/fulltext"
	"github.com/ai-microsoft/haystack/server/core/workspace"
)

var rePrefix = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_-]+`)

type SimpleContentSearchEngine struct {
	Workspace *workspace.Workspace
	OrClauses []*SimpleContentSearchEngineAndClause
}

type SimpleContentSearchEngineAndClause struct {
	Regex    *regexp.Regexp
	AndTerms []*SimpleContentSearchEngineTerm
}

type SimpleContentSearchEngineTerm struct {
	Pattern string
	Prefix  string
}

func (q *SimpleContentSearchEngine) CollectDocuments() (*fulltext.SearchResult, error) {
	rs := []*fulltext.SearchResult{}
	// Collect the documents for each or clause
	for _, orClause := range q.OrClauses {
		r, err := orClause.CollectDocuments(q.Workspace.ID)
		if err != nil {
			continue
		}

		rs = append(rs, r)
	}

	if len(rs) == 0 {
		return &fulltext.SearchResult{}, nil
	}

	// Merge the results, we use the first result as the base and merge all other results into it
	result := rs[0]
	for _, r := range rs[1:] {
		for docid := range r.DocIds {
			result.DocIds[docid] = struct{}{}
		}
	}

	if len(q.OrClauses) > 1 {
		log.Printf("Merged Documents: ==>`%s` found %d documents", q.String(), len(result.DocIds))
	}

	return result, nil
}

func (q *SimpleContentSearchEngineAndClause) CollectDocuments(workspaceId string) (*fulltext.SearchResult, error) {
	// Collect the documents for each term
	rs := []*fulltext.SearchResult{}
	for _, term := range q.AndTerms {
		r := term.CollectDocuments(workspaceId)
		rs = append(rs, &r)
	}

	if len(rs) == 0 {
		return &fulltext.SearchResult{
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
		log.Printf("Merged Documents: =>`%s` found %d documents", q.String(), len(result.DocIds))
	}

	return result, nil
}

func (q *SimpleContentSearchEngineTerm) CollectDocuments(workspaceId string) fulltext.SearchResult {
	r := fulltext.Search(workspaceId, q.Prefix, -1)
	log.Printf("CollectDocuments: |--`%s` found %d documents", q.String(), len(r.DocIds))
	return r
}

func NewSimpleContentSearchEngine(workspace *workspace.Workspace) *SimpleContentSearchEngine {
	return &SimpleContentSearchEngine{
		Workspace: workspace,
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
		results = append(results, match[4:6]) // match[4] is the start of the match, match[5] is the end of the match
	}

	return results
}

func (q *SimpleContentSearchEngine) Compile(query string, caseSensitive bool) error {
	query = strings.TrimSpace(query)
	if query == "" {
		return errors.New("query is empty")
	}

	maxWildcardLength := strconv.Itoa(conf.Get().Server.Search.MaxWildcardLength)
	maxKeywordDistance := strconv.Itoa(conf.Get().Server.Search.MaxKeywordDistance)

	orClauses := []*SimpleContentSearchEngineAndClause{}
	for _, orClause := range strings.Split(query, "|") {
		orClause = strings.TrimSpace(orClause)
		if orClause == "" {
			continue
		}

		andPatterns := []*SimpleContentSearchEngineTerm{}
		regPatterns := []string{}
		for _, andPattern := range strings.Split(orClause, " ") {
			andPattern = strings.TrimSpace(andPattern)
			if andPattern == "" || andPattern == "AND" {
				continue
			}

			prefixes := rePrefix.FindAllString(andPattern, 1)
			if len(prefixes) > 0 {
				regPattern := andPattern
				regPattern = strings.ReplaceAll(regPattern, ".", "\\.")
				regPattern = strings.ReplaceAll(regPattern, "*", ".{0,"+maxWildcardLength+"}")
				regPattern = strings.ReplaceAll(regPattern, "?", ".?")
				regPattern = strings.ReplaceAll(regPattern, "[", "\\[")
				regPattern = strings.ReplaceAll(regPattern, "]", "\\]")
				regPattern = strings.ReplaceAll(regPattern, "^", "\\^")
				regPattern = strings.ReplaceAll(regPattern, "$", "\\$")
				regPattern = strings.ReplaceAll(regPattern, ":", "\\:")
				regPatterns = append(regPatterns, regPattern)

				andPatterns = append(andPatterns, &SimpleContentSearchEngineTerm{
					Pattern: andPattern,
					Prefix:  strings.ToLower(prefixes[0]),
				})
			}
		}

		if len(andPatterns) == 0 {
			continue
		}

		casePattern := ""
		if !caseSensitive {
			casePattern = "(?i)"
		}
		reg, err := regexp.Compile(casePattern + "(^|[^a-zA-Z0-9])(" + strings.Join(regPatterns, ".{0,"+maxKeywordDistance+"}[^a-zA-Z0-9]") + ")")
		if err != nil {
			return err
		}

		orClauses = append(orClauses, &SimpleContentSearchEngineAndClause{
			Regex:    reg,
			AndTerms: andPatterns,
		})
	}

	if len(orClauses) == 0 {
		return errors.New("query is empty")
	}

	q.OrClauses = orClauses
	return nil
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
