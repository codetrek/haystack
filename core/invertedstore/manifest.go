package invertedstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// segMeta describes one immutable, live segment file in the MANIFEST: its seal-sequence id
// (== the seg-%06d.dat suffix), its merge level, the codec ids needed to read its data
// blocks and term-dict region (a reader must never guess a region's codec), the tableId
// range it covers (for prune-by-table on Search/merge), and its on-disk size.
type segMeta struct {
	Id        uint64 `json:"id"`
	Level     int    `json:"level"`
	DataCodec byte   `json:"dataCodec"`
	DictCodec byte   `json:"dictCodec"`
	MinTable  uint32 `json:"minTable"`
	MaxTable  uint32 `json:"maxTable"`
	Size      int64  `json:"size"`
}

// tableInfo is one entry of the table catalog (replaces pebble's table rows).
type tableInfo struct {
	Id          int       `json:"id"`
	CreatedAt   time.Time `json:"createdAt"`
	Description string    `json:"description"`
}

// manifest is the store's only durable metadata: the storage version, the live segment set,
// the table catalog, and the monotonic next-ids. It carries NO recovery watermark — recovery
// is indexer-driven (design §9), so the store need only be crash-consistent. It is replaced
// atomically (write MANIFEST.tmp, fsync, rename, fsync dir) on every seal/merge/table change.
type manifest struct {
	FormatVersion  int               `json:"formatVersion"` // bump on any breaking manifest change
	StorageVersion string            `json:"storageVersion"`
	Segments       []segMeta         `json:"segments"`
	Tables         map[int]tableInfo `json:"tables"`
	NextTableId    int               `json:"nextTableId"`
	NextSegId      uint64            `json:"nextSegId"`
}

// newManifest returns a fresh, empty manifest for a not-yet-written store. Ids start at 1 so
// the first table/segment is 1 (a 0 id is "absent").
func newManifest() *manifest {
	return &manifest{FormatVersion: 1, Tables: map[int]tableInfo{}, NextTableId: 1, NextSegId: 1}
}

// readManifest loads dir/MANIFEST. A missing MANIFEST (a fresh dir) is NOT an error — it
// yields a fresh empty manifest (so Open can bootstrap). A torn MANIFEST.tmp is ignored: the
// atomic write below never renames a partial file into place, so only a fully-written MANIFEST
// is ever read.
func readManifest(dir string) (*manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "MANIFEST"))
	if os.IsNotExist(err) {
		return newManifest(), nil
	}
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m.Tables == nil {
		m.Tables = map[int]tableInfo{}
	}
	return &m, nil
}

// writeManifest atomically replaces dir/MANIFEST with m: marshal, write MANIFEST.tmp, fsync it,
// rename it over MANIFEST, then fsync the directory so the rename itself is durable. A crash at any
// point leaves either the old MANIFEST or the new one — never a torn file (a half-written
// MANIFEST.tmp is never renamed and is ignored by readManifest). Kept for the table-catalog paths
// (CreateTable/DeleteTable) where the marshal+fsync already run on the worker with no concurrent
// reader of the head/segment set — the spill/merge paths instead split this (marshalManifest under
// the lock, writeManifestBytes outside it) so a reader never blocks on the fsync (P9/T8, design §6).
func writeManifest(dir string, m *manifest) error {
	b, err := marshalManifest(m)
	if err != nil {
		return err
	}
	return writeManifestBytes(dir, b)
}

// marshalManifest serializes a manifest to its on-disk JSON bytes. It is split out of writeManifest
// so a writer (spill/installMerge) can capture the bytes WHILE it briefly holds s.mu (the marshal
// reads s.man's maps/slices, which a reader may also be reading under RLock — concurrent reads are
// safe, but the in-memory s.man must not be mutated concurrently), then perform the slow fsync via
// writeManifestBytes OUTSIDE the lock. No I/O here, so it is cheap to run under the lock.
func marshalManifest(m *manifest) ([]byte, error) {
	return json.Marshal(m)
}

// beforeManifestFsync, when non-nil, is invoked by writeManifestBytes at the START of its I/O (after
// the marshaled bytes are captured, before the file write + fsyncs). Test-only observability/blocking
// hook (P9/T8): a test installs one that blocks, kicks a real spill on the worker so it reaches this
// point, and asserts a concurrent Search returns promptly — proving the writer holds NO lock across
// its I/O. nil in production (one predictable, never-taken branch). Same parallel-safety constraint
// as the merge observers: a test installing it MUST NOT run t.Parallel.
var beforeManifestFsync func()

// writeManifestBytes durably installs the already-marshaled MANIFEST bytes b: write MANIFEST.tmp,
// fsync it, rename it over MANIFEST, then fsync the directory (same atomic, crash-safe sequence as
// writeManifest). It touches NO shared in-memory state — only the filesystem — so the spill/merge
// paths call it OUTSIDE s.mu, keeping the two fsyncs off the reader-blocking critical section
// (design §6: "readers never block on a writer's I/O"). All writes run on the single mpsc worker, so
// there is never a concurrent writeManifestBytes racing for the MANIFEST.tmp file.
func writeManifestBytes(dir string, b []byte) error {
	if beforeManifestFsync != nil {
		beforeManifestFsync()
	}
	tmp := filepath.Join(dir, "MANIFEST.tmp")
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, "MANIFEST")); err != nil {
		return err
	}
	// fsync the dir so the rename is durable
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
