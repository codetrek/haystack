package indexer

import (
	"crypto/md5"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/codetrek/haystack/packages/core/idtable"
)

// idAllocator is the package-level id allocator injected by server.go before Run is called.
var idAllocator *idtable.Allocator

// SetIdAllocator injects the Allocator instance used by GetDocumentId.
// Must be called before indexer.Run.
func SetIdAllocator(a *idtable.Allocator) {
	idAllocator = a
}

var NotIndexiableFileExts = map[string]struct{}{
	".ds_store": {},
	".exe":      {},
	".dll":      {},
	".lib":      {},
	".so":       {},
	".class":    {},
	".jar":      {},
	".pyc":      {},
	".pyo":      {},
	".bin":      {},
	".pdb":      {},
	".dmp":      {},
	".wasm":     {},

	".png":  {},
	".jpg":  {},
	".jpeg": {},
	".gif":  {},
	".bmp":  {},
	".ico":  {},
	".svg":  {},
	".tiff": {},
	".webp": {},

	".mp4":  {},
	".mkv":  {},
	".avi":  {},
	".mov":  {},
	".wmv":  {},
	".mp3":  {},
	".wav":  {},
	".flac": {},
	".aac":  {},
	".ogg":  {},
	".opus": {},

	".pdf":  {},
	".doc":  {},
	".docx": {},
	".xls":  {},
	".xlsx": {},
	".ppt":  {},
	".pptx": {},

	".zip": {},
	".tar": {},
	".gz":  {},
	".bz2": {},
	".7z":  {},
	".rar": {},
	".xz":  {},
}

func GetDocumentId(relPath string) (string, error) {
	relPath = filepath.ToSlash(relPath)
	v := md5.Sum([]byte(relPath))
	if idAllocator == nil {
		return "", fmt.Errorf("id allocator not initialized")
	}
	return idAllocator.GetId(v[:])
}

func IsNotIndexiable(relPath string) bool {
	fileExt := strings.ToLower(filepath.Ext(relPath))
	if _, ok := NotIndexiableFileExts[fileExt]; ok {
		return true
	}
	return false
}
