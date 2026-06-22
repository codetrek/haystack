package tokenizer

import (
	"math/rand"
	"strings"
	"testing"
)

// equalTokens compares two token slices, treating nil and empty as equal.
func equalTokens(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestFindASCIITokensMatchesRegexExhaustive pins the hand scanner to reASCII
// over every string up to length 7 from an alphabet that exercises each
// character class reASCII distinguishes (digit, letter, underscore, dot, and a
// separator).
func TestFindASCIITokensMatchesRegexExhaustive(t *testing.T) {
	alphabet := []byte{'0', 'a', '_', '.', '-'}
	buf := make([]byte, 0, 7)
	var rec func(depth, maxLen int)
	checked := 0
	rec = func(depth, maxLen int) {
		s := string(buf)
		got := findASCIITokensInto(nil, s)
		want := reASCII.FindAllString(s, -1)
		if !equalTokens(got, want) {
			t.Fatalf("scanner != regex for %q:\n got=%q\nwant=%q", s, got, want)
		}
		checked++
		if depth == maxLen {
			return
		}
		for _, c := range alphabet {
			buf = append(buf, c)
			rec(depth+1, maxLen)
			buf = buf[:len(buf)-1]
		}
	}
	rec(0, 7)
	t.Logf("exhaustive: %d strings checked", checked)
}

// TestFindASCIITokensMatchesRegexRandom hits the length bounds ({1,8} digit
// runs, {1,2} alnum runs, 3..80 token length) with longer random strings over a
// richer alphabet, plus targeted boundary strings.
func TestFindASCIITokensMatchesRegexRandom(t *testing.T) {
	check := func(s string) {
		got := findASCIITokensInto(nil, s)
		want := reASCII.FindAllString(s, -1)
		if !equalTokens(got, want) {
			t.Fatalf("scanner != regex for %q:\n got=%q\nwant=%q", s, got, want)
		}
	}

	// Targeted boundary cases.
	for _, s := range []string{
		strings.Repeat("a", 79), strings.Repeat("a", 80), strings.Repeat("a", 81),
		strings.Repeat("a", 200),
		strings.Repeat("0", 7), strings.Repeat("0", 8), strings.Repeat("0", 9),
		"1234567.8", "12345678.9", "123456789.0",
		"a." + strings.Repeat("0", 8) + ".b", "a." + strings.Repeat("0", 9),
		"x.1a.", "1.2.", "a.b.", ".1.2", "ab.", "_._._",
		strings.Repeat("a_", 50) + "b", strings.Repeat("_", 90),
		"a" + strings.Repeat("_", 80) + "b",
	} {
		check(s)
	}

	// Random strings over a rich alphabet, fixed seed for determinism.
	alphabet := []byte("abXY09._-/ ")
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 500000; i++ {
		n := 1 + r.Intn(24)
		b := make([]byte, n)
		for j := range b {
			b[j] = alphabet[r.Intn(len(alphabet))]
		}
		check(string(b))
	}

	// Real code / mixed samples.
	for _, s := range []string{asciiSample, mixedSample, cjkSample} {
		check(s)
	}
}

// TestFindASCIITokenSpansMatchesRegex pins the span variant (used by the
// wildcard search path) to reASCII.FindAllStringIndex.
func TestFindASCIITokenSpansMatchesRegex(t *testing.T) {
	check := func(s string) {
		got := findASCIITokenSpansInto(nil, s)
		want := reASCII.FindAllStringIndex(s, -1)
		if len(got) != len(want) {
			t.Fatalf("span count differs for %q: got=%v want=%v", s, got, want)
		}
		for i := range got {
			if got[i][0] != want[i][0] || got[i][1] != want[i][1] {
				t.Fatalf("span %d differs for %q: got=%v want=%v", i, s, got[i], want[i])
			}
		}
	}
	for _, s := range []string{
		"", "abc", "a.b.c", "http.Request", "foo_bar baz-qux",
		"*abc-def", "1.2.3 v4.5", "no_match_xx end", asciiSample, mixedSample,
	} {
		check(s)
	}
	alphabet := []byte("abXY09._-* ")
	r := rand.New(rand.NewSource(2))
	for i := 0; i < 100000; i++ {
		n := 1 + r.Intn(20)
		b := make([]byte, n)
		for j := range b {
			b[j] = alphabet[r.Intn(len(alphabet))]
		}
		check(string(b))
	}
}
