package tokenizer

import (
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/go-ego/gse"
)

// CJKTokenizer handles tokenization of CJK (Chinese, Japanese, Korean) text
// using the gse segmenter. The gse dictionary is lazily loaded on first use
// via sync.Once to avoid startup cost when CJK tokenization isn't needed.
type CJKTokenizer struct {
	once   sync.Once
	seg    gse.Segmenter
	loaded bool
}

// ensureLoaded lazily initializes the gse segmenter on first use.
func (t *CJKTokenizer) ensureLoaded() {
	t.once.Do(func() {
		// LoadDict with no args uses the embedded dictionary.
		t.seg.LoadDict()
		t.loaded = true
	})
}

// isPunctOrSpace reports whether every rune in the string is whitespace or punctuation.
func isPunctOrSpace(s string) bool {
	for _, r := range s {
		if !unicode.IsSpace(r) && !unicode.IsPunct(r) && !unicode.IsSymbol(r) {
			return false
		}
	}
	return true
}

// TokenizeForIndex tokenizes CJK text for indexing.
// It uses gse to segment the text, collects unique tokens (min 1 rune),
// filters out pure whitespace/punctuation, and returns sorted results.
func (t *CJKTokenizer) TokenizeForIndex(str string) []string {
	if strings.TrimSpace(str) == "" {
		return []string{}
	}

	t.ensureLoaded()

	segments := t.seg.Cut(str, true)

	uniqueTokens := make(map[string]struct{}, len(segments))
	for _, seg := range segments {
		token := strings.TrimSpace(seg)
		if len(token) == 0 {
			continue
		}
		if isPunctOrSpace(token) {
			continue
		}
		uniqueTokens[strings.ToLower(token)] = struct{}{}
	}

	result := make([]string, 0, len(uniqueTokens))
	for token := range uniqueTokens {
		result = append(result, token)
	}

	if len(result) == 0 {
		return []string{}
	}

	sort.Strings(result)
	return result
}

// TokenizeForSearch tokenizes CJK text for searching.
// It uses gse to segment the text and returns tokens with empty wildcards.
func (t *CJKTokenizer) TokenizeForSearch(s string, exactMatching bool) ([]string, []string) {
	if strings.TrimSpace(s) == "" {
		return []string{}, nil
	}

	t.ensureLoaded()

	segments := t.seg.Cut(s, true)

	exists := make(map[string]struct{}, len(segments))
	result := []string{}

	for _, seg := range segments {
		token := strings.TrimSpace(seg)
		if len(token) == 0 {
			continue
		}
		if isPunctOrSpace(token) {
			continue
		}
		if _, ok := exists[token]; ok {
			continue
		}
		exists[token] = struct{}{}
		result = append(result, token)
	}

	return result, nil
}
