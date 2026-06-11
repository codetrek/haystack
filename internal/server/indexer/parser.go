package indexer

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/codetrek/haystack/internal/conf"
	"github.com/codetrek/haystack/internal/core/documents"
	"github.com/codetrek/haystack/internal/core/workspace"
	"github.com/codetrek/haystack/searchcore/tokenizer"
)

// ParseFile represents a file to be parsed
type ParseFile struct {
	Workspace   *workspace.Workspace
	RelFilePath string
}

// Parser handles concurrent file parsing operations
type Parser struct {
	ch      chan ParseFile
	stop    chan struct{}
	done    chan struct{}
	workers int
}

// NewParser creates a new Parser instance
func NewParser() *Parser {
	return &Parser{
		ch:   make(chan ParseFile, 32),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
}

// Start initializes the parser with worker goroutines
func (p *Parser) Start(wg *sync.WaitGroup) {
	// Set worker count based on configuration
	p.workers = conf.Get().Server.IndexWorkers

	for i := range p.workers {
		wg.Add(1)
		go p.run(i, wg)
	}
}

func (p *Parser) Stop() {
	close(p.stop)
	for range p.workers {
		<-p.done
	}
	close(p.done)
	defer log.Printf("[Indexer] Parser stopped")
}

// run executes the parsing logic in a worker goroutine
func (p *Parser) run(id int, wg *sync.WaitGroup) {
	log.Printf("[Indexer] Parser %d started", id)
	defer wg.Done()

	for {
		select {
		case <-p.stop:
			p.done <- struct{}{}
			return
		case file := <-p.ch:
			p.processFile(file)
		}
	}
}

// processFile handles the parsing of a single file
func (p *Parser) processFile(file ParseFile) error {
	doc, newDoc, oversize, err := parse(file)
	if err != nil {
		return fmt.Errorf("failed to parse file: %w", err)
	}

	// If the document is nil, it means the file has not changed
	if doc == nil {
		return nil
	}

	writer.Add(file.Workspace, doc, newDoc)

	if !oversize {
		symbolParser.Add(file.Workspace, file.RelFilePath)
	}

	file.Workspace.AddIndexingFiles(1)

	return nil
}

// Add queues a file for parsing
func (p *Parser) Add(workspace *workspace.Workspace, relPath string) {
	p.ch <- ParseFile{
		Workspace:   workspace,
		RelFilePath: relPath,
	}
}

// parse reads and processes a file, returning a Document
func parse(file ParseFile) (doc *documents.Document, newfile bool, oversize bool, err error) {
	fullPath := filepath.Join(file.Workspace.Path, file.RelFilePath)
	id, err := GetDocumentId(file.RelFilePath)
	if err != nil {
		return nil, false, false, fmt.Errorf("failed to get docid: %w", err)
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		return nil, false, false, fmt.Errorf("failed to stat file: %w", err)
	}

	fileSizeExceedLimit := info.Size() > conf.Get().Server.MaxFileSize
	if fileSizeExceedLimit {
		log.Printf("[Indexer] File `%s` (%.2f MiB) is too large to index, skipping", file.RelFilePath, float64(info.Size())/1024/1024)
	}

	existing, _ := documents.GetDocument(file.Workspace.Id, id, false)
	// If the document exists and the modified time is the same, return nil
	if existing != nil &&
		existing.ModifiedTime == info.ModTime().UnixNano() {
		// log.Printf("[Indexer] File `%s` has not been touched, skipping", file.RelFilePath)
		return nil, false, fileSizeExceedLimit, nil
	}

	var hash string
	var words []string
	if fileSizeExceedLimit {
		hash = ""
		words = []string{}
	} else {
		content, err := os.ReadFile(fullPath)
		if err != nil {
			return nil, false, false, fmt.Errorf("failed to read file: %w", err)
		}
		if !IsLikelyText(content) {
			//			log.Printf("[Indexer] File `%s` is not a text file, skipping", file.RelFilePath)
			return nil, false, fileSizeExceedLimit, nil
		}

		hash = GetContentHash(content)
		// If the document exists and the hash is the same, return nil
		if existing != nil && existing.Hash == hash {
			// log.Printf("[Indexer] File hash for `%s` has not changed, skipping", file.RelFilePath)
			return nil, false, fileSizeExceedLimit, nil
		}

		// We only index the content if the file size is below the limit
		words = tokenizer.TokenizeForIndex(string(content))
	}

	return &documents.Document{
		ID:           id,
		RelPath:      file.RelFilePath,
		Size:         info.Size(),
		ModifiedTime: info.ModTime().UnixNano(),
		LastSyncTime: time.Now().UnixNano(),
		Hash:         hash,
		Words:        words,
		PathWords:    tokenizer.TokenizeForIndex(file.RelFilePath),
	}, existing == nil, fileSizeExceedLimit, nil
}
