package indexer

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/codetrek/haystack/core/documents"
	"github.com/codetrek/haystack/internal/conf"
	"github.com/codetrek/haystack/internal/core/workspace"
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

	maxFileSize := conf.Get().Server.MaxFileSize
	fileSizeExceedLimit := info.Size() > maxFileSize
	if fileSizeExceedLimit {
		log.Printf("[Indexer] File `%s` (%.2f MiB) is too large to index, skipping", file.RelFilePath, float64(info.Size())/1024/1024)
	}

	existing, _ := stInst.GetDocument(file.Workspace.Id, id)
	// If the document exists and the modified time is the same, return nil.
	// (Done before reading the file, so unchanged files cost only a stat.)
	if existing != nil &&
		existing.ModifiedTime == info.ModTime().UnixNano() {
		return nil, false, fileSizeExceedLimit, nil
	}

	in := documents.ContentInput{
		ID:          id,
		RelPath:     file.RelFilePath,
		Size:        info.Size(),
		ModTime:     info.ModTime().UnixNano(),
		Now:         time.Now().UnixNano(),
		MaxFileSize: maxFileSize,
	}
	if existing != nil {
		// Lets BuildContentDocument skip tokenization when the content hash is
		// unchanged (oversize files have an empty hash and never match).
		in.PriorHash = existing.Hash
	}
	if !fileSizeExceedLimit {
		content, err := os.ReadFile(fullPath)
		if err != nil {
			return nil, false, false, fmt.Errorf("failed to read file: %w", err)
		}
		in.Content = content
	}

	res := documents.BuildContentDocument(in)
	if res.NonText {
		// Not a text file, skip.
		return nil, false, fileSizeExceedLimit, nil
	}
	if res.Unchanged {
		// Content hash unchanged vs the existing document; nothing to re-index.
		return nil, false, fileSizeExceedLimit, nil
	}

	return res.Doc, existing == nil, res.Oversize, nil
}
