package vectorstore

import (
	"encoding/binary"
	"fmt"
	"math"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

// controlStore is the bbolt-backed CONTROL plane of a vectorstore directory: the
// small, transactional metadata that used to live in the hand-rolled manifest +
// records.wal + tomb.dat + recovery rescan (durability.md). It owns a single
// bbolt DB at <dir>/control.db and exposes typed get/put helpers plus a write-txn
// wrapper so one bbolt commit == one atomic structural change.
//
// PLANE BOUNDARY (load-bearing): only the control plane lives here — manifest
// metadata, the durable head record, the docId→segId map, and tombstones. The
// DATA plane (sealed-segment vectors.dat + graph-<name>.dat and the zero-copy
// getVectorRef mmap aliasing) STAYS flat-mmap and is never moved into bbolt:
// boxing bulk vectors/graphs in bbolt would destroy the zero-copy hot path and
// bloat the file under long-lived read transactions.
//
// This is the foundation increment: the type, the bucket schema, and the
// serialization adapter for the control-plane records. The head bucket is wired
// into Store as of increment 2 (head record durability replaces records.wal); the
// docseg/tomb buckets are created on open but filled in by later increments
// (recovery map, tombstones).
type controlStore struct {
	db   *bolt.DB
	path string

	// failNextUpdate, when non-nil, is returned by the NEXT update() call (which it
	// then clears) INSTEAD of running the bbolt txn — a test-only seam to force a
	// failed durable commit on the real Put/Delete path (the bbolt analog of the old
	// WAL fsync fault). bbolt opens its own file internally, so the osFile fault hook
	// cannot reach its fsync; this seam injects the same "the durable commit failed"
	// condition at the one call site Put/Delete go through. Nil in production.
	failNextUpdate error
	// failClose, when non-nil, is returned by Close() (after still releasing the OS
	// lock) — a test-only seam to drive Store.Close's control-store error path. Nil
	// in production.
	failClose error
}

// controlDBName is the bbolt file inside a store directory. It sits beside the
// flat data-plane seg-* dirs and (during migration) the legacy manifest.
const controlDBName = "control.db"

// Bucket names. Each is one logical control-plane table.
var (
	bktMeta      = []byte("meta")      // fixed keys: version, headSegId, primaryMetric
	bktAttrDecls = []byte("attrdecls") // property → kind(1)
	bktSegments  = []byte("segments")  // segId(8) → {gen, vecCount, tombCount}
	bktIndexes   = []byte("indexes")   // name → {type, metric, M, efC, efS}
	bktIndexSegs = []byte("indexsegs") // (name,segId) → {gen, state}
	bktHead      = []byte("head")      // docId(8) → {id, vector, norm, payload} (incr 2)
	bktDocSeg    = []byte("docseg")    // docId(8) → segId(8)                (incr 4/3)
	bktTomb      = []byte("tomb")      // (segId,slot) → present              (incr 3)
)

// allBuckets is the full set created on open, in a stable order. docseg/tomb are
// created empty now and populated by later increments; head is wired in incr 2.
var allBuckets = [][]byte{
	bktMeta, bktAttrDecls, bktSegments, bktIndexes, bktIndexSegs,
	bktHead, bktDocSeg, bktTomb,
}

// Fixed keys inside the meta bucket.
var (
	metaKeyVersion = []byte("version")       // uint64 manifest version (LE)
	metaKeyHead    = []byte("headSegId")     // int64 head segId (LE)
	metaKeyMetric  = []byte("primaryMetric") // 1 byte Metric
)

// openControlStore opens (creating if absent) the bbolt control DB at
// <dir>/control.db and ensures every bucket in the schema exists. The caller owns
// the directory; openControlStore does not create it. The single open write-txn
// that creates the buckets is the only schema migration point.
func openControlStore(dir string) (*controlStore, error) {
	path := filepath.Join(dir, controlDBName)
	// Timeout guards against a stale flock from a crashed peer holding the file;
	// bbolt takes an exclusive OS lock, so a second opener fails fast instead of
	// blocking forever. NoSync stays OFF: control-plane writes must be durable.
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("controlstore: open %s: %w", path, err)
	}
	cs := &controlStore{db: db, path: path}
	if err := db.Update(func(tx *bolt.Tx) error {
		return ensureBuckets(tx, allBuckets)
	}); err != nil {
		db.Close()
		return nil, err
	}
	return cs, nil
}

// ensureBuckets creates every named bucket in tx if it is not already present.
// It is the single schema-migration point; CreateBucketIfNotExists is idempotent,
// so a reopen is a no-op. A blank/invalid bucket name surfaces the bbolt error.
func ensureBuckets(tx *bolt.Tx, names [][]byte) error {
	for _, name := range names {
		if _, err := tx.CreateBucketIfNotExists(name); err != nil {
			return fmt.Errorf("controlstore: create bucket %q: %w", name, err)
		}
	}
	return nil
}

// Close releases the bbolt DB and its OS lock. Safe to call on a nil store.
func (cs *controlStore) Close() error {
	if cs == nil || cs.db == nil {
		return nil
	}
	if cs.failClose != nil {
		_ = cs.db.Close() // still release the OS lock; surface the injected error
		return cs.failClose
	}
	return cs.db.Close()
}

// update runs fn inside a single bbolt read-write transaction. The transaction
// commits atomically when fn returns nil and is fully rolled back (no partial
// state) when fn returns an error or panics — this is the "1 commit = 1 atomic
// structural change" boundary that replaces the manifest tmp+fsync+rename+CRC
// rewrite. Callers compose several put* helpers inside one update() to make a
// structural change (e.g. a seal) a single durable step.
func (cs *controlStore) update(fn func(tx *bolt.Tx) error) error {
	if cs.failNextUpdate != nil {
		err := cs.failNextUpdate
		cs.failNextUpdate = nil
		return err
	}
	return cs.db.Update(fn)
}

// view runs fn inside a single bbolt read-only transaction.
func (cs *controlStore) view(fn func(tx *bolt.Tx) error) error {
	return cs.db.View(fn)
}

// --- meta bucket -----------------------------------------------------------

// putMeta writes the three store-global scalars (manifest version, head segId,
// primary metric) in tx. It is the bbolt replacement for the manifest header.
func putMeta(tx *bolt.Tx, version uint64, head segID, m Metric) error {
	b := tx.Bucket(bktMeta)
	if err := b.Put(metaKeyVersion, leU64(uint64(version))); err != nil {
		return err
	}
	if err := b.Put(metaKeyHead, leU64(uint64(head))); err != nil {
		return err
	}
	return b.Put(metaKeyMetric, []byte{byte(m)})
}

// getMeta reads the store-global scalars. ok is false when the meta bucket has
// not been written yet (a fresh control DB), so the caller can treat it as a new
// store exactly as a missing manifest did.
func getMeta(tx *bolt.Tx) (version uint64, head segID, m Metric, ok bool, err error) {
	b := tx.Bucket(bktMeta)
	v := b.Get(metaKeyVersion)
	h := b.Get(metaKeyHead)
	mt := b.Get(metaKeyMetric)
	if v == nil || h == nil || mt == nil {
		return 0, 0, 0, false, nil
	}
	if len(v) != 8 || len(h) != 8 || len(mt) != 1 {
		return 0, 0, 0, false, fmt.Errorf("controlstore: corrupt meta record")
	}
	return binary.LittleEndian.Uint64(v), segID(binary.LittleEndian.Uint64(h)), Metric(mt[0]), true, nil
}

// --- attrdecls bucket ------------------------------------------------------

// putAttrDecl declares (or re-declares) one attr-index property → kind. The key
// is the property name; the value is the single kind byte. Idempotent.
func putAttrDecl(tx *bolt.Tx, d attrDecl) error {
	return tx.Bucket(bktAttrDecls).Put([]byte(d.Property), []byte{byte(d.Kind)})
}

// listAttrDecls returns all declared attr indexes, in bbolt key (property) order.
func listAttrDecls(tx *bolt.Tx) ([]attrDecl, error) {
	var out []attrDecl
	c := tx.Bucket(bktAttrDecls).Cursor()
	for k, v := c.First(); k != nil; k, v = c.Next() {
		if len(v) != 1 {
			return nil, fmt.Errorf("controlstore: corrupt attrdecl %q", k)
		}
		out = append(out, attrDecl{Property: string(k), Kind: AttrKind(v[0])})
	}
	return out, nil
}

// --- segments bucket -------------------------------------------------------

// putSegment writes one sealed-segment entry keyed by segId. This replaces the
// manifest segments block; the on-disk dir name stays derived (seg-<id>-<gen>/),
// so the value carries gen + counts only, no path. Value layout: gen(4) |
// vecCount(8) | tombCount(8).
func putSegment(tx *bolt.Tx, e segmentEntry) error {
	val := make([]byte, 4+8+8)
	binary.LittleEndian.PutUint32(val[0:], e.Gen)
	binary.LittleEndian.PutUint64(val[4:], e.VecCount)
	binary.LittleEndian.PutUint64(val[12:], e.TombCount)
	return tx.Bucket(bktSegments).Put(segKey(e.SegID), val)
}

// getSegment reads one segment entry; ok is false if that segId is absent.
func getSegment(tx *bolt.Tx, id segID) (segmentEntry, bool, error) {
	v := tx.Bucket(bktSegments).Get(segKey(id))
	if v == nil {
		return segmentEntry{}, false, nil
	}
	e, err := decodeSegment(id, v)
	return e, err == nil, err
}

// deleteSegment removes a segment entry (used by merge when old inputs retire).
func deleteSegment(tx *bolt.Tx, id segID) error {
	return tx.Bucket(bktSegments).Delete(segKey(id))
}

// listSegments returns every segment entry, ordered by segId (bbolt iterates keys
// in byte order; segKey is big-endian so byte order == numeric order).
func listSegments(tx *bolt.Tx) ([]segmentEntry, error) {
	var out []segmentEntry
	c := tx.Bucket(bktSegments).Cursor()
	for k, v := c.First(); k != nil; k, v = c.Next() {
		if len(k) != 8 {
			return nil, fmt.Errorf("controlstore: corrupt segment key (len %d)", len(k))
		}
		e, err := decodeSegment(segID(binary.BigEndian.Uint64(k)), v)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func decodeSegment(id segID, v []byte) (segmentEntry, error) {
	if len(v) != 20 {
		return segmentEntry{}, fmt.Errorf("controlstore: corrupt segment value (len %d)", len(v))
	}
	return segmentEntry{
		SegID:     id,
		Gen:       binary.LittleEndian.Uint32(v[0:]),
		VecCount:  binary.LittleEndian.Uint64(v[4:]),
		TombCount: binary.LittleEndian.Uint64(v[12:]),
	}, nil
}

// --- indexes bucket --------------------------------------------------------

// putIndexConfig writes one named vector index config keyed by name. Value
// layout: type(1) | metric(1) | M(4) | efC(4) | efS(4). The type byte reuses the
// manifest's indexTypeByte encoding (0 == hnsw) so the two formats agree.
func putIndexConfig(tx *bolt.Tx, ic indexConfigEntry) error {
	val := make([]byte, 1+1+4+4+4)
	val[0] = indexTypeByte(ic.Type)
	val[1] = byte(ic.Metric)
	binary.LittleEndian.PutUint32(val[2:], uint32(ic.M))
	binary.LittleEndian.PutUint32(val[6:], uint32(ic.EfConstruction))
	binary.LittleEndian.PutUint32(val[10:], uint32(ic.EfSearch))
	return tx.Bucket(bktIndexes).Put([]byte(ic.Name), val)
}

// getIndexConfig reads one named index config; ok is false if absent.
func getIndexConfig(tx *bolt.Tx, name string) (indexConfigEntry, bool, error) {
	v := tx.Bucket(bktIndexes).Get([]byte(name))
	if v == nil {
		return indexConfigEntry{}, false, nil
	}
	ic, err := decodeIndexConfig(name, v)
	return ic, err == nil, err
}

// listIndexConfigs returns every index config, ordered by name.
func listIndexConfigs(tx *bolt.Tx) ([]indexConfigEntry, error) {
	var out []indexConfigEntry
	c := tx.Bucket(bktIndexes).Cursor()
	for k, v := c.First(); k != nil; k, v = c.Next() {
		ic, err := decodeIndexConfig(string(k), v)
		if err != nil {
			return nil, err
		}
		out = append(out, ic)
	}
	return out, nil
}

func decodeIndexConfig(name string, v []byte) (indexConfigEntry, error) {
	if len(v) != 14 {
		return indexConfigEntry{}, fmt.Errorf("controlstore: corrupt index config %q (len %d)", name, len(v))
	}
	return indexConfigEntry{
		Name:           name,
		Type:           indexTypeFromByte(v[0]),
		Metric:         Metric(v[1]),
		M:              int(binary.LittleEndian.Uint32(v[2:])),
		EfConstruction: int(binary.LittleEndian.Uint32(v[6:])),
		EfSearch:       int(binary.LittleEndian.Uint32(v[10:])),
	}, nil
}

// --- indexsegs bucket ------------------------------------------------------

// putIndexSeg writes one (index, segment) build-state entry. Key: name |
// segId(8, big-endian) so a cursor scan groups by name then segId. Value:
// gen(4) | state(1).
func putIndexSeg(tx *bolt.Tx, is indexSegEntry) error {
	val := make([]byte, 4+1)
	binary.LittleEndian.PutUint32(val[0:], is.Gen)
	val[4] = byte(is.State)
	return tx.Bucket(bktIndexSegs).Put(indexSegKey(is.Index, is.SegID), val)
}

// getIndexSeg reads one (index, segment) state; ok is false if absent.
func getIndexSeg(tx *bolt.Tx, index string, id segID) (indexSegEntry, bool, error) {
	v := tx.Bucket(bktIndexSegs).Get(indexSegKey(index, id))
	if v == nil {
		return indexSegEntry{}, false, nil
	}
	if len(v) != 5 {
		return indexSegEntry{}, false, fmt.Errorf("controlstore: corrupt indexseg value (len %d)", len(v))
	}
	return indexSegEntry{
		Index: index,
		SegID: id,
		Gen:   binary.LittleEndian.Uint32(v[0:]),
		State: segState(v[4]),
	}, true, nil
}

// deleteIndexSeg removes one (index, segment) entry (merge retires old inputs).
func deleteIndexSeg(tx *bolt.Tx, index string, id segID) error {
	return tx.Bucket(bktIndexSegs).Delete(indexSegKey(index, id))
}

// listIndexSegs returns every (index, segment) entry, ordered by (name, segId).
func listIndexSegs(tx *bolt.Tx) ([]indexSegEntry, error) {
	var out []indexSegEntry
	c := tx.Bucket(bktIndexSegs).Cursor()
	for k, v := c.First(); k != nil; k, v = c.Next() {
		if len(k) < 8 || len(v) != 5 {
			return nil, fmt.Errorf("controlstore: corrupt indexseg entry")
		}
		name := string(k[:len(k)-8])
		id := segID(binary.BigEndian.Uint64(k[len(k)-8:]))
		out = append(out, indexSegEntry{
			Index: name,
			SegID: id,
			Gen:   binary.LittleEndian.Uint32(v[0:]),
			State: segState(v[4]),
		})
	}
	return out, nil
}

// --- head bucket -----------------------------------------------------------
//
// The head bucket is the durable form of the in-memory brute head: one row per
// LIVE head docId, written transactionally on every Put/Delete (replacing the
// per-write append+fsync records.wal). One bbolt commit per write IS the
// durability boundary — a returned Put is durable because db.Update fsyncs the
// committing page. The in-memory flat head stays for brute-search speed and is
// rebuilt from this bucket at Open (rebuildHead, NOT a WAL replay).
//
// Value layout: idLen(4) | id | norm(4) | vecLen(4) | vec(N*4) | plLen(4) | pl.
// The string id is stored alongside the vector for the SAME reason the WAL
// stored it: it makes the id↔docId mapping crash-safe independently of idtable's
// lazily-committed batch. On Open, rebuildHead re-drives the allocator for each
// id (reconstructing the same monotonic docId) and rebuilds idToDoc, so a crash
// with no idtable commit still recovers every head doc's mapping.

// headRecord is one durable head row: the caller's string id, its docId, the
// stored-form vector + norm, and the serialized payload blob.
type headRecord struct {
	ID      string
	DocID   int64
	Stored  []float32
	Norm    float32
	Payload []byte
}

// putString writes a length-prefixed string (len uint32 LE, then bytes) at off
// and returns the offset past it. getString reverses it.
func putString(buf []byte, off int, s string) int {
	binary.LittleEndian.PutUint32(buf[off:], uint32(len(s)))
	off += 4
	copy(buf[off:], s)
	return off + len(s)
}

// encodeHeadValue serializes everything but the docId (which is the bucket key).
func encodeHeadValue(r headRecord) []byte {
	size := 4 + len(r.ID) + 4 + 4 + len(r.Stored)*4 + 4 + len(r.Payload)
	buf := make([]byte, size)
	off := putString(buf, 0, r.ID)
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(r.Norm))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], uint32(len(r.Stored)))
	off += 4
	for _, v := range r.Stored {
		binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(v))
		off += 4
	}
	binary.LittleEndian.PutUint32(buf[off:], uint32(len(r.Payload)))
	off += 4
	copy(buf[off:], r.Payload)
	return buf
}

// decodeHeadValue reverses encodeHeadValue, supplying the docId from the key. It
// validates every length against the buffer so a structurally corrupt value
// fails recovery loud rather than panicking on an out-of-range slice (the bbolt
// analog of the old WAL CRC/torn-tail rejection).
func decodeHeadValue(docID int64, v []byte) (headRecord, error) {
	r := headRecord{DocID: docID}
	if len(v) < 4 {
		return r, fmt.Errorf("controlstore: corrupt head value (len %d)", len(v))
	}
	idLen := int(binary.LittleEndian.Uint32(v))
	off := 4
	if idLen < 0 || off+idLen+4+4 > len(v) {
		return r, fmt.Errorf("controlstore: corrupt head value (id len %d overruns %d)", idLen, len(v))
	}
	r.ID = string(v[off : off+idLen])
	off += idLen
	r.Norm = math.Float32frombits(binary.LittleEndian.Uint32(v[off:]))
	off += 4
	n := int(binary.LittleEndian.Uint32(v[off:]))
	off += 4
	if n < 0 || off+n*4+4 > len(v) {
		return r, fmt.Errorf("controlstore: corrupt head value (vec len %d overruns %d)", n, len(v))
	}
	r.Stored = make([]float32, n)
	for i := 0; i < n; i++ {
		r.Stored[i] = math.Float32frombits(binary.LittleEndian.Uint32(v[off:]))
		off += 4
	}
	pl := int(binary.LittleEndian.Uint32(v[off:]))
	off += 4
	if pl < 0 || off+pl > len(v) {
		return r, fmt.Errorf("controlstore: corrupt head value (payload len %d overruns %d)", pl, len(v))
	}
	if pl > 0 {
		r.Payload = make([]byte, pl)
		copy(r.Payload, v[off:off+pl])
	}
	return r, nil
}

// putHeadRecord writes (or replaces) one head row keyed by docId. Called inside
// the Put write-txn; the commit makes the row durable.
func putHeadRecord(tx *bolt.Tx, r headRecord) error {
	return tx.Bucket(bktHead).Put(segKey(segID(r.DocID)), encodeHeadValue(r))
}

// deleteHeadRecord removes the head row for docId (a head Delete). Deleting an
// absent key is a no-op in bbolt.
func deleteHeadRecord(tx *bolt.Tx, docID int64) error {
	return tx.Bucket(bktHead).Delete(segKey(segID(docID)))
}

// clearHead empties the head bucket in tx. Seal calls it in the SAME write-txn
// that adds the new sealed segment, so the head rows move out of the head bucket
// and into the sealed flat segment atomically: a crash leaves either the old head
// (no segment) or the new segment (empty head), never both.
func clearHead(tx *bolt.Tx) error {
	b := tx.Bucket(bktHead)
	var doomed [][]byte
	c := b.Cursor()
	for k, _ := c.First(); k != nil; k, _ = c.Next() {
		doomed = append(doomed, append([]byte(nil), k...))
	}
	for _, k := range doomed {
		if err := b.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// listHeadRecords returns every head row in docId order (segKey is big-endian, so
// the cursor yields ascending docId — the monotonic Put insertion order the flat
// head must be rebuilt in). The slice is the recovery feed for rebuildHead.
func listHeadRecords(tx *bolt.Tx) ([]headRecord, error) {
	var out []headRecord
	c := tx.Bucket(bktHead).Cursor()
	for k, v := c.First(); k != nil; k, v = c.Next() {
		if len(k) != 8 {
			return nil, fmt.Errorf("controlstore: corrupt head key (len %d)", len(k))
		}
		r, err := decodeHeadValue(int64(binary.BigEndian.Uint64(k)), v)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// --- reconciliation deletes -----------------------------------------------
//
// writeManifestLocked is a full reconciliation: it (re)writes every live record
// and then deletes any bucket key no longer backed by live in-memory state. A
// merge replacing N inputs with M outputs commits both the new keys and the
// retired-key deletes in ONE txn, so the segment/index-seg set is never
// transiently inconsistent on disk. Deleting during a cursor scan is safe in
// bbolt as long as we re-seek by collecting the doomed keys first.

// deleteAttrDeclsNotIn removes every attrdecl whose property is not a key of
// keep (the live attr-index set).
func deleteAttrDeclsNotIn(tx *bolt.Tx, keep map[string]AttrKind) error {
	b := tx.Bucket(bktAttrDecls)
	var doomed [][]byte
	c := b.Cursor()
	for k, _ := c.First(); k != nil; k, _ = c.Next() {
		if _, ok := keep[string(k)]; !ok {
			doomed = append(doomed, append([]byte(nil), k...))
		}
	}
	for _, k := range doomed {
		if err := b.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// deleteSegmentsNotIn removes every segment entry whose segId is not in keep
// (the live sealed-segment set) — i.e. inputs a merge just retired.
func deleteSegmentsNotIn(tx *bolt.Tx, keep map[segID]bool) error {
	b := tx.Bucket(bktSegments)
	var doomed [][]byte
	c := b.Cursor()
	for k, _ := c.First(); k != nil; k, _ = c.Next() {
		if len(k) != 8 || !keep[segID(binary.BigEndian.Uint64(k))] {
			doomed = append(doomed, append([]byte(nil), k...))
		}
	}
	for _, k := range doomed {
		if err := b.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// deleteIndexConfigsNotIn removes every index config whose name is not in keep
// (the live index set) — a Drop just removed it.
func deleteIndexConfigsNotIn(tx *bolt.Tx, keep map[string]bool) error {
	b := tx.Bucket(bktIndexes)
	var doomed [][]byte
	c := b.Cursor()
	for k, _ := c.First(); k != nil; k, _ = c.Next() {
		if !keep[string(k)] {
			doomed = append(doomed, append([]byte(nil), k...))
		}
	}
	for _, k := range doomed {
		if err := b.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// deleteIndexSegsNotIn removes every (index, segment) entry for which keep
// returns false — a merge retired the segment or a Drop removed the index.
func deleteIndexSegsNotIn(tx *bolt.Tx, keep func(index string, id segID) bool) error {
	b := tx.Bucket(bktIndexSegs)
	var doomed [][]byte
	c := b.Cursor()
	for k, _ := c.First(); k != nil; k, _ = c.Next() {
		if len(k) < 8 {
			doomed = append(doomed, append([]byte(nil), k...))
			continue
		}
		name := string(k[:len(k)-8])
		id := segID(binary.BigEndian.Uint64(k[len(k)-8:]))
		if !keep(name, id) {
			doomed = append(doomed, append([]byte(nil), k...))
		}
	}
	for _, k := range doomed {
		if err := b.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// --- key encoders ----------------------------------------------------------

// segKey encodes a segId as 8 big-endian bytes so bbolt's byte-ordered cursor
// iterates segments in ascending numeric id order.
func segKey(id segID) []byte {
	var k [8]byte
	binary.BigEndian.PutUint64(k[:], uint64(id))
	return k[:]
}

// indexSegKey encodes (name, segId) as name-bytes || segId(8, big-endian). The
// name is the prefix so a cursor seek to a name scans that index's segments in
// id order; the fixed 8-byte big-endian suffix keeps the boundary unambiguous
// for any name (names never contain a trailing 8-byte numeric collision because
// the suffix length is fixed and split off by position, not by a separator).
func indexSegKey(name string, id segID) []byte {
	k := make([]byte, len(name)+8)
	copy(k, name)
	binary.BigEndian.PutUint64(k[len(name):], uint64(id))
	return k
}

// leU64 encodes v as 8 little-endian bytes (meta scalar values). Big-endian is
// used only where byte order must equal numeric order for cursor iteration
// (segKey/indexSegKey); fixed-key meta values have no ordering requirement.
func leU64(v uint64) []byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	return b[:]
}
