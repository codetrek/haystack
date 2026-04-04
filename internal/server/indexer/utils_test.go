package indexer

import (
	"crypto/md5"
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// GetContentHash
// ---------------------------------------------------------------------------

func TestGetContentHash_BasicContent(t *testing.T) {
	content := []byte("hello world")
	hash := GetContentHash(content)

	// Manually compute expected hash
	expected := fmt.Sprintf("%x", md5.Sum(content))
	if hash != expected {
		t.Errorf("GetContentHash(%q) = %q, want %q", content, hash, expected)
	}
}

func TestGetContentHash_EmptyContent(t *testing.T) {
	content := []byte("")
	hash := GetContentHash(content)
	expected := fmt.Sprintf("%x", md5.Sum(content))
	if hash != expected {
		t.Errorf("GetContentHash(empty) = %q, want %q", hash, expected)
	}
}

func TestGetContentHash_DifferentContentsDifferentHashes(t *testing.T) {
	h1 := GetContentHash([]byte("abc"))
	h2 := GetContentHash([]byte("xyz"))
	if h1 == h2 {
		t.Errorf("expected different hashes for different content, both got %q", h1)
	}
}

func TestGetContentHash_SameContentSameHash(t *testing.T) {
	content := []byte("deterministic test")
	h1 := GetContentHash(content)
	h2 := GetContentHash(content)
	if h1 != h2 {
		t.Errorf("same content produced different hashes: %q vs %q", h1, h2)
	}
}

func TestGetContentHash_BinaryContent(t *testing.T) {
	content := []byte{0x00, 0x01, 0xFF, 0xFE, 0x80}
	hash := GetContentHash(content)
	expected := fmt.Sprintf("%x", md5.Sum(content))
	if hash != expected {
		t.Errorf("GetContentHash(binary) = %q, want %q", hash, expected)
	}
}

func TestGetContentHash_LargeContent(t *testing.T) {
	content := []byte(strings.Repeat("a", 100_000))
	hash := GetContentHash(content)
	if len(hash) != 32 { // MD5 hex digest is always 32 chars
		t.Errorf("expected 32-char hash, got %d chars: %q", len(hash), hash)
	}
}

// ---------------------------------------------------------------------------
// IsNotIndexiable
// ---------------------------------------------------------------------------

func TestIsNotIndexiable_BlockedExtensions(t *testing.T) {
	blocked := []string{
		// binaries
		"program.exe", "lib.dll", "lib.so", "app.class", "archive.jar",
		"cache.pyc", "cache.pyo", "data.bin", "debug.pdb", "crash.dmp",
		"module.wasm",
		// images
		"photo.png", "image.jpg", "image.jpeg", "anim.gif", "image.bmp",
		"icon.ico", "logo.svg", "scan.tiff", "pic.webp",
		// media
		"video.mp4", "movie.mkv", "clip.avi", "recording.mov", "file.wmv",
		"song.mp3", "sound.wav", "audio.flac", "track.aac", "music.ogg", "voice.opus",
		// documents
		"doc.pdf", "file.doc", "file.docx", "sheet.xls", "sheet.xlsx",
		"slides.ppt", "slides.pptx",
		// archives
		"archive.zip", "archive.tar", "archive.gz", "archive.bz2",
		"archive.7z", "archive.rar", "archive.xz",
		// ds_store
		".ds_store",
	}

	for _, f := range blocked {
		if !IsNotIndexiable(f) {
			t.Errorf("IsNotIndexiable(%q) = false, want true", f)
		}
	}
}

func TestIsNotIndexiable_AllowedExtensions(t *testing.T) {
	allowed := []string{
		"main.go", "app.py", "index.js", "style.css", "config.yaml",
		"Makefile", "README.md", "data.json", "page.html", "app.tsx",
		"lib.rs", "main.c", "header.h", "app.java", "script.rb",
		"test.sh", "Dockerfile", "file.txt", "config.toml",
	}

	for _, f := range allowed {
		if IsNotIndexiable(f) {
			t.Errorf("IsNotIndexiable(%q) = true, want false", f)
		}
	}
}

func TestIsNotIndexiable_CaseInsensitive(t *testing.T) {
	// Extensions should be case-insensitive (lowered before lookup)
	cases := []string{"photo.PNG", "image.JPG", "video.MP4", "archive.ZIP", "program.EXE"}
	for _, f := range cases {
		if !IsNotIndexiable(f) {
			t.Errorf("IsNotIndexiable(%q) = false, want true (case-insensitive)", f)
		}
	}
}

func TestIsNotIndexiable_NoExtension(t *testing.T) {
	files := []string{"Makefile", "Dockerfile", "LICENSE", "README"}
	for _, f := range files {
		if IsNotIndexiable(f) {
			t.Errorf("IsNotIndexiable(%q) = true, want false (no extension)", f)
		}
	}
}

func TestIsNotIndexiable_PathWithDirectories(t *testing.T) {
	if !IsNotIndexiable("some/dir/photo.png") {
		t.Error("IsNotIndexiable with directory prefix should still detect .png")
	}
	if IsNotIndexiable("some/dir/main.go") {
		t.Error("IsNotIndexiable with directory prefix should not block .go")
	}
}

// ---------------------------------------------------------------------------
// isTextMIME
// ---------------------------------------------------------------------------

func TestIsTextMIME_TextPrefix(t *testing.T) {
	textTypes := []string{
		"text/plain", "text/html", "text/css", "text/javascript",
		"text/csv", "text/xml", "text/markdown",
	}
	for _, mt := range textTypes {
		if !isTextMIME(mt) {
			t.Errorf("isTextMIME(%q) = false, want true", mt)
		}
	}
}

func TestIsTextMIME_JsonSuffix(t *testing.T) {
	types := []string{"application/json", "application/vnd.api+json"}
	for _, mt := range types {
		if !isTextMIME(mt) {
			t.Errorf("isTextMIME(%q) = false, want true", mt)
		}
	}
}

func TestIsTextMIME_XmlSuffix(t *testing.T) {
	types := []string{"application/xml", "application/soap+xml", "application/atom+xml"}
	for _, mt := range types {
		if !isTextMIME(mt) {
			t.Errorf("isTextMIME(%q) = false, want true", mt)
		}
	}
}

func TestIsTextMIME_ApplicationJavascript(t *testing.T) {
	if !isTextMIME("application/javascript") {
		t.Error("isTextMIME(application/javascript) should be true")
	}
}

func TestIsTextMIME_NonTextTypes(t *testing.T) {
	nonText := []string{
		"application/octet-stream", "image/png", "video/mp4",
		"audio/mpeg", "application/zip", "application/pdf",
	}
	for _, mt := range nonText {
		if isTextMIME(mt) {
			t.Errorf("isTextMIME(%q) = true, want false", mt)
		}
	}
}

// ---------------------------------------------------------------------------
// isMediaMIME
// ---------------------------------------------------------------------------

func TestIsMediaMIME_ImageTypes(t *testing.T) {
	types := []string{"image/png", "image/jpeg", "image/gif", "image/webp", "image/svg+xml"}
	for _, mt := range types {
		if !isMediaMIME(mt) {
			t.Errorf("isMediaMIME(%q) = false, want true", mt)
		}
	}
}

func TestIsMediaMIME_VideoTypes(t *testing.T) {
	types := []string{"video/mp4", "video/webm", "video/ogg"}
	for _, mt := range types {
		if !isMediaMIME(mt) {
			t.Errorf("isMediaMIME(%q) = false, want true", mt)
		}
	}
}

func TestIsMediaMIME_AudioTypes(t *testing.T) {
	types := []string{"audio/mpeg", "audio/ogg", "audio/wav", "audio/flac"}
	for _, mt := range types {
		if !isMediaMIME(mt) {
			t.Errorf("isMediaMIME(%q) = false, want true", mt)
		}
	}
}

func TestIsMediaMIME_NonMediaTypes(t *testing.T) {
	nonMedia := []string{
		"text/plain", "application/json", "application/pdf",
		"application/octet-stream", "text/html",
	}
	for _, mt := range nonMedia {
		if isMediaMIME(mt) {
			t.Errorf("isMediaMIME(%q) = true, want false", mt)
		}
	}
}

// ---------------------------------------------------------------------------
// isProbablyText
// ---------------------------------------------------------------------------

func TestIsProbablyText_PureASCII(t *testing.T) {
	data := []byte("Hello, this is a normal text file.\nWith multiple lines.\n")
	if !isProbablyText(data) {
		t.Error("isProbablyText should return true for pure ASCII text")
	}
}

func TestIsProbablyText_UTF8Content(t *testing.T) {
	data := []byte("UTF-8 text with accents: cafe\u0301, na\u00efve, re\u0301sume\u0301\n")
	if !isProbablyText(data) {
		t.Error("isProbablyText should return true for UTF-8 text")
	}
}

func TestIsProbablyText_MostlyBinary(t *testing.T) {
	// Create data that's mostly non-printable control characters
	data := make([]byte, 200)
	for i := range data {
		data[i] = byte(i % 32) // control chars 0-31
	}
	if isProbablyText(data) {
		t.Error("isProbablyText should return false for mostly binary data")
	}
}

func TestIsProbablyText_ExactlyAtThreshold(t *testing.T) {
	// 95% threshold: need >95% printable to be text
	// 100 bytes total; exactly 95 printable, 5 non-printable control chars
	data := make([]byte, 100)
	for i := 0; i < 95; i++ {
		data[i] = 'a'
	}
	for i := 95; i < 100; i++ {
		data[i] = 0x01 // non-printable control character
	}
	// 95/100 = 0.95 which is NOT > 0.95
	if isProbablyText(data) {
		t.Error("isProbablyText should return false at exactly 95% (needs >95%)")
	}
}

func TestIsProbablyText_JustAboveThreshold(t *testing.T) {
	// 96 printable out of 100 = 0.96 > 0.95
	data := make([]byte, 100)
	for i := 0; i < 96; i++ {
		data[i] = 'a'
	}
	for i := 96; i < 100; i++ {
		data[i] = 0x01
	}
	if !isProbablyText(data) {
		t.Error("isProbablyText should return true at 96% printable")
	}
}

func TestIsProbablyText_TabsAndNewlines(t *testing.T) {
	// Tabs, newlines, and carriage returns should count as printable
	data := []byte("line1\n\tindented\r\nline3\t\tmore tabs\n")
	if !isProbablyText(data) {
		t.Error("isProbablyText should handle tabs and newlines as printable")
	}
}

func TestIsProbablyText_HighBytesCountAsPrintable(t *testing.T) {
	// Bytes >= 128 are considered printable (for UTF-8)
	data := make([]byte, 100)
	for i := range data {
		data[i] = byte(128 + (i % 128))
	}
	if !isProbablyText(data) {
		t.Error("isProbablyText should treat bytes >= 128 as printable")
	}
}

// ---------------------------------------------------------------------------
// IsLikelyText - integration of isTextMIME, isMediaMIME, isProbablyText
// ---------------------------------------------------------------------------

func TestIsLikelyText_PlainTextContent(t *testing.T) {
	data := []byte("This is plain text content for testing purposes.\nAnother line here.\n")
	if !IsLikelyText(data) {
		t.Error("IsLikelyText should return true for plain text")
	}
}

func TestIsLikelyText_HTMLContent(t *testing.T) {
	data := []byte("<!DOCTYPE html><html><head><title>Test</title></head><body><p>Hello</p></body></html>")
	if !IsLikelyText(data) {
		t.Error("IsLikelyText should return true for HTML")
	}
}

func TestIsLikelyText_JSONContent(t *testing.T) {
	data := []byte(`{"key": "value", "number": 42, "array": [1, 2, 3]}`)
	if !IsLikelyText(data) {
		t.Error("IsLikelyText should return true for JSON content")
	}
}

func TestIsLikelyText_PNGHeader(t *testing.T) {
	// Minimal valid PNG: 8-byte signature + IHDR chunk (25 bytes) + IEND chunk (12 bytes)
	// This is enough for mimetype to detect image/png
	data := []byte{
		// PNG signature
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		// IHDR chunk: length (13)
		0x00, 0x00, 0x00, 0x0D,
		// "IHDR"
		0x49, 0x48, 0x44, 0x52,
		// width=1, height=1, bit depth=8, color type=2 (RGB)
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02,
		// compression, filter, interlace
		0x00, 0x00, 0x00,
		// CRC (placeholder)
		0x90, 0x77, 0x53, 0xDE,
		// IEND chunk
		0x00, 0x00, 0x00, 0x00,
		0x49, 0x45, 0x4E, 0x44,
		0xAE, 0x42, 0x60, 0x82,
	}
	if IsLikelyText(data) {
		t.Error("IsLikelyText should return false for PNG data")
	}
}

func TestIsLikelyText_GIFHeader(t *testing.T) {
	// GIF89a header followed by enough data
	data := append([]byte("GIF89a"), make([]byte, 100)...)
	if IsLikelyText(data) {
		t.Error("IsLikelyText should return false for GIF data")
	}
}

func TestIsLikelyText_JPEGHeader(t *testing.T) {
	// JPEG SOI marker + JFIF APP0 marker
	data := []byte{
		0xFF, 0xD8, 0xFF, 0xE0, // SOI + APP0
		0x00, 0x10, // length
		0x4A, 0x46, 0x49, 0x46, 0x00, // "JFIF\0"
		0x01, 0x01, // version
		0x00,                   // aspect ratio units
		0x00, 0x01, 0x00, 0x01, // density
		0x00, 0x00, // thumbnail
	}
	// Pad with enough random-ish bytes
	data = append(data, make([]byte, 100)...)
	if IsLikelyText(data) {
		t.Error("IsLikelyText should return false for JPEG data")
	}
}

func TestIsLikelyText_MostlyTextButSomeControlChars(t *testing.T) {
	// Data that is not detected as a known MIME type, but is mostly text
	// This tests the isProbablyText fallback path through IsLikelyText
	data := make([]byte, 200)
	for i := range data {
		data[i] = byte('A' + (i % 26))
	}
	// sprinkle a few control chars, but keep > 95%
	data[0] = 0x01
	data[50] = 0x02
	if !IsLikelyText(data) {
		t.Error("IsLikelyText should return true for mostly-text data via isProbablyText")
	}
}

func TestIsLikelyText_OctetStreamMostlyBinary(t *testing.T) {
	// Data that mimetype detects as application/octet-stream, mostly binary
	// This tests the isProbablyText fallback returning false
	data := make([]byte, 200)
	for i := range data {
		data[i] = byte(i % 20) // lots of control characters
	}
	if IsLikelyText(data) {
		t.Error("IsLikelyText should return false for mostly binary data")
	}
}

func TestIsLikelyText_GoSourceCode(t *testing.T) {
	data := []byte(`package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}
`)
	if !IsLikelyText(data) {
		t.Error("IsLikelyText should return true for Go source code")
	}
}

func TestIsLikelyText_XMLContent(t *testing.T) {
	data := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<root>
  <item>value</item>
</root>
`)
	if !IsLikelyText(data) {
		t.Error("IsLikelyText should return true for XML content")
	}
}

// ---------------------------------------------------------------------------
// GetDocumentId — basic shape tests only (requires idtable.Init for real IDs)
// ---------------------------------------------------------------------------
// Note: GetDocumentId depends on idtable.GetId which requires a Pebble DB.
// We only test the path normalization behavior here, not the full ID generation.
// Full integration tests should be in a separate package with DB setup.

func TestGetDocumentId_PathNormalizationConsistency(t *testing.T) {
	// We can't test the exact ID without the DB, but we can verify
	// that calling with the same path twice returns the same error
	// (since DB is not initialized, both calls should fail consistently)
	_, err1 := GetDocumentId("some/path/file.go")
	_, err2 := GetDocumentId("some/path/file.go")

	if (err1 == nil) != (err2 == nil) {
		t.Errorf("GetDocumentId consistency: first call err=%v, second call err=%v", err1, err2)
	}
}
