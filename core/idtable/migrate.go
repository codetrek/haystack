package idtable

import (
	"encoding/binary"
	"fmt"

	"github.com/codetrek/haystack/core/kv"
	bolt "go.etcd.io/bbolt"
)

// MigrateFromKV performs a one-time copy of a legacy kv.Store-backed idtable
// (key→id entries under srcKeyTypeKey, the nextId counter under srcKeyTypeNextId)
// into a standalone bbolt file at dstPath. It is idempotent: if dstPath already
// holds migrated entries it is a no-op, so it is safe to call on every startup.
//
// The legacy layout (written by the old kv-backed allocator) is:
//   - key:    {srcKeyTypeKey} + rawKey  ->  8-byte big-endian id
//   - nextId: {srcKeyTypeNextId}         ->  decimal-string int64
//
// The bbolt layout matches Open's: keys bucket (rawKey -> 8-byte id) + meta
// bucket (nextId -> 8-byte id). Byte-identical id values, so no reindex.
func MigrateFromKV(src kv.Store, dstPath string, srcKeyTypeKey, srcKeyTypeNextId byte) error {
	if src == nil {
		return fmt.Errorf("idtable: migrate: source store is nil")
	}

	// Read everything from the source first, outside the bbolt transaction, so we
	// never hold a write txn open across the legacy store's scan. The scan's key
	// slices are only valid during the callback, so copy them.
	type entry struct {
		key []byte
		id  int64
	}
	var entries []entry
	err := src.Scan([]byte{srcKeyTypeKey}, func(k, v []byte) bool {
		if len(k) < 1 || len(v) < 8 {
			return true // skip malformed legacy rows
		}
		rawKey := append([]byte(nil), k[1:]...) // strip the key-type prefix byte
		entries = append(entries, entry{key: rawKey, id: int64(binary.BigEndian.Uint64(v))})
		return true
	})
	if err != nil {
		return fmt.Errorf("idtable: migrate: scan source: %w", err)
	}

	nextId := int64(1)
	if nv, err := src.Get([]byte{srcKeyTypeNextId}); err != nil {
		return fmt.Errorf("idtable: migrate: read nextId: %w", err)
	} else if nv != nil {
		nextId = parseId(string(nv))
	}
	if nextId < 0 {
		return fmt.Errorf("idtable: migrate: invalid source nextId")
	}

	db, err := bolt.Open(dstPath, 0600, &bolt.Options{Timeout: openTimeout})
	if err != nil {
		return fmt.Errorf("idtable: migrate: open dst %s: %w", dstPath, err)
	}
	defer db.Close()

	return db.Update(func(tx *bolt.Tx) error {
		kb, err := tx.CreateBucketIfNotExists(bucketKeys)
		if err != nil {
			return err
		}
		mb, err := tx.CreateBucketIfNotExists(bucketMeta)
		if err != nil {
			return err
		}
		// Idempotency: a destination that was already migrated (key entries OR the
		// nextId counter present — the empty-source case writes only the counter)
		// or written by a running allocator must not be overwritten.
		if kb.Stats().KeyN > 0 || mb.Get(metaNextId) != nil {
			return nil
		}

		for _, e := range entries {
			// A fresh slice per Put: bbolt retains the value slice by reference until
			// commit, so a reused buffer would corrupt earlier entries.
			id := make([]byte, 8)
			binary.BigEndian.PutUint64(id, uint64(e.id))
			if err := kb.Put(e.key, id); err != nil {
				return err
			}
		}
		nv := make([]byte, 8)
		binary.BigEndian.PutUint64(nv, uint64(nextId))
		return mb.Put(metaNextId, nv)
	})
}
