package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/codetrek/haystack/internal/core/storage"
	"github.com/codetrek/haystack/core/collection"
	"github.com/codetrek/haystack/core/kv"
)

// faultInjectStore wraps a real kv.Store and injects a Put error for a
// specific key prefix (or for all Puts after a counter threshold).
type faultInjectStore struct {
	kv.Store
	// failKeyPrefix: if non-nil, Put returns errFault when the key starts with this prefix.
	failKeyPrefix []byte
	errFault      error
}

func (f *faultInjectStore) Put(key, value []byte) error {
	if f.failKeyPrefix != nil && len(key) >= len(f.failKeyPrefix) {
		match := true
		for i, b := range f.failKeyPrefix {
			if key[i] != b {
				match = false
				break
			}
		}
		if match {
			return f.errFault
		}
	}
	return f.Store.Put(key, value)
}

// TestMigrateLegacyRecords_UnparseableRecord verifies that an entry whose
// value is not valid JSON is silently skipped (the callback returns true),
// MigrateLegacyRecords returns nil, and the bad key is left untouched.
func TestMigrateLegacyRecords_UnparseableRecord(t *testing.T) {
	tempDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tempDir, "data"), 0)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()

	garbageKey := []byte(fmt.Sprintf("%c%d", collection.DefaultKeyTypeRecord, 99))
	garbageVal := []byte("\xff\xfe garbage not json {{{")
	db.Put(garbageKey, garbageVal)

	// Also seed a valid legacy record to confirm it migrates fine.
	now := time.Now().UTC().Truncate(time.Second)
	db.Put(oldRecordKey(1), encodeOldWorkspace(1, "/ws/good", true, now, now, now))
	db.Put([]byte{collection.DefaultKeyTypeIncrId}, []byte("99"))

	if err := MigrateLegacyRecords(db, collection.Options{}); err != nil {
		t.Fatalf("MigrateLegacyRecords should return nil even with an unparseable record, got: %v", err)
	}

	// The garbage key must still be present and unchanged.
	got, err := db.Get(garbageKey)
	if err != nil {
		t.Fatalf("db.Get garbage key: %v", err)
	}
	if string(got) != string(garbageVal) {
		t.Errorf("garbage key value changed: got %q, want %q", got, garbageVal)
	}

	// The valid legacy record should have been migrated to new format.
	migratedVal, err := db.Get(oldRecordKey(1))
	if err != nil {
		t.Fatalf("db.Get migrated key: %v", err)
	}
	var rec collection.Record
	if err := json.Unmarshal(migratedVal, &rec); err != nil {
		t.Fatalf("migrated record is not valid JSON: %v", err)
	}
	if rec.Name != "/ws/good" {
		t.Errorf("migrated record Name = %q, want /ws/good", rec.Name)
	}
}

// TestMigrateLegacyRecords_AmbiguousRecord verifies that a record that is
// valid JSON but has neither "name" nor "path" fields is skipped silently.
func TestMigrateLegacyRecords_AmbiguousRecord(t *testing.T) {
	tempDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tempDir, "data"), 0)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()

	ambiguousKey := oldRecordKey(7)
	ambiguousVal := []byte(`{"id":9}`)
	db.Put(ambiguousKey, ambiguousVal)

	db.Put([]byte{collection.DefaultKeyTypeIncrId}, []byte("9"))

	if err := MigrateLegacyRecords(db, collection.Options{}); err != nil {
		t.Fatalf("MigrateLegacyRecords should return nil for ambiguous record, got: %v", err)
	}

	// The ambiguous key must remain unchanged.
	got, err := db.Get(ambiguousKey)
	if err != nil {
		t.Fatalf("db.Get ambiguous key: %v", err)
	}
	if string(got) != string(ambiguousVal) {
		t.Errorf("ambiguous key value changed: got %q, want %q", got, ambiguousVal)
	}
}

// TestMigrateLegacyRecords_PutFailure verifies that when db.Put returns an
// error during the migration write-back loop, MigrateLegacyRecords propagates
// a non-nil error.
func TestMigrateLegacyRecords_PutFailure(t *testing.T) {
	tempDir := t.TempDir()
	realDB, err := storage.Open(filepath.Join(tempDir, "data"), 0)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer realDB.Close()

	// Seed one old-format record so the migration loop runs and tries to Put.
	now := time.Now().UTC().Truncate(time.Second)
	realDB.Put(oldRecordKey(5), encodeOldWorkspace(5, "/ws/fail", true, now, now, now))
	realDB.Put([]byte{collection.DefaultKeyTypeIncrId}, []byte("5"))

	// Wrap with a fault-injecting store that fails any Put for the record key prefix.
	injected := &faultInjectStore{
		Store:         realDB,
		failKeyPrefix: []byte{collection.DefaultKeyTypeRecord},
		errFault:      errors.New("injected Put error"),
	}

	err = MigrateLegacyRecords(injected, collection.Options{})
	if err == nil {
		t.Fatal("expected MigrateLegacyRecords to return a non-nil error on Put failure")
	}
}
