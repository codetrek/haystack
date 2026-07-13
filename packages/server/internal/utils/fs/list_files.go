package fsutils

import (
	"os"
	"path/filepath"
)

// FileInfo holds information about a file with its relative path
type FileInfo struct {
	Path         string // Relative path from root
	Size         int64  // File size in bytes
	ModifiedTime int64  // Last modified time in nanoseconds
}

type ListFileFilter interface {
	Match(path string, isDir bool) bool
}

type ListFileOptions struct {
	Filter ListFileFilter
}

// ListFiles lists all regular files in a directory and its subdirectories,
// applying gitignore rules to exclude ignored files.
// Parameters:
//   - rootPath: The root directory to start searching from
//
// Returns:
//   - []FileInfo: List of non-ignored regular files
//   - error: Any error encountered during file traversal
func ListFiles(rootPath string, options ListFileOptions, cb func(fileInfo FileInfo) bool) error {
	// Normalize and abs the root path
	rootPath, err := filepath.Abs(rootPath)
	if err != nil {
		return err
	}

	// Use a queue-based approach instead of recursion
	type pathItem struct {
		fullPath string
		relPath  string
	}

	// Initialize the queue with the root path
	queue := []pathItem{{fullPath: rootPath, relPath: ""}}

	// Process the queue in a loop
	for len(queue) > 0 {
		// Dequeue a path
		current := queue[0]
		queue = queue[1:]

		// Read the directory contents
		entries, err := os.ReadDir(current.fullPath)
		if err != nil {
			// Skip directories that can't be read
			continue
		}

		// Process each entry
		for _, entry := range entries {
			// Create entry's relative path
			entryName := entry.Name()

			// Construct the entry's paths
			entryRelPath := entryName
			if current.relPath != "" {
				entryRelPath = filepath.Join(current.relPath, entryName)
			}
			entryFullPath := filepath.Join(current.fullPath, entryName)

			if options.Filter != nil && !options.Filter.Match(entryRelPath, entry.IsDir()) {
				continue
			}

			// Handle directories first
			if entry.IsDir() {
				// Enqueue this directory for processing
				queue = append(queue, pathItem{
					fullPath: entryFullPath,
					relPath:  entryRelPath,
				})
				continue
			}

			// Get file info for size
			info, err := entry.Info()
			if err != nil {
				// Skip files that can't be accessed
				continue
			}

			fileInfo := FileInfo{
				Path:         filepath.ToSlash(entryRelPath),
				Size:         info.Size(),
				ModifiedTime: info.ModTime().UnixNano(),
			}

			if continueScan := cb(fileInfo); !continueScan {
				return nil
			}
		}
	}

	return nil
}
