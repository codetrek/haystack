package tokenizer

import "unicode"

// isCJK reports whether the rune belongs to a CJK character set:
// Chinese (Han), Japanese (Hiragana, Katakana), or Korean (Hangul).
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hangul, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hiragana, r)
}

// containsCJK reports whether the string contains any CJK characters
// (Chinese, Japanese, or Korean).
func containsCJK(s string) bool {
	for _, r := range s {
		if isCJK(r) {
			return true
		}
	}
	return false
}
