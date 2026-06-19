package vectorstore

import (
	"encoding/binary"
)

// segID identifies a records segment. The head has its own segId; sealed
// segments get monotonically increasing ones from the manifest version space.
type segID int64

// segState is the (index, segment) build state. In Phase 2 there is exactly one
// index (the default HNSW), so this lives directly on the segment entry.
type segState uint8

const (
	segPending segState = 0 // no graph yet → brute-searched
	segIndexed segState = 1 // graph built → graph-searched
)

// segmentEntry is the control-plane record for one sealed records segment, stored
// in the controlStore segments bucket keyed by SegID. The on-disk directory is
// derived as seg-<SegID>-<Gen>/ (paths are not stored; §4.8). The per-segment
// build state is NOT here — a segment's records are index-agnostic, so build state
// is per-(index,segment) in the indexsegs bucket (§4.8).
type segmentEntry struct {
	SegID     segID
	Gen       uint32
	VecCount  uint64
	TombCount uint64
}

// indexConfigEntry persists one named vector index's config (architecture §4.8
// "索引配置 name → VectorIndexConfig"), stored in the controlStore indexes bucket
// keyed by Name. Value bytes: type(1, 0=hnsw) | metric(1) | M(4) | EfConstruction(4)
// | EfSearch(4).
type indexConfigEntry struct {
	Name                        string
	Type                        string
	Metric                      Metric
	M, EfConstruction, EfSearch int
}

// indexSegEntry persists one (index, segment) build state (§4.8 "index-段:
// (indexName,segId)→{gen,状态}"), stored in the controlStore indexsegs bucket keyed
// by (name, segId). An entry with State == segPending means that index has no graph
// for that segment yet (served by the brute leg until built).
type indexSegEntry struct {
	Index string
	SegID segID
	Gen   uint32
	State segState
}

// attrDecl is one persisted attr-index declaration: the property name and its
// kind (Keyword/Numeric). The set of declarations is store-global config; it lives
// in the controlStore attrdecls bucket so a reopen knows which fields to index and
// every newly sealed/merged segment builds the right per-segment postings (§6/§7).
type attrDecl struct {
	Property string
	Kind     AttrKind
}

// manifest is the in-memory snapshot of the whole-store control plane. Since the
// bbolt migration it is no longer a serialized on-disk file: it is the carrier
// loadControlManifest fills by reading the controlStore buckets in one read-txn,
// and the shape sweepOrphansLocked iterates. Persistence is the controlStore
// write-txn in writeManifestLocked (one bbolt commit == one atomic structural
// change), replacing the former hand-rolled serialize+CRC32+tmp+fsync+rename+
// dir-fsync rewrite.
type manifest struct {
	Version   uint64
	Head      segID
	Metric    Metric
	AttrDecls []attrDecl // declared attr-index set
	Segments  []segmentEntry
	Indexes   []indexConfigEntry // named vector index configs
	IndexSegs []indexSegEntry    // per-(index,segment) build state
}

// indexTypeByte encodes a vector index Type for the controlStore indexes bucket.
// v1 only ever stores "hnsw" (CreateVectorIndex rejects any other Type), so the
// on-disk code is always 0; future types (e.g. ivfpq) add a switch here (new codes
// are backward-readable). Pairs with indexTypeFromByte, which decodes 0 -> "hnsw".
func indexTypeByte(string) byte { return 0 }

// indexTypeFromByte decodes the index Type byte. v1 only ever wrote 0 = hnsw.
func indexTypeFromByte(b byte) string { return "hnsw" }

// appendU32 appends v as 4 little-endian bytes — the package's little-endian append
// primitive for per-segment data-plane records (used by attrfile.go).
func appendU32(b []byte, v uint32) []byte {
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], v)
	return append(b, tmp[:]...)
}
