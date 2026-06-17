package vectorstore

import (
	"testing"

	bolt "go.etcd.io/bbolt"
)

// commitManifest writes a whole *manifest snapshot into a control store in one
// write-txn, exactly as writeManifestLocked composes its put* helpers. It is the
// test fixture for the bbolt-backed control-plane round-trip (the persistence that
// replaced the hand-rolled serialize+CRC32+tmp+fsync+rename manifest file).
func commitManifest(t *testing.T, cs *controlStore, m *manifest) {
	t.Helper()
	requireNoError(t, cs.update(func(tx *bolt.Tx) error {
		if err := putMeta(tx, m.Version, m.Head, m.Metric); err != nil {
			return err
		}
		for _, d := range m.AttrDecls {
			if err := putAttrDecl(tx, d); err != nil {
				return err
			}
		}
		for _, e := range m.Segments {
			if err := putSegment(tx, e); err != nil {
				return err
			}
		}
		for _, ic := range m.Indexes {
			if err := putIndexConfig(tx, ic); err != nil {
				return err
			}
		}
		for _, is := range m.IndexSegs {
			if err := putIndexSeg(tx, is); err != nil {
				return err
			}
		}
		return nil
	}))
}

// loadManifest reads the whole control plane back out of a control store, the
// read-side loadControlManifest performs during recovery. ok is false on a fresh
// (never-committed) store.
func loadManifest(t *testing.T, cs *controlStore) (*manifest, bool) {
	t.Helper()
	m := &manifest{}
	var ok bool
	requireNoError(t, cs.view(func(tx *bolt.Tx) error {
		version, head, metric, present, err := getMeta(tx)
		if err != nil {
			return err
		}
		if !present {
			return nil
		}
		ok = true
		m.Version, m.Head, m.Metric = version, head, metric
		if m.AttrDecls, err = listAttrDecls(tx); err != nil {
			return err
		}
		if m.Segments, err = listSegments(tx); err != nil {
			return err
		}
		if m.Indexes, err = listIndexConfigs(tx); err != nil {
			return err
		}
		m.IndexSegs, err = listIndexSegs(tx)
		return err
	}))
	return m, ok
}

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

// TestManifest_WriteReadRoundTrip pins the control-plane round-trip: a committed
// snapshot reloads with the same scalars and segment set. (Was the manifest-file
// round-trip; now exercises the bbolt buckets, the as-built persistence.)
func TestManifest_WriteReadRoundTrip(t *testing.T) {
	cs, _ := openTestControlStore(t)
	commitManifest(t, cs, sampleManifest())

	got, ok := loadManifest(t, cs)
	if !ok {
		t.Fatal("loadManifest must report a committed control store as present")
	}
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

// TestManifest_V4_PerIndexRoundTrip pins the control store carrying the N named
// index configs (indexes bucket) and the per-(index,segment) build state
// (indexsegs bucket), so a reopen restores every index's config + which (index,seg)
// graphs are built. Records, configs, and states round-trip through one commit.
func TestManifest_V4_PerIndexRoundTrip(t *testing.T) {
	cs, _ := openTestControlStore(t)
	commitManifest(t, cs, &manifest{
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
	})

	got, ok := loadManifest(t, cs)
	if !ok {
		t.Fatal("loadManifest must report the committed control store as present")
	}
	if got.Version != 9 || got.Metric != Cosine || len(got.Segments) != 2 {
		t.Fatalf("records block round-trip broken: %+v", got)
	}
	// Index configs come back ordered by name (bbolt key order): default, euclid.
	if len(got.Indexes) != 2 || got.Indexes[1].Name != "euclid" || got.Indexes[1].Metric != Euclidean || got.Indexes[1].M != 8 {
		t.Fatalf("index config block round-trip broken: %+v", got.Indexes)
	}
	// Index-seg states come back ordered by (name, segId); (euclid, seg 2) is last.
	last := got.IndexSegs[len(got.IndexSegs)-1]
	if len(got.IndexSegs) != 4 || last.Index != "euclid" || last.SegID != 2 || last.State != segPending {
		t.Fatalf("index-seg state block round-trip broken: %+v", got.IndexSegs)
	}
}

// TestManifest_V3_AttrDeclsRoundTrip pins the control store carrying the declared
// attr-index set (property + kind), so a reopen restores which fields are indexed.
func TestManifest_V3_AttrDeclsRoundTrip(t *testing.T) {
	cs, _ := openTestControlStore(t)
	commitManifest(t, cs, &manifest{
		Version: 3, Head: 0, Metric: Cosine,
		AttrDecls: []attrDecl{{Property: "color", Kind: Keyword}, {Property: "price", Kind: Numeric}},
		Segments:  []segmentEntry{{SegID: 1, Gen: 0, VecCount: 10, TombCount: 2}},
	})

	got, _ := loadManifest(t, cs)
	// attr decls come back ordered by property (bbolt key order): color, price.
	if len(got.AttrDecls) != 2 || got.AttrDecls[0].Property != "color" || got.AttrDecls[1].Kind != Numeric {
		t.Fatalf("attr decls round-trip = %#v", got.AttrDecls)
	}
}

// TestManifest_MissingIsFreshStore pins that a never-committed control store reads
// back as "not present" — the fresh-store signal that used to be a missing manifest
// file (os.IsNotExist) and now drives recover()'s head-only rebuild from the head
// bucket.
func TestManifest_MissingIsFreshStore(t *testing.T) {
	cs, _ := openTestControlStore(t)
	if _, ok := loadManifest(t, cs); ok {
		t.Fatal("a fresh control store must read back as not-present (fresh-store signal)")
	}
}

// TestManifest_CorruptRecordRejected is the bbolt analog of the old CRC-mismatch
// test: a structurally corrupt control record (here a truncated meta value) must be
// rejected by the loader rather than silently mis-read, so recovery fails loud.
func TestManifest_CorruptRecordRejected(t *testing.T) {
	cs, _ := openTestControlStore(t)
	commitManifest(t, cs, sampleManifest())
	// Overwrite the version meta value with a too-short byte slice (corruption a
	// torn write / bit-rot could produce). getMeta must reject the bad length.
	requireNoError(t, cs.update(func(tx *bolt.Tx) error {
		return tx.Bucket(bktMeta).Put(metaKeyVersion, []byte{0x01})
	}))
	err := cs.view(func(tx *bolt.Tx) error {
		_, _, _, _, gerr := getMeta(tx)
		return gerr
	})
	if err == nil {
		t.Fatal("loadManifest must reject a corrupt (wrong-length) meta record")
	}
}

// TestManifest_ReopenIsDurable pins durability across a control-DB close+reopen: a
// committed snapshot survives a full Close (bbolt fsync) and reloads intact — the
// recovery property the manifest file's fsync+rename used to provide.
func TestManifest_ReopenIsDurable(t *testing.T) {
	cs, dir := openTestControlStore(t)
	commitManifest(t, cs, sampleManifest())
	requireNoError(t, cs.Close())

	cs2, err := openControlStore(dir)
	requireNoError(t, err)
	t.Cleanup(func() { cs2.Close() })
	got, ok := loadManifest(t, cs2)
	if !ok || got.Version != 7 || got.Head != 5 || len(got.Segments) != 2 {
		t.Fatalf("committed control state did not survive close+reopen: ok=%v %+v", ok, got)
	}
}

// TestLoadControlManifest_MetaErrorPropagates drives loadControlManifest's
// getMeta-error return: a corrupt (wrong-length) meta value must fail recovery's
// control-plane load loud rather than mis-read.
func TestLoadControlManifest_MetaErrorPropagates(t *testing.T) {
	cs, _ := openTestControlStore(t)
	commitManifest(t, cs, sampleManifest())
	requireNoError(t, cs.update(func(tx *bolt.Tx) error {
		return tx.Bucket(bktMeta).Put(metaKeyVersion, []byte{0x01}) // too short
	}))
	s := &Store{cs: cs}
	if _, _, err := s.loadControlManifest(); err == nil {
		t.Fatal("loadControlManifest must propagate a corrupt meta record")
	}
}

// TestLoadControlManifest_SegmentErrorPropagates drives loadControlManifest's
// listSegments-error return: a corrupt segment value (present meta, so the load
// proceeds past getMeta into the block readers) must fail loud.
func TestLoadControlManifest_SegmentErrorPropagates(t *testing.T) {
	cs, _ := openTestControlStore(t)
	commitManifest(t, cs, sampleManifest())
	requireNoError(t, cs.update(func(tx *bolt.Tx) error {
		return tx.Bucket(bktSegments).Put(segKey(1), []byte{0x00, 0x01}) // wrong length
	}))
	s := &Store{cs: cs}
	if _, _, err := s.loadControlManifest(); err == nil {
		t.Fatal("loadControlManifest must propagate a corrupt segment record")
	}
}
