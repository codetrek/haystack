package internal

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/codetrek/haystack/server/core/storage"
)

const (
	KeyTypeWorkspaceIncrId = storage.KeyTypeWorkspaceIncrId
	KeyTypeWorkspace       = storage.KeyTypeWorkspace
)

func EncodeWorkspaceIncrIdKey() []byte {
	return []byte(fmt.Sprintf("%c", KeyTypeWorkspaceIncrId))
}

func EncodeWorkspaceKey(workspaceid int) []byte {
	return []byte(fmt.Sprintf("%c%d", KeyTypeWorkspace, workspaceid))
}

func DecodeWorkspaceKey(key string) (int, error) {
	if !storage.IsKeyType(key, storage.KeyTypeWorkspace) {
		return -1, errors.New("invalid key type")
	}

	return strconv.Atoi(string(key[1:]))
}
