package collection

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Default on-disk key-type prefix bytes for the collection Catalog.
//
// These values match the existing haystack workspace registry (storage.KeyTypeWorkspaceIncrId
// and storage.KeyTypeWorkspace) to ensure on-disk compatibility.
//
// Byte 0 (NUL) is reserved as the zero-value sentinel meaning "use default";
// it cannot be selected as a custom prefix.
const (
	// DefaultKeyTypeIncrId is the default on-disk prefix byte for the
	// auto-increment id counter key. Value 1 matches the legacy registry.
	DefaultKeyTypeIncrId = byte(1)

	// DefaultKeyTypeRecord is the default on-disk prefix byte for collection
	// record keys. Value 2 matches the legacy registry.
	DefaultKeyTypeRecord = byte(2)
)

// encodeIncrIdKey returns the on-disk key for the auto-increment id counter.
func (c *Catalog) encodeIncrIdKey() []byte {
	return []byte{c.keyTypeIncrId}
}

// encodeRecordKey returns the on-disk key for a collection record.
func (c *Catalog) encodeRecordKey(id int) []byte {
	return []byte(fmt.Sprintf("%c%d", c.keyTypeRecord, id))
}

// encodeRecordScanPrefix returns the prefix used to scan all collection records.
func (c *Catalog) encodeRecordScanPrefix() []byte {
	return []byte{c.keyTypeRecord}
}

// decodeRecordKey parses a raw record key, returning the collection id.
// Returns -1 if the key is malformed or has the wrong type byte.
func (c *Catalog) decodeRecordKey(key string) int {
	if len(key) == 0 || key[0] != c.keyTypeRecord {
		return -1
	}
	suffix := key[1:]
	id, err := strconv.Atoi(strings.TrimSpace(suffix))
	if err != nil {
		return -1
	}
	return id
}

// marshalRecord serialises a Record to JSON. Record has only JSON-safe fields,
// so Marshal cannot fail for well-formed input.
func marshalRecord(r *Record) ([]byte, error) {
	return json.Marshal(r)
}

// unmarshalRecord deserialises a Record from JSON.
func unmarshalRecord(data []byte) (*Record, error) {
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
