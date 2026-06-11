package storage

const (
	// Workspace registry key types (1 = incr-id counter, 2 = record) are now
	// owned by searchcore/collection (collection.DefaultKeyTypeIncrId /
	// DefaultKeyTypeRecord); they are intentionally not redeclared here.

	// Document key types 10-13, inverted index key types 20-22, and id-table
	// key types 28-29 are now owned by the searchcore sub-packages
	// (documents, invertedindex, idtable). They are not redeclared here to
	// keep a single source of truth; see the collision canary in storage_test.go.

	// Symbol types
	KeyTypeSymbol             = byte(30) // symbol inverted id
	KeyTypeSymbolDocFunctions = byte(31) // "df:"
	KeyTypeSymbolWords        = byte(33) // symbol words inverted id

)

func IsKeyType(key string, keyType byte) bool {
	if len(key) == 0 {
		return false
	}

	return key[0] == keyType
}
