package gitutils

import (
	"path/filepath"
	"testing"
)

// relUnderBase is a hot-path fast version of filepath.Rel for the case where
// base is an ancestor of absPath. This test pins it to filepath.Rel's output
// for that case, and verifies it correctly declines (ok=false) when absPath is
// not under base so the caller falls back to filepath.Rel.
func TestRelUnderBaseMatchesFilepathRel(t *testing.T) {
	// Pairs where base is an ancestor of (or equal to) absPath: relUnderBase
	// must return ok=true and the exact filepath.Rel result.
	ancestor := [][2]string{
		{"/repo", "/repo"},                       // equal -> "."
		{"/repo", "/repo/main.go"},               // one level
		{"/repo", "/repo/a/b/c.go"},              // multi level
		{"/repo/a/b", "/repo/a/b/c.go"},          // deeper base
		{"/", "/foo"},                            // filesystem root (base ends in sep)
		{"/", "/a/b"},                            // root, nested
		{"/repo/sub", "/repo/sub/.gitignore"},    // dotfile
		{"/r", "/r/x"},                           // short base
	}
	for _, p := range ancestor {
		base, abs := p[0], p[1]
		got, ok := relUnderBase(abs, base)
		if !ok {
			t.Errorf("relUnderBase(%q, %q): ok=false, want true", abs, base)
			continue
		}
		want, err := filepath.Rel(base, abs)
		if err != nil {
			t.Fatalf("filepath.Rel(%q, %q) unexpected error: %v", base, abs, err)
		}
		if got != want {
			t.Errorf("relUnderBase(%q, %q) = %q, want %q (filepath.Rel)", abs, base, got, want)
		}
	}

	// Pairs where absPath is NOT under base: relUnderBase must decline so the
	// caller falls back to filepath.Rel.
	notUnder := [][2]string{
		{"/repo", "/repository"},   // prefix string but not a path ancestor
		{"/repo", "/other/x"},      // unrelated
		{"/repo/a", "/repo"},       // absPath shorter than base
		{"/repo/foobar", "/repo/foo"}, // sibling sharing a name prefix
	}
	for _, p := range notUnder {
		base, abs := p[0], p[1]
		if _, ok := relUnderBase(abs, base); ok {
			t.Errorf("relUnderBase(%q, %q): ok=true, want false (not under base)", abs, base)
		}
	}
}

// dirOf is a hot-path fast version of filepath.Dir for clean paths. This pins
// it to filepath.Dir's output across representative inputs, including the edge
// cases that must fall back to filepath.Dir.
func TestDirOfMatchesFilepathDir(t *testing.T) {
	cases := []string{
		"/repo/a/b/c.go",
		"/repo/a/b",
		"/repo/file",
		"/repo",
		"/a",
		"/",
		"relative/path", // no leading sep
		"single",        // no separator at all
	}
	for _, p := range cases {
		if got, want := dirOf(p), filepath.Dir(p); got != want {
			t.Errorf("dirOf(%q) = %q, want %q (filepath.Dir)", p, got, want)
		}
	}
}
