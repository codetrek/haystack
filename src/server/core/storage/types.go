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
)

func IsKeyType(key string, keyType byte) bool {
	if len(key) == 0 {
		return false
	}

	return key[0] == keyType
}
