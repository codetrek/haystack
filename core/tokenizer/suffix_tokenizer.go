package tokenizer

import (
	"regexp"
	"sort"
	"strings"
)

// minASCIISuffixLen / minCJKSuffixLen are the shortest suffixes (in runes)
// emitted per script. Non-CJK (Latin/digits/…) words match the main index's
// min-3 rule; single CJK characters are meaningful words, so CJK suffixes go
// down to one rune.
const (
	minASCIISuffixLen = 3
	minCJKSuffixLen   = 1
)

// maxBaseChunkRunes bounds suffix expansion. A base token longer than this is
// split into consecutive fixed-size chunks, each expanded independently, so a
// length-L token costs O(L) instead of O(L^2) (a minified blob or base64 string
// would otherwise blow up memory). A substring that straddles a chunk boundary
// is not retrievable — an accepted trade-off for pathologically long tokens.
const maxBaseChunkRunes = 64

// reSuffixWord matches maximal runs of Unicode letters/numbers. Everything else
// — '_', '.', '-', '/', whitespace, punctuation and '*' — is a separator. This
// deliberately differs from the other tokenizers: camelCase is NOT split (suffix
// expansion already yields every internal substring, so a split is redundant)
// and snake/kebab/path separators (including '_') simply cut, like any other
// punctuation. Accented Latin, Cyrillic, Greek, … are kept (they are letters).
var reSuffixWord = regexp.MustCompile(`[\p{L}\p{N}]+`)

// SuffixTokenizer is a self-contained tokenizer that, for indexing, expands each
// base token into all of its rune-suffixes. This lets a prefix-only inverted
// index answer "ends with" / "contains" queries: a query X prefix-matches any
// stored suffix that begins with X, i.e. any base token that contains X.
//
// It does its OWN segmentation (Unicode letter/number words + CJK FST segments)
// rather than delegating to MixedTokenizer, so that the switches below take
// effect on real input. Crucially, TokenizeForIndex and TokenizeForSearch share
// the same base-token segmentation AND honour the same DropShortTokens policy,
// so a query never emits a keyword the index could not have stored.
type SuffixTokenizer struct {
	// DropShortTokens controls a base token whose full length is below the
	// minimum suffix length for its script (a non-CJK token of 1–2 runes). Suffix
	// indexes are commonly built over short text, so the default (false)
	// preserves such a token verbatim (at index) and emits it (at search); set
	// true to discard it in BOTH paths.
	DropShortTokens bool

	// FilterStopwords, when true, removes CJK stopwords from the base tokens in
	// both paths. The default (false) keeps them — a substring index usually
	// wants to match inside stopwords too.
	FilterStopwords bool

	// cjk provides the embedded FST segmenter (lazily loaded). Only its raw
	// segmentation (cut) is used; stopword/punctuation policy is applied here so
	// the switches above are honoured.
	cjk CJKTokenizer
}

var _ Tokenizer = (*SuffixTokenizer)(nil)

// baseTokens segments str into lowercased base tokens — Unicode letter/number
// words and CJK FST segments (punctuation dropped, stopwords dropped only when
// FilterStopwords is set). It performs NO suffix expansion and NO deduplication;
// callers handle those. Shared by the index path (expanded) and the search path
// (verbatim prefixes), which is what keeps the two symmetric.
func (t *SuffixTokenizer) baseTokens(str string) []string {
	str = normalizeForSegmentation(str)
	if str == "" {
		return nil
	}

	var out []string
	for _, run := range splitIntoRuns(str) {
		if run.isCJK {
			out = t.appendCJKBase(out, run.text)
		} else {
			out = appendWordBase(out, run.text)
		}
	}
	// Bound token length in BOTH paths: a token longer than maxBaseChunkRunes is
	// split into fixed-size chunks, so index expansion stays O(L) and the search
	// path stays symmetric (a long query is chunked exactly as the index chunked
	// it, so its chunks can still prefix-match the stored chunk suffixes).
	return chunkTokens(out)
}

// chunkTokens splits any token longer than maxBaseChunkRunes into consecutive
// fixed-size chunks. A substring straddling a chunk boundary becomes
// unretrievable — an accepted trade-off for pathologically long tokens. It is a
// no-op (returns the input slice) when nothing is over-length.
func chunkTokens(toks []string) []string {
	needs := false
	for _, tok := range toks {
		if len([]rune(tok)) > maxBaseChunkRunes {
			needs = true
			break
		}
	}
	if !needs {
		return toks
	}
	out := make([]string, 0, len(toks))
	for _, tok := range toks {
		runes := []rune(tok)
		if len(runes) <= maxBaseChunkRunes {
			out = append(out, tok)
			continue
		}
		for start := 0; start < len(runes); start += maxBaseChunkRunes {
			end := start + maxBaseChunkRunes
			if end > len(runes) {
				end = len(runes)
			}
			out = append(out, string(runes[start:end]))
		}
	}
	return out
}

// appendCJKBase appends the FST segments of a CJK run (lowercased, punctuation
// and — when enabled — stopwords removed).
func (t *SuffixTokenizer) appendCJKBase(dst []string, run string) []string {
	t.cjk.ensureLoaded()
	for _, seg := range t.cjk.cut(run) {
		seg = strings.TrimSpace(seg)
		if seg == "" || isPunctOrSpace(seg) {
			continue
		}
		if t.FilterStopwords && isStopWord(seg) {
			continue
		}
		dst = append(dst, strings.ToLower(seg))
	}
	return dst
}

// appendWordBase appends the letter/number words of a non-CJK run, lowercased,
// keeping words of every length (camelCase is not split; '_' and punctuation
// are separators).
func appendWordBase(dst []string, run string) []string {
	for _, w := range reSuffixWord.FindAllString(run, -1) {
		dst = append(dst, strings.ToLower(w))
	}
	return dst
}

// belowMin reports whether tok is shorter than the minimum suffix length for its
// script — the tokens that index/search treat specially (keep verbatim, or drop
// under DropShortTokens).
func belowMin(tok string) bool {
	minLen := minASCIISuffixLen
	if containsCJK(tok) {
		minLen = minCJKSuffixLen
	}
	return len([]rune(tok)) < minLen
}

// TokenizeForIndex expands every base token into its rune-suffixes, returning a
// sorted, deduplicated list.
func (t *SuffixTokenizer) TokenizeForIndex(str string) []string {
	base := t.baseTokens(str)

	uniq := make(map[string]struct{})
	for _, tok := range base {
		appendSuffixes(uniq, tok, t.DropShortTokens)
	}

	result := make([]string, 0, len(uniq))
	for s := range uniq {
		result = append(result, s)
	}
	sort.Strings(result)
	return result
}

// appendSuffixes adds the rune-suffixes (length >= the script minimum) of tok to
// uniq. tok is already length-bounded to maxBaseChunkRunes by baseTokens. A token
// shorter than the minimum yields no qualifying suffix: unless dropShort is set
// it is preserved verbatim (suffix indexes are commonly built over short text),
// otherwise it is discarded. An empty token is always discarded.
func appendSuffixes(uniq map[string]struct{}, tok string, dropShort bool) {
	minLen := minASCIISuffixLen
	if containsCJK(tok) {
		minLen = minCJKSuffixLen
	}
	runes := []rune(tok)
	n := len(runes)
	if n == 0 {
		return
	}
	if n < minLen {
		if !dropShort {
			uniq[tok] = struct{}{}
		}
		return
	}
	for k := 0; n-k >= minLen; k++ {
		uniq[string(runes[k:])] = struct{}{}
	}
}

// TokenizeForSearch returns the query's base tokens, deduplicated in first-seen
// order, with no suffix expansion: the query is matched as a prefix against the
// suffix index. The token set is symmetric with TokenizeForIndex — same base
// segmentation, and DropShortTokens drops the same sub-minimum tokens — so a
// query never emits a keyword the index could not have stored.
//
// '*' is a separator and is therefore dropped from keywords. exactMatching does
// not change the keyword set (the index is a contains/candidate index; '*' is a
// wildcard to discard when false and a literal handled by the engine's regex
// when true). wildcards is always nil: for a suffix index "*foo"/"foo*"/"foo"
// all reduce to the prefix query "foo", so there is no separate wildcard channel.
func (t *SuffixTokenizer) TokenizeForSearch(s string, exactMatching bool) ([]string, []string) {
	base := t.baseTokens(s)

	seen := make(map[string]struct{}, len(base))
	out := []string{}
	for _, tok := range base {
		// Symmetry with TokenizeForIndex: a sub-minimum token the index would
		// have dropped must not be emitted as a (structurally dead) keyword.
		if t.DropShortTokens && belowMin(tok) {
			continue
		}
		if _, ok := seen[tok]; ok {
			continue
		}
		seen[tok] = struct{}{}
		out = append(out, tok)
	}
	return out, nil
}
