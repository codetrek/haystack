package internal

import (
	"strconv"

	"github.com/ai-microsoft/haystack/server/core/pebble"
)

var (
	db pebble.DB
)

func Init(database pebble.DB) error {
	db = database
	return nil
}

func GetNextId() (int, error) {
	str, err := db.Get(EncodeWorkspaceIncrIdKey())
	if err != nil {
		return 0, err
	}

	var nextId int = 0
	if str != nil {
		i, err := strconv.Atoi(string(str))
		if err != nil {
			return 0, err
		}
		nextId = i
	}

	db.Put(EncodeWorkspaceIncrIdKey(), []byte(strconv.Itoa(nextId+1)))

	return nextId, nil
}

func GetAllWorkspaces() (map[int]string, error) {
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

func GetWorkspace(id int) (string, error) {
	v, err := db.Get(EncodeWorkspaceKey(id))
	if err != nil {
		return "", err
	}

	return string(v), nil
}

func SaveWorkspace(id int, workspaceJson string) error {
	return db.Put(EncodeWorkspaceKey(id), []byte(workspaceJson))
}

// DeleteWorkspace deletes a workspace and all of its documents and keywords
func DeleteWorkspace(id int) error {
	return db.Delete(EncodeWorkspaceKey(id))
}
