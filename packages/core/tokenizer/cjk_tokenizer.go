package tokenizer

import (
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/codetrek/haystack/packages/core/tokenizer/fstcjk"
)

// CJKTokenizer handles tokenization of CJK (Chinese, Japanese, Korean) text.
//
// Segmentation is byte-identical to go-ego/gse v1.0.2 Cut(text, true): the
// dictionary is a prebuilt vellum FST (word -> freq) embedded into the binary
// via //go:embed and loaded once on first use. This avoids gse's
// parse-and-build-at-startup of an in-RAM cedar trie while keeping token
// boundaries unchanged (no reindex). The FST segmenter is lazily initialized on
// first use via sync.Once to avoid the load cost when CJK tokenization isn't
// needed.
type CJKTokenizer struct {
	once   sync.Once
	seg    *fstcjk.Segmenter
	loaded bool
}

// ensureLoaded lazily initializes the embedded FST segmenter on first use.
//
// fstcjk.Open() is self-contained (the dictionary is embedded via //go:embed),
// so unlike the previous gse LoadDict() path it does not resolve a
// runtime.Caller build-machine path. If the embedded FST ever fails to load
// (corrupt embed), seg stays nil and segmentation degrades to "no tokens"
// rather than panicking; loaded reflects whether a usable segmenter exists.
func (t *CJKTokenizer) ensureLoaded() {
	t.once.Do(func() {
		seg, err := fstcjk.Open()
		if err != nil {
			return
		}
		t.seg = seg
		t.loaded = true
	})
}

// cut segments str via the embedded FST after stripping control bytes that
// could corrupt segmentation (see normalizeForSegmentation). It returns nil if
// the segmenter failed to load.
func (t *CJKTokenizer) cut(str string) []string {
	if t.seg == nil {
		return nil
	}
	return t.seg.Cut(normalizeForSegmentation(str))
}

// normalizeForSegmentation strips NUL (\x00) and the other C0 control bytes
// BEFORE segmentation. gse's cedar trie uses \x00 as a key-terminator sentinel,
// so a NUL embedded in text (e.g. 中\x00国) makes gse merge across it
// (["中\x00", "国"]) — a structural divergence the FST cannot reproduce. NUL and
// C0 controls never appear in legitimate indexed text, so removing them here
// guarantees byte-identical boundaries to gse on all real input while making the
// one pathological case impossible to reach.
//
// Tab (\t), line feed (\n) and carriage return (\r) are preserved: gse and the
// FST already segment them identically, and they are legitimate whitespace that
// the wrapper's TrimSpace/isPunctOrSpace post-processing drops. DEL (\x7f) and
// the rest of the C0 range likewise segment identically, but they are not
// meaningful text, so we strip them too for cleanliness.
func normalizeForSegmentation(s string) string {
	// Fast path: scan for any byte we would strip; if none, return s unchanged
	// (no allocation).
	hasControl := false
	for i := 0; i < len(s); i++ {
		if isStrippedControl(s[i]) {
			hasControl = true
			break
		}
	}
	if !hasControl {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isStrippedControl(c) {
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// isStrippedControl reports whether byte c is a control byte we remove before
// segmentation: NUL, the C0 control range (\x00–\x1f) except tab/LF/CR, and DEL
// (\x7f). These are single-byte values that never form part of a multi-byte
// UTF-8 sequence (UTF-8 continuation/lead bytes are all >= 0x80), so removing
// them cannot corrupt surrounding runes.
func isStrippedControl(c byte) bool {
	if c == '\t' || c == '\n' || c == '\r' {
		return false
	}
	return c < 0x20 || c == 0x7f
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
// It segments the text via the embedded FST (byte-identical to gse
// Cut(text, true)), collects unique tokens (min 1 rune), filters out pure
// whitespace/punctuation and CJK stop words, and returns sorted results.
func (t *CJKTokenizer) TokenizeForIndex(str string) []string {
	if strings.TrimSpace(str) == "" {
		return []string{}
	}

	t.ensureLoaded()

	segments := t.cut(str)

	uniqueTokens := make(map[string]struct{}, len(segments))
	for _, seg := range segments {
		token := strings.TrimSpace(seg)
		if len(token) == 0 {
			continue
		}
		if isPunctOrSpace(token) {
			continue
		}
		if isStopWord(token) {
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
// It segments the text via the embedded FST (byte-identical to gse
// Cut(text, true)), filters CJK stop words, and returns tokens with empty wildcards.
func (t *CJKTokenizer) TokenizeForSearch(s string, exactMatching bool) ([]string, []string) {
	if strings.TrimSpace(s) == "" {
		return []string{}, nil
	}

	t.ensureLoaded()

	segments := t.cut(s)

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
		if isStopWord(token) {
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
