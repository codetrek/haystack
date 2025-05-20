package storage

const (
	// Workspace key types
	KeyTypeWorkspaceIncrId = byte(1) // "wi:"
	KeyTypeWorkspace       = byte(2) // "ws:"

	// Document key types
	KeyTypeDocWorkspace = byte(10)
	KeyTypeDocWords     = byte(11) // "dw:"
	KeyTypeDocMeta      = byte(12) // "dm:"
	KeyTypeDocPath      = byte(13) // "dp:"

	// Inverted index key types
	KeyTypeInvertedRow         = byte(20) // "kw:"
	KeyTypeInvertedTable       = byte(21)
	KeyTypeInvertedNextTableId = byte(22)
)

func IsKeyType(key string, keyType byte) bool {
	if len(key) == 0 {
		return false
	}

	return key[0] == keyType
}
