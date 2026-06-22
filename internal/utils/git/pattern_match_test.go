package gitutils

import (
	"testing"

	gitignore "github.com/sabhiram/go-gitignore"
)

// matcherSet is a fast path that must be byte-for-byte equivalent to compiling
// each pattern with the go-gitignore library. This test pins that equivalence
// across a broad pattern × path matrix: for every pattern, a single-pattern
// matcherSet must agree with a single-pattern library compile on every path.
func TestMatcherSetMatchesLibrary(t *testing.T) {
	patterns := []string{
		// Class A — plain literals
		"node_modules", ".DS_Store", "build", "vendor", "important.log",
		"a.b.c", "Makefile", "log", "dist",
		// Class B — *suffix
		"*.log", "*.tmp", "*.min.js", "*o", "*.gz", "*.a",
		// Class C — dir/
		"node_modules/", "build/", "dist/", "vendor/", "generated/",
		// Class D — anchored
		"/coverage", "/.env", "/dist", "/src/", "/build/", "/main.go",
		// Complex — must route to the library fallback (equivalence is then
		// trivially preserved, but we still verify it).
		"**/__pycache__/", "foo*bar", "a?b", "*.l[oa]g", "doc/*.html",
		"src/main.go", "*", "**", "/a/b", "a/b/", "node_*", "foo/**",
		"build*", "*.{js,ts}", "a+b",
	}
	paths := []string{
		"/node_modules", "/node_modules/", "/a/node_modules", "/a/node_modules/x",
		"/src/node_modules/y", "/node_modules.bak", "/node_x",
		"/app.log", "/a/b/app.log", "/important.log", "/x.log/", "/.gz", "/a.gz",
		"/.DS_Store", "/a/.DS_Store/", "/build", "/build/", "/a/build/x", "/builds",
		"/coverage", "/coverage/", "/a/coverage", "/.env", "/.environment",
		"/src/", "/src", "/src/main.go", "/dist", "/dist/", "/a/dist", "/distinct",
		"/x.min.js", "/a/x.min.js", "/foo", "/foobar", "/foozbar", "/a/b/c",
		"/Makefile", "/__pycache__/", "/a/__pycache__/", "/azb", "/a/b/",
		"/main.go", "/a/main.go", "/doc/git.html", "/generated/", "/vendor",
		"/foo.a", "/lib.a", "/a+b", "/log", "/catalog",
		// adversarial: prefix confusion, short segments, anchored dirs, dotfiles
		"/a", "/o", "/x.o", "/buildx", "/buildx/", "/.env/", "/.environments",
		"/a.b.cd", "/xa.b.c", "/a.b.c", "/node_modulesx", "/x/node_modules",
		"/coverages", "/src/sub/", "/main.gox", "/.DS_Store/x", "/build/sub/deep",
	}

	for _, p := range patterns {
		fast := compileMatcherSet([]string{p})
		lib := gitignore.CompileIgnoreLines(p)
		for _, f := range paths {
			got := fast.matches(f)
			want := lib.MatchesPath(f)
			if got != want {
				t.Errorf("pattern %q path %q: fast=%v lib=%v", p, f, got, want)
			}
		}
	}
}

// TestMatcherSetEngagesFastPath guards that the shapes we intend to accelerate
// actually take the fast path (no library fallback), and that genuinely complex
// shapes are routed to the fallback. Without this, a too-conservative classifier
// would silently fall back to the library and the optimization would be inert.
func TestMatcherSetEngagesFastPath(t *testing.T) {
	fast := []string{
		"node_modules", ".DS_Store", "important.log", "a.b.c",
		"*.log", "*.min.js", "*o",
		"build/", "node_modules/",
		"/coverage", "/.env", "/src/", "/build/",
	}
	for _, p := range fast {
		if m := compileMatcherSet([]string{p}); m.fallback != nil {
			t.Errorf("pattern %q expected fast path, got library fallback", p)
		}
	}

	complex := []string{
		"**/__pycache__/", "foo*bar", "a?b", "*.l[oa]g", "doc/*.html",
		"src/main.go", "*", "**", "/a/b", "a/b/", "node_*", "a+b",
	}
	for _, p := range complex {
		if m := compileMatcherSet([]string{p}); m.fallback == nil {
			t.Errorf("pattern %q expected library fallback, got fast path", p)
		}
	}
}

// TestMatcherSetMixedEquivalentToLibrary verifies that a realistic .gitignore
// mixing fast-path and fallback (complex) patterns matches exactly as the
// library compiling all of them together — i.e. the OR over fast classes plus
// the library fallback reproduces the library's whole-file result.
func TestMatcherSetMixedEquivalentToLibrary(t *testing.T) {
	mixed := []string{
		"node_modules/", "*.log", "build/", "/coverage", ".DS_Store",
		"**/__pycache__/", "doc/*.html", "*.min.js", "/dist", "vendor",
		"node_*", "*.a",
	}
	fast := compileMatcherSet(mixed)
	lib := gitignore.CompileIgnoreLines(mixed...)

	paths := []string{
		"/node_modules/", "/a/node_modules/x", "/app.log", "/a/b/c.log",
		"/build/", "/a/build/y", "/coverage", "/coverage/file", "/.DS_Store",
		"/a/__pycache__/", "/__pycache__/x", "/doc/git.html", "/x.min.js",
		"/dist", "/dist/z", "/a/dist", "/vendor", "/vendor/lib", "/node_x",
		"/lib.a", "/main.go", "/a/b/c", "/src/app.js", "/readme.md",
	}
	for _, f := range paths {
		if got, want := fast.matches(f), lib.MatchesPath(f); got != want {
			t.Errorf("mixed set, path %q: fast=%v lib=%v", f, got, want)
		}
	}
}
