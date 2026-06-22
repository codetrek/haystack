package tokenizer

// findASCIITokensInto appends to dst the tokens of s that reASCII would match,
// using a hand-written byte scanner instead of the regexp engine (which
// dominates ASCII tokenization CPU). It reproduces reASCII exactly:
//
//	ALT1: [a-zA-Z0-9][a-zA-Z0-9_]{1,78}[a-zA-Z0-9]   (alnum token, length 3..80)
//	ALT2: ([0-9]{1,8}\.|[a-zA-Z0-9]{1,2}\.)+([0-9]{1,8}|[a-zA-Z0-9]{1,2})
//
// At each position ALT1 is tried first (leftmost-first); ALT2 only when ALT1
// fails. Matches are non-overlapping, left to right. Equivalence to reASCII is
// pinned by an exhaustive differential test.
func findASCIITokensInto(dst []string, s string) []string {
	p := 0
	for p < len(s) {
		if e := scanAlt1(s, p); e > p {
			dst = append(dst, s[p:e])
			p = e
			continue
		}
		if e := scanAlt2(s, p); e > p {
			dst = append(dst, s[p:e])
			p = e
			continue
		}
		p++
	}
	return dst
}

// findASCIITokenSpansInto is findASCIITokensInto but appends [start,end) byte
// spans instead of substrings, for callers that need token positions (the
// wildcard search path).
func findASCIITokenSpansInto(dst [][2]int, s string) [][2]int {
	p := 0
	for p < len(s) {
		if e := scanAlt1(s, p); e > p {
			dst = append(dst, [2]int{p, e})
			p = e
			continue
		}
		if e := scanAlt2(s, p); e > p {
			dst = append(dst, [2]int{p, e})
			p = e
			continue
		}
		p++
	}
	return dst
}

// scanAlt1 returns the end index of an ALT1 match starting at p, or p if none.
func scanAlt1(s string, p int) int {
	if p >= len(s) || !isAlnum(s[p]) {
		return p
	}
	// Maximal run of [a-zA-Z0-9_] from p.
	e := p
	for e < len(s) && isAlnumU(s[e]) {
		e++
	}
	hi := e - p
	if hi > 80 {
		hi = 80
	}
	// Greedy: longest L in [3, hi] whose last char is alnum (not '_').
	for L := hi; L >= 3; L-- {
		if isAlnum(s[p+L-1]) {
			return p + L
		}
	}
	return p
}

// scanAlt2 returns the end index of an ALT2 (dotted) match starting at p, or p
// if none.
func scanAlt2(s string, p int) int {
	q := p
	lastSegStart := -1
	for {
		segLen := scanSegment(s, q)
		if segLen == 0 {
			break
		}
		lastSegStart = q
		q += segLen
	}
	if lastSegStart < 0 {
		return p // ALT2's "+" needs at least one segment
	}
	if fl := scanFinal(s, q); fl > 0 {
		return q + fl
	}
	// The "+" was greedy but no final follows the last segment; give it back and
	// match the final against the last segment's value. This is only valid when a
	// segment still remains before it (the "+" requires >= 1 segment), so a lone
	// "X." with no final does not match.
	if lastSegStart > p {
		if fl := scanFinal(s, lastSegStart); fl > 0 {
			return lastSegStart + fl
		}
	}
	return p
}

// scanSegment matches one "X." segment at q — [0-9]{1,8}\. (tried first) or
// [a-zA-Z0-9]{1,2}\. — and returns its byte length, or 0.
func scanSegment(s string, q int) int {
	// [0-9]{1,8}\. : the digit run (<=8) must be immediately followed by '.'.
	if q < len(s) && isDigit(s[q]) {
		k := 0
		for q+k < len(s) && k < 8 && isDigit(s[q+k]) {
			k++
		}
		if q+k < len(s) && s[q+k] == '.' {
			return k + 1
		}
	}
	// [a-zA-Z0-9]{1,2}\. : greedy 2 then 1 alnum, followed by '.'.
	if q < len(s) && isAlnum(s[q]) {
		if q+2 < len(s) && isAlnum(s[q+1]) && s[q+2] == '.' {
			return 3
		}
		if q+1 < len(s) && s[q+1] == '.' {
			return 2
		}
	}
	return 0
}

// scanFinal matches the ALT2 final — [0-9]{1,8} (tried first) or
// [a-zA-Z0-9]{1,2} — at q and returns its byte length, or 0.
func scanFinal(s string, q int) int {
	if q < len(s) && isDigit(s[q]) {
		k := 0
		for q+k < len(s) && k < 8 && isDigit(s[q+k]) {
			k++
		}
		return k
	}
	if q < len(s) && isAlnum(s[q]) {
		if q+1 < len(s) && isAlnum(s[q+1]) {
			return 2
		}
		return 1
	}
	return 0
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
func isAlnumU(c byte) bool { return isAlnum(c) || c == '_' }
