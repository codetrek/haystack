package documents

import (
	"crypto/md5"
	"fmt"
	"testing"

	"github.com/codetrek/haystack/core/tokenizer"
	"github.com/stretchr/testify/assert"
)

func TestContentHash_MatchesMd5Hex(t *testing.T) {
	// Byte-identical to the old internal utils.Md5Hash (fmt %x of md5.Sum) so the
	// move does not force a reindex.
	for _, in := range [][]byte{nil, {}, []byte("hello"), []byte("package main\n"), {0, 1, 2, 255}} {
		assert.Equal(t, fmt.Sprintf("%x", md5.Sum(in)), ContentHash(in))
	}
	assert.Equal(t, "5d41402abc4b2a76b9719d911017c592", ContentHash([]byte("hello")))
}

func TestIsLikelyText(t *testing.T) {
	assert.True(t, IsLikelyText([]byte("hello world\nthis is text\n")))
	assert.True(t, IsLikelyText([]byte(`{"a":1,"b":"two"}`)))          // application/json
	assert.False(t, IsLikelyText([]byte("\x89PNG\r\n\x1a\n\x00\x00"))) // image/png
	assert.False(t, IsLikelyText([]byte{0, 0, 0, 0, 1, 2, 3, 4}))      // application/octet-stream, low printable
	assert.True(t, IsLikelyText([]byte{}))                             // empty -> mimetype defaults to text/plain (matches old behavior)
}

func TestIsTextMIME_Classification(t *testing.T) {
	for _, m := range []string{"text/plain", "text/html", "application/json", "application/xml",
		"application/javascript", "application/vnd.api+json", "application/svg+xml"} {
		assert.True(t, isTextMIME(m), m)
	}
	for _, m := range []string{"application/octet-stream", "image/png"} {
		assert.False(t, isTextMIME(m), m)
	}
}

func TestIsMediaMIME_Classification(t *testing.T) {
	for _, m := range []string{"image/png", "video/mp4", "audio/mpeg"} {
		assert.True(t, isMediaMIME(m), m)
	}
	for _, m := range []string{"text/plain", "application/json"} {
		assert.False(t, isMediaMIME(m), m)
	}
}

func TestIsProbablyText(t *testing.T) {
	assert.True(t, isProbablyText([]byte("hello\tworld\r\nmore"))) // printable incl tab/cr
	assert.True(t, isProbablyText([]byte{0xc3, 0xa9}))             // high bytes (>=128) counted as printable
	assert.False(t, isProbablyText([]byte{0, 0, 0, 1}))            // no printable
	assert.False(t, isProbablyText([]byte{}))                      // empty -> NaN -> false
}

func TestIsProbablyText_Thresholds(t *testing.T) {
	mk := func(printable, binary int) []byte {
		d := make([]byte, 0, printable+binary)
		for i := 0; i < printable; i++ {
			d = append(d, 'a')
		}
		for i := 0; i < binary; i++ {
			d = append(d, 0)
		}
		return d
	}
	assert.False(t, isProbablyText(mk(95, 5))) // exactly 95% -> needs strictly > 0.95
	assert.True(t, isProbablyText(mk(96, 4)))  // 96% -> text
	assert.True(t, isProbablyText([]byte("中文内容测试\n")))

	bin := make([]byte, 100)
	for i := range bin {
		bin[i] = 0x01
	}
	assert.False(t, isProbablyText(bin))
}

func TestIsLikelyText_HeadersAndBinary(t *testing.T) {
	assert.True(t, IsLikelyText([]byte("<html><body>hi</body></html>")))
	assert.True(t, IsLikelyText([]byte("package main\n\nfunc main() {}\n")))
	assert.False(t, IsLikelyText([]byte("GIF89a\x00\x00\x00\x00"))) // image/gif
	bin := make([]byte, 100)
	for i := range bin {
		bin[i] = byte(i % 32) // control chars
	}
	assert.False(t, IsLikelyText(bin))
}

func TestBuildContentDocument_Normal(t *testing.T) {
	content := []byte("package main\nfunc Hello() {}\n")
	res := BuildContentDocument(ContentInput{
		ID: "doc1", RelPath: "src/hello.go", Size: int64(len(content)),
		ModTime: 111, Now: 222, MaxFileSize: 1 << 20, Content: content,
	})
	assert.False(t, res.Oversize)
	assert.False(t, res.NonText)
	if assert.NotNil(t, res.Doc) {
		assert.Equal(t, "doc1", res.Doc.ID)
		assert.Equal(t, "src/hello.go", res.Doc.RelPath)
		assert.Equal(t, int64(len(content)), res.Doc.Size)
		assert.Equal(t, int64(111), res.Doc.ModifiedTime)
		assert.Equal(t, int64(222), res.Doc.LastSyncTime)
		assert.Equal(t, ContentHash(content), res.Doc.Hash)
		assert.Equal(t, tokenizer.TokenizeForIndex(string(content)), res.Doc.Words)
		assert.Equal(t, tokenizer.TokenizeForIndex("src/hello.go"), res.Doc.PathWords)
	}
}

func TestBuildContentDocument_Oversize(t *testing.T) {
	// Oversize: indexed with empty Words and Hash, but PathWords still tokenized.
	res := BuildContentDocument(ContentInput{
		ID: "doc2", RelPath: "big/blob.bin", Size: 5_000_000,
		ModTime: 1, Now: 2, MaxFileSize: 1 << 20, Content: nil,
	})
	assert.True(t, res.Oversize)
	assert.False(t, res.NonText)
	if assert.NotNil(t, res.Doc) {
		assert.Equal(t, "", res.Doc.Hash)
		assert.Equal(t, []string{}, res.Doc.Words)
		assert.Equal(t, tokenizer.TokenizeForIndex("big/blob.bin"), res.Doc.PathWords)
		assert.Equal(t, int64(5_000_000), res.Doc.Size)
	}
}

func TestBuildContentDocument_NonText(t *testing.T) {
	res := BuildContentDocument(ContentInput{
		ID: "doc3", RelPath: "img/logo.png", Size: 8,
		ModTime: 1, Now: 2, MaxFileSize: 1 << 20, Content: []byte("\x89PNG\r\n\x1a\n"),
	})
	assert.True(t, res.NonText)
	assert.False(t, res.Oversize)
	assert.Nil(t, res.Doc)
}

func TestBuildContentDocument_UnchangedSkipsTokenize(t *testing.T) {
	content := []byte("package main\nfunc Hello() {}\n")
	res := BuildContentDocument(ContentInput{
		ID: "d", RelPath: "a.go", Size: int64(len(content)),
		MaxFileSize: 1 << 20, Content: content, PriorHash: ContentHash(content),
	})
	assert.True(t, res.Unchanged)
	assert.False(t, res.NonText)
	assert.Nil(t, res.Doc)

	// A non-matching PriorHash builds the document normally.
	res2 := BuildContentDocument(ContentInput{
		ID: "d", RelPath: "a.go", Size: int64(len(content)),
		MaxFileSize: 1 << 20, Content: content, PriorHash: "deadbeef",
	})
	assert.False(t, res2.Unchanged)
	assert.NotNil(t, res2.Doc)

	// No prior hash (new document) never reports Unchanged.
	res3 := BuildContentDocument(ContentInput{
		ID: "d", RelPath: "a.go", Size: int64(len(content)),
		MaxFileSize: 1 << 20, Content: content, PriorHash: "",
	})
	assert.False(t, res3.Unchanged)
	assert.NotNil(t, res3.Doc)
}
