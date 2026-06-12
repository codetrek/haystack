// Package tokenizer provides text tokenization for indexing and search,
// supporting ASCII, CJK, camelCase/snake_case decomposition, and stopword filtering.
package tokenizer

// Tokenizer defines the interface for text tokenization used by the inverted index.
// Different implementations handle different character sets (ASCII, CJK, etc.).
type Tokenizer interface {
	// TokenizeForIndex tokenizes a string for indexing.
	// It returns a sorted list of unique, normalized tokens suitable for storage
	// in an inverted index.
	TokenizeForIndex(str string) []string

	// TokenizeForSearch tokenizes a string for searching.
	// It returns the search tokens and any wildcard tokens extracted from the input.
	// When exactMatching is true, tokens are kept as-is without wildcard processing.
	TokenizeForSearch(s string, exactMatching bool) (tokens []string, wildcards []string)
}

// DefaultTokenizer is the package-level tokenizer instance used by the
// backward-compatible package-level functions (TokenizeForIndex, TokenizeForSearch).
var DefaultTokenizer Tokenizer = &MixedTokenizer{}
