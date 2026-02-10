package fsutils

import (
	"os"
	"path/filepath"
	"testing"

	gitutils "github.com/codetrek/haystack/utils/git"
)

type GitIgnoreFilter struct {
	ignore *gitutils.GitIgnore
}

func (f *GitIgnoreFilter) Match(path string, isDir bool) bool {
	return !f.ignore.IsIgnored(path, isDir)
}

func TestListFiles(t *testing.T) {
	// Create a temporary directory structure for testing
	tempDir, err := os.MkdirTemp("", "gitignore-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create test directory structure
	dirs := []string{
		"subdir1",
		"subdir1/subsubdir",
		"subdir2",
		"subdir2/ignored_dir",
	}

	// Create the directories
	for _, dir := range dirs {
		err := os.MkdirAll(filepath.Join(tempDir, dir), 0755)
		if err != nil {
			t.Fatalf("Failed to create directory %s: %v", dir, err)
		}
	}

	// Create test files
	files := []string{
		"root_file.txt",
		"should_ignore.log",
		"subdir1/file1.txt",
		"subdir1/file2.log",
		"subdir1/subsubdir/deep_file.txt",
		"subdir1/subsubdir/should_ignore.tmp",
		"subdir2/file3.txt",
		"subdir2/should_ignore.log",
		"subdir2/ignored_dir/ignored_file.txt",
	}

	for _, file := range files {
		filePath := filepath.Join(tempDir, file)
		f, err := os.Create(filePath)
		if err != nil {
			t.Fatalf("Failed to create file %s: %v", file, err)
		}
		f.Close()
	}

	// Create root .gitignore
	rootGitIgnore := `
# Ignore log files in all directories
*.log

# Ignore the entire ignored_dir directory
ignored_dir/
`

	err = os.WriteFile(filepath.Join(tempDir, ".gitignore"), []byte(rootGitIgnore), 0644)
	if err != nil {
		t.Fatalf("Failed to create root .gitignore: %v", err)
	}

	// Create subdirectory .gitignore that overrides some root rules
	subDirGitIgnore := `
# Don't ignore this specific log file
!file2.log

# Ignore tmp files
*.tmp
`

	err = os.WriteFile(filepath.Join(tempDir, "subdir1", ".gitignore"), []byte(subDirGitIgnore), 0644)
	if err != nil {
		t.Fatalf("Failed to create subdir .gitignore: %v", err)
	}

	// List files
	fileInfos := []FileInfo{}
	err = ListFiles(tempDir, ListFileOptions{
		Filter: &GitIgnoreFilter{
			ignore: gitutils.NewGitIgnore(tempDir, true),
		},
	}, func(fileInfo FileInfo) bool {
		fileInfos = append(fileInfos, fileInfo)
		return true
	})

	if err != nil {
		t.Fatalf("ListFiles failed: %v", err)
	}

	// Create map for easier testing
	fileMap := make(map[string]bool) // Path -> exists

	for _, info := range fileInfos {
		fileMap[info.Path] = true
	}

	// Helper function to check if a file exists in the results
	checkFile := func(relPath string, shouldExist bool) {
		exists := fileMap[relPath]
		if exists != shouldExist {
			if shouldExist {
				t.Errorf("File %s should exist in results but doesn't", relPath)
			} else {
				t.Errorf("File %s shouldn't exist in results but does", relPath)
			}
		}
	}

	// Files that should be included
	expectedFiles := []string{
		filepath.ToSlash("root_file.txt"),
		filepath.ToSlash("subdir1/file1.txt"),
		filepath.ToSlash("subdir1/file2.log"), // Not ignored due to override
		filepath.ToSlash("subdir1/subsubdir/deep_file.txt"),
		filepath.ToSlash("subdir2/file3.txt"),
	}

	for _, expected := range expectedFiles {
		checkFile(expected, true)
	}

	// Files that should not be included
	ignoredFiles := []string{
		filepath.ToSlash("should_ignore.log"),
		filepath.ToSlash("subdir1/subsubdir/should_ignore.tmp"),
		filepath.ToSlash("subdir2/should_ignore.log"),
		filepath.ToSlash("subdir2/ignored_dir/ignored_file.txt"),
	}

	for _, ignored := range ignoredFiles {
		checkFile(ignored, false)
	}

	// Directories should not be included
	dirPaths := []string{
		filepath.ToSlash("subdir1"),
		filepath.ToSlash("subdir2"),
		filepath.ToSlash("subdir1/subsubdir"),
		filepath.ToSlash("subdir2/ignored_dir"),
	}

	for _, dir := range dirPaths {
		checkFile(dir, false)
	}
}
