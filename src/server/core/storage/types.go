package storage

const (
	// Workspace key types
	KeyTypeWorkspaceIncrId = byte(1) // "wi:"
	KeyTypeWorkspace       = byte(2) // "ws:"

	// Document key types
	KeyTypeDocWords = byte(10) // "dw:"
	KeyTypeDocMeta  = byte(11) // "dm:"
	KeyTypeDocPath  = byte(12) // "dp:"

	// Inverted index key types
	KeyTypeKeyword = byte(20) // "kw:"
)

func IsKeyType(key string, keyType byte) bool {
	if len(key) == 0 {
		return false
	}

	return key[0] == keyType
}
