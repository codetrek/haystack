package indexer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/server/core/symbols"
	"github.com/ai-microsoft/haystack/server/core/workspace"
	"github.com/ai-microsoft/haystack/shared/running"
)

const (
	// MaxBatchSize is the maximum number of files to parse in a single batch
	MaxBatchSize = 1000
)

// ParseBatch represents a batch of files to be parsed
type ParseBatch struct {
	Workspace *workspace.Workspace
	Files     []string
}

// For ctags language options
func GetLangFromFilename(filename string) string {
	ext := strings.TrimPrefix(filepath.Ext(filename), ".")
	ext = strings.ToLower(ext)

	switch ext {
	case "js", "jsx":
		return "javascript"
	case "ts", "tsx":
		return "typescript"
	case "py":
		return "python"
	case "rs":
		return "rust"
	case "go":
		return "go"
	case "cc", "cpp", "cxx", "hh", "h", "hxx", "hpp":
		return "c++"
	case "c":
		return "c"
	case "cs":
		return "C#"
	case "rb":
		return "ruby"
	case "java":
		return "Java"
	case "php":
		return "php"
	case "swift":
		return "swift"
	default:
		return ""
	}
}

func getCtagsPath() (string, error) {
	ctagsPath := filepath.Join(running.ExecutablePath(), "ctags")
	if runtime.GOOS == "windows" {
		ctagsPath += ".exe"
	}
	if _, err := os.Stat(ctagsPath); err == nil {
		return ctagsPath, nil
	}

	var cmd *exec.Cmd

	if runtime.GOOS == "windows" {
		cmd = exec.Command("where", "ctags")
	} else {
		cmd = exec.Command("which", "-a", "ctags")
	}

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to locate ctags: %v", err)
	}

	paths := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}

		versionCmd := exec.Command(path, "--version")
		if out, err := versionCmd.CombinedOutput(); err == nil {
			log.Printf("[SymbolParser] ctags found and working at: %s\n", path)
			log.Println(string(out))
			return path, nil
		} else {
			log.Printf("[SymbolParser] Warning: ctags at %s failed to run: %v\n", path, err)
		}
	}

	return "", fmt.Errorf("no working ctags executable found")
}

func parseFunction(ctagsPath string, inputFile string, language string, workspacePath string) ([]symbols.DocFunction, error) {
	args := []string{
		"--fields=+n",             // Include line numbers
		"--fields=+K",             // Include kind/type
		"--fields=+S",             // Include scope
		"--fields=+l",             // Include language
		"--extras=+q",             // Include qualified name
		"--output-format=json",    // Use JSON output format
		"--languages=" + language, // Specify language
		"-L",                      // Read file names from file
		inputFile,                 // File containing list of files to parse
	}

	if language == "c++" {
		args = append(args, "--c++-kinds=+p") // Include function prototypes
	}

	cmd := exec.Command(ctagsPath, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Execute command
	err := cmd.Run()
	if err != nil {
		log.Printf("[SymbolParser] Error executing Ctags command: %v", err)
		return nil, err
	}

	// Check for stderr output
	// if stderr.Len() > 0 {
	// 	log.Printf("[SymbolParser] Ctags warning: %s", stderr.String())
	// }
	// log.Printf("[SymbolParser] Parsed %d functions from %s", strings.Count(stdout.String(), "\n"), inputFile)

	// Map to organize functions by file path
	fileMap := make(map[string]*symbols.DocFunction)
	lines := strings.Split(stdout.String(), "\n")
	workspacePath = filepath.ToSlash(workspacePath)

	for _, line := range lines {
		if line == "" {
			continue
		}

		// Parse JSON object
		var obj map[string]interface{}
		err := json.Unmarshal([]byte(line), &obj)
		if err != nil {
			log.Printf("[SymbolParser] Error parsing JSON line: %v", err)
			continue
		}

		// Only include function and method symbols
		kind, ok := obj["kind"].(string)
		if !ok {
			continue
		}
		_, hasSign := obj["signature"].(string)

		if kind == "function" || kind == "method" || (kind == "prototype" && hasSign && language == "c++") {
			name, _ := obj["name"].(string)
			path, _ := obj["path"].(string)
			lineNum, _ := obj["line"].(float64)

			// Skip anonymous functions/namespaces
			if strings.Contains(name, "__anon") {
				continue
			}

			path = filepath.ToSlash(path)
			relPath := path
			if strings.HasPrefix(path, workspacePath) {
				relPath = strings.TrimPrefix(path, workspacePath)
				relPath = strings.TrimPrefix(relPath, "/")
			}

			// Create or get the DocFunction for this file
			docFunc, exists := fileMap[path]
			if !exists {
				docFunc = &symbols.DocFunction{
					ID:        GetDocumentId(path),
					RelPath:   relPath,
					Functions: []symbols.Function{},
				}
				fileMap[path] = docFunc
			}

			// Add the function to the document's function list
			docFunc.Functions = append(docFunc.Functions, symbols.Function{
				Name: name,
				Line: int(lineNum),
			})
		}
	}

	// Convert map to slice
	functions := make([]symbols.DocFunction, 0, len(fileMap))
	for _, docFunc := range fileMap {
		functions = append(functions, *docFunc)
	}

	return functions, nil
}

// Parser handles concurrent file parsing operations
type SymbolParser struct {
	ch    chan ParseBatch
	stop  chan struct{}
	done  chan struct{}
	ctags string

	// cache files for each workspace
	cacheMap   map[*workspace.Workspace][]string
	cacheMutex sync.Mutex
	flushTimer *time.Timer
}

// NewParser creates a new Parser instance
func NewSymbolParser() *SymbolParser {
	p := &SymbolParser{
		ch:       make(chan ParseBatch, 32),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		cacheMap: make(map[*workspace.Workspace][]string),
	}
	p.flushTimer = time.NewTimer(5 * time.Second)
	return p
}

// Start initializes the parser with worker goroutines
func (p *SymbolParser) Start(wg *sync.WaitGroup) {
	if !conf.Get().Embedding.Enabled {
		log.Printf("[SymbolParser] SymbolParser disabled")
		return
	}

	ctagsPath, err := getCtagsPath()
	if err != nil {
		log.Printf("[SymbolParser] Error getting ctags path: %v", err)
		return
	}
	p.ctags = ctagsPath
	log.Printf("[SymbolParser] Using ctags at %s", p.ctags)

	for i := 0; i < conf.Get().Server.SymbolParserWorkers; i++ {
		wg.Add(1)
		go p.run(i, wg)
	}

	// Start a goroutine to flush the cache periodically
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-p.stop:
				return
			case <-p.flushTimer.C:
				p.flushCache()
				p.flushTimer.Reset(5 * time.Second)
			}
		}
	}()
}

func (p *SymbolParser) Stop() {
	if !conf.Get().Embedding.Enabled {
		return
	}

	close(p.stop)
	for range conf.Get().Server.SymbolParserWorkers {
		<-p.done
	}
	close(p.done)
	defer log.Printf("[SymbolParser] SymbolParser stopped")
}

// flushCache writes all cached files to the channel
func (p *SymbolParser) flushCache() {
	p.cacheMutex.Lock()
	defer p.cacheMutex.Unlock()

	if len(p.cacheMap) == 0 {
		return
	}

	totalFiles := 0
	for _, files := range p.cacheMap {
		totalFiles += len(files)
	}

	if totalFiles == 0 {
		return
	}

	for workspace, files := range p.cacheMap {
		if len(files) == 0 {
			continue
		}

		// Create a batch with all files for this workspace
		batch := ParseBatch{
			Workspace: workspace,
			Files:     files,
		}

		// Send the entire batch at once
		p.ch <- batch
	}

	p.cacheMap = make(map[*workspace.Workspace][]string)
}

// run executes the parsing logic in a worker goroutine
func (p *SymbolParser) run(id int, wg *sync.WaitGroup) {
	log.Printf("[SymbolParser] SymbolParser %d started", id)
	defer wg.Done()
	for {
		select {
		case <-p.stop:
			p.done <- struct{}{}
			return
		case batch := <-p.ch:
			p.processFileBatch(batch)
		}
	}
}

// Add queues a file for parsing
func (p *SymbolParser) Add(workspace *workspace.Workspace, relPath string) {
	if !conf.Get().Embedding.Enabled || p.ctags == "" {
		return
	}
	p.cacheMutex.Lock()
	defer p.cacheMutex.Unlock()

	p.cacheMap[workspace] = append(p.cacheMap[workspace], relPath)

	// check if the cache has reached the batch size
	if len(p.cacheMap[workspace]) >= MaxBatchSize {
		batch := ParseBatch{
			Workspace: workspace,
			Files:     p.cacheMap[workspace],
		}
		// Send the batch for this workspace
		p.ch <- batch

		delete(p.cacheMap, workspace)
		p.flushTimer.Reset(5 * time.Second)
	}
}

func (p *SymbolParser) processFileBatch(batch ParseBatch) error {
	tmpDir := filepath.Join(conf.Get().Global.DataPath, "data", "tmp", fmt.Sprintf("%d", batch.Workspace.Id))

	// Check if directory exists before creating it
	if _, statErr := os.Stat(tmpDir); os.IsNotExist(statErr) {
		// Directory doesn't exist, create it
		err := os.MkdirAll(tmpDir, 0755)
		if err != nil {
			return fmt.Errorf("failed to create temp directory: %w", err)
		}
	}

	// Group files by language
	filesByLang := make(map[string][]string)

	for _, relPath := range batch.Files {
		fullPath := filepath.Join(batch.Workspace.Path, relPath)
		lang := GetLangFromFilename(relPath)

		// Skip files with unrecognized languages
		if lang == "" {
			continue
		}

		filesByLang[lang] = append(filesByLang[lang], fullPath)
	}

	// Process each language group separately
	timestamp := time.Now().UnixNano()
	baseRandom := rand.Intn(10000)

	docs := []symbols.DocFunction{}
	docsPaths := make(map[string]bool)

	for lang, fullPaths := range filesByLang {
		if len(fullPaths) == 0 {
			continue
		}

		// Create a unique temp file for each language
		tmpFilePath := filepath.Join(tmpDir, fmt.Sprintf("%d_%d_%s", timestamp, baseRandom, lang))

		err := os.WriteFile(tmpFilePath, []byte(strings.Join(fullPaths, "\n")), 0644)
		if err != nil {
			return fmt.Errorf("failed to write to temp file for %s: %w", lang, err)
		}
		// Process the files for this language
		docsWithFunction, err := parseFunction(p.ctags, tmpFilePath, lang, batch.Workspace.Path)
		if err != nil {
			log.Printf("[SymbolParser] Error parsing symbols for language %s: %v", lang, err)
			continue
		}

		// Add the parsed functions to the docs array
		for _, docFunc := range docsWithFunction {
			docsPaths[docFunc.RelPath] = true
			docs = append(docs, docFunc)
		}

		// Clean up the temp file after processing
		_ = os.Remove(tmpFilePath)
	}

	// Now go through all files in the batch and add those missing from docs
	for _, relPath := range batch.Files {
		// Skip if already in docs
		if docsPaths[relPath] {
			continue
		}

		// Skip files with unrecognized languages or non-indexable
		lang := GetLangFromFilename(relPath)
		if lang == "" {
			continue
		}

		// Add with empty functions array
		docFunc := symbols.DocFunction{
			ID:        GetDocumentId(filepath.Join(batch.Workspace.Path, relPath)),
			RelPath:   relPath,
			Functions: []symbols.Function{},
		}
		docs = append(docs, docFunc)
	}

	err := symbols.AddFunctions(batch.Workspace.Id, docs)
	if err != nil {
		log.Printf("[SymbolParser] Error adding functions: %v", err)
		return err
	}

	batch.Workspace.AddSymbolParsedFiles(len(batch.Files))
	return nil
}
