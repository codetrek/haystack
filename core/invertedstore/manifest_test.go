package invertedstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := &manifest{
		FormatVersion: 1, StorageVersion: "1.6", NextTableId: 3, NextSegId: 5,
		Tables:   map[int]tableInfo{1: {Id: 1, Description: "files"}},
		Segments: []segMeta{{Id: 4, Level: 0, DataCodec: codecSnappy, DictCodec: codecZstd, MinTable: 1, MaxTable: 1, Size: 123}},
	}
	if err := writeManifest(dir, m); err != nil {
		t.Fatal(err)
	}
	// no stray tmp left behind
	if _, err := os.Stat(filepath.Join(dir, "MANIFEST.tmp")); !os.IsNotExist(err) {
		t.Fatal("MANIFEST.tmp should not linger after atomic rename")
	}
	got, err := readManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.NextTableId != 3 || got.NextSegId != 5 || len(got.Segments) != 1 ||
		got.Segments[0].Id != 4 || got.Tables[1].Description != "files" {
		t.Fatalf("manifest round-trip mismatch: %+v", got)
	}
}

func TestManifestMissingIsEmpty(t *testing.T) {
	// reading a dir with no MANIFEST yields a fresh empty manifest, not an error
	m, err := readManifest(t.TempDir())
	if err != nil || m == nil || len(m.Segments) != 0 {
		t.Fatalf("fresh dir should give empty manifest: %v %+v", err, m)
	}
}

// TestReadManifestMalformedJSONIsError: a MANIFEST whose bytes are present but NOT valid JSON is a
// hard error (a torn/corrupt file the atomic writer should never have produced), not a silent
// empty manifest — readManifest surfaces the json.Unmarshal failure.
func TestReadManifestMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "MANIFEST"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := readManifest(dir)
	if err == nil {
		t.Fatalf("malformed MANIFEST should error, got manifest %+v", m)
	}
}

// TestReadManifestNilTablesSeeded: a valid MANIFEST that omits the "tables" field unmarshals with a
// nil Tables map; readManifest must seed a non-nil empty map so callers (CreateTable) can index it
// without a nil-map panic.
func TestReadManifestNilTablesSeeded(t *testing.T) {
	dir := t.TempDir()
	// JSON with no "tables" key at all → m.Tables unmarshals to nil.
	if err := os.WriteFile(filepath.Join(dir, "MANIFEST"),
		[]byte(`{"formatVersion":3,"nextTableId":1,"nextSegId":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := readManifest(dir)
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	if m.Tables == nil {
		t.Fatal("readManifest must seed a non-nil Tables map when the MANIFEST omits it")
	}
}

// TestReadManifestReadErrorNotNotExist: a read error that is NOT os.IsNotExist (here: MANIFEST is a
// DIRECTORY, so os.ReadFile fails with EISDIR) is surfaced as an error, distinct from the
// missing-file bootstrap path.
func TestReadManifestReadErrorNotNotExist(t *testing.T) {
	dir := t.TempDir()
	// Make MANIFEST a directory: os.ReadFile returns a non-NotExist error (read: is a directory).
	if err := os.Mkdir(filepath.Join(dir, "MANIFEST"), 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := readManifest(dir)
	if err == nil {
		t.Fatalf("reading a MANIFEST that is a directory should error, got %+v", m)
	}
	if os.IsNotExist(err) {
		t.Fatalf("error should not be IsNotExist (that is the bootstrap path): %v", err)
	}
}

// TestWriteManifestMarshalError: when the (test-injected) marshal step fails, writeManifest returns
// that error WITHOUT touching the filesystem — no MANIFEST(.tmp) is written.
func TestWriteManifestMarshalError(t *testing.T) {
	dir := t.TempDir()
	sentinel := errors.New("marshal boom")
	marshalManifestErr = sentinel
	t.Cleanup(func() { marshalManifestErr = nil })

	err := writeManifest(dir, newManifest())
	if !errors.Is(err, sentinel) {
		t.Fatalf("writeManifest should return the marshal error, got %v", err)
	}
	// A marshal failure precedes all I/O: no MANIFEST or MANIFEST.tmp is created.
	if _, statErr := os.Stat(filepath.Join(dir, "MANIFEST")); !os.IsNotExist(statErr) {
		t.Fatalf("no MANIFEST should exist after a marshal failure: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "MANIFEST.tmp")); !os.IsNotExist(statErr) {
		t.Fatalf("no MANIFEST.tmp should exist after a marshal failure: %v", statErr)
	}
}

// TestWriteManifestBytesWriteError: when writing the MANIFEST.tmp bytes fails (the tmp path is a
// symlink to /dev/full → ENOSPC on write), writeManifestBytes surfaces the write error and never
// renames a torn file over MANIFEST.
func TestWriteManifestBytesWriteError(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("/dev/full not available")
	}
	dir := t.TempDir()
	// os.Create truncates but follows the symlink → the underlying open target is /dev/full, so the
	// subsequent f.Write returns ENOSPC.
	if err := os.Symlink("/dev/full", filepath.Join(dir, "MANIFEST.tmp")); err != nil {
		t.Fatal(err)
	}
	err := writeManifestBytes(dir, []byte(`{"formatVersion":3}`))
	if err == nil {
		t.Fatal("writeManifestBytes should return the /dev/full write error")
	}
	// The write failed before the rename: no MANIFEST was installed.
	if _, statErr := os.Stat(filepath.Join(dir, "MANIFEST")); !os.IsNotExist(statErr) {
		t.Fatalf("no MANIFEST should be installed after a write failure: %v", statErr)
	}
}

// TestWriteManifestBytesRenameError: when the rename of MANIFEST.tmp over MANIFEST fails (here:
// MANIFEST is a NON-EMPTY directory, so rename cannot replace it), writeManifestBytes surfaces the
// rename error.
func TestWriteManifestBytesRenameError(t *testing.T) {
	dir := t.TempDir()
	// A non-empty directory named MANIFEST: os.Rename(tmp, MANIFEST) fails (cannot overwrite a
	// non-empty directory with a file).
	manDir := filepath.Join(dir, "MANIFEST")
	if err := os.Mkdir(manDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manDir, "occupant"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := writeManifestBytes(dir, []byte(`{"formatVersion":3}`))
	if err == nil {
		t.Fatal("writeManifestBytes should return the rename error when MANIFEST is a non-empty dir")
	}
}
