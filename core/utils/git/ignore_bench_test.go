package gitutils

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Benchmark goal (per AGENTS.md principle 2): measure WHERE the per-entry cost
// of GitIgnore.IsIgnored actually goes during a real workspace scan, so we can
// attribute it to either (1) the library's per-pattern regexp matching, or
// (2) the surrounding Go overhead in this file (alloc/lock/path work).
//
// The tree below mimics a realistic repo: a root .gitignore with a typical mix
// of literal basenames, globs, anchored and negated patterns, one nested
// .gitignore, plus ignored subtrees (node_modules/, build/) that a real scan
// would prune. We pre-collect the exact (relPath, isDir) multiset that
// fsutils.ListFiles would ask the filter about (skipping descent into ignored
// dirs), so the timed region contains zero os.ReadDir I/O — only gitignore CPU.

const benchRootGitignore = `# dependencies
node_modules/
vendor/
# build artifacts
build/
dist/
*.o
*.a
*.exe
# logs & temp
*.log
*.tmp
.DS_Store
# anchored
/coverage
/.env
# globstar
**/__pycache__/
# negation
!important.log
`

const benchNestedGitignore = `*.local
generated/
`

// bench tree shape — moderate so setup is fast but large enough to amortize the
// one-time gitignore load and to give pprof plenty of samples.
const (
	benchDepth    = 5 // directory nesting depth
	benchBranch   = 3 // subdirs per directory
	benchFilesDir = 8 // files per directory
)

type benchEntry struct {
	rel   string
	isDir bool
}

// buildBenchTree writes a realistic directory tree under root and returns it.
func buildBenchTree(tb testing.TB, root string) {
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(benchRootGitignore), 0644); err != nil {
		tb.Fatal(err)
	}
	var rec func(dir string, depth int)
	rec = func(dir string, depth int) {
		// Files in this directory: a realistic mix of kept and ignored names.
		names := []string{
			"main.go", "helper.go", "util.go", "types.go",
			"app.log", "scratch.tmp", "data.txt", "important.log",
		}
		for i := 0; i < benchFilesDir && i < len(names); i++ {
			_ = os.WriteFile(filepath.Join(dir, names[i]), []byte("x"), 0644)
		}
		if depth >= benchDepth {
			return
		}
		for b := range benchBranch {
			name := fmt.Sprintf("pkg%d", b)
			// Sprinkle ignored subtrees so the scan prunes them like a real repo.
			if depth == 1 && b == 0 {
				name = "node_modules"
			} else if depth == 2 && b == 1 {
				name = "build"
			}
			sub := filepath.Join(dir, name)
			if err := os.MkdirAll(sub, 0755); err != nil {
				tb.Fatal(err)
			}
			// One nested .gitignore partway down to exercise multi-level rules.
			if depth == 1 && b == 1 {
				_ = os.WriteFile(filepath.Join(sub, ".gitignore"), []byte(benchNestedGitignore), 0644)
			}
			rec(sub, depth+1)
		}
	}
	rec(root, 0)
}

// collectScanEntries replays the exact BFS that fsutils.ListFiles performs,
// recording every (relPath, isDir) the filter is asked about and pruning
// descent into ignored directories — i.e. the real per-scan call multiset.
func collectScanEntries(root string) []benchEntry {
	g := NewGitIgnore(root, true)
	var out []benchEntry
	type item struct{ full, rel string }
	queue := []item{{full: root, rel: ""}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		entries, err := os.ReadDir(cur.full)
		if err != nil {
			continue
		}
		for _, e := range entries {
			rel := e.Name()
			if cur.rel != "" {
				rel = filepath.Join(cur.rel, e.Name())
			}
			out = append(out, benchEntry{rel: rel, isDir: e.IsDir()})
			if e.IsDir() && !g.IsIgnored(rel, true) {
				queue = append(queue, item{full: filepath.Join(cur.full, e.Name()), rel: rel})
			}
		}
	}
	return out
}

// BenchmarkGitIgnore_ColdScan models a real per-workspace scan: a fresh
// GitIgnore (cold cache + lazy .gitignore load) replaying the whole visited
// sequence once. ns/op and allocs/op are per FULL TREE pass; divide by the
// entry count logged below for per-entry figures.
func BenchmarkGitIgnore_ColdScan(b *testing.B) {
	root := b.TempDir()
	buildBenchTree(b, root)
	entries := collectScanEntries(root)
	b.Logf("scan entries=%d (dirs+files the filter is asked about)", len(entries))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g := NewGitIgnore(root, true)
		for _, e := range entries {
			g.IsIgnored(e.rel, e.isDir)
		}
	}
}

// BenchmarkGitIgnore_WarmLookup isolates steady-state cost: one pre-warmed
// GitIgnore (gitignores loaded, dir cache filled), then only FILE lookups in
// the timed loop. This is the pprof target for attributing time to the
// library's regexp matching vs. this file's surrounding overhead.
func BenchmarkGitIgnore_WarmLookup(b *testing.B) {
	root := b.TempDir()
	buildBenchTree(b, root)
	entries := collectScanEntries(root)

	g := NewGitIgnore(root, true)
	var files []benchEntry
	for _, e := range entries {
		g.IsIgnored(e.rel, e.isDir) // warm load + dir cache
		if !e.isDir {
			files = append(files, e)
		}
	}
	b.Logf("file lookups/op=%d", len(files))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, e := range files {
			g.IsIgnored(e.rel, e.isDir)
		}
	}
}

// walkAllEntries walks the whole tree under root, pruning only .git, and returns
// every (relPath, isDir) — used to validate ignore decisions on a real repo
// independently of gitignore-based pruning (so the entry set is identical across
// builds and a cross-binary diff lines up).
func walkAllEntries(root string) []benchEntry {
	var out []benchEntry
	type item struct{ full, rel string }
	q := []item{{full: root, rel: ""}}
	for len(q) > 0 {
		cur := q[0]
		q = q[1:]
		des, err := os.ReadDir(cur.full)
		if err != nil {
			continue
		}
		for _, d := range des {
			rel := d.Name()
			if cur.rel != "" {
				rel = filepath.Join(cur.rel, d.Name())
			}
			out = append(out, benchEntry{rel: rel, isDir: d.IsDir()})
			if d.IsDir() && d.Name() != ".git" {
				q = append(q, item{full: filepath.Join(cur.full, d.Name()), rel: rel})
			}
		}
	}
	return out
}

// BenchmarkGitIgnore_RealRepo benchmarks IsIgnored against a real repository's
// directory tree and .gitignore files. Set GITIGNORE_BENCH_ROOT to the repo
// path; the benchmark is skipped otherwise (so it is inert on CI).
func BenchmarkGitIgnore_RealRepo(b *testing.B) {
	root := os.Getenv("GITIGNORE_BENCH_ROOT")
	if root == "" {
		b.Skip("set GITIGNORE_BENCH_ROOT to a repo path to benchmark real-world gitignore matching")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		b.Fatal(err)
	}
	entries := collectScanEntries(abs)
	if len(entries) == 0 {
		b.Skipf("no entries under %s", abs)
	}
	var files []benchEntry
	for _, e := range entries {
		if !e.isDir {
			files = append(files, e)
		}
	}
	b.Logf("real repo %s: %d scan entries (%d files)", abs, len(entries), len(files))

	b.Run("ColdScan", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			g := NewGitIgnore(abs, true)
			for _, e := range entries {
				g.IsIgnored(e.rel, e.isDir)
			}
		}
	})
	b.Run("WarmLookup", func(b *testing.B) {
		g := NewGitIgnore(abs, true)
		for _, e := range entries {
			g.IsIgnored(e.rel, e.isDir)
		}
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, e := range files {
				g.IsIgnored(e.rel, e.isDir)
			}
		}
	})
}

// TestRealRepoDecisions records the ignore decision for every entry of a real
// repository. With GITIGNORE_BENCH_DUMP set it writes "rel\tisDir\tignored"
// lines to that file so two builds (e.g. the library baseline and the fast
// path) can be diffed for real-world equivalence. Skipped without
// GITIGNORE_BENCH_ROOT.
func TestRealRepoDecisions(t *testing.T) {
	root := os.Getenv("GITIGNORE_BENCH_ROOT")
	if root == "" {
		t.Skip("set GITIGNORE_BENCH_ROOT to a repo path to record real-world ignore decisions")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	entries := walkAllEntries(abs)
	g := NewGitIgnore(abs, true)

	dump := os.Getenv("GITIGNORE_BENCH_DUMP")
	var sb strings.Builder
	ignored := 0
	for _, e := range entries {
		ig := g.IsIgnored(e.rel, e.isDir)
		if ig {
			ignored++
		}
		if dump != "" {
			fmt.Fprintf(&sb, "%s\t%v\t%v\n", e.rel, e.isDir, ig)
		}
	}
	t.Logf("%s: %d entries, %d ignored", abs, len(entries), ignored)
	if dump != "" {
		if err := os.WriteFile(dump, []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
