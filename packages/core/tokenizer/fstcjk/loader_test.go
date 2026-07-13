package fstcjk

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blevesearch/vellum"
)

// loader_test.go covers the FST-loading surface (Open/buildEmbedded/LoadBytes/
// OpenMmap/readTotalFreq/Close) and both hmm() branches WITHOUT depending on gse
// or the tools-tagged generator. It builds tiny in-memory vellum FSTs so the
// success and error paths are reachable in a normal `go test` run.

// tinyFST returns a minimal valid vellum FST containing a couple of words, so
// the loader paths can be exercised without the multi-MB embedded dict.
func tinyFST(t testing.TB) []byte {
	t.Helper()
	var b bytes.Buffer
	bld, err := vellum.New(&b, nil)
	if err != nil {
		t.Fatalf("vellum.New: %v", err)
	}
	// Keys MUST be inserted in lexicographic byte order.
	if err := bld.Insert([]byte("中"), 5); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := bld.Insert([]byte("中国"), 100); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := bld.Close(); err != nil {
		t.Fatalf("close builder: %v", err)
	}
	return b.Bytes()
}

// TestLoadBytes covers LoadBytes success and the vellum.Load error path (bad
// magic / truncated bytes), which the embedded happy path never reaches.
func TestLoadBytes(t *testing.T) {
	s, err := LoadBytes(tinyFST(t), 1000)
	if err != nil {
		t.Fatalf("LoadBytes valid: %v", err)
	}
	if s.TotalFreq() != 1000 {
		t.Fatalf("totalFreq = %v, want 1000", s.TotalFreq())
	}
	if f, ok := s.findFreq([]rune("中国"), nil); !ok || f != 100 {
		t.Fatalf("findFreq(中国) = %v,%v want 100,true", f, ok)
	}

	if _, err := LoadBytes([]byte("not an fst at all"), 1); err == nil {
		t.Fatal("LoadBytes(garbage): expected error, got nil")
	}
}

// TestBuildEmbedded covers the Open() once-body helper: success plus the
// totalFreq-parse and FST-load error branches.
func TestBuildEmbedded(t *testing.T) {
	s, err := buildEmbedded(tinyFST(t), "53226742\n")
	if err != nil {
		t.Fatalf("buildEmbedded valid: %v", err)
	}
	if s.TotalFreq() != 53226742 {
		t.Fatalf("totalFreq = %v", s.TotalFreq())
	}

	if _, err := buildEmbedded(tinyFST(t), "not-a-number"); err == nil {
		t.Fatal("buildEmbedded(bad totalFreq): expected error")
	}
	if _, err := buildEmbedded([]byte("garbage"), "100"); err == nil {
		t.Fatal("buildEmbedded(bad fst): expected error")
	}
}

// TestOpenEmbedded exercises the production Open() entry point: it must return a
// usable segmenter and be idempotent (the sync.Once returns the cached value).
func TestOpenEmbedded(t *testing.T) {
	s1, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s2, err := Open()
	if err != nil {
		t.Fatalf("Open (cached): %v", err)
	}
	if s1 != s2 {
		t.Fatal("Open did not return the cached singleton")
	}
	if s1.owned {
		t.Fatal("embedded segmenter must not be owned (no mmap to Close)")
	}
	// Closing a non-owned segmenter is a no-op and must not error.
	if err := s1.Close(); err != nil {
		t.Fatalf("Close non-owned: %v", err)
	}
}

// TestOpenMmap covers the mmap loader: a valid FST + totalfreq file round-trips,
// Close() releases the mmap, and the file-not-found / bad-totalfreq error paths
// are reached.
func TestOpenMmap(t *testing.T) {
	dir := t.TempDir()
	fstPath := filepath.Join(dir, "dict.fst")
	tfPath := filepath.Join(dir, "dict.totalfreq")
	if err := os.WriteFile(fstPath, tinyFST(t), 0o644); err != nil {
		t.Fatalf("write fst: %v", err)
	}
	if err := os.WriteFile(tfPath, []byte("12345\n"), 0o644); err != nil {
		t.Fatalf("write totalfreq: %v", err)
	}

	s, err := OpenMmap(fstPath, tfPath)
	if err != nil {
		t.Fatalf("OpenMmap: %v", err)
	}
	if s.TotalFreq() != 12345 {
		t.Fatalf("totalFreq = %v, want 12345", s.TotalFreq())
	}
	if !s.owned {
		t.Fatal("mmap segmenter must be owned so Close releases the mmap")
	}
	if f, ok := s.findFreq([]rune("中国"), nil); !ok || f != 100 {
		t.Fatalf("findFreq(中国) = %v,%v want 100,true", f, ok)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close owned: %v", err)
	}

	// Error path: FST file missing.
	if _, err := OpenMmap(filepath.Join(dir, "nope.fst"), tfPath); err == nil {
		t.Fatal("OpenMmap(missing fst): expected error")
	}

	// Error path: totalfreq file unreadable -> OpenMmap must close the fst and
	// propagate the error.
	if _, err := OpenMmap(fstPath, filepath.Join(dir, "nope.totalfreq")); err == nil {
		t.Fatal("OpenMmap(missing totalfreq): expected error")
	}
}

// TestReadTotalFreq covers the sidecar parser: a valid value, a missing file,
// and malformed content.
func TestReadTotalFreq(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	if err := os.WriteFile(good, []byte(" 53226742 \n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	v, err := readTotalFreq(good)
	if err != nil {
		t.Fatalf("readTotalFreq good: %v", err)
	}
	if v != 53226742 {
		t.Fatalf("readTotalFreq = %v, want 53226742", v)
	}

	if _, err := readTotalFreq(filepath.Join(dir, "absent")); err == nil {
		t.Fatal("readTotalFreq(absent): expected error")
	}

	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("not-a-float"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := readTotalFreq(bad); err == nil {
		t.Fatal("readTotalFreq(bad content): expected error")
	}
}

// TestHmmBranches drives both branches of Segmenter.hmm directly: the
// known-word branch (freq>0 -> emit rune-by-rune) and the OOV branch (not a
// word / freq 0 -> hmm.Cut). Using the embedded dict guarantees "中国" is a real
// word and a contrived run of rare chars is OOV.
func TestHmmBranches(t *testing.T) {
	s := loadFST(t)

	// Branch 1: known multi-rune word with freq>0 is emitted rune-by-rune.
	word := "中国"
	if f, ok := s.findFreq([]rune(word), nil); !ok || f <= 0 {
		t.Fatalf("precondition: %q must be a freq>0 word (got %v,%v)", word, f, ok)
	}
	got := s.hmm(word, []rune(word), nil, nil)
	wantChars := []string{"中", "国"}
	if !eqSlices(got, wantChars) {
		t.Fatalf("hmm(known word) = %q, want rune-by-rune %q", got, wantChars)
	}

	// Branch 2: an OOV run (not a dictionary word) falls through to hmm.Cut,
	// which must return a non-empty segmentation covering the input runes.
	oov := "鿜鿝鿞" // CJK ext / rare block, not a dictionary word
	if _, ok := s.findFreq([]rune(oov), nil); ok {
		// If by chance it IS a word in some dict version, skip rather than fail
		// the fidelity contract; the branch is still covered by the assert below
		// only when it's genuinely OOV.
		t.Skipf("%q unexpectedly in dict; pick another OOV sample", oov)
	}
	out := s.hmm(oov, []rune(oov), nil, nil)
	if len(out) == 0 {
		t.Fatalf("hmm(OOV) returned no tokens for %q", oov)
	}
	if strings.Join(out, "") != oov {
		t.Fatalf("hmm(OOV) lost runes: joined %q != input %q", strings.Join(out, ""), oov)
	}
}
