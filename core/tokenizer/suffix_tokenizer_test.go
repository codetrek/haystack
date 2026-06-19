package tokenizer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// All expectations are exact, full outputs for real string inputs through the
// self-contained SuffixTokenizer (no inner is mocked). Index outputs are sorted
// by sort.Strings (UTF-8 byte order); assert.Equal checks them exactly.

type sfxCase struct {
	name string
	in   string
	want []string
}

func runIdx(t *testing.T, st *SuffixTokenizer, cases []sfxCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, st.TokenizeForIndex(tc.in))
		})
	}
}

// ── Segmentation: camelCase NOT split, '_'/punct/'*' are separators, non-ASCII
//    letters kept, short text preserved ──────────────────────────────────────

func TestSuffixTokenizer_IndexWords(t *testing.T) {
	runIdx(t, &SuffixTokenizer{}, []sfxCase{
		{"camelCase is not split", "UserService.java",
			[]string{"ava", "erservice", "ervice", "ice", "java", "rservice", "rvice", "serservice", "service", "userservice", "vice"}},
		{"underscore splits like punctuation", "user_service.go",
			[]string{"ervice", "go", "ice", "rvice", "service", "user", "vice"}},
		{"underscore between caps and digits", "IMG_2024", []string{"024", "2024", "img"}},
		{"accented latin kept", "café", []string{"afé", "café"}},
		{"diaeresis kept mid-word", "naïve", []string{"aïve", "naïve", "ïve"}},
		{"umlaut kept", "über", []string{"ber", "über"}},
		{"single-char parts preserved", "a-b-c", []string{"a", "b", "c"}},
		{"dotted numbers, prefix-redundant '1' dropped", "192.168.1.1", []string{"168", "192"}},
		{"version string", "v1.2.3", []string{"2", "3", "v1"}},
		{"colon separated", "key:value", []string{"alue", "key", "lue", "value"}},
		{"asterisk is a separator", "foo*bar", []string{"bar", "foo"}},
		{"leading asterisk dropped", "*foo", []string{"foo"}},
		{"filename short extension", "README.md", []string{"adme", "dme", "eadme", "md", "readme"}},
		{"empty", "", []string{}},
		{"punctuation only", "!!!", []string{}},
	})
}

func TestSuffixTokenizer_IndexCJK(t *testing.T) {
	runIdx(t, &SuffixTokenizer{}, []sfxCase{
		{"single word kept whole", "北京大学", []string{"京大学", "北京大学", "大学", "学"}},
		{"segmented into two words", "自然语言处理", []string{"处理", "然语言", "理", "自然语言", "言", "语言"}},
		{"stopwords kept by default", "我爱北京天安门", []string{"京", "北京", "天安门", "安门", "我", "爱", "门"}},
	})
}

// ── Index/Search symmetry: every search keyword is a prefix of some stored
//    suffix key (so it can retrieve), and the exact token sets are pinned ─────

func TestSuffixTokenizer_Symmetry(t *testing.T) {
	cases := []struct {
		name string
		in   string
		idx  []string
		srch []string
	}{
		{"camelCase word", "userId", []string{"erid", "rid", "serid", "userid"}, []string{"userid"}},
		{"short word kept both sides", "go", []string{"go"}, []string{"go"}},
		{"plain word", "service", []string{"ervice", "ice", "rvice", "service", "vice"}, []string{"service"}},
	}
	st := &SuffixTokenizer{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotIdx := st.TokenizeForIndex(tc.in)
			gotSrch, wild := st.TokenizeForSearch(tc.in, false)
			assert.Equal(t, tc.idx, gotIdx)
			assert.Equal(t, tc.srch, gotSrch)
			assert.Nil(t, wild)
			// Symmetry invariant: each search keyword prefixes some index key.
			idxSet := gotIdx
			for _, q := range gotSrch {
				matched := false
				for _, k := range idxSet {
					if strings.HasPrefix(k, q) {
						matched = true
						break
					}
				}
				assert.True(t, matched, "search keyword %q must prefix some index key", q)
			}
		})
	}
}

// ── Switches, paired (both states on the same input) ─────────────────────────

func TestSuffixTokenizer_DropShortTokens(t *testing.T) {
	keep := &SuffixTokenizer{}
	drop := &SuffixTokenizer{DropShortTokens: true}

	// Index: short tokens kept verbatim (default) vs dropped.
	assert.Equal(t, []string{"go", "言", "语言"}, keep.TokenizeForIndex("Go语言"))
	assert.Equal(t, []string{"言", "语言"}, drop.TokenizeForIndex("Go语言"))
	assert.Equal(t, []string{"a", "b", "c"}, keep.TokenizeForIndex("a-b-c"))
	assert.Equal(t, []string{}, drop.TokenizeForIndex("a-b-c"))

	// Search is symmetric: the short query "go" is emitted when kept, dropped
	// when the index would have dropped it (no structurally-dead keyword).
	k, _ := keep.TokenizeForSearch("Go", false)
	assert.Equal(t, []string{"go"}, k)
	d, _ := drop.TokenizeForSearch("Go", false)
	assert.Equal(t, []string{}, d)

	// A single CJK rune meets minCJKSuffixLen (1), so DropShortTokens never
	// drops it on either side.
	assert.Equal(t, []string{"学"}, drop.TokenizeForIndex("学"))
	dc, _ := drop.TokenizeForSearch("学", false)
	assert.Equal(t, []string{"学"}, dc)
}

func TestSuffixTokenizer_FilterStopwords(t *testing.T) {
	keep := &SuffixTokenizer{}
	filter := &SuffixTokenizer{FilterStopwords: true}

	assert.Equal(t, []string{"好", "很", "我", "电脑", "的", "脑"}, keep.TokenizeForIndex("我的电脑很好"))
	assert.Equal(t, []string{"好", "电脑", "脑"}, filter.TokenizeForIndex("我的电脑很好"))

	// No stopwords present → switch is a no-op.
	assert.Equal(t, []string{"京大学", "北京大学", "大学", "学"}, keep.TokenizeForIndex("北京大学"))
	assert.Equal(t, []string{"京大学", "北京大学", "大学", "学"}, filter.TokenizeForIndex("北京大学"))

	// Search honours it symmetrically.
	k, _ := keep.TokenizeForSearch("我的电脑", false)
	assert.Equal(t, []string{"我", "的", "电脑"}, k)
	f, _ := filter.TokenizeForSearch("我的电脑", false)
	assert.Equal(t, []string{"电脑"}, f)
}

// ── Search: '*' dropped, exactMatching does not change keywords ──────────────

func TestSuffixTokenizer_Search(t *testing.T) {
	st := &SuffixTokenizer{}
	cases := []sfxCase{
		{"ascii words", "hello world", []string{"hello", "world"}},
		{"asterisk dropped, split", "foo*bar", []string{"foo", "bar"}},
		{"leading asterisk", "*foobar", []string{"foobar"}},
		{"trailing asterisk", "foobar*", []string{"foobar"}},
		{"cjk phrase whole", "中华人民共和国", []string{"中华人民共和国"}},
		{"repeated deduped first-seen", "foo foo bar", []string{"foo", "bar"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, wild := st.TokenizeForSearch(tc.in, false)
			assert.Equal(t, tc.want, got)
			assert.Nil(t, wild)
		})
	}

	// exactMatching does not change the keyword set ('*' is a separator either
	// way; exactness is enforced by the engine's regex, not the keywords).
	for _, in := range []string{"foo*bar", "user_service.go", "中华人民共和国"} {
		a, _ := st.TokenizeForSearch(in, false)
		b, _ := st.TokenizeForSearch(in, true)
		assert.Equal(t, a, b, "exactMatching must not change keywords for %q", in)
	}
}

// ── Long-token chunking bounds expansion (D2) + prefix-dedup ─────────────────

func TestSuffixTokenizer_LongTokenChunking(t *testing.T) {
	st := &SuffixTokenizer{}

	// 64 'a' then 64 'b': each over-length run is chunked at maxBaseChunkRunes,
	// and (after prefix-dedup) every suffix of a chunk collapses into its single
	// longest suffix. No key spans the 'a'->'b' boundary and none exceeds one
	// chunk.
	got := st.TokenizeForIndex(strings.Repeat("a", maxBaseChunkRunes) + strings.Repeat("b", maxBaseChunkRunes))
	assert.Equal(t, []string{strings.Repeat("a", maxBaseChunkRunes), strings.Repeat("b", maxBaseChunkRunes)}, got)
}

func TestSuffixTokenizer_PrefixDedup(t *testing.T) {
	st := &SuffixTokenizer{}

	// Retrieval is a keyword PREFIX scan, so a key that is a prefix of another key
	// is redundant (the longer key covers the same queries) and is dropped.

	// Repetitive token: every suffix is a prefix of the longest → only it survives.
	assert.Equal(t, []string{"aaaaaa"}, st.TokenizeForIndex("aaaaaa"))

	// "cat" is a prefix of "category" → dropped (query "cat" still finds it via
	// "category").
	assert.Equal(t, []string{"ategory", "category", "egory", "gory", "ory", "tegory"},
		st.TokenizeForIndex("category cat"))

	// "1" is a prefix of "168" and "192" → dropped.
	assert.Equal(t, []string{"168", "192"}, st.TokenizeForIndex("1 168 192"))

	// Sanity: no key in the result is a prefix of another, and the dropped key's
	// queries still resolve (a search keyword still prefixes a kept key).
	got := st.TokenizeForIndex("ababab")
	for i := range got {
		for j := range got {
			if i != j {
				assert.False(t, strings.HasPrefix(got[j], got[i]),
					"%q must not be a prefix of %q", got[i], got[j])
			}
		}
	}
}
