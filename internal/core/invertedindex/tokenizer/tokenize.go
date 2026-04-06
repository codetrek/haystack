package tokenizer

// TokenizeForIndex is used to tokenize a string for indexing.
// It collects words from the string, splits them into smaller parts if necessary,
// and returns a sorted list of unique words.
//
// This is a backward-compatible package-level wrapper that delegates to DefaultTokenizer.
func TokenizeForIndex(str string) []string {
	return DefaultTokenizer.TokenizeForIndex(str)
}

// TokenizeForSearch is used to tokenize a string for searching.
// It collects words from the string, splits them into smaller parts if necessary,
// and returns a sorted list of unique words. It also handles exact matching.
//
// This is a backward-compatible package-level wrapper that delegates to DefaultTokenizer.
func TokenizeForSearch(s string, exactMatching bool) ([]string, []string) {
	return DefaultTokenizer.TokenizeForSearch(s, exactMatching)
}
