package gitutils

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"

	gitignore "github.com/sabhiram/go-gitignore"
)

// GitIgnore represents the entire gitignore system
type GitIgnore struct {
	rootPath   string
	ruleFiles  map[string]*GitIgnoreRules
	cache      map[string]bool // Cache for directory paths only
	mutex      sync.RWMutex    // Mutex to protect shared data
	ignoreCase bool
}

// GitIgnoreRules represents a single .gitignore file
type GitIgnoreRules struct {
	baseDir     string
	negate      *gitignore.GitIgnore
	ignorer     *gitignore.GitIgnore
	isGitRoot   bool
	hasPatterns bool // ignorer holds at least one positive pattern
}

func NewGitIgnoreRules(patterns []string, baseDir string) (*GitIgnoreRules, error) {
	// Create ignorer using the library
	negate := []string{}
	positive := []string{}
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" || strings.HasPrefix(pattern, "#") {
			continue // Skip empty lines and comments
		}

		if strings.HasPrefix(pattern, "!") {
			negate = append(negate, strings.TrimPrefix(pattern, "!"))
		} else {
			positive = append(positive, pattern)
		}
	}

	var neg *gitignore.GitIgnore
	if len(negate) > 0 {
		neg = gitignore.CompileIgnoreLines(negate...)
	}

	return &GitIgnoreRules{
		baseDir:     baseDir,
		negate:      neg,
		ignorer:     gitignore.CompileIgnoreLines(positive...),
		hasPatterns: len(positive) > 0,
	}, nil
}

// NewGitIgnoreRulesFromFile creates a GitIgnoreRuleFile from a file
func NewGitIgnoreRulesFromFile(filePath string, ignoreCase bool) (*GitIgnoreRules, error) {
	baseDir := filepath.Dir(filePath)

	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Read lines from file
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return NewGitIgnoreRules(lines, baseDir)
}

func NewGitIgnoreRulesFromString(rules string, baseDir string, ignoreCase bool) (*GitIgnoreRules, error) {
	// Split rules into lines
	lines := strings.Split(rules, "\n")

	return NewGitIgnoreRules(lines, baseDir)
}

// NewGitIgnore creates a new GitIgnore system
func NewGitIgnore(rootPath string, ignoreCase bool) *GitIgnore {
	rootPath = filepath.Clean(rootPath)
	if !filepath.IsAbs(rootPath) {
		return nil
	}

	ignorer := &GitIgnore{
		rootPath:   rootPath,
		ruleFiles:  make(map[string]*GitIgnoreRules),
		cache:      make(map[string]bool),
		mutex:      sync.RWMutex{},
		ignoreCase: ignoreCase,
	}

	// Load root .gitignore if exists
	ignorer.loadGitIgnoreForDir(rootPath)

	return ignorer
}

// IsIgnored checks if a path should be ignored by this .gitignore file
func (f *GitIgnoreRules) IsIgnored(absPath string, isDir bool) bool {
	// With no positive patterns this file can never ignore a path (a negate-only
	// or empty .gitignore), so skip the relative-path construction and matching
	// entirely. Most ancestor directories on the scan path have no .gitignore and
	// land here.
	if !f.hasPatterns {
		return false
	}

	// Get the path relative to the base directory. In the scan hot path baseDir
	// is always an ancestor of absPath, so we derive the relative path with a
	// prefix slice and skip filepath.Rel's double Clean — its allocations
	// dominate this function's allocation profile. Fall back to filepath.Rel for
	// the rare non-ancestor caller.
	rel, ok := relUnderBase(absPath, f.baseDir)
	if !ok {
		var err error
		rel, err = filepath.Rel(f.baseDir, absPath)
		if err != nil {
			return false
		}
	}

	// Normalize path separators to forward slashes
	relPath := "/" + filepath.ToSlash(rel)
	if isDir && !strings.HasSuffix(relPath, "/") {
		relPath += "/"
	}

	if f.isNegate(relPath) {
		return false
	}

	// Use the library's matching function
	return f.ignorer.MatchesPath(relPath)
}

// relUnderBase returns the path of absPath relative to base — using the OS
// separator, with no leading separator, and "." when the two are equal — but
// only when base is an ancestor of (or equal to) absPath. The boolean is false
// when absPath is not under base, in which case the caller should fall back to
// filepath.Rel. For the ancestor case this matches filepath.Rel's output
// without allocating.
func relUnderBase(absPath, base string) (string, bool) {
	if absPath == base {
		return ".", true
	}
	if len(absPath) <= len(base) || !strings.HasPrefix(absPath, base) {
		return "", false
	}
	// Require a separator boundary so "/foo" is not treated as a prefix of
	// "/foobar". base may itself end in a separator (e.g. a filesystem root).
	if os.IsPathSeparator(base[len(base)-1]) {
		return absPath[len(base):], true
	}
	if os.IsPathSeparator(absPath[len(base)]) {
		return absPath[len(base)+1:], true
	}
	return "", false
}

func (f *GitIgnoreRules) isNegate(relPath string) bool {
	if f.negate == nil {
		return false
	}
	return f.negate.MatchesPath(relPath)
}

var outOfRoot = filepath.Clean("../")

// IsIgnored checks if a path should be ignored by considering all applicable .gitignore rules
func (g *GitIgnore) IsIgnored(relPath string, isDir bool) bool {
	relPath = filepath.Clean(relPath)
	if relPath == "." || relPath == "" || strings.HasPrefix(relPath, outOfRoot) {
		return false
	}

	// Only cache directory results
	var cacheKey string
	if isDir {
		cacheKey = relPath

		// Check cache first for directories
		g.mutex.RLock()
		if result, exists := g.cache[cacheKey]; exists {
			g.mutex.RUnlock()
			return result
		}
		g.mutex.RUnlock()
	}

	baseName := filepath.Base(relPath)
	// Case insensitive checking for .git and .gitignore
	baseNameLower := strings.ToLower(baseName)
	if isDir && baseNameLower == ".git" {
		return true
	} else if !isDir && baseNameLower == ".gitignore" {
		return true
	}

	// Convert the relative path to absolute
	absPath := filepath.Join(g.rootPath, relPath)
	if absPath == g.rootPath {
		return false
	}

	// Start with the directory containing the file/dir
	dirPath := absPath
	if !isDir {
		dirPath = filepath.Dir(absPath)
	}

	// Prepare list of directories to check, starting from most specific
	var dirsToCheck []string
	currPath := dirPath
	for currPath != g.rootPath && strings.HasPrefix(currPath, g.rootPath) {
		dirsToCheck = append(dirsToCheck, currPath)
		currPath = filepath.Dir(currPath)
	}
	// Add the root directory last (least specific)
	dirsToCheck = append(dirsToCheck, g.rootPath)

	// First check for negation rules (these have highest precedence)
	for _, dir := range dirsToCheck {
		if ruleFile := g.loadGitIgnoreForDir(dir); ruleFile != nil {
			if ruleFile.isNegate(relPath) {
				if isDir {
					g.cacheResult(cacheKey, false)
				}
				return false
			}
			if ruleFile.isGitRoot {
				break
			}
		}
	}

	// Check if parent directory is ignored
	parentDir := filepath.Dir(absPath)
	if parentDir != g.rootPath {
		parentRelPath, err := filepath.Rel(g.rootPath, parentDir)
		if err == nil {
			// If parent directory is ignored, files within it are also ignored
			if g.IsIgnored(parentRelPath, true) {
				return true
			}
		}
	}

	// Then check for ignore rules
	for _, dir := range dirsToCheck {
		g.mutex.RLock()
		ruleFile, exists := g.ruleFiles[dir]
		g.mutex.RUnlock()

		if exists && ruleFile != nil {
			if ruleFile.IsIgnored(absPath, isDir) {
				if isDir {
					g.cacheResult(cacheKey, true)
				}
				return true
			}
		}

		if ruleFile.isGitRoot {
			break
		}
	}

	if isDir {
		g.cacheResult(cacheKey, false)
	}
	return false
}

// cacheResult stores a directory result in the cache
func (g *GitIgnore) cacheResult(key string, ignored bool) {
	g.mutex.Lock()
	g.cache[key] = ignored
	g.mutex.Unlock()
}

// ClearCache clears the directory path cache
func (g *GitIgnore) ClearCache() {
	g.mutex.Lock()
	g.cache = make(map[string]bool)
	g.mutex.Unlock()
}

// loadGitIgnoreForDir loads .gitignore file for a directory if it exists
func (g *GitIgnore) loadGitIgnoreForDir(dir string) *GitIgnoreRules {
	// Use read lock to check if already loaded
	g.mutex.RLock()
	if rf, ok := g.ruleFiles[dir]; ok {
		g.mutex.RUnlock()
		return rf
	}
	g.mutex.RUnlock()

	// Need to load, use write lock
	g.mutex.Lock()
	defer g.mutex.Unlock()

	// Double check to ensure no other goroutine loaded the same directory while acquiring lock
	if rf, ok := g.ruleFiles[dir]; ok {
		return rf
	}

	var rf *GitIgnoreRules

	gitIgnorePath := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(gitIgnorePath); err == nil {
		rf, _ = NewGitIgnoreRulesFromFile(gitIgnorePath, g.ignoreCase)
	}

	if rf == nil {
		// Create an empty GitIgnoreRules when no .gitignore file exists
		rf = &GitIgnoreRules{
			baseDir: dir,
			ignorer: gitignore.CompileIgnoreLines(),
		}
	}

	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		rf.isGitRoot = true
	}

	g.ruleFiles[dir] = rf
	return rf
}
