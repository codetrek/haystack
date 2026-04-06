package tokenizer

// MixedTokenizer dispatches tokenization to the appropriate implementation
// based on the input text. If the text contains CJK characters, it uses
// CJKTokenizer (which also handles embedded ASCII). If the text is pure ASCII,
// it uses ASCIITokenizer directly — avoiding any gse dictionary overhead.
type MixedTokenizer struct {
	ascii ASCIITokenizer
	cjk   CJKTokenizer
}

// TokenizeForIndex tokenizes a string for indexing.
// It detects whether the input contains CJK characters and dispatches accordingly.
func (t *MixedTokenizer) TokenizeForIndex(str string) []string {
	if containsCJK(str) {
		return t.cjk.TokenizeForIndex(str)
	}
	return t.ascii.TokenizeForIndex(str)
}

// TokenizeForSearch tokenizes a string for searching.
// It detects whether the input contains CJK characters and dispatches accordingly.
func (t *MixedTokenizer) TokenizeForSearch(s string, exactMatching bool) ([]string, []string) {
	if containsCJK(s) {
		return t.cjk.TokenizeForSearch(s, exactMatching)
	}
	return t.ascii.TokenizeForSearch(s, exactMatching)
}
