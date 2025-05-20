package storage

const (
	// Workspace key types
	KeyTypeWorkspaceIncrId = byte(1) // "wi:"
	KeyTypeWorkspace       = byte(2) // "ws:"

	// Fulltext key types
	KeyTypeFT         = byte(10)
	KeyTypeFTDocWords = byte(11) // "dw:"
	KeyTypeFTDocMeta  = byte(12) // "dm:"
	KeyTypeFTDocPath  = byte(13) // "dp:"

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
