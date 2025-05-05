package gitutils

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func setupBasicTestEnvironment(t *testing.T) (string, func()) {
	// Create temporary directory
	tempDir, err := os.MkdirTemp("", "gitignore-test")
	assert.NoError(t, err, "Failed to create temp dir")

	// Create test files and directories
	testFile := filepath.Join(tempDir, "test.txt")
	err = os.WriteFile(testFile, []byte("test"), 0644)
	assert.NoError(t, err, "Failed to create test file")

	// Create dir directory
	dirPath := filepath.Join(tempDir, "dir")
	err = os.MkdirAll(dirPath, 0755)
	assert.NoError(t, err, "Failed to create dir directory")

	// Create .gitignore file
	gitignoreContent := `
# Comment line
*.log
!important.log
dir/
/test.txt
**/temp.txt
`
	gitignorePath := filepath.Join(tempDir, ".gitignore")
	err = os.WriteFile(gitignorePath, []byte(gitignoreContent), 0644)
	assert.NoError(t, err, "Failed to create .gitignore")

	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	return tempDir, cleanup
}

func TestBasicGitIgnoreRules(t *testing.T) {
	tempDir, cleanup := setupBasicTestEnvironment(t)
	defer cleanup()

	ruleFile, err := NewGitIgnoreRulesFromFile(filepath.Join(tempDir, ".gitignore"), true)
	assert.NoError(t, err, "Failed to create rule file")

	t.Run("log file patterns", func(t *testing.T) {
		t.Run("normal log file", func(t *testing.T) {
			fullPath := filepath.Join(tempDir, "test.log")
			assert.True(t, ruleFile.IsIgnored(fullPath, false),
				"Log file should be ignored by *.log pattern")
		})

		t.Run("important log file", func(t *testing.T) {
			fullPath := filepath.Join(tempDir, "important.log")
			assert.False(t, ruleFile.IsIgnored(fullPath, false),
				"important.log should not be ignored due to !important.log rule")
		})
	})

	t.Run("directory patterns", func(t *testing.T) {
		t.Run("dir directory", func(t *testing.T) {
			fullPath := filepath.Join(tempDir, "dir")
			assert.True(t, ruleFile.IsIgnored(fullPath, true),
				"dir/ directory should be ignored")
		})
	})

	t.Run("root file patterns", func(t *testing.T) {
		t.Run("root test.txt", func(t *testing.T) {
			fullPath := filepath.Join(tempDir, "test.txt")
			assert.True(t, ruleFile.IsIgnored(fullPath, false),
				"Root test.txt should be ignored by /test.txt pattern")
		})
	})

	t.Run("glob patterns", func(t *testing.T) {
		t.Run("root temp file", func(t *testing.T) {
			fullPath := filepath.Join(tempDir, "temp.txt")
			assert.True(t, ruleFile.IsIgnored(fullPath, false),
				"temp.txt should be ignored by **/temp.txt pattern")
		})

		t.Run("subdir temp file", func(t *testing.T) {
			fullPath := filepath.Join(tempDir, "subdir", "temp.txt")
			assert.True(t, ruleFile.IsIgnored(fullPath, false),
				"subdir/temp.txt should be ignored by **/temp.txt pattern")
		})

		t.Run("other file", func(t *testing.T) {
			fullPath := filepath.Join(tempDir, "other.txt")
			assert.False(t, ruleFile.IsIgnored(fullPath, false),
				"other.txt should not be ignored")
		})
	})
}

func createTestEnvironmentForSystem(t *testing.T) (string, []string, []string) {
	// Create temporary directory
	tempDir, err := os.MkdirTemp("", "gitignore-test")
	assert.NoError(t, err, "Failed to create temp dir")

	// Create directory structure
	dirs := []string{
		"subdir1",
		"subdir1/subsubdir",
		"subdir2",
		"subdir2/ignored_dir",
		"subdir2/ignored_dir/subdir",
	}

	for _, dir := range dirs {
		err := os.MkdirAll(filepath.Join(tempDir, dir), 0755)
		assert.NoError(t, err, "Failed to create directory %s", dir)
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
		"subdir2/ignored_dir/subdir/deep_file.txt",
	}

	for _, file := range files {
		filePath := filepath.Join(tempDir, file)
		f, err := os.Create(filePath)
		assert.NoError(t, err, "Failed to create file %s", file)
		f.Close()
	}

	// Create root directory .gitignore
	rootGitIgnore := `
# Ignore log files in all directories
*.log

# Ignore the entire ignored_dir directory
ignored_dir/
`
	err = os.WriteFile(filepath.Join(tempDir, ".gitignore"), []byte(rootGitIgnore), 0644)
	assert.NoError(t, err, "Failed to create root .gitignore")

	// Create subdirectory .gitignore
	subDirGitIgnore := `
# Don't ignore this specific log file
!file2.log

# Ignore tmp files
*.tmp
`
	err = os.WriteFile(filepath.Join(tempDir, "subdir1", ".gitignore"), []byte(subDirGitIgnore), 0644)
	assert.NoError(t, err, "Failed to create subdir .gitignore")

	return tempDir, dirs, files
}

func TestGitIgnoreSystem(t *testing.T) {
	tempDir, _, _ := createTestEnvironmentForSystem(t)
	defer os.RemoveAll(tempDir)

	ignorer := NewGitIgnore(tempDir, true)

	t.Run("root directory rules", func(t *testing.T) {
		t.Run("root file not ignored", func(t *testing.T) {
			assert.False(t, ignorer.IsIgnored("root_file.txt", false),
				"Root level text file should not be ignored")
		})

		t.Run("root log file ignored", func(t *testing.T) {
			assert.True(t, ignorer.IsIgnored("should_ignore.log", false),
				"Root level log file should be ignored")
		})
	})

	t.Run("subdirectory rules", func(t *testing.T) {
		t.Run("subdir file not ignored", func(t *testing.T) {
			assert.False(t, ignorer.IsIgnored("subdir1/file1.txt", false),
				"Subdirectory text file should not be ignored")
		})

		t.Run("subdir log file not ignored due to override", func(t *testing.T) {
			assert.False(t, ignorer.IsIgnored("subdir1/file2.log", false),
				"Subdirectory log file should not be ignored due to override")
		})

		t.Run("subdir tmp file ignored", func(t *testing.T) {
			assert.True(t, ignorer.IsIgnored("subdir1/subsubdir/should_ignore.tmp", false),
				"Subdirectory tmp file should be ignored")
		})
	})

	t.Run("directory rules", func(t *testing.T) {
		t.Run("ignored dir ignored", func(t *testing.T) {
			assert.True(t, ignorer.IsIgnored("subdir2/ignored_dir", true),
				"Ignored directory should be ignored")
		})

		t.Run("file in ignored dir ignored", func(t *testing.T) {
			assert.True(t, ignorer.IsIgnored("subdir2/ignored_dir/ignored_file.txt", false),
				"File in ignored directory should be ignored")
		})

		t.Run("subdir in ignored dir ignored", func(t *testing.T) {
			assert.True(t, ignorer.IsIgnored("subdir2/ignored_dir/subdir", true),
				"Subdirectory in ignored directory should be ignored")
		})

		t.Run("deep file in ignored dir ignored", func(t *testing.T) {
			assert.True(t, ignorer.IsIgnored("subdir2/ignored_dir/subdir/deep_file.txt", false),
				"Deep file in ignored directory should be ignored")
		})
	})

	t.Run("special path rules", func(t *testing.T) {
		t.Run("non-existent file", func(t *testing.T) {
			assert.False(t, ignorer.IsIgnored("non_existent.txt", false),
				"Non-existent file should not be ignored")
		})

		t.Run("root directory path", func(t *testing.T) {
			assert.False(t, ignorer.IsIgnored("", true),
				"Root directory path should not be ignored")
		})

		t.Run("parent directory path", func(t *testing.T) {
			assert.False(t, ignorer.IsIgnored("../test.txt", false),
				"Parent directory path should not be ignored")
		})
	})
}

func TestGitIgnoreEdgeCases(t *testing.T) {
	t.Run("empty gitignore", func(t *testing.T) {
		tempDir, err := os.MkdirTemp("", "gitignore-test")
		assert.NoError(t, err, "Failed to create temp dir")
		defer os.RemoveAll(tempDir)

		emptyGitIgnore := filepath.Join(tempDir, ".gitignore")
		err = os.WriteFile(emptyGitIgnore, []byte(""), 0644)
		assert.NoError(t, err, "Failed to create empty .gitignore")

		testFile := filepath.Join(tempDir, "test.txt")
		err = os.WriteFile(testFile, []byte("test"), 0644)
		assert.NoError(t, err, "Failed to create test file")

		ignorer := NewGitIgnore(tempDir, true)
		assert.False(t, ignorer.IsIgnored("test.txt", false), "File should not be ignored with empty .gitignore")
	})

	t.Run("invalid gitignore pattern", func(t *testing.T) {
		tempDir, err := os.MkdirTemp("", "gitignore-test")
		assert.NoError(t, err, "Failed to create temp dir")
		defer os.RemoveAll(tempDir)

		invalidGitIgnore := filepath.Join(tempDir, "subdir", ".gitignore")
		err = os.MkdirAll(filepath.Dir(invalidGitIgnore), 0755)
		assert.NoError(t, err, "Failed to create subdir")

		err = os.WriteFile(invalidGitIgnore, []byte("invalid pattern [*"), 0644)
		assert.NoError(t, err, "Failed to create invalid .gitignore")

		ignorer := NewGitIgnore(tempDir, true)
		assert.False(t, ignorer.IsIgnored("subdir/test.txt", false), "File should not be ignored with invalid .gitignore pattern")
	})

	t.Run("non-existent path", func(t *testing.T) {
		tempDir, err := os.MkdirTemp("", "gitignore-test")
		assert.NoError(t, err, "Failed to create temp dir")
		defer os.RemoveAll(tempDir)

		ignorer := NewGitIgnore(tempDir, true)
		assert.False(t, ignorer.IsIgnored("non_existent.txt", false), "Non-existent path should not be ignored")
	})

	t.Run("empty path", func(t *testing.T) {
		tempDir, err := os.MkdirTemp("", "gitignore-test")
		assert.NoError(t, err, "Failed to create temp dir")
		defer os.RemoveAll(tempDir)

		ignorer := NewGitIgnore(tempDir, true)
		assert.False(t, ignorer.IsIgnored("", false), "Empty path should not be ignored")
	})

	t.Run("root path", func(t *testing.T) {
		tempDir, err := os.MkdirTemp("", "gitignore-test")
		assert.NoError(t, err, "Failed to create temp dir")
		defer os.RemoveAll(tempDir)

		ignorer := NewGitIgnore(tempDir, true)
		assert.False(t, ignorer.IsIgnored("/", false), "Root path should not be ignored")
	})
}
