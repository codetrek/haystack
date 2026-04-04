package internal

import (
	"github.com/codetrek/haystack/internal/core/pebble"
)

var (
	db pebble.DB
)

func Init(database pebble.DB) error {
	db = database
	return nil
}

func GetNextId() (int, error) {
	return db.GetIncrementalId(EncodeWorkspaceIncrIdKey())
}

func ScanAll() (map[int]string, error) {
	workspaces := map[int]string{}
	db.Scan([]byte{KeyTypeWorkspace}, func(key, value []byte) bool {
		k, err := DecodeWorkspaceKey(string(key))
		if err == nil {
			workspaces[k] = string(value)
		}
		return true
	})

	return workspaces, nil
}

func Get(id int) (string, error) {
	v, err := db.Get(EncodeWorkspaceKey(id))
	if err != nil {
		return "", err
	}

	return string(v), nil
}

func Save(id int, workspaceJson string) error {
	return db.Put(EncodeWorkspaceKey(id), []byte(workspaceJson))
}

// Delete deletes a workspace and all of its documents and keywords
func Delete(id int) error {
	return db.Delete(EncodeWorkspaceKey(id))
}
