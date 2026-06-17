package vectorstore

import (
	"hash/crc32"
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
			{SegID: 1, Gen: 0, VecCount: 100, TombCount: 3},
			{SegID: 2, Gen: 0, VecCount: 200, TombCount: 0},
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
	if got.Segments[0].SegID != 1 || got.Segments[0].VecCount != 100 {
		t.Fatalf("seg0 = %+v", got.Segments[0])
	}
	if got.Segments[1].SegID != 2 || got.Segments[1].VecCount != 200 {
		t.Fatalf("seg1 = %+v", got.Segments[1])
	}
}

// TestManifest_V4_PerIndexRoundTrip pins the v4 manifest carrying the N named
// index configs (Indexes) and the per-(index,segment) build state (IndexSegs),
// so a reopen restores every index's config + which (index,seg) graphs are built.
func TestManifest_V4_PerIndexRoundTrip(t *testing.T) {
	m := &manifest{
		Version: 9,
		Head:    headSegID,
		Metric:  Cosine,
		Segments: []segmentEntry{
			{SegID: 1, Gen: 0, VecCount: 50, TombCount: 2},
			{SegID: 2, Gen: 0, VecCount: 30, TombCount: 0},
		},
		Indexes: []indexConfigEntry{
			{Name: "default", Type: "hnsw", Metric: Cosine, M: 16, EfConstruction: 200, EfSearch: 64},
			{Name: "euclid", Type: "hnsw", Metric: Euclidean, M: 8, EfConstruction: 80, EfSearch: 32},
		},
		IndexSegs: []indexSegEntry{
			{Index: "default", SegID: 1, Gen: 0, State: segIndexed},
			{Index: "default", SegID: 2, Gen: 0, State: segIndexed},
			{Index: "euclid", SegID: 1, Gen: 0, State: segIndexed},
			{Index: "euclid", SegID: 2, Gen: 0, State: segPending},
		},
	}
	b := serializeManifest(m)
	got, err := parseManifest(b)
	requireNoError(t, err)
	if got.Version != 9 || got.Metric != Cosine || len(got.Segments) != 2 {
		t.Fatalf("records block round-trip broken: %+v", got)
	}
	if len(got.Indexes) != 2 || got.Indexes[1].Name != "euclid" || got.Indexes[1].Metric != Euclidean || got.Indexes[1].M != 8 {
		t.Fatalf("index config block round-trip broken: %+v", got.Indexes)
	}
	if len(got.IndexSegs) != 4 || got.IndexSegs[3].Index != "euclid" || got.IndexSegs[3].State != segPending {
		t.Fatalf("index-seg state block round-trip broken: %+v", got.IndexSegs)
	}
	// fmtver byte is bumped to 4.
	if b[4] != 4 {
		t.Fatalf("manifest format version byte = %d, want 4", b[4])
	}
}

// TestManifest_V4_RejectsV3Byte forges a v3 format byte AND re-stamps the trailing
// CRC (over b[:len-4]) so parseManifest fails SPECIFICALLY on the version-byte gate
// rather than the CRC gate — proving the hard cut rejects an older format byte
// (adversarial appendix #22).
func TestManifest_V4_RejectsV3Byte(t *testing.T) {
	m := &manifest{Version: 1, Head: headSegID, Metric: Cosine}
	b := serializeManifest(m)
	b[4] = 3 // forge a v3 byte
	// Re-stamp the CRC so the version-byte check (not the CRC check) is what rejects.
	crc := crc32.ChecksumIEEE(b[:len(b)-4])
	for i := 0; i < 4; i++ {
		b[len(b)-4+i] = byte(crc >> (8 * i))
	}
	if _, err := parseManifest(b); err == nil {
		t.Fatal("a non-v4 format byte must be rejected (hard cut)")
	}
}

func TestManifest_NoTmpLeftBehind(t *testing.T) {
	dir := t.TempDir()
	requireNoError(t, writeManifest(dir, sampleManifest()))
	if _, err := os.Stat(filepath.Join(dir, "manifest.tmp")); !os.IsNotExist(err) {
		t.Fatal("manifest.tmp should be renamed away, not left behind")
	}
}

// TestManifest_V3_AttrDeclsRoundTrip pins the manifest carrying the declared
// attr-index set (property + kind), so a reopen restores which fields are indexed.
func TestManifest_V3_AttrDeclsRoundTrip(t *testing.T) {
	m := &manifest{
		Version: 3, Head: 0, Metric: Cosine,
		AttrDecls: []attrDecl{{Property: "color", Kind: Keyword}, {Property: "price", Kind: Numeric}},
		Segments:  []segmentEntry{{SegID: 1, Gen: 0, VecCount: 10, TombCount: 2}},
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
