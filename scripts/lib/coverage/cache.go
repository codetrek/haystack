package main

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
)

// FileCache caches file contents to avoid repeated file reads
type FileCache struct {
	files map[string][]string
}

// NewFileCache creates a new FileCache instance
func NewFileCache() *FileCache {
	return &FileCache{
		files: make(map[string][]string),
	}
}

// GetLines returns the lines of a file, caching the result
func (fc *FileCache) GetLines(path string) ([]string, error) {
	if lines, ok := fc.files[path]; ok {
		return lines, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	fc.files[path] = lines
	return lines, nil
}

// AreLinesIgnorable checks if all lines in the range are empty or comments
func (fc *FileCache) AreLinesIgnorable(path string, startLine, endLine int) bool {
	// startLine and endLine are 1-based
	lines, err := fc.GetLines(path)
	if err != nil {
		// If we can't read the file, assume not ignorable to be safe
		return false
	}

	if startLine > endLine {
		return true
	}

	for i := startLine; i <= endLine; i++ {
		if i < 1 || i > len(lines) {
			// Line out of bounds, treat as not ignorable or ignore?
			// If out of bounds, it shouldn't happen if coverage is correct.
			return false
		}
		lineContent := strings.TrimSpace(lines[i-1])
		if lineContent == "" {
			continue
		}
		if strings.HasPrefix(lineContent, "//") {
			continue
		}
		// Found a non-empty, non-comment line
		return false
	}
	return true
}

// ASTCache caches parsed AST files to avoid re-parsing
type ASTCache struct {
	files map[string]*ast.File
	fset  *token.FileSet
}

// NewASTCache creates a new ASTCache instance
func NewASTCache() *ASTCache {
	return &ASTCache{
		files: make(map[string]*ast.File),
		fset:  token.NewFileSet(),
	}
}

// GetAST returns the parsed AST for a file, caching the result
func (ac *ASTCache) GetAST(path string) (*ast.File, *token.FileSet, error) {
	if f, ok := ac.files[path]; ok {
		return f, ac.fset, nil
	}

	f, err := parser.ParseFile(ac.fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, nil, err
	}

	ac.files[path] = f
	return f, ac.fset, nil
}
