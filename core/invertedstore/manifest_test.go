package invertedstore

import (
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
