package tokenizer

import (
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/go-ego/gse"
)

// CJKTokenizer handles tokenization of CJK (Chinese, Japanese, Korean) text
// using the gse segmenter. It also processes any ASCII portions in the input
// by delegating to ASCIITokenizer.
//
// The gse dictionary is loaded lazily via sync.Once — it is only initialized
// the first time a CJK string is actually tokenized.
type CJKTokenizer struct {
	seg   gse.Segmenter
	once  sync.Once
	ascii ASCIITokenizer
}

// ensureLoaded initializes the gse segmenter with the embedded dictionary.
// It is safe to call from multiple goroutines; the dictionary loads exactly once.
func (t *CJKTokenizer) ensureLoaded() {
	t.once.Do(func() {
		t.seg.SkipLog = true
		// Load embedded zh dictionary (simplified + traditional).
		// ja dict could be added later if needed; gse's zh dict already covers
		// most kanji used in Japanese text.
		_ = t.seg.LoadDictEmbed()
	})
}

// TokenizeForIndex tokenizes a string containing CJK characters for indexing.
// CJK portions are segmented with gse; ASCII portions are handled by ASCIITokenizer.
// Returns a sorted, deduplicated list of normalized tokens.
func (t *CJKTokenizer) TokenizeForIndex(str string) []string {
	t.ensureLoaded()

	uniqueWords := make(map[string]struct{})

	// Use gse.Cut with HMM for better segmentation accuracy
	segments := t.seg.Cut(str, true)
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}

		if containsCJK(seg) {
			// CJK token: minimum 1 rune
			if utf8.RuneCountInString(seg) >= 1 {
				uniqueWords[seg] = struct{}{}
			}
		} else {
			// ASCII portion: delegate to ASCIITokenizer for proper splitting
			asciiTokens := t.ascii.TokenizeForIndex(seg)
			for _, tok := range asciiTokens {
				uniqueWords[tok] = struct{}{}
			}
		}
	}

	if len(uniqueWords) == 0 {
		return []string{}
	}

	result := make([]string, 0, len(uniqueWords))
	for word := range uniqueWords {
		result = append(result, word)
	}
	sort.Strings(result)

	return result
}

// TokenizeForSearch tokenizes a string containing CJK characters for searching.
// CJK portions are segmented with gse; ASCII portions are handled by ASCIITokenizer.
// Returns tokens and wildcards consistent with the search interface.
func (t *CJKTokenizer) TokenizeForSearch(s string, exactMatching bool) ([]string, []string) {
	t.ensureLoaded()

	exists := map[string]struct{}{}
	var result []string
	var wildcards []string

	push := func(word string) {
		if _, ok := exists[word]; ok {
			return
		}
		exists[word] = struct{}{}
		result = append(result, word)
	}

	// Use gse.Cut with HMM
	segments := t.seg.Cut(s, true)
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}

		if containsCJK(seg) {
			// CJK token: minimum 1 rune
			if utf8.RuneCountInString(seg) >= 1 {
				push(seg)
			}
		} else {
			// ASCII portion: delegate to ASCIITokenizer for proper handling
			asciiTokens, asciiWildcards := t.ascii.TokenizeForSearch(seg, exactMatching)
			for _, tok := range asciiTokens {
				push(tok)
			}
			wildcards = append(wildcards, asciiWildcards...)
		}
	}

	return result, wildcards
}
