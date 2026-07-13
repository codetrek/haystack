package indexer

import (
	"testing"
)

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
// GetDocumentId — path-normalization shape only (real IDs need an idtable DB).
// ---------------------------------------------------------------------------

func TestGetDocumentId_PathNormalizationConsistency(t *testing.T) {
	// We can't test the exact ID without the DB, but calling with the same path
	// twice should fail (or succeed) consistently while the DB is uninitialized.
	_, err1 := GetDocumentId("some/path/file.go")
	_, err2 := GetDocumentId("some/path/file.go")

	if (err1 == nil) != (err2 == nil) {
		t.Errorf("GetDocumentId consistency: first call err=%v, second call err=%v", err1, err2)
	}
}
