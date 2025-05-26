package storage

const (
	// Workspace key types
	KeyTypeWorkspaceIncrId = byte(1)
	KeyTypeWorkspace       = byte(2)

	// Document key types
	KeyTypeDocWorkspace = byte(10)
	KeyTypeDocWords     = byte(11)
	KeyTypeDocMeta      = byte(12)
	KeyTypeDocPath      = byte(13)

	// Inverted index key types
	KeyTypeInvertedRow         = byte(20)
	KeyTypeInvertedTable       = byte(21)
	KeyTypeInvertedNextTableId = byte(22)

	// Symbol types
	KeyTypeSymbol             = byte(30) // symbol inverted id
	KeyTypeSymbolDocFunctions = byte(31) // "df:"
	KeyTypeEmbeddingFuncFlag  = byte(32) // "ef:"
	KeyTypeSymbolWords        = byte(33) // symbol words inverted id
)

func IsKeyType(key string, keyType byte) bool {
	if len(key) == 0 {
		return false
	}

	return key[0] == keyType
}
