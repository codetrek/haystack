package indexer

import (
	"crypto/md5"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/codetrek/haystack/internal/utils"
	"github.com/codetrek/haystack/core/idtable"

	"github.com/gabriel-vasile/mimetype"
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

func GetContentHash(content []byte) string {
	return utils.Md5Hash(content)
}

func IsNotIndexiable(relPath string) bool {
	fileExt := strings.ToLower(filepath.Ext(relPath))
	if _, ok := NotIndexiableFileExts[fileExt]; ok {
		return true
	}
	return false
}

// IsLikelyText checks if the data is likely to be text based on its MIME type
// and a heuristic for binary data. It returns true if the data is likely text.
func IsLikelyText(data []byte) bool {
	minetype := mimetype.Detect(data)
	if isTextMIME(minetype.String()) {
		return true
	}

	if isMediaMIME(minetype.String()) {
		return false
	}

	return isProbablyText(data)
}

// isTextMIME checks if the MIME type is a text type or a known text-like type
// such as JSON or XML. It returns true if the MIME type is likely to be text.
// This function is a simplified version and may not cover all cases.
// It is used to determine if the content is likely to be text based on its MIME type.
func isTextMIME(mtype string) bool {
	if strings.HasPrefix(mtype, "text/") {
		return true
	}

	if strings.HasSuffix(mtype, "+json") ||
		strings.HasSuffix(mtype, "+xml") ||
		mtype == "application/json" ||
		mtype == "application/xml" ||
		mtype == "application/javascript" {
		return true
	}
	return false
}

// isMediaMIME checks if the MIME type is a media type such as image, video,
// or audio. It returns true if the MIME type is likely to be media.
// This function is a simplified version and may not cover all cases.
// It is used to determine if the content is likely to be media based on its MIME type.
func isMediaMIME(mtype string) bool {
	if strings.HasPrefix(mtype, "image/") ||
		strings.HasPrefix(mtype, "video/") ||
		strings.HasPrefix(mtype, "audio/") {
		return true
	}
	return false
}

// isProbablyText checks if the data is likely to be text based on a heuristic.
// It counts the number of printable characters and checks if they are
// above a certain threshold. This is a simple heuristic and may not be
// perfect, but it should work for most text files.
func isProbablyText(data []byte) bool {
	var printable int
	for _, b := range data {
		if (b >= 32 && b <= 126) || b == '\n' || b == '\r' || b == '\t' || (b >= 128) {
			printable++
		}
	}

	// Check if the ratio of printable characters is greater than 95%
	// This is a heuristic and may not be perfect
	// but should work for most text files.
	return float64(printable)/float64(len(data)) > 0.95
}
