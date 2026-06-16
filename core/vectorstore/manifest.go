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
// directory is derived as seg-<SegID>-<Gen>/ (paths are not stored; §4.8).
type segmentEntry struct {
	SegID     segID
	Gen       uint32
	VecCount  uint64
	TombCount uint64
	State     segState
}

// manifest is the whole-store metadata snapshot, rewritten atomically on each
// structural change (seal). Per-write durability is the head WAL, not this file.
type manifest struct {
	Version  uint64
	Head     segID
	Metric   Metric
	Segments []segmentEntry
}

var magicManifest = [4]byte{'V', 'S', 'M', 'F'}

// manifestVersionByte is the on-disk format version. v2 added the persisted
// store Metric (1 byte after head) so Open can reject a metric mismatch: the
// on-disk vector form is metric-dependent, so reopening under a different metric
// silently mis-reads. The format predates any production data, so v1 is not
// read back — a v1 byte is rejected as an incompatible format.
const manifestVersionByte = 2

// serializeManifest encodes a manifest as: magic(4) | fmtver(1) | version(8) |
// head(8) | metric(1) | nSeg(4) | [segId(8) gen(4) vec(8) tomb(8) state(1)]* |
// crc32(4). The CRC covers everything before it.
func serializeManifest(m *manifest) []byte {
	body := make([]byte, 0, 4+1+8+8+1+4+len(m.Segments)*29+4)
	body = append(body, magicManifest[:]...)
	body = append(body, manifestVersionByte)
	body = appendU64(body, m.Version)
	body = appendU64(body, uint64(m.Head))
	body = append(body, byte(m.Metric))
	body = appendU32(body, uint32(len(m.Segments)))
	for _, e := range m.Segments {
		body = appendU64(body, uint64(e.SegID))
		body = appendU32(body, e.Gen)
		body = appendU64(body, e.VecCount)
		body = appendU64(body, e.TombCount)
		body = append(body, byte(e.State))
	}
	crc := crc32.ChecksumIEEE(body)
	return appendU32(body, crc)
}

func parseManifest(b []byte) (*manifest, error) {
	if len(b) < 4+1+8+8+1+4+4 {
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
	nSeg := int(binary.LittleEndian.Uint32(b[off:]))
	off += 4
	m.Segments = make([]segmentEntry, nSeg)
	for i := 0; i < nSeg; i++ {
		var e segmentEntry
		e.SegID = segID(binary.LittleEndian.Uint64(b[off:]))
		off += 8
		e.Gen = binary.LittleEndian.Uint32(b[off:])
		off += 4
		e.VecCount = binary.LittleEndian.Uint64(b[off:])
		off += 8
		e.TombCount = binary.LittleEndian.Uint64(b[off:])
		off += 8
		e.State = segState(b[off])
		off++
		m.Segments[i] = e
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
