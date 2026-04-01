package searcher

import (
	"crypto/md5"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/codetrek/haystack/server/core/documents"
	"github.com/codetrek/haystack/server/core/invertedindex"
	"github.com/codetrek/haystack/server/core/symbols"
	"github.com/codetrek/haystack/server/core/workspace"
	"github.com/codetrek/haystack/server/indexer"
	"github.com/codetrek/haystack/shared/types"

	"github.com/AntoineAugusti/wordsegmentation"
	"github.com/AntoineAugusti/wordsegmentation/corpus"
)

var (
	englishCorpus     corpus.EnglishCorpus
	corpusInitialized bool
)

// initEnglishCorpus ensures englishCorpus is initialized only once
func initEnglishCorpus() {
	if !corpusInitialized {
		englishCorpus = corpus.NewEnglishCorpus()
		corpusInitialized = true
	}
}

func isFileChanged(workspace *workspace.Workspace, doc *documents.Document) bool {
	// Get document information from documents package
	fullPath := filepath.Join(workspace.Path, doc.RelPath)
	fileInfo, err := os.Stat(fullPath)
	if err != nil {
		indexer.RemoveFile(workspace, doc.RelPath)
		return false
	}

	if fileInfo.ModTime().UnixNano() != doc.ModifiedTime {
		indexer.AddOrSyncFile(workspace, doc.RelPath)
		return false
	}

	if doc.Hash == "" {
		return false
	}

	fileContent, err := os.ReadFile(fullPath)
	if err != nil {
		return false
	}

	currentHash := fmt.Sprintf("%x", md5.Sum(fileContent))
	if currentHash != doc.Hash {
		indexer.AddOrSyncFile(workspace, doc.RelPath)
		return false
	}

	return true
}

func getFunctionFileMatch(workspace *workspace.Workspace, queryFunctionWords []string, docId string) (map[string][]types.SymbolsFileMatch, error) {
	doc, err := documents.GetDocument(workspace.Id, docId, false)
	if err != nil || doc == nil {
		return nil, err
	}

	if !isFileChanged(workspace, doc) {
		return nil, nil
	}

	functions, err := symbols.GetDocFunctions(workspace.Id, docId)
	if err != nil {
		return nil, err
	}

	symbolFiles := make(map[string][]types.SymbolsFileMatch)

	for _, f := range functions {
		fn := strings.ToLower(f.Name)
		index := 0
		matched := true
		for _, queryWord := range queryFunctionWords {
			pos := strings.Index(fn[index:], strings.ToLower(queryWord))
			if pos == -1 {
				matched = false
				break
			}
			index += pos + len(queryWord)
		}

		if matched {
			symbolFiles[f.Name] = append(symbolFiles[f.Name], types.SymbolsFileMatch{
				Path: doc.RelPath,
				Line: f.Line,
			})
		}
	}

	return symbolFiles, nil
}

func fuzzySearchSymbols(workspace *workspace.Workspace, req *types.SearchSymbolsRequest) (types.SymbolsContentResults, error) {
	initEnglishCorpus()
	result := types.SymbolsContentResults{
		Query:   req.Query,
		Symbols: []types.SymbolContent{},
	}
	swt, err := symbols.GetSymbolWordsTable(workspace.Id)

	if err != nil {
		return result, err
	}

	// Split query with space
	// words := strings.Fields(req.Query)
	words := wordsegmentation.Segment(englishCorpus, req.Query)
	r := invertedindex.Search(swt.InvertedId, words[0], -1, func(k string) bool {
		for _, word := range words {
			if !strings.Contains(k, word) {
				return false
			}
		}
		return true
	})

	symbolFiles := make(map[string][]types.SymbolsFileMatch)
	fileCount := 0
	for docId := range r.DocIds {

		fileCount++
		if fileCount > req.Limit.MaxResultsPerFile {
			break
		}

		s, err := getFunctionFileMatch(workspace, words, docId)
		if err != nil {
			continue
		}

		for name, files := range s {
			symbolFiles[name] = append(symbolFiles[name], files...)

			if len(result.Symbols) >= req.Limit.MaxResults {
				break
			}
		}
	}

	// Convert map to array of SymbolContent
	for name, files := range symbolFiles {
		result.Symbols = append(result.Symbols, types.SymbolContent{
			Name:  name,
			Files: files,
		})
	}

	return result, nil
}

func searchSymbols(workspace *workspace.Workspace, req *types.SearchSymbolsRequest) (types.SymbolsContentResults, error) {
	result := types.SymbolsContentResults{
		Query:   req.Query,
		Symbols: []types.SymbolContent{},
	}

	st, err := symbols.GetSymbolTable(workspace.Id)
	if err != nil {
		return result, err
	}

	r := invertedindex.GetDocs(st.InvertedId, req.Query)
	log.Printf("Query: %s, invertedId:%d len docids(%d)", req.Query, st.InvertedId, len(r.DocIds))

	// Group results by symbol name
	symbolFiles := make(map[string][]types.SymbolsFileMatch)

	for docId := range r.DocIds {
		// Get document info for file path
		doc, err := documents.GetDocument(workspace.Id, docId, false)
		if err != nil || doc == nil {
			continue
		}

		if !isFileChanged(workspace, doc) {
			continue
		}

		// Get functions from this document
		functions, err := symbols.GetDocFunctions(workspace.Id, docId)
		if err != nil {
			continue
		}

		// For each function/symbol in the document, add it to our results
		for _, f := range functions {
			if f.Name == req.Query {
				symbolFiles[f.Name] = append(symbolFiles[f.Name], types.SymbolsFileMatch{
					Path: doc.RelPath,
					Line: f.Line,
				})
			}
		}
	}

	for name, files := range symbolFiles {
		result.Symbols = append(result.Symbols, types.SymbolContent{
			Name:  name,
			Files: files,
		})
	}
	return result, nil
}

func SearchSymbols(workspace *workspace.Workspace, req *types.SearchSymbolsRequest) (types.SymbolsContentResults, error) {
	return fuzzySearchSymbols(workspace, req)
}
