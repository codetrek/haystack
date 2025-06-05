package searcher

import (
	"crypto/md5"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"

	"github.com/ai-microsoft/haystack/server/core/documents"
	"github.com/ai-microsoft/haystack/server/core/invertedindex"
	"github.com/ai-microsoft/haystack/server/core/symbols"
	"github.com/ai-microsoft/haystack/server/core/workspace"
	"github.com/ai-microsoft/haystack/server/indexer"
	"github.com/ai-microsoft/haystack/shared/types"

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

	countMap := make(map[string]int)
	// Split query with space
	// words := strings.Fields(req.Query)
	words := wordsegmentation.Segment(englishCorpus, req.Query)
	for _, word := range words {
		r := invertedindex.Search(swt.InvertedId, strings.ToLower(word), -1)
		log.Printf("word: %s, len docids(%d)", word, len(r.DocIds))
		for docId := range r.DocIds {
			countMap[docId]++
		}
	}

	symbolFiles := make(map[string][]types.SymbolsFileMatch)
	fileCount := 0
	for docId, count := range countMap {
		if count != len(words) {
			continue
		}

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

func searchSymbolsEmbedding(workspace *workspace.Workspace, req *types.SearchSymbolsRequest) (types.SymbolsContentResults, error) {
	result := types.SymbolsContentResults{
		Query:   req.Query,
		Symbols: []types.SymbolContent{},
	}

	resp, err := symbols.EmbeddingSearch(workspace.Id, req.Query, *req.Limit)
	if err != nil || resp.Code != 0 {
		return result, err
	}

	st, err := symbols.GetSymbolTable(workspace.Id)
	if err != nil {
		return result, err
	}

	// Use a map to collect all files for each symbol
	symbolFiles := make(map[string][]types.SymbolsFileMatch)
	// Keep track of the order of symbols as they appear in resp.Data
	var symbolOrder []string

	for _, ss := range resp.Data {
		if ss.Score > 1.2 {
			continue
		}

		symbolOrder = append(symbolOrder, ss.Symbol)
		r := invertedindex.GetDocs(st.InvertedId, ss.Symbol)
		// log.Printf("query: %s, symbol: %s, score: %f, len docs: %d", req.Query, ss.Symbol, ss.Score, len(r.DocIds))

		fileCorpusCount := 0
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
			randomN := 30
			if len(functions) == randomN {
				rand.Shuffle(len(functions), func(i, j int) {
					functions[i], functions[j] = functions[j], functions[i]
				})
			}
			if err != nil {
				continue
			}

			// For each function/symbol in the document, add it to our results
			for _, f := range functions {
				if f.Name == ss.Symbol {
					symbolFiles[ss.Symbol] = append(symbolFiles[f.Name], types.SymbolsFileMatch{
						Path: doc.RelPath,
						Line: f.Line,
					})
				}
			}

			if fileCorpusCount++; fileCorpusCount >= req.Limit.MaxResultsPerFile {
				break
			}
		}
	}

	for _, name := range symbolOrder {
		if files, ok := symbolFiles[name]; ok {
			result.Symbols = append(result.Symbols, types.SymbolContent{
				Name:  name,
				Files: files,
			})
		}
	}

	return result, nil

}

func SearchSymbols(workspace *workspace.Workspace, req *types.SearchSymbolsRequest) (types.SymbolsContentResults, error) {
	if req.Fuzzy {
		return fuzzySearchSymbols(workspace, req)
	} else {
		return searchSymbolsEmbedding(workspace, req)
	}
}
