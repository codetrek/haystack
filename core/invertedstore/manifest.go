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

// writeManifest atomically replaces dir/MANIFEST with m: write MANIFEST.tmp, fsync it, rename
// it over MANIFEST, then fsync the directory so the rename itself is durable. A crash at any
// point leaves either the old MANIFEST or the new one — never a torn file (a half-written
// MANIFEST.tmp is never renamed and is ignored by readManifest).
func writeManifest(dir string, m *manifest) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
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
