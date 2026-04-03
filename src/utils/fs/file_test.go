package fsutils

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCopyFile_BasicSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src.txt")
	dst := filepath.Join(tmpDir, "dst.txt")

	assert.NoError(t, os.WriteFile(src, []byte("hello world"), 0644))
	assert.NoError(t, CopyFile(src, dst))

	content, err := os.ReadFile(dst)
	assert.NoError(t, err)
	assert.Equal(t, "hello world", string(content))
}

func TestCopyFile_SrcNotExist(t *testing.T) {
	tmpDir := t.TempDir()
	err := CopyFile(filepath.Join(tmpDir, "nonexistent"), filepath.Join(tmpDir, "dst"))
	assert.Error(t, err)
}

func TestCopyFile_DstBadPath(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src.txt")
	assert.NoError(t, os.WriteFile(src, []byte("test"), 0644))

	err := CopyFile(src, "/nonexistent/dir/dst.txt")
	assert.Error(t, err)
}

func TestReadFileWithDefault_FileExists(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.txt")
	assert.NoError(t, os.WriteFile(path, []byte("content"), 0644))

	result := ReadFileWithDefault(path, []byte("default"))
	assert.Equal(t, "content", string(result))
}

func TestReadFileWithDefault_FileNotExist(t *testing.T) {
	result := ReadFileWithDefault("/nonexistent/file.txt", []byte("default"))
	assert.Equal(t, "default", string(result))
}

func TestReadFileWithDefault_EmptyDefault(t *testing.T) {
	result := ReadFileWithDefault("/nonexistent/file.txt", []byte(""))
	assert.Equal(t, "", string(result))
}
