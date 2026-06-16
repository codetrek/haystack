package vectorstore

import (
	"os"
	"path/filepath"
	"testing"
)

func sampleManifest() *manifest {
	return &manifest{
		Version: 7,
		Head:    segID(5),
		Metric:  Euclidean,
		Segments: []segmentEntry{
			{SegID: 1, Gen: 0, VecCount: 100, TombCount: 3, State: segPending},
			{SegID: 2, Gen: 0, VecCount: 200, TombCount: 0, State: segIndexed},
		},
	}
}

func TestManifest_WriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := sampleManifest()
	requireNoError(t, writeManifest(dir, m))

	got, err := readManifest(dir)
	requireNoError(t, err)
	if got.Version != 7 || got.Head != 5 || got.Metric != Euclidean || len(got.Segments) != 2 {
		t.Fatalf("manifest mismatch: %+v", got)
	}
	if got.Segments[0].SegID != 1 || got.Segments[0].State != segPending {
		t.Fatalf("seg0 = %+v", got.Segments[0])
	}
	if got.Segments[1].SegID != 2 || got.Segments[1].State != segIndexed || got.Segments[1].VecCount != 200 {
		t.Fatalf("seg1 = %+v", got.Segments[1])
	}
}

func TestManifest_NoTmpLeftBehind(t *testing.T) {
	dir := t.TempDir()
	requireNoError(t, writeManifest(dir, sampleManifest()))
	if _, err := os.Stat(filepath.Join(dir, "manifest.tmp")); !os.IsNotExist(err) {
		t.Fatal("manifest.tmp should be renamed away, not left behind")
	}
}

// TestManifest_V3_AttrDeclsRoundTrip pins the v3 manifest carrying the declared
// attr-index set (property + kind), so a reopen restores which fields are indexed.
func TestManifest_V3_AttrDeclsRoundTrip(t *testing.T) {
	m := &manifest{
		Version: 3, Head: 0, Metric: Cosine,
		AttrDecls: []attrDecl{{Property: "color", Kind: Keyword}, {Property: "price", Kind: Numeric}},
		Segments:  []segmentEntry{{SegID: 1, Gen: 0, VecCount: 10, TombCount: 2, State: segIndexed}},
	}
	b := serializeManifest(m)
	got, err := parseManifest(b)
	requireNoError(t, err)
	if len(got.AttrDecls) != 2 || got.AttrDecls[0].Property != "color" || got.AttrDecls[1].Kind != Numeric {
		t.Fatalf("attr decls round-trip = %#v", got.AttrDecls)
	}
}

func TestManifest_CorruptCRCRejected(t *testing.T) {
	dir := t.TempDir()
	requireNoError(t, writeManifest(dir, sampleManifest()))
	path := filepath.Join(dir, "manifest")
	data, err := os.ReadFile(path)
	requireNoError(t, err)
	data[len(data)-1] ^= 0xFF // corrupt the trailing CRC
	requireNoError(t, os.WriteFile(path, data, 0644))
	if _, err := readManifest(dir); err == nil {
		t.Fatal("readManifest must reject a corrupt CRC")
	}
}

func TestManifest_MissingIsNotExist(t *testing.T) {
	_, err := readManifest(t.TempDir())
	if !os.IsNotExist(err) {
		t.Fatalf("missing manifest err = %v, want os.IsNotExist", err)
	}
}

// TestManifest_StrandedTmpIgnored applies adversarial review #3 (CRITICAL): a
// crash mid-write can leave a stranded manifest.tmp next to a previously
// committed manifest. readManifest reads "manifest" (the rename target), never
// ".tmp", so the committed manifest must still load and the garbage tmp must be
// ignored — proving the rename is the true commit boundary.
func TestManifest_StrandedTmpIgnored(t *testing.T) {
	dir := t.TempDir()
	// A valid committed manifest is present...
	requireNoError(t, writeManifest(dir, sampleManifest()))
	// ...and a stranded, torn manifest.tmp from a crashed later write.
	requireNoError(t, os.WriteFile(filepath.Join(dir, "manifest.tmp"), []byte("torn garbage not a manifest"), 0644))

	got, err := readManifest(dir)
	requireNoError(t, err)
	if got.Version != 7 || got.Head != 5 || len(got.Segments) != 2 {
		t.Fatalf("readManifest must return the committed manifest, ignoring the stranded tmp: %+v", got)
	}
}
