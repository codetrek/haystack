package gitutils

import (
	"os"
	"strings"

	gitignore "github.com/sabhiram/go-gitignore"
)

// matcherSet matches a path against a set of gitignore patterns. The common
// pattern shapes are matched directly with plain string operations instead of
// the go-gitignore library's per-pattern backtracking regexp (which dominates
// scan CPU); any pattern shape not handled here is compiled into a library
// fallback, so the overall result is identical to compiling every pattern with
// the library.
//
// The path passed to matches is the library's path form: forward slashes and a
// trailing "/" for directories (e.g. "/a/b/c.go", "/a/dir/"). The leading "/" is
// optional — the ignore loop passes it, the negation loop passes a root-relative
// path without it — so the matchers below accept both forms, matching the
// library, which treats a leading slash as transparent.
type matcherSet struct {
	literals []string          // class A: bare segment names; a segment equals the literal
	suffixes []string          // class B: "*suffix" globs; a segment ends with suffix
	dirs     []dirMatcher      // class C: "dir/" patterns
	anchored []anchoredMatcher // class D: "/name" or "/name/" patterns
	fallback *gitignore.GitIgnore
}

// dirMatcher matches a "dir/" pattern: the directory as a non-leading segment
// (mid, "/dir/") or as the leading segment of a root-relative path (head,
// "dir/").
type dirMatcher struct {
	mid  string
	head string
}

// anchoredMatcher matches a pattern anchored at the .gitignore directory,
// against the path with any single leading slash removed.
// "/name"  -> exact="name", prefix="name/" (the file itself or anything under it)
// "/name/" -> exact="",     prefix="name/" (a directory and its contents only)
type anchoredMatcher struct {
	exact  string
	prefix string
}

// compileMatcherSet classifies patterns into the fast matchers above, with a
// library fallback for shapes it does not handle. patterns must already be
// trimmed and free of blank/comment lines (NewGitIgnoreRules guarantees this).
func compileMatcherSet(patterns []string) *matcherSet {
	m := &matcherSet{}
	var complex []string
	for _, p := range patterns {
		if !m.classify(p) {
			complex = append(complex, p)
		}
	}
	if len(complex) > 0 {
		m.fallback = gitignore.CompileIgnoreLines(complex...)
	}
	return m
}

// classify routes one pattern to a fast matcher and reports true, or reports
// false to indicate the pattern must go to the library fallback.
func (m *matcherSet) classify(p string) bool {
	if p == "" {
		return true // no-op; caller already filters blanks
	}

	// Class D: anchored to the .gitignore directory (leading slash).
	if p[0] == '/' {
		body := p[1:]
		if body == "" {
			return false // "/" alone — let the library decide
		}
		if strings.HasSuffix(body, "/") {
			name := body[:len(body)-1]
			if isPlainSegment(name) {
				m.anchored = append(m.anchored, anchoredMatcher{prefix: name + "/"})
				return true
			}
			return false
		}
		if isPlainSegment(body) {
			m.anchored = append(m.anchored, anchoredMatcher{exact: body, prefix: body + "/"})
			return true
		}
		return false
	}

	// Class C: "dir/" — a directory name with a trailing slash and nothing else.
	if strings.HasSuffix(p, "/") {
		name := p[:len(p)-1]
		if isPlainSegment(name) {
			m.dirs = append(m.dirs, dirMatcher{mid: "/" + name + "/", head: name + "/"})
			return true
		}
		return false
	}

	// Any remaining internal slash is a path-shaped pattern — leave to library.
	if strings.IndexByte(p, '/') >= 0 {
		return false
	}

	// Class B: "*suffix" — a single leading star followed by a plain suffix.
	if p[0] == '*' {
		suf := p[1:]
		if suf != "" && isPlainSegment(suf) {
			m.suffixes = append(m.suffixes, suf)
			return true
		}
		return false
	}

	// Class A: a plain literal segment name.
	if isPlainSegment(p) {
		m.literals = append(m.literals, p)
		return true
	}
	return false
}

// matches reports whether any pattern in the set matches path f.
func (m *matcherSet) matches(f string) bool {
	// Mirror the library's MatchesPath normalization: convert OS separators to
	// forward slashes. On non-Windows this is a compile-time no-op. The negation
	// loop in GitIgnore.IsIgnored passes raw, OS-separated paths, so without this
	// the fast matchers would miss every match on Windows.
	if os.PathSeparator != '/' {
		f = strings.ReplaceAll(f, string(os.PathSeparator), "/")
	}

	// Class C: "dir" appears as a segment followed by "/", either after another
	// segment ("/dir/") or as the leading segment ("dir/").
	for i := range m.dirs {
		d := &m.dirs[i]
		if strings.Contains(f, d.mid) || strings.HasPrefix(f, d.head) {
			return true
		}
	}
	// Class D: anchored at the start of the path, with any single leading slash
	// removed so both "/name..." and "name..." forms match.
	if len(m.anchored) > 0 {
		g := f
		if len(g) > 0 && g[0] == '/' {
			g = g[1:]
		}
		for i := range m.anchored {
			am := &m.anchored[i]
			if am.exact != "" && g == am.exact {
				return true
			}
			if strings.HasPrefix(g, am.prefix) {
				return true
			}
		}
	}
	if (len(m.literals) > 0 || len(m.suffixes) > 0) && m.matchSegments(f) {
		return true
	}
	if m.fallback != nil && m.fallback.MatchesPath(f) {
		return true
	}
	return false
}

// matchSegments walks the '/'-separated segments of f without allocating and
// tests each against the literal and suffix matchers.
func (m *matcherSet) matchSegments(f string) bool {
	start := 0
	for i := 0; i <= len(f); i++ {
		if i == len(f) || f[i] == '/' {
			if i > start {
				seg := f[start:i]
				for _, lit := range m.literals {
					if seg == lit {
						return true
					}
				}
				for _, suf := range m.suffixes {
					if len(seg) >= len(suf) && strings.HasSuffix(seg, suf) {
						return true
					}
				}
			}
			start = i + 1
		}
	}
	return false
}

// isPlainSegment reports whether s consists only of characters the library
// treats literally and that carry no glob or regexp meaning: letters, digits,
// and '.', '_', '-'. ('.' is escaped to a literal dot by the library, so a
// plain string comparison matches the library exactly.) Anything else (glob
// metacharacters * ? [, regexp metacharacters + ( ) ^ $ | etc.) forces the
// pattern to the library fallback.
func isPlainSegment(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}
