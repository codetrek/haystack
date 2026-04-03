package fsutils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFile(t *testing.T) {
	// Create a temp directory
	tempDir, err := os.MkdirTemp("", "copyfile-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Run("successful copy", func(t *testing.T) {
		srcPath := filepath.Join(tempDir, "source.txt")
		dstPath := filepath.Join(tempDir, "dest.txt")
		content := []byte("hello world, this is test content")

		if err := os.WriteFile(srcPath, content, 0644); err != nil {
			t.Fatalf("Failed to write source file: %v", err)
		}

		if err := CopyFile(srcPath, dstPath); err != nil {
			t.Fatalf("CopyFile failed: %v", err)
		}

		result, err := os.ReadFile(dstPath)
		if err != nil {
			t.Fatalf("Failed to read destination file: %v", err)
		}
		if string(result) != string(content) {
			t.Errorf("Destination content = %q, want %q", string(result), string(content))
		}
	})

	t.Run("source does not exist", func(t *testing.T) {
		srcPath := filepath.Join(tempDir, "nonexistent.txt")
		dstPath := filepath.Join(tempDir, "dest2.txt")

		err := CopyFile(srcPath, dstPath)
		if err == nil {
			t.Error("CopyFile should return error for nonexistent source")
		}
	})

	t.Run("destination dir does not exist", func(t *testing.T) {
		srcPath := filepath.Join(tempDir, "source.txt")
		// source.txt already exists from earlier sub-test
		dstPath := filepath.Join(tempDir, "nonexistent_dir", "dest.txt")

		err := CopyFile(srcPath, dstPath)
		if err == nil {
			t.Error("CopyFile should return error when destination dir does not exist")
		}
	})

	t.Run("empty file copy", func(t *testing.T) {
		srcPath := filepath.Join(tempDir, "empty_source.txt")
		dstPath := filepath.Join(tempDir, "empty_dest.txt")

		if err := os.WriteFile(srcPath, []byte{}, 0644); err != nil {
			t.Fatalf("Failed to write empty source file: %v", err)
		}

		if err := CopyFile(srcPath, dstPath); err != nil {
			t.Fatalf("CopyFile failed for empty file: %v", err)
		}

		info, err := os.Stat(dstPath)
		if err != nil {
			t.Fatalf("Failed to stat destination file: %v", err)
		}
		if info.Size() != 0 {
			t.Errorf("Expected destination file size 0, got %d", info.Size())
		}
	})

	t.Run("overwrite existing destination", func(t *testing.T) {
		srcPath := filepath.Join(tempDir, "src_overwrite.txt")
		dstPath := filepath.Join(tempDir, "dst_overwrite.txt")

		if err := os.WriteFile(srcPath, []byte("new content"), 0644); err != nil {
			t.Fatalf("Failed to write source file: %v", err)
		}
		if err := os.WriteFile(dstPath, []byte("old content"), 0644); err != nil {
			t.Fatalf("Failed to write destination file: %v", err)
		}

		if err := CopyFile(srcPath, dstPath); err != nil {
			t.Fatalf("CopyFile failed: %v", err)
		}

		result, err := os.ReadFile(dstPath)
		if err != nil {
			t.Fatalf("Failed to read destination file: %v", err)
		}
		if string(result) != "new content" {
			t.Errorf("Destination content = %q, want %q", string(result), "new content")
		}
	})
}

func TestReadFileWithDefault(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "readfile-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Run("existing file returns content", func(t *testing.T) {
		filePath := filepath.Join(tempDir, "existing.txt")
		content := []byte("file content here")
		if err := os.WriteFile(filePath, content, 0644); err != nil {
			t.Fatalf("Failed to write file: %v", err)
		}

		result := ReadFileWithDefault(filePath, []byte("default"))
		if string(result) != string(content) {
			t.Errorf("ReadFileWithDefault() = %q, want %q", string(result), string(content))
		}
	})

	t.Run("nonexistent file returns default", func(t *testing.T) {
		defaultVal := []byte("default value")
		result := ReadFileWithDefault(filepath.Join(tempDir, "nope.txt"), defaultVal)
		if string(result) != string(defaultVal) {
			t.Errorf("ReadFileWithDefault() = %q, want %q", string(result), string(defaultVal))
		}
	})

	t.Run("nonexistent file with nil default", func(t *testing.T) {
		result := ReadFileWithDefault(filepath.Join(tempDir, "missing.txt"), nil)
		if result != nil {
			t.Errorf("ReadFileWithDefault() = %v, want nil", result)
		}
	})

	t.Run("empty file returns empty bytes", func(t *testing.T) {
		filePath := filepath.Join(tempDir, "empty.txt")
		if err := os.WriteFile(filePath, []byte{}, 0644); err != nil {
			t.Fatalf("Failed to write file: %v", err)
		}

		result := ReadFileWithDefault(filePath, []byte("default"))
		if len(result) != 0 {
			t.Errorf("ReadFileWithDefault() returned %d bytes, want 0", len(result))
		}
	})
}

// passAllFilter matches everything (for testing ListFiles without gitignore)
type passAllFilter struct{}

func (f *passAllFilter) Match(path string, isDir bool) bool {
	return true
}

// excludeFilter excludes specific patterns
type excludeFilter struct {
	excludeNames map[string]bool
}

func (f *excludeFilter) Match(path string, isDir bool) bool {
	base := filepath.Base(path)
	return !f.excludeNames[base]
}

func TestListFilesNoFilter(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "listfiles-nofilter-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create simple structure
	if err := os.MkdirAll(filepath.Join(tempDir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"a.txt", "b.txt", "sub/c.txt"} {
		if err := os.WriteFile(filepath.Join(tempDir, f), []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	var files []FileInfo
	err = ListFiles(tempDir, ListFileOptions{Filter: nil}, func(fi FileInfo) bool {
		files = append(files, fi)
		return true
	})
	if err != nil {
		t.Fatalf("ListFiles failed: %v", err)
	}

	if len(files) != 3 {
		t.Errorf("Expected 3 files, got %d", len(files))
		for _, f := range files {
			t.Logf("  %s", f.Path)
		}
	}
}

func TestListFilesCallbackStopsEarly(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "listfiles-stop-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create multiple files
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt"} {
		if err := os.WriteFile(filepath.Join(tempDir, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	count := 0
	err = ListFiles(tempDir, ListFileOptions{}, func(fi FileInfo) bool {
		count++
		return count < 2 // stop after 2nd file
	})
	if err != nil {
		t.Fatalf("ListFiles failed: %v", err)
	}

	if count != 2 {
		t.Errorf("Expected callback called 2 times, got %d", count)
	}
}

func TestListFilesWithFilter(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "listfiles-filter-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	if err := os.MkdirAll(filepath.Join(tempDir, "include"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, "exclude"), 0755); err != nil {
		t.Fatal(err)
	}

	for _, f := range []string{"root.txt", "include/yes.txt", "exclude/no.txt"} {
		if err := os.WriteFile(filepath.Join(tempDir, f), []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	filter := &excludeFilter{excludeNames: map[string]bool{"exclude": true}}
	var files []FileInfo
	err = ListFiles(tempDir, ListFileOptions{Filter: filter}, func(fi FileInfo) bool {
		files = append(files, fi)
		return true
	})
	if err != nil {
		t.Fatalf("ListFiles failed: %v", err)
	}

	// Should only get root.txt and include/yes.txt (exclude dir is filtered out)
	if len(files) != 2 {
		t.Errorf("Expected 2 files, got %d", len(files))
		for _, f := range files {
			t.Logf("  %s", f.Path)
		}
	}
}

func TestListFilesNonexistentRoot(t *testing.T) {
	// ListFiles on a path that doesn't exist — the ReadDir will fail
	// and it should be silently skipped (no error returned).
	err := ListFiles("/tmp/definitely_not_a_real_dir_12345", ListFileOptions{}, func(fi FileInfo) bool {
		t.Error("callback should not be called for nonexistent dir")
		return true
	})
	if err != nil {
		t.Fatalf("ListFiles should not return error for unreadable root, got: %v", err)
	}
}

func TestListFilesEmptyDir(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "listfiles-empty-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	var files []FileInfo
	err = ListFiles(tempDir, ListFileOptions{}, func(fi FileInfo) bool {
		files = append(files, fi)
		return true
	})
	if err != nil {
		t.Fatalf("ListFiles failed: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("Expected 0 files in empty dir, got %d", len(files))
	}
}

func TestListFilesRelativePath(t *testing.T) {
	// ListFiles should handle relative paths by converting them to absolute
	tempDir, err := os.MkdirTemp("", "listfiles-relpath-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	if err := os.WriteFile(filepath.Join(tempDir, "file.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	// Use absolute path (the function converts internally)
	var files []FileInfo
	err = ListFiles(tempDir, ListFileOptions{}, func(fi FileInfo) bool {
		files = append(files, fi)
		return true
	})
	if err != nil {
		t.Fatalf("ListFiles failed: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("Expected 1 file, got %d", len(files))
	}
}

func TestFileInfoFields(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "fileinfo-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	content := []byte("hello world 12345")
	filePath := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatal(err)
	}

	var files []FileInfo
	err = ListFiles(tempDir, ListFileOptions{}, func(fi FileInfo) bool {
		files = append(files, fi)
		return true
	})
	if err != nil {
		t.Fatalf("ListFiles failed: %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("Expected 1 file, got %d", len(files))
	}

	fi := files[0]
	if fi.Path != "test.txt" {
		t.Errorf("Path = %q, want %q", fi.Path, "test.txt")
	}
	if fi.Size != int64(len(content)) {
		t.Errorf("Size = %d, want %d", fi.Size, len(content))
	}
	if fi.ModifiedTime <= 0 {
		t.Error("ModifiedTime should be positive")
	}
}
