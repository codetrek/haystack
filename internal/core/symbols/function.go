package symbols

import (
	"log"
	"strconv"
	"strings"
	"unicode"

	"github.com/codetrek/haystack/core/idtable"
	"github.com/codetrek/haystack/core/kv"
	"github.com/codetrek/haystack/core/tokenizer"
	"github.com/codetrek/haystack/internal/conf"
)

type Function struct {
	Name string
	Line int
}

type DocFunction struct {
	ID        string
	RelPath   string
	Functions []Function
}

const MaxBatchSize = 512

var NewBatch = func(db kv.Store) kv.Batch {
	return db.NewBatch(MaxBatchSize)
}

func GetDocFunctions(workspaceid int, docid string) ([]Function, error) {
	functions, err := db.Get(EncodeDocFunctionsKey(workspaceid, docid))
	if err != nil {
		return nil, err
	}

	funcs := []Function{}
	if len(functions) == 0 {
		return funcs, nil
	}

	// Parse the functions data
	// Format: symbol_1#line_number,line_number|symbol_2#line_number...
	functionsStr := string(functions)
	if len(functionsStr) == 0 {
		return funcs, nil
	}

	functionEntries := strings.Split(functionsStr, "|")

	for _, entry := range functionEntries {
		if len(entry) == 0 {
			continue
		}

		parts := strings.Split(entry, "#")
		if len(parts) != 2 {
			continue
		}

		symbolName := parts[0]
		lineNumbers := strings.Split(parts[1], ",")

		for _, lineStr := range lineNumbers {
			lineNum, err := strconv.Atoi(lineStr)
			if err != nil {
				continue
			}

			funcs = append(funcs, Function{
				Name: symbolName,
				Line: lineNum,
			})
		}
	}

	return funcs, nil
}

// getUniqueFunctionNames extracts unique function names from a slice of Functions
func getUniqueFunctionNames(functions []Function) []string {
	uniqueNames := make(map[string]bool)
	var result []string

	for _, f := range functions {
		if !uniqueNames[f.Name] {
			uniqueNames[f.Name] = true
			result = append(result, f.Name)
		}
	}

	return result
}

func saveDocFunctions(batch kv.Batch, workspaceid int, doc *DocFunction) {
	if len(doc.Functions) == 0 {
		batch.Delete(EncodeDocFunctionsKey(workspaceid, doc.ID))
	} else {
		// Format: symbol_1#line_number,line_number|symbol_2#line_number...
		// Group functions by name since the same function name might appear on multiple lines
		functionMap := make(map[string][]int)

		for _, function := range doc.Functions {
			functionMap[function.Name] = append(functionMap[function.Name], function.Line)
		}

		var functionEntries []string
		for name, lines := range functionMap {
			// Create line numbers part as comma-separated values
			lineNumbers := make([]string, len(lines))
			for i, line := range lines {
				lineNumbers[i] = strconv.Itoa(line)
			}

			// Format each function as "name#line_number,line_number,..."
			entry := name + "#" + strings.Join(lineNumbers, ",")
			functionEntries = append(functionEntries, entry)
		}

		// Join all function entries with the '|' separator
		functionsStr := strings.Join(functionEntries, "|")

		// Save to database
		batch.Put(EncodeDocFunctionsKey(workspaceid, doc.ID), []byte(functionsStr))
	}
}

func splitCamelCasePart(input string) []string {

	var result []string
	runes := []rune(input)
	length := len(runes)

	start := 0
	for i := 1; i < length; i++ {
		curr := runes[i]
		prev := runes[i-1]

		if unicode.IsUpper(curr) {
			if unicode.IsLower(prev) || (i+1 < length && unicode.IsLower(runes[i+1])) {
				result = append(result, string(runes[start:i]))
				start = i
			}
		} else if unicode.IsDigit(curr) && !unicode.IsDigit(prev) {
			result = append(result, string(runes[start:i]))
			start = i
		} else if !unicode.IsDigit(curr) && unicode.IsDigit(prev) {
			result = append(result, string(runes[start:i]))
			start = i
		} else if curr == '_' {
			// Split on underscore, but don't include the underscore in the result
			if start < i {
				result = append(result, string(runes[start:i]))
			}
			start = i + 1 // Skip the underscore itself
		} else if prev == '_' {
			// This handles the case where we already skipped the underscore
			// but we still need to mark the beginning of a new word
			start = i
		}
	}
	// append the last segment
	if start < length {
		result = append(result, string(runes[start:]))
	}
	return result
}

func SplitCamelCase(name string) []string {
	parts := strings.Split(name, "::")
	var result []string
	for _, part := range parts {
		result = append(result, splitCamelCasePart(part)...)
	}
	return result
}

// symbolIndexUpdate is one doc's worth of inverted-index notifications, collected
// INSIDE a worker task and replayed via idxInst.NewBatch()/Update/Commit AFTER the
// task returns. A symbol doc touches BOTH the symbol table (function names) and the
// symbol-words table (tokenized words of those names), so each carries two ops.
//
// docid is the int64-decoded form the inverted index keys postings by; keywords is
// the doc's CURRENT full keyword set (empty/nil ⇒ delete — the store diffs it
// against its own forward map, design §4/§8). The names/words variants share this
// shape, so they collapse into one slice of (InvertedId, docid, keywords) tuples.
type symbolIndexUpdate struct {
	tableID int
	docid   int64
	words   []string
}

// collectSymbolIndexUpdates builds the (symbol-words + symbol) index notifications
// for one doc WITHOUT touching the inverted index. The table-meta lookups read the
// kv store (db.Get), so this must run on the worker, but it issues NO idxInst.Update
// — the actual async apply is hoisted outside the worker by replayIndexUpdates.
//
// The inverted index owns the forward map keyed by (InvertedId, docid) and diffs the
// CURRENT keyword set against the stored one internally, so we pass only the new
// words/names — no stale old set. A removed word is retracted by the store on its own.
func collectSymbolIndexUpdates(workspaceid int, docId string, newFuncNames []string) []symbolIndexUpdate {
	updates := make([]symbolIndexUpdate, 0, 2)
	docid := idtable.DecodeId(docId)

	sw, err := GetSymbolWordsTable(workspaceid)
	if err != nil {
		log.Println("[Symbols] Error: failed to get symbol words table:", err)
		return updates
	}

	wordsInNewFuncNames := []string{}
	for _, fn := range newFuncNames {
		words := tokenizer.TokenizeForIndex(fn)
		for _, word := range words {
			wordsInNewFuncNames = append(wordsInNewFuncNames, strings.ToLower(word))
		}
	}
	updates = append(updates, symbolIndexUpdate{tableID: sw.InvertedId, docid: docid, words: wordsInNewFuncNames})

	s, err := GetSymbolTable(workspaceid)
	if err != nil {
		log.Println("[Symbols] Error: failed to get symbol table:", err)
		return updates
	}
	updates = append(updates, symbolIndexUpdate{tableID: s.InvertedId, docid: docid, words: newFuncNames})

	return updates
}

// replayIndexUpdates applies the collected index notifications in ONE inverted-index
// batch. It MUST be called OUTSIDE any mpsc.RunFunc worker task: a Batch.Commit (and
// Update) enqueues onto the SAME single-worker shared queue (q.AddFunc, a blocking
// channel send). Calling it from inside the worker would block forever once the
// channel buffer fills — the worker cannot drain what it is itself trying to send.
// This mirrors documents.Store.indexDocuments and is guarded by
// save_no_deadlock_test.go in this package.
func replayIndexUpdates(updates []symbolIndexUpdate) {
	if idxInst == nil || len(updates) == 0 {
		return
	}
	b := idxInst.NewBatch()
	for _, u := range updates {
		b.Update(u.tableID, u.docid, u.words)
	}
	b.Commit()
}

func DeleteDocument(workspaceId int, docId string) error {
	if !conf.Get().Symbols.EnableFeature {
		return nil
	}

	var (
		invertedId int
		doIndex    bool
	)
	err := mpsc.RunFunc(func() error {
		if db.IsClosed() {
			log.Println("[Symbols] Database is closed, skip deleting document")
			return nil
		}
		s, err := GetSymbolTable(workspaceId)
		if err != nil {
			log.Println("[Symbols] Error: failed to get symbols:", err)
			return err
		}

		batch := NewBatch(db)
		batch.Delete(EncodeDocFunctionsKey(workspaceId, docId))
		err = batch.Commit()
		if err != nil {
			log.Println("[Symbols] Failed to delete document:", err)
			return err
		}

		invertedId = s.InvertedId
		doIndex = true
		return nil
	})
	if err != nil {
		return err
	}

	// Notify the index OUTSIDE the worker task: empty keyword set ⇒ delete. The store
	// diffs against its forward map and retracts every posting this doc held under the
	// symbol table (no oldWords arg). Hoisting it out of the worker avoids the
	// AddFunc-from-the-worker self-send deadlock.
	if doIndex {
		replayIndexUpdates([]symbolIndexUpdate{{tableID: invertedId, docid: idtable.DecodeId(docId), words: []string{}}})
	}
	return nil
}

func AddFunctions(workspaceid int, functions []DocFunction) error {
	var indexUpdates []symbolIndexUpdate
	err := mpsc.RunFunc(func() error {
		if db.IsClosed() {
			log.Println("[Symbols] Database is closed, skip saving new functions")
			return nil
		}

		batch := NewBatch(db)

		for _, df := range functions {
			// COLLECT the index notifications inside the worker (the table-meta lookups
			// read db), but DEFER the actual idxInst apply until after RunFunc returns:
			// idxInst.Update/Batch.Commit enqueues onto the SAME shared mpsc worker, so
			// applying here would self-deadlock once the channel buffer fills on a real
			// batch (MaxBatchSize up to ~2000 sends). See replayIndexUpdates.
			newFuncNames := getUniqueFunctionNames(df.Functions)
			indexUpdates = append(indexUpdates, collectSymbolIndexUpdates(workspaceid, df.ID, newFuncNames)...)

			saveDocFunctions(batch, workspaceid, &df)
		}

		err := batch.Commit()
		if err != nil {
			log.Println("[Symbols] Error: failed to save new documents:", err)
		}

		return err
	})
	if err != nil {
		return err
	}

	// Apply all per-doc index notifications in ONE batch OUTSIDE the worker task.
	replayIndexUpdates(indexUpdates)
	return nil
}
