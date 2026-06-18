package tokenizer

import (
	"sort"
	"strings"
)

// MixedTokenizer handles text that may contain both ASCII and CJK content.
// It splits input into CJK and non-CJK runs, routing each to the appropriate
// tokenizer, then merges and deduplicates the results.
type MixedTokenizer struct {
	ascii ASCIITokenizer
	cjk   CJKTokenizer
}

// textRun represents a contiguous run of text that is either CJK or non-CJK.
type textRun struct {
	text  string
	isCJK bool
}

// splitIntoRuns splits the input text into contiguous runs of CJK and non-CJK characters.
func splitIntoRuns(s string) []textRun {
	if len(s) == 0 {
		return nil
	}

	var runs []textRun
	var current strings.Builder
	currentIsCJK := false
	first := true

	for _, r := range s {
		charIsCJK := isCJK(r)

		if first {
			currentIsCJK = charIsCJK
			first = false
		} else if charIsCJK != currentIsCJK {
			// Transition: flush the current run
			runs = append(runs, textRun{text: current.String(), isCJK: currentIsCJK})
			current.Reset()
			currentIsCJK = charIsCJK
		}

		current.WriteRune(r)
	}

	// Flush the last run
	if current.Len() > 0 {
		runs = append(runs, textRun{text: current.String(), isCJK: currentIsCJK})
	}

	return runs
}

// TokenizeForIndex tokenizes text that may contain both ASCII and CJK content.
// It splits the text into CJK and non-CJK runs, tokenizes each with the
// appropriate tokenizer, and returns merged, deduplicated, sorted results.
func (t *MixedTokenizer) TokenizeForIndex(str string) []string {
	// Strip NUL/C0 control bytes up front. A NUL is not a CJK rune, so without
	// this it would fragment a CJK run in splitIntoRuns (中\x00国 -> "中","国")
	// before CJKTokenizer's own normalization could remove it, reaching the
	// index as split tokens. Stripping here keeps boundaries byte-identical to
	// gse on real (control-free) text while making the cedar-sentinel
	// divergence unreachable. It is a no-op for the ASCII regex path.
	str = normalizeForSegmentation(str)

	if !containsCJK(str) {
		// Fast path: pure ASCII text
		return t.ascii.TokenizeForIndex(str)
	}

	runs := splitIntoRuns(str)

	uniqueTokens := make(map[string]struct{})

	for _, run := range runs {
		var tokens []string
		if run.isCJK {
			tokens = t.cjk.TokenizeForIndex(run.text)
		} else {
			tokens = t.ascii.TokenizeForIndex(run.text)
		}
		for _, token := range tokens {
			uniqueTokens[token] = struct{}{}
		}
	}

	if len(uniqueTokens) == 0 {
		return []string{}
	}

	result := make([]string, 0, len(uniqueTokens))
	for token := range uniqueTokens {
		result = append(result, token)
	}

	sort.Strings(result)
	return result
}

// TokenizeForSearch tokenizes text for searching, handling mixed ASCII and CJK content.
// It splits the text into runs and delegates to the appropriate tokenizer for each.
func (t *MixedTokenizer) TokenizeForSearch(s string, exactMatching bool) ([]string, []string) {
	// See TokenizeForIndex: strip NUL/C0 controls so a NUL cannot fragment a CJK
	// run before CJKTokenizer normalizes it.
	s = normalizeForSegmentation(s)

	if !containsCJK(s) {
		// Fast path: pure ASCII text
		return t.ascii.TokenizeForSearch(s, exactMatching)
	}

	runs := splitIntoRuns(s)

	exists := make(map[string]struct{})
	var result []string
	var wildcards []string

	for _, run := range runs {
		var tokens []string
		var wc []string
		if run.isCJK {
			tokens, wc = t.cjk.TokenizeForSearch(run.text, exactMatching)
		} else {
			tokens, wc = t.ascii.TokenizeForSearch(run.text, exactMatching)
		}
		for _, token := range tokens {
			if _, ok := exists[token]; !ok {
				exists[token] = struct{}{}
				result = append(result, token)
			}
		}
		wildcards = append(wildcards, wc...)
	}

	return result, wildcards
}
