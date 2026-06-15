package collection

import (
	"encoding/json"
	"fmt"
)

// Default on-disk key-type prefix bytes for the collection Catalog.
//
// Values 1 (incr-id counter) and 2 (record) match the legacy haystack
// workspace registry on-disk layout, so existing data is readable without
// remapping key prefixes.
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

// unmarshalRecord deserialises a Record from JSON.
func unmarshalRecord(data []byte) (*Record, error) {
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
