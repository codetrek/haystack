package tokenizer

import (
	"regexp"
	"sort"
	"strings"
)

var reASCII = regexp.MustCompile(`[a-zA-Z0-9][a-zA-Z0-9_]{1,78}[a-zA-Z0-9]|([0-9]{1,8}\.|[a-zA-Z0-9]{1,2}\.)+([0-9]{1,8}|[a-zA-Z0-9]{1,2})`)

// ASCIITokenizer handles tokenization of ASCII text (Latin alphabet, digits,
// and common programming identifiers). It splits camelCase, snake_case, and
// kebab-case identifiers and enforces a minimum token length of 3 characters.
type ASCIITokenizer struct{}

// TokenizeForIndex tokenizes a string for indexing.
// It collects words from the string, splits them into smaller parts if necessary,
// and returns a sorted list of unique words.
func (t *ASCIITokenizer) TokenizeForIndex(str string) []string {
	words := reASCII.FindAllString(str, -1)

	uniqueWords := make(map[string]struct{}, len(words))
	pushWord := func(word string) {
		if len(word) < 3 || len(word) > 80 {
			return
		}
		uniqueWords[strings.ToLower(word)] = struct{}{}
	}

	var splitBuf []string
	for _, word := range words {
		// Reuse splitBuf across words: the sub-tokens are slices of word and are
		// copied into uniqueWords by pushWord, so they need not outlive this loop.
		splitBuf = camelSnakeSplitInto(splitBuf[:0], word)

		for _, word := range splitBuf {
			pushWord(word)
		}
	}

	result := make([]string, 0, len(uniqueWords))
	for word := range uniqueWords {
		result = append(result, word)
	}
	if len(result) == 0 {
		return result
	}

	sort.Strings(result)

	// Remove the words that have prefix duplicate.
	deduped := make([]string, 0, len(result))
	for i := 0; i < len(result)-1; i++ {
		if strings.HasPrefix(result[i+1], result[i]) {
			continue
		}
		deduped = append(deduped, result[i])
	}

	return append(deduped, result[len(result)-1]) // Append the last word
}

// TokenizeForSearch tokenizes a string for searching.
// It collects words from the string, splits them into smaller parts if necessary,
// and returns a sorted list of unique words. It also handles exact matching.
func (t *ASCIITokenizer) TokenizeForSearch(s string, exactMatching bool) ([]string, []string) {
	exists := map[string]struct{}{}
	result := []string{}
	wildcards := []string{}

	var push = func(word string) {
		if _, ok := exists[word]; ok {
			return
		}
		exists[word] = struct{}{}
		result = append(result, word)
	}

	if exactMatching {
		for _, w := range reASCII.FindAllString(s, -1) {
			push(w)
		}
		return result, nil
	}

	poses := reASCII.FindAllStringIndex(s, -1)
	for _, pos := range poses {
		start := pos[0]
		end := pos[1]

		// For the pattern "*abc-def", we'll skip "abc" since "abc" may not be a keyword,
		// and we want to match "def" instead.
		if start > 0 && (s[start-1] == '*') {
			r := CamelSnakeSplit(s[start:end])
			wildcards = append(wildcards, r[0])
			if len(r) > 1 {
				push(r[1])
			}
			continue
		}
		push(s[start:end])
	}

	return result, wildcards
}
