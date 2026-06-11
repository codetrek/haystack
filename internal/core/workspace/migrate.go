package workspace

import (
	"encoding/json"
	"log"
	"time"

	"github.com/codetrek/haystack/internal/shared/types"
	"github.com/codetrek/haystack/searchcore/collection"
	"github.com/codetrek/haystack/searchcore/kv"
)

// extraPayload is the workspace-specific data stored in collection.Record.Extra.
type extraPayload struct {
	UseGlobalFilters bool           `json:"use_global_filters"`
	Filters          *types.Filters `json:"filters,omitempty"`
}

// encodeExtra serialises workspace-specific filter config into the opaque Extra
// field of a collection.Record.
func encodeExtra(useGlobalFilters bool, filters *types.Filters) []byte {
	b, _ := json.Marshal(extraPayload{
		UseGlobalFilters: useGlobalFilters,
		Filters:          filters,
	})
	return b
}

// decodeExtra deserialises the Extra field produced by encodeExtra.  If the
// payload is nil/empty or malformed, sensible defaults are returned (global
// filters enabled, nil custom filters).
func decodeExtra(extra []byte) (useGlobalFilters bool, filters *types.Filters) {
	useGlobalFilters = true // safe default
	if len(extra) == 0 {
		return
	}
	var p extraPayload
	if err := json.Unmarshal(extra, &p); err != nil {
		return
	}
	return p.UseGlobalFilters, p.Filters
}

// legacyWorkspaceJSON is the OLD on-disk workspace format.
type legacyWorkspaceJSON struct {
	ID               int            `json:"id"`
	Path             string         `json:"path"`
	UseGlobalFilters bool           `json:"use_global_filters"`
	Filters          *types.Filters `json:"filters,omitempty"`
	CreatedAt        time.Time      `json:"created_time"`
	LastAccessed     time.Time      `json:"last_accessed_time"`
	LastFullSync     time.Time      `json:"last_full_sync_time"`
}

// migrationProbe is used to distinguish old vs. new records.
type migrationProbe struct {
	Name string `json:"name"` // present in new collection.Record format
	Path string `json:"path"` // present in old workspace format
}

// MigrateLegacyRecords scans all key-type-2 records in db and converts any
// old-format workspace JSON (has "path", lacks "name") to the new
// collection.Record JSON format (has "name").
//
// It is idempotent: records already in the new format are skipped untouched.
// It does NOT modify the incr-id counter (key-type 1).
//
// Call this BEFORE collection.New so that the Catalog sees only new-format data.
func MigrateLegacyRecords(db kv.Store, opts collection.Options) error {
	keyTypeRecord := opts.KeyTypeRecord
	if keyTypeRecord == 0 {
		keyTypeRecord = collection.DefaultKeyTypeRecord
	}

	prefix := []byte{keyTypeRecord}
	type kv2 struct {
		key []byte
		val []byte
	}
	var toMigrate []kv2

	scanErr := db.Scan(prefix, func(key, value []byte) bool {
		// Probe: determine if this is an old or new format record.
		var probe migrationProbe
		if err := json.Unmarshal(value, &probe); err != nil {
			log.Printf("[Workspace/Migrate] Skipping unparseable record key %q: %v", string(key), err)
			return true
		}

		if probe.Name != "" {
			// Already new format — skip.
			return true
		}

		if probe.Path == "" {
			// Neither name nor path — skip with warning.
			log.Printf("[Workspace/Migrate] Skipping ambiguous record key %q (no name or path)", string(key))
			return true
		}

		// Old format — queue for migration.
		keyCopy := make([]byte, len(key))
		copy(keyCopy, key)
		valCopy := make([]byte, len(value))
		copy(valCopy, value)
		toMigrate = append(toMigrate, kv2{keyCopy, valCopy})
		return true
	})

	if scanErr != nil {
		return scanErr
	}

	for _, item := range toMigrate {
		var old legacyWorkspaceJSON
		if err := json.Unmarshal(item.val, &old); err != nil {
			log.Printf("[Workspace/Migrate] Failed to unmarshal old record key %q: %v — skipping", string(item.key), err)
			continue
		}

		newRecord := collection.Record{
			ID:           old.ID,
			Name:         old.Path,
			CreatedAt:    old.CreatedAt,
			LastAccessed: old.LastAccessed,
			LastFullSync: old.LastFullSync,
			Extra:        encodeExtra(old.UseGlobalFilters, old.Filters),
		}

		newVal, err := json.Marshal(newRecord)
		if err != nil {
			log.Printf("[Workspace/Migrate] Failed to marshal new record for key %q: %v — skipping", string(item.key), err)
			continue
		}

		if err := db.Put(item.key, newVal); err != nil {
			log.Printf("[Workspace/Migrate] Failed to write migrated record key %q: %v — skipping", string(item.key), err)
			continue
		}

		log.Printf("[Workspace/Migrate] Migrated workspace id=%d path=%q to new format", old.ID, old.Path)
	}

	return nil
}
