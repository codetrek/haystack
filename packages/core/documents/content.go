package documents

import (
	"crypto/md5"
	"fmt"
	"strings"

	"github.com/codetrek/haystack/packages/core/tokenizer"

	"github.com/gabriel-vasile/mimetype"
)

// ContentHash returns the lowercase md5 hex of data. Byte-identical to the
// indexer's old utils.Md5Hash, so moving content hashing here does not change
// docids or force a reindex.
func ContentHash(data []byte) string {
	return fmt.Sprintf("%x", md5.Sum(data))
}

// IsLikelyText reports whether data is likely text, by MIME type with a
// printable-ratio fallback. Moved verbatim from the indexer so core owns the
// full file-content tokenization pipeline.
func IsLikelyText(data []byte) bool {
	mtype := mimetype.Detect(data)
	if isTextMIME(mtype.String()) {
		return true
	}
	if isMediaMIME(mtype.String()) {
		return false
	}
	return isProbablyText(data)
}

// isTextMIME reports whether the MIME type is text or a known text-like type
// (JSON/XML/JavaScript).
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

// isMediaMIME reports whether the MIME type is image/video/audio.
func isMediaMIME(mtype string) bool {
	if strings.HasPrefix(mtype, "image/") ||
		strings.HasPrefix(mtype, "video/") ||
		strings.HasPrefix(mtype, "audio/") {
		return true
	}
	return false
}

// isProbablyText reports whether the printable-byte ratio exceeds 95%. Empty
// data yields a NaN ratio, which is not > 0.95, so empty data is "not text".
func isProbablyText(data []byte) bool {
	var printable int
	for _, b := range data {
		if (b >= 32 && b <= 126) || b == '\n' || b == '\r' || b == '\t' || (b >= 128) {
			printable++
		}
	}
	return float64(printable)/float64(len(data)) > 0.95
}

// ContentInput carries everything BuildContentDocument needs: already-read bytes
// plus caller-supplied stat metadata, identity and clock. It performs no I/O and
// reads no config — the indexer shell resolves MaxFileSize (from conf), allocates
// ID (idtable), stats the file, reads Content, and injects Now.
type ContentInput struct {
	ID          string // caller-allocated docid
	RelPath     string // tokenized into PathWords verbatim
	Size        int64  // os.FileInfo.Size()
	ModTime     int64  // info.ModTime().UnixNano()
	Now         int64  // injected clock -> LastSyncTime
	MaxFileSize int64  // resolved from conf.Get().Server.MaxFileSize
	Content     []byte // file bytes; nil when Oversize (caller skips the read)

	// PriorHash is the existing document's content hash, or "" when there is no
	// existing document. When it matches the freshly computed hash, the content
	// is unchanged and BuildContentDocument returns Unchanged WITHOUT tokenizing
	// (restoring the indexer's pre-tokenize short-circuit).
	PriorHash string
}

// ContentResult is the outcome of BuildContentDocument. Doc is nil when NonText
// or Unchanged. Oversize means the file exceeded MaxFileSize and is indexed with
// empty Words and Hash (its PathWords are still tokenized). Unchanged means the
// content hash equals PriorHash, so the document need not be re-indexed.
type ContentResult struct {
	Doc       *Document
	Oversize  bool
	NonText   bool
	Unchanged bool
}

// BuildContentDocument is the pure assembly that was inline in the indexer's
// parse(): size gate -> text-likeness gate -> content hash -> (skip when
// unchanged) -> TokenizeForIndex of the content and the path -> an assembled
// Document. It does NOT perform the Store-backed "mtime unchanged" short-circuit
// (that reads live Store state and stays in the indexer); the hash-unchanged
// short-circuit IS done here, before the expensive tokenization, via PriorHash.
func BuildContentDocument(in ContentInput) ContentResult {
	pathWords := tokenizer.TokenizeForIndex(in.RelPath)

	if in.Size > in.MaxFileSize {
		return ContentResult{Doc: &Document{
			ID:           in.ID,
			RelPath:      in.RelPath,
			Size:         in.Size,
			ModifiedTime: in.ModTime,
			LastSyncTime: in.Now,
			Hash:         "",
			Words:        []string{},
			PathWords:    pathWords,
		}, Oversize: true}
	}

	if !IsLikelyText(in.Content) {
		return ContentResult{NonText: true}
	}

	hash := ContentHash(in.Content)
	// Content unchanged: skip the (expensive) content tokenization entirely, as
	// the original indexer did before assembling the Document.
	if in.PriorHash != "" && hash == in.PriorHash {
		return ContentResult{Unchanged: true}
	}

	return ContentResult{Doc: &Document{
		ID:           in.ID,
		RelPath:      in.RelPath,
		Size:         in.Size,
		ModifiedTime: in.ModTime,
		LastSyncTime: in.Now,
		Hash:         hash,
		Words:        tokenizer.TokenizeForIndex(string(in.Content)),
		PathWords:    pathWords,
	}}
}
