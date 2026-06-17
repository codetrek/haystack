package vectorstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// openTestControlStore opens a control DB in a fresh temp dir and registers its
// Close on cleanup. It returns the store and the directory.
func openTestControlStore(t *testing.T) (*controlStore, string) {
	t.Helper()
	dir := t.TempDir()
	cs, err := openControlStore(dir)
	requireNoError(t, err)
	t.Cleanup(func() { cs.Close() })
	return cs, dir
}

// TestControlStore_OpenCreatesFileAndBuckets proves open creates control.db and
// every bucket in the schema, so later increments can assume the buckets exist.
func TestControlStore_OpenCreatesFileAndBuckets(t *testing.T) {
	cs, dir := openTestControlStore(t)

	if _, err := os.Stat(filepath.Join(dir, controlDBName)); err != nil {
		t.Fatalf("control.db not created: %v", err)
	}
	err := cs.view(func(tx *bolt.Tx) error {
		for _, name := range allBuckets {
			if tx.Bucket(name) == nil {
				t.Errorf("bucket %q missing after open", name)
			}
		}
		return nil
	})
	requireNoError(t, err)
}

// TestControlStore_ReopenIsIdempotent proves reopening an existing control DB
// keeps prior data and does not clobber buckets (CreateBucketIfNotExists).
func TestControlStore_ReopenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	cs1, err := openControlStore(dir)
	requireNoError(t, err)
	requireNoError(t, cs1.update(func(tx *bolt.Tx) error {
		return putMeta(tx, 42, segID(7), Euclidean)
	}))
	requireNoError(t, cs1.Close())

	cs2, err := openControlStore(dir)
	requireNoError(t, err)
	defer cs2.Close()
	var ver uint64
	var head segID
	var m Metric
	requireNoError(t, cs2.view(func(tx *bolt.Tx) error {
		v, h, mt, ok, err := getMeta(tx)
		if err != nil {
			return err
		}
		if !ok {
			t.Fatal("meta lost across reopen")
		}
		ver, head, m = v, h, mt
		return nil
	}))
	if ver != 42 || head != 7 || m != Euclidean {
		t.Fatalf("reopen meta = (%d,%d,%v), want (42,7,euclidean)", ver, head, m)
	}
}

// TestControlStore_MetaRoundTrip round-trips the store-global scalars and proves
// a fresh DB reports ok=false (the "missing manifest == fresh store" contract).
func TestControlStore_MetaRoundTrip(t *testing.T) {
	cs, _ := openTestControlStore(t)

	// Fresh: meta not written yet.
	requireNoError(t, cs.view(func(tx *bolt.Tx) error {
		_, _, _, ok, err := getMeta(tx)
		if err != nil {
			return err
		}
		if ok {
			t.Fatal("fresh control DB must report meta ok=false")
		}
		return nil
	}))

	requireNoError(t, cs.update(func(tx *bolt.Tx) error {
		return putMeta(tx, 9, headSegID, Cosine)
	}))
	requireNoError(t, cs.view(func(tx *bolt.Tx) error {
		v, h, m, ok, err := getMeta(tx)
		if err != nil {
			return err
		}
		if !ok || v != 9 || h != headSegID || m != Cosine {
			t.Fatalf("meta = (%d,%d,%v,ok=%v), want (9,%d,cosine,true)", v, h, m, ok, headSegID)
		}
		return nil
	}))
}

// TestControlStore_AttrDeclRoundTrip round-trips attr-index declarations and
// proves listAttrDecls returns them in property-name order.
func TestControlStore_AttrDeclRoundTrip(t *testing.T) {
	cs, _ := openTestControlStore(t)
	requireNoError(t, cs.update(func(tx *bolt.Tx) error {
		if err := putAttrDecl(tx, attrDecl{Property: "price", Kind: Numeric}); err != nil {
			return err
		}
		return putAttrDecl(tx, attrDecl{Property: "color", Kind: Keyword})
	}))
	var got []attrDecl
	requireNoError(t, cs.view(func(tx *bolt.Tx) error {
		var err error
		got, err = listAttrDecls(tx)
		return err
	}))
	if len(got) != 2 || got[0].Property != "color" || got[0].Kind != Keyword ||
		got[1].Property != "price" || got[1].Kind != Numeric {
		t.Fatalf("attr decls = %#v, want color/Keyword then price/Numeric", got)
	}
}

// TestControlStore_SegmentRoundTrip round-trips segment entries, proves get/list/
// delete, and that list is ordered by segId (the cursor relies on big-endian keys).
func TestControlStore_SegmentRoundTrip(t *testing.T) {
	cs, _ := openTestControlStore(t)
	want := []segmentEntry{
		{SegID: 2, Gen: 1, VecCount: 200, TombCount: 5},
		{SegID: 1, Gen: 0, VecCount: 100, TombCount: 0},
		{SegID: 10, Gen: 3, VecCount: 33, TombCount: 1},
	}
	requireNoError(t, cs.update(func(tx *bolt.Tx) error {
		for _, e := range want {
			if err := putSegment(tx, e); err != nil {
				return err
			}
		}
		return nil
	}))

	// Single get.
	requireNoError(t, cs.view(func(tx *bolt.Tx) error {
		e, ok, err := getSegment(tx, 2)
		if err != nil {
			return err
		}
		if !ok || e.Gen != 1 || e.VecCount != 200 || e.TombCount != 5 {
			t.Fatalf("getSegment(2) = %+v ok=%v", e, ok)
		}
		_, ok, err = getSegment(tx, 999)
		if err != nil {
			return err
		}
		if ok {
			t.Fatal("getSegment(absent) must report ok=false")
		}
		return nil
	}))

	// List is segId-ordered: 1, 2, 10.
	var list []segmentEntry
	requireNoError(t, cs.view(func(tx *bolt.Tx) error {
		var err error
		list, err = listSegments(tx)
		return err
	}))
	if len(list) != 3 || list[0].SegID != 1 || list[1].SegID != 2 || list[2].SegID != 10 {
		t.Fatalf("listSegments order = %v, want [1 2 10]", segIDs(list))
	}

	// Delete retires an entry.
	requireNoError(t, cs.update(func(tx *bolt.Tx) error { return deleteSegment(tx, 2) }))
	requireNoError(t, cs.view(func(tx *bolt.Tx) error {
		_, ok, err := getSegment(tx, 2)
		if err != nil {
			return err
		}
		if ok {
			t.Fatal("deleteSegment(2) did not remove the entry")
		}
		return nil
	}))
}

// TestControlStore_IndexConfigRoundTrip round-trips named index configs.
func TestControlStore_IndexConfigRoundTrip(t *testing.T) {
	cs, _ := openTestControlStore(t)
	requireNoError(t, cs.update(func(tx *bolt.Tx) error {
		if err := putIndexConfig(tx, indexConfigEntry{Name: "default", Type: "hnsw", Metric: Cosine, M: 16, EfConstruction: 200, EfSearch: 64}); err != nil {
			return err
		}
		return putIndexConfig(tx, indexConfigEntry{Name: "euclid", Type: "hnsw", Metric: Euclidean, M: 8, EfConstruction: 80, EfSearch: 32})
	}))

	requireNoError(t, cs.view(func(tx *bolt.Tx) error {
		ic, ok, err := getIndexConfig(tx, "euclid")
		if err != nil {
			return err
		}
		if !ok || ic.Metric != Euclidean || ic.M != 8 || ic.EfConstruction != 80 || ic.EfSearch != 32 || ic.Type != "hnsw" {
			t.Fatalf("getIndexConfig(euclid) = %+v ok=%v", ic, ok)
		}
		_, ok, err = getIndexConfig(tx, "nope")
		if err != nil {
			return err
		}
		if ok {
			t.Fatal("getIndexConfig(absent) must report ok=false")
		}
		return nil
	}))

	var list []indexConfigEntry
	requireNoError(t, cs.view(func(tx *bolt.Tx) error {
		var err error
		list, err = listIndexConfigs(tx)
		return err
	}))
	if len(list) != 2 || list[0].Name != "default" || list[1].Name != "euclid" {
		t.Fatalf("listIndexConfigs = %#v, want default then euclid", list)
	}
}

// TestControlStore_IndexSegRoundTrip round-trips per-(index,segment) build states
// and proves list ordering groups by name then segId and delete works.
func TestControlStore_IndexSegRoundTrip(t *testing.T) {
	cs, _ := openTestControlStore(t)
	requireNoError(t, cs.update(func(tx *bolt.Tx) error {
		entries := []indexSegEntry{
			{Index: "default", SegID: 2, Gen: 0, State: segIndexed},
			{Index: "default", SegID: 1, Gen: 0, State: segPending},
			{Index: "euclid", SegID: 1, Gen: 0, State: segIndexed},
		}
		for _, e := range entries {
			if err := putIndexSeg(tx, e); err != nil {
				return err
			}
		}
		return nil
	}))

	requireNoError(t, cs.view(func(tx *bolt.Tx) error {
		is, ok, err := getIndexSeg(tx, "default", 1)
		if err != nil {
			return err
		}
		if !ok || is.State != segPending {
			t.Fatalf("getIndexSeg(default,1) = %+v ok=%v", is, ok)
		}
		_, ok, err = getIndexSeg(tx, "default", 99)
		if err != nil {
			return err
		}
		if ok {
			t.Fatal("getIndexSeg(absent) must report ok=false")
		}
		return nil
	}))

	var list []indexSegEntry
	requireNoError(t, cs.view(func(tx *bolt.Tx) error {
		var err error
		list, err = listIndexSegs(tx)
		return err
	}))
	// Ordered by (name, segId): (default,1) (default,2) (euclid,1).
	if len(list) != 3 ||
		list[0].Index != "default" || list[0].SegID != 1 ||
		list[1].Index != "default" || list[1].SegID != 2 ||
		list[2].Index != "euclid" || list[2].SegID != 1 {
		t.Fatalf("listIndexSegs order = %#v", list)
	}

	requireNoError(t, cs.update(func(tx *bolt.Tx) error { return deleteIndexSeg(tx, "default", 2) }))
	requireNoError(t, cs.view(func(tx *bolt.Tx) error {
		_, ok, err := getIndexSeg(tx, "default", 2)
		if err != nil {
			return err
		}
		if ok {
			t.Fatal("deleteIndexSeg(default,2) did not remove the entry")
		}
		return nil
	}))
}

// TestControlStore_TxnAtomicity proves a failing update() leaves NO partial state:
// a put that succeeds, followed by a returned error in the SAME txn, rolls back
// the put. This is the "1 commit = 1 atomic structural change" guarantee that
// replaces the manifest tmp+rename+CRC dance.
func TestControlStore_TxnAtomicity(t *testing.T) {
	cs, _ := openTestControlStore(t)

	// Seed a committed baseline so we can prove the failed txn does not mutate it.
	requireNoError(t, cs.update(func(tx *bolt.Tx) error {
		return putSegment(tx, segmentEntry{SegID: 1, Gen: 0, VecCount: 10, TombCount: 0})
	}))

	sentinel := errors.New("boom")
	err := cs.update(func(tx *bolt.Tx) error {
		// Mutate the existing entry AND add a new one, then fail.
		if err := putSegment(tx, segmentEntry{SegID: 1, Gen: 9, VecCount: 999, TombCount: 9}); err != nil {
			return err
		}
		if err := putSegment(tx, segmentEntry{SegID: 2, Gen: 0, VecCount: 20, TombCount: 0}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("update should surface fn error, got %v", err)
	}

	requireNoError(t, cs.view(func(tx *bolt.Tx) error {
		e, ok, err := getSegment(tx, 1)
		if err != nil {
			return err
		}
		if !ok || e.Gen != 0 || e.VecCount != 10 {
			t.Fatalf("failed txn must not mutate seg 1: got %+v ok=%v", e, ok)
		}
		if _, ok, err := getSegment(tx, 2); err != nil {
			return err
		} else if ok {
			t.Fatal("failed txn must not add seg 2")
		}
		return nil
	}))
}

// TestControlStore_TxnAtomicityPanicRollback proves a panic inside update()
// rolls the transaction back too (bbolt rolls back on panic before re-raising),
// so a programming bug mid-structural-change cannot leave partial state on disk.
func TestControlStore_TxnAtomicityPanicRollback(t *testing.T) {
	cs, _ := openTestControlStore(t)

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic to propagate out of update")
			}
		}()
		_ = cs.update(func(tx *bolt.Tx) error {
			if err := putSegment(tx, segmentEntry{SegID: 5, Gen: 0, VecCount: 1, TombCount: 0}); err != nil {
				return err
			}
			panic("mid-txn bug")
		})
	}()

	requireNoError(t, cs.view(func(tx *bolt.Tx) error {
		if _, ok, err := getSegment(tx, 5); err != nil {
			return err
		} else if ok {
			t.Fatal("panic mid-txn must roll back the put")
		}
		return nil
	}))
}

// TestControlStore_CloseNilSafe proves Close tolerates a nil store/db (so Open
// error paths can defer cs.Close() unconditionally in later increments).
func TestControlStore_CloseNilSafe(t *testing.T) {
	var cs *controlStore
	if err := cs.Close(); err != nil {
		t.Fatalf("nil controlStore Close = %v, want nil", err)
	}
}

// TestControlStore_OpenErrorPath proves openControlStore surfaces a bolt.Open
// failure rather than returning a half-open store. Pointing the store "dir" at a
// regular file makes <file>/control.db unopenable (a path component is not a dir).
func TestControlStore_OpenErrorPath(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "iam-a-file")
	requireNoError(t, os.WriteFile(notADir, []byte("x"), 0600))
	cs, err := openControlStore(notADir)
	if err == nil {
		cs.Close()
		t.Fatal("openControlStore must fail when the dir path is a regular file")
	}
}

// TestControlStore_PutMetaReadOnlyTxnErrors drives putMeta's intermediate Put
// error returns: a Put on a bucket from a read-only view txn returns
// bolt.ErrTxNotWritable, so each Put guard in putMeta returns it rather than
// silently dropping a scalar. This pins the all-or-nothing write contract.
func TestControlStore_PutMetaReadOnlyTxnErrors(t *testing.T) {
	cs, _ := openTestControlStore(t)
	err := cs.view(func(tx *bolt.Tx) error {
		return putMeta(tx, 1, headSegID, Cosine)
	})
	if !errors.Is(err, bolt.ErrTxNotWritable) {
		t.Fatalf("putMeta in a read-only txn = %v, want ErrTxNotWritable", err)
	}
}

// TestControlStore_CorruptValuesRejected proves the decode guards reject a
// wrong-length value in each variable bucket instead of mis-reading garbage. The
// corrupt bytes are written directly through the raw bbolt handle (bypassing the
// put* encoders) to simulate a torn / future-format record.
func TestControlStore_CorruptValuesRejected(t *testing.T) {
	cs, _ := openTestControlStore(t)

	// Seed one malformed value in each variable-length bucket.
	requireNoError(t, cs.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(bktSegments).Put(segKey(1), []byte{0x01, 0x02}); err != nil { // want 20
			return err
		}
		if err := tx.Bucket(bktIndexes).Put([]byte("bad"), []byte{0x00}); err != nil { // want 14
			return err
		}
		if err := tx.Bucket(bktIndexSegs).Put(indexSegKey("bad", 1), []byte{0x00}); err != nil { // want 5
			return err
		}
		if err := tx.Bucket(bktAttrDecls).Put([]byte("bad"), []byte{0x01, 0x02}); err != nil { // want 1
			return err
		}
		return tx.Bucket(bktMeta).Put(metaKeyVersion, []byte{0x01}) // want 8 (with head+metric below)
	}))

	requireNoError(t, cs.view(func(tx *bolt.Tx) error {
		if _, err := listSegments(tx); err == nil {
			t.Error("listSegments must reject a wrong-length segment value")
		}
		if _, _, err := getSegment(tx, 1); err == nil {
			t.Error("getSegment must reject a wrong-length segment value")
		}
		if _, err := listIndexConfigs(tx); err == nil {
			t.Error("listIndexConfigs must reject a wrong-length config value")
		}
		if _, _, err := getIndexConfig(tx, "bad"); err == nil {
			t.Error("getIndexConfig must reject a wrong-length config value")
		}
		if _, err := listIndexSegs(tx); err == nil {
			t.Error("listIndexSegs must reject a wrong-length indexseg value")
		}
		if _, _, err := getIndexSeg(tx, "bad", 1); err == nil {
			t.Error("getIndexSeg must reject a wrong-length indexseg value")
		}
		if _, err := listAttrDecls(tx); err == nil {
			t.Error("listAttrDecls must reject a wrong-length attrdecl value")
		}
		return nil
	}))
}

// TestControlStore_MetaCorruptLengthRejected proves getMeta rejects a meta record
// whose scalar lengths are wrong (a torn/future write), distinct from the
// "not written yet" ok=false case which all-three-absent produces.
func TestControlStore_MetaCorruptLengthRejected(t *testing.T) {
	cs, _ := openTestControlStore(t)
	requireNoError(t, cs.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bktMeta)
		if err := b.Put(metaKeyVersion, []byte{0x01}); err != nil { // want 8
			return err
		}
		if err := b.Put(metaKeyHead, leU64(0)); err != nil {
			return err
		}
		return b.Put(metaKeyMetric, []byte{byte(Cosine)})
	}))
	err := cs.view(func(tx *bolt.Tx) error {
		_, _, _, ok, err := getMeta(tx)
		if err == nil {
			t.Errorf("getMeta must reject a wrong-length scalar (ok=%v)", ok)
		}
		return nil
	})
	requireNoError(t, err)
}

// TestControlStore_EnsureBucketsRejectsBlankName drives ensureBuckets' create-
// error branch (the schema-migration failure path openControlStore wraps): a
// blank bucket name makes CreateBucketIfNotExists return ErrBucketNameRequired,
// which must propagate wrapped rather than be swallowed.
func TestControlStore_EnsureBucketsRejectsBlankName(t *testing.T) {
	cs, _ := openTestControlStore(t)
	err := cs.db.Update(func(tx *bolt.Tx) error {
		return ensureBuckets(tx, [][]byte{[]byte("")})
	})
	if !errors.Is(err, bolt.ErrBucketNameRequired) {
		t.Fatalf("ensureBuckets blank name = %v, want ErrBucketNameRequired", err)
	}
}

// segIDs is a small test helper for readable failure messages.
func segIDs(es []segmentEntry) []segID {
	out := make([]segID, len(es))
	for i, e := range es {
		out[i] = e.SegID
	}
	return out
}

// TestControlStore_HeadRecordRoundTrip pins the head-bucket adapter: put two rows
// keyed by docId, list them back in ascending docId order with id/vector/norm/
// payload intact, delete one, and clear the rest.
func TestControlStore_HeadRecordRoundTrip(t *testing.T) {
	cs, _ := openTestControlStore(t)
	r1 := headRecord{ID: "alpha", DocID: 7, Stored: []float32{0.6, 0.8}, Norm: 5, Payload: []byte("pa")}
	r2 := headRecord{ID: "beta", DocID: 2, Stored: []float32{1, 2, 3}, Norm: 0, Payload: nil}
	requireNoError(t, cs.update(func(tx *bolt.Tx) error {
		if err := putHeadRecord(tx, r1); err != nil {
			return err
		}
		return putHeadRecord(tx, r2)
	}))

	var got []headRecord
	requireNoError(t, cs.view(func(tx *bolt.Tx) error {
		var lerr error
		got, lerr = listHeadRecords(tx)
		return lerr
	}))
	// Ascending docId order: docId 2 (beta) before docId 7 (alpha).
	if len(got) != 2 || got[0].ID != "beta" || got[1].ID != "alpha" {
		t.Fatalf("listHeadRecords order = %+v, want [beta(2) alpha(7)]", got)
	}
	if got[1].DocID != 7 || got[1].Norm != 5 || len(got[1].Stored) != 2 || got[1].Stored[1] != 0.8 || string(got[1].Payload) != "pa" {
		t.Fatalf("alpha row mismatch: %+v", got[1])
	}
	if len(got[0].Payload) != 0 {
		t.Fatalf("beta row should have empty payload, got %v", got[0].Payload)
	}

	// Delete beta, then clear the bucket.
	requireNoError(t, cs.update(func(tx *bolt.Tx) error { return deleteHeadRecord(tx, 2) }))
	requireNoError(t, cs.view(func(tx *bolt.Tx) error {
		recs, lerr := listHeadRecords(tx)
		if lerr != nil {
			return lerr
		}
		if len(recs) != 1 || recs[0].ID != "alpha" {
			t.Fatalf("after delete: %+v, want [alpha]", recs)
		}
		return nil
	}))
	requireNoError(t, cs.update(clearHead))
	requireNoError(t, cs.view(func(tx *bolt.Tx) error {
		recs, lerr := listHeadRecords(tx)
		if lerr != nil {
			return lerr
		}
		if len(recs) != 0 {
			t.Fatalf("after clearHead: %d rows, want 0", len(recs))
		}
		return nil
	}))
}

// TestControlStore_HeadValueCorruptionRejected drives decodeHeadValue's length-
// overrun guards: a too-short value, an id length that overruns, a vec length that
// overruns, and a payload length that overruns must each fail (loud), not panic.
func TestControlStore_HeadValueCorruptionRejected(t *testing.T) {
	good := encodeHeadValue(headRecord{ID: "x", DocID: 1, Stored: []float32{1, 2}, Norm: 1, Payload: []byte("p")})
	cases := map[string][]byte{
		"too short":        {0x00, 0x01},
		"id overruns":      {0xff, 0xff, 0xff, 0x7f}, // huge idLen, nothing after
		"vec overruns":     append([]byte(nil), corruptVecLen(good)...),
		"payload overruns": append([]byte(nil), corruptPayloadLen(good)...),
	}
	for name, v := range cases {
		if _, err := decodeHeadValue(1, v); err == nil {
			t.Fatalf("decodeHeadValue(%q) = nil error, want corruption error", name)
		}
	}
	// listHeadRecords must reject a wrong-length key, too.
	cs, _ := openTestControlStore(t)
	requireNoError(t, cs.update(func(tx *bolt.Tx) error {
		return tx.Bucket(bktHead).Put([]byte{0x01, 0x02}, good) // 2-byte key, want 8
	}))
	if err := cs.view(func(tx *bolt.Tx) error {
		_, lerr := listHeadRecords(tx)
		return lerr
	}); err == nil {
		t.Fatal("listHeadRecords must reject a wrong-length head key")
	}
}

// corruptVecLen rewrites the vec-length field of an encoded head value to a value
// that overruns the buffer. Layout: idLen(4)|id|norm(4)|vecLen(4)|...
func corruptVecLen(v []byte) []byte {
	out := append([]byte(nil), v...)
	idLen := int(uint32(out[0]) | uint32(out[1])<<8 | uint32(out[2])<<16 | uint32(out[3])<<24)
	off := 4 + idLen + 4 // past idLen, id, norm
	out[off] = 0xff
	out[off+1] = 0xff
	out[off+2] = 0xff
	out[off+3] = 0x7f
	return out
}

// corruptPayloadLen rewrites the payload-length field to overrun the buffer.
func corruptPayloadLen(v []byte) []byte {
	out := append([]byte(nil), v...)
	idLen := int(uint32(out[0]) | uint32(out[1])<<8 | uint32(out[2])<<16 | uint32(out[3])<<24)
	off := 4 + idLen + 4 // past idLen, id, norm → vecLen
	n := int(uint32(out[off]) | uint32(out[off+1])<<8 | uint32(out[off+2])<<16 | uint32(out[off+3])<<24)
	plOff := off + 4 + n*4 // past vecLen + vec → payloadLen
	out[plOff] = 0xff
	out[plOff+1] = 0xff
	out[plOff+2] = 0xff
	out[plOff+3] = 0x7f
	return out
}
