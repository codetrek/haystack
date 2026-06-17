package vectorstore

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
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

// segmentEntry is the manifest record for one sealed records segment. The on-disk
// directory is derived as seg-<SegID>-<Gen>/ (paths are not stored; §4.8). As of
// v4 the per-segment build state moved OFF this entry to the per-(index,segment)
// IndexSegs block — a segment's records are index-agnostic, so its entry no longer
// carries a single State byte (§4.8).
type segmentEntry struct {
	SegID     segID
	Gen       uint32
	VecCount  uint64
	TombCount uint64
}

// indexConfigEntry persists one named vector index's config (architecture §4.8
// "索引配置 name → VectorIndexConfig"). On-disk bytes: nameLen(2) | name |
// type(1, 0=hnsw) | metric(1) | M(4) | EfConstruction(4) | EfSearch(4).
type indexConfigEntry struct {
	Name                        string
	Type                        string
	Metric                      Metric
	M, EfConstruction, EfSearch int
}

// indexSegEntry persists one (index, segment) build state (§4.8 "index-段:
// (indexName,segId)→{gen,状态}"). On-disk bytes: nameLen(2) | name | segId(8) |
// gen(4) | state(1). An entry with State == segPending means that index has no
// graph for that segment yet (served by the brute leg until built).
type indexSegEntry struct {
	Index string
	SegID segID
	Gen   uint32
	State segState
}

// attrDecl is one persisted attr-index declaration: the property name and its
// kind (Keyword/Numeric). The set of declarations is store-global config; it
// lives in the manifest so a reopen knows which fields to index and every
// newly sealed/merged segment builds the right per-segment postings (§6/§7).
type attrDecl struct {
	Property string
	Kind     AttrKind
}

// manifest is the whole-store metadata snapshot, rewritten atomically on each
// structural change (seal). Per-write durability is the head WAL, not this file.
type manifest struct {
	Version   uint64
	Head      segID
	Metric    Metric
	AttrDecls []attrDecl // declared attr-index set (v3)
	Segments  []segmentEntry
	Indexes   []indexConfigEntry // named vector index configs (v4)
	IndexSegs []indexSegEntry    // per-(index,segment) build state (v4)
}

var magicManifest = [4]byte{'V', 'S', 'M', 'F'}

// manifestVersionByte is the on-disk format version. v2 added the persisted
// store Metric (1 byte after head) so Open can reject a metric mismatch: the
// on-disk vector form is metric-dependent, so reopening under a different metric
// silently mis-reads. v3 adds the declared attr-index set (nDecls + [kind|prop])
// between metric and the segments block. v4 generalizes the single index to N
// named vector indexes: it DROPS the per-segment state byte (a segment's records
// are index-agnostic) and appends two blocks after the segments — the index
// configs (Indexes) and the per-(index,segment) states (IndexSegs). The format
// predates any production data, so an older byte is rejected as incompatible.
const manifestVersionByte = 4

// indexTypeByte encodes a vector index Type for the manifest. v1 only ever stores
// "hnsw" (CreateVectorIndex rejects any other Type), so the on-disk code is always
// 0; future types (e.g. ivfpq) add a switch here and bump no version (new codes are
// backward-readable). Pairs with indexTypeFromByte, which decodes 0 -> "hnsw".
func indexTypeByte(string) byte { return 0 }

// indexTypeFromByte decodes the index Type byte. v1 only ever wrote 0 = hnsw.
func indexTypeFromByte(b byte) string { return "hnsw" }

// serializeManifest encodes a manifest as: magic(4) | fmtver(1) | version(8) |
// head(8) | metric(1) | nDecls(4) | [kind(1) propLen(2) prop]* | nSeg(4) |
// [segId(8) gen(4) vec(8) tomb(8)]* | nIdx(4) |
// [nameLen(2) name type(1) metric(1) M(4) efC(4) efS(4)]* | nIdxSeg(4) |
// [nameLen(2) name segId(8) gen(4) state(1)]* | crc32(4). The CRC covers
// everything before it. (v4 drops the per-seg state byte; state is per-(index,
// segment) in the IndexSegs block.)
func serializeManifest(m *manifest) []byte {
	body := make([]byte, 0, 4+1+8+8+1+4+4+len(m.Segments)*28+4+len(m.Indexes)*24+4+len(m.IndexSegs)*23+4)
	body = append(body, magicManifest[:]...)
	body = append(body, manifestVersionByte)
	body = appendU64(body, m.Version)
	body = appendU64(body, uint64(m.Head))
	body = append(body, byte(m.Metric))
	body = appendU32(body, uint32(len(m.AttrDecls)))
	for _, d := range m.AttrDecls {
		body = append(body, byte(d.Kind))
		body = appendU16(body, uint16(len(d.Property)))
		body = append(body, d.Property...)
	}
	body = appendU32(body, uint32(len(m.Segments)))
	for _, e := range m.Segments {
		body = appendU64(body, uint64(e.SegID))
		body = appendU32(body, e.Gen)
		body = appendU64(body, e.VecCount)
		body = appendU64(body, e.TombCount)
	}
	// index configs (v4).
	body = appendU32(body, uint32(len(m.Indexes)))
	for _, ic := range m.Indexes {
		body = appendU16(body, uint16(len(ic.Name)))
		body = append(body, ic.Name...)
		body = append(body, indexTypeByte(ic.Type))
		body = append(body, byte(ic.Metric))
		body = appendU32(body, uint32(ic.M))
		body = appendU32(body, uint32(ic.EfConstruction))
		body = appendU32(body, uint32(ic.EfSearch))
	}
	// per-(index,segment) states (v4).
	body = appendU32(body, uint32(len(m.IndexSegs)))
	for _, is := range m.IndexSegs {
		body = appendU16(body, uint16(len(is.Index)))
		body = append(body, is.Index...)
		body = appendU64(body, uint64(is.SegID))
		body = appendU32(body, is.Gen)
		body = append(body, byte(is.State))
	}
	crc := crc32.ChecksumIEEE(body)
	return appendU32(body, crc)
}

func parseManifest(b []byte) (*manifest, error) {
	if len(b) < 4+1+8+8+1+4+4+4 {
		return nil, fmt.Errorf("manifest: too short (%d bytes)", len(b))
	}
	stored := binary.LittleEndian.Uint32(b[len(b)-4:])
	if crc32.ChecksumIEEE(b[:len(b)-4]) != stored {
		return nil, fmt.Errorf("manifest: CRC mismatch (corrupt)")
	}
	if string(b[0:4]) != string(magicManifest[:]) {
		return nil, fmt.Errorf("manifest: bad magic %q", b[0:4])
	}
	if b[4] != manifestVersionByte {
		return nil, fmt.Errorf("manifest: unsupported format version %d (want %d)", b[4], manifestVersionByte)
	}
	off := 5 // skip magic(4)+fmtver(1)
	m := &manifest{}
	m.Version = binary.LittleEndian.Uint64(b[off:])
	off += 8
	m.Head = segID(binary.LittleEndian.Uint64(b[off:]))
	off += 8
	m.Metric = Metric(b[off])
	off++
	if off+4 > len(b)-4 {
		return nil, fmt.Errorf("manifest: truncated attr-decl count")
	}
	nDecls := int(binary.LittleEndian.Uint32(b[off:]))
	off += 4
	m.AttrDecls = make([]attrDecl, 0, nDecls)
	for i := 0; i < nDecls; i++ {
		if off+3 > len(b)-4 {
			return nil, fmt.Errorf("manifest: truncated attr-decl header")
		}
		kind := AttrKind(b[off])
		off++
		pl := int(binary.LittleEndian.Uint16(b[off:]))
		off += 2
		if off+pl > len(b)-4 {
			return nil, fmt.Errorf("manifest: truncated attr-decl property")
		}
		m.AttrDecls = append(m.AttrDecls, attrDecl{Property: string(b[off : off+pl]), Kind: kind})
		off += pl
	}
	if off+4 > len(b)-4 {
		return nil, fmt.Errorf("manifest: truncated segment count")
	}
	nSeg := int(binary.LittleEndian.Uint32(b[off:]))
	off += 4
	m.Segments = make([]segmentEntry, nSeg)
	for i := 0; i < nSeg; i++ {
		if off+28 > len(b)-4 {
			return nil, fmt.Errorf("manifest: truncated segment entry")
		}
		var e segmentEntry
		e.SegID = segID(binary.LittleEndian.Uint64(b[off:]))
		off += 8
		e.Gen = binary.LittleEndian.Uint32(b[off:])
		off += 4
		e.VecCount = binary.LittleEndian.Uint64(b[off:])
		off += 8
		e.TombCount = binary.LittleEndian.Uint64(b[off:])
		off += 8
		m.Segments[i] = e
	}
	// index configs (v4)
	if off+4 > len(b)-4 {
		return nil, fmt.Errorf("manifest: truncated index count")
	}
	nIdx := int(binary.LittleEndian.Uint32(b[off:]))
	off += 4
	m.Indexes = make([]indexConfigEntry, 0, nIdx)
	for i := 0; i < nIdx; i++ {
		if off+2 > len(b)-4 {
			return nil, fmt.Errorf("manifest: truncated index name len")
		}
		nl := int(binary.LittleEndian.Uint16(b[off:]))
		off += 2
		if off+nl+14 > len(b)-4 {
			return nil, fmt.Errorf("manifest: truncated index config")
		}
		name := string(b[off : off+nl])
		off += nl
		typ := indexTypeFromByte(b[off])
		off++
		met := Metric(b[off])
		off++
		mM := int(binary.LittleEndian.Uint32(b[off:]))
		off += 4
		efC := int(binary.LittleEndian.Uint32(b[off:]))
		off += 4
		efS := int(binary.LittleEndian.Uint32(b[off:]))
		off += 4
		m.Indexes = append(m.Indexes, indexConfigEntry{Name: name, Type: typ, Metric: met, M: mM, EfConstruction: efC, EfSearch: efS})
	}
	// per-(index,segment) states (v4)
	if off+4 > len(b)-4 {
		return nil, fmt.Errorf("manifest: truncated index-seg count")
	}
	nIS := int(binary.LittleEndian.Uint32(b[off:]))
	off += 4
	m.IndexSegs = make([]indexSegEntry, 0, nIS)
	for i := 0; i < nIS; i++ {
		if off+2 > len(b)-4 {
			return nil, fmt.Errorf("manifest: truncated index-seg name len")
		}
		nl := int(binary.LittleEndian.Uint16(b[off:]))
		off += 2
		if off+nl+13 > len(b)-4 {
			return nil, fmt.Errorf("manifest: truncated index-seg entry")
		}
		idx := string(b[off : off+nl])
		off += nl
		sid := segID(binary.LittleEndian.Uint64(b[off:]))
		off += 8
		gen := binary.LittleEndian.Uint32(b[off:])
		off += 4
		st := segState(b[off])
		off++
		m.IndexSegs = append(m.IndexSegs, indexSegEntry{Index: idx, SegID: sid, Gen: gen, State: st})
	}
	return m, nil
}

// writeManifest atomically rewrites dir/manifest via tmp+fsync+rename+dir-fsync
// (the audit-#76 pattern). On any step error the tmp file is removed.
func writeManifest(dir string, m *manifest) error {
	tmp := filepath.Join(dir, "manifest.tmp")
	final := filepath.Join(dir, "manifest")
	f, err := fsCreate(tmp)
	if err != nil {
		return fmt.Errorf("writeManifest: create tmp: %w", err)
	}
	if _, err := f.Write(serializeManifest(m)); err != nil {
		f.Close()
		fsRemove(tmp)
		return fmt.Errorf("writeManifest: write: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		fsRemove(tmp)
		return fmt.Errorf("writeManifest: sync: %w", err)
	}
	if err := f.Close(); err != nil {
		fsRemove(tmp)
		return fmt.Errorf("writeManifest: close: %w", err)
	}
	if err := fsRename(tmp, final); err != nil {
		fsRemove(tmp)
		return fmt.Errorf("writeManifest: rename: %w", err)
	}
	if err := fsyncDir(dir); err != nil {
		return fmt.Errorf("writeManifest: fsync dir: %w", err)
	}
	return nil
}

// readManifest loads dir/manifest. A missing file returns an os.IsNotExist error
// (a fresh store); a corrupt CRC returns a non-nil error.
func readManifest(dir string) (*manifest, error) {
	path := filepath.Join(dir, "manifest")
	f, err := fsOpen(path)
	if err != nil {
		return nil, err // os.IsNotExist for a fresh store
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	buf := make([]byte, st.Size())
	if _, err := f.ReadAt(buf, 0); err != nil {
		return nil, err
	}
	return parseManifest(buf)
}

func appendU32(b []byte, v uint32) []byte {
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], v)
	return append(b, tmp[:]...)
}

func appendU64(b []byte, v uint64) []byte {
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], v)
	return append(b, tmp[:]...)
}

// ensure os import is used even if readManifest's error path is the only consumer.
var _ = os.IsNotExist
