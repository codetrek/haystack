package tokenizer

import (
	"regexp"
	"sort"
	"strings"
)

var re = regexp.MustCompile(`[a-zA-Z0-9][a-zA-Z0-9_]{1,78}[a-zA-Z0-9]|([0-9]{1,8}\.|[a-zA-Z0-9]{1,2}\.)+([0-9]{1,8}|[a-zA-Z0-9]{1,2})`)

func collectWords(s string) []string {
	result := make([]string, 0, 10)
	for _, w := range re.FindAllString(s, -1) {
		w = strings.Trim(w, ".-_")
		if len(w) < 3 || len(w) > 80 {
			continue
		}
		result = append(result, w)
	}
	return result
}

// TokenizeForIndex is used to tokenize a string for indexing.
// It collects words from the string, splits them into smaller parts if necessary,
// and returns a sorted list of unique words.
func TokenizeForIndex(str string) []string {
	words := collectWords(str) //reIndex.FindAllString(str, -1)

	uniqueWords := make(map[string]struct{})
	pushWord := func(word string) {
		if len(word) < 3 || len(word) > 80 {
			return
		}
		uniqueWords[strings.ToLower(word)] = struct{}{}
	}

	for _, word := range words {
		camelSplited := CamelSnakeSplit(word)

		for _, word := range camelSplited {
			pushWord(word)
		}
	}

	result := make([]string, 0, len(uniqueWords))
	for word := range uniqueWords {
		result = append(result, word)
	}

	sort.Strings(result)
	return result
}

// TokenizeForSearch is used to tokenize a string for searching.
// It collects words from the string, splits them into smaller parts if necessary,
// and returns a sorted list of unique words. It also handles exact matching.
func TokenizeForSearch(s string, exactMatching bool) ([]string, []string) {
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
		for _, w := range collectWords(s) {
			push(w)
		}
		return result, nil
	}

	poses := re.FindAllStringIndex(s, -1)
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
