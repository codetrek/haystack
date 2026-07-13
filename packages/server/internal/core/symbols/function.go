package symbols

import (
	"log"
	"strconv"
	"strings"
	"unicode"

	"github.com/codetrek/haystack/packages/core/idtable"
	"github.com/codetrek/haystack/packages/core/kv"
	"github.com/codetrek/haystack/packages/core/tokenizer"
	"github.com/codetrek/haystack/server/internal/conf"
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

func updateSymbolWordsInverseIndex(workspaceid int, docId string, newFuncNames []string) {
	sw, err := GetSymbolWordsTable(workspaceid)
	if err != nil {
		log.Println("[Symbols] Error: failed to get symbol words table:", err)
		return
	}

	wordsInNewFuncNames := []string{}
	for _, fn := range newFuncNames {
		words := tokenizer.TokenizeForIndex(fn)
		for _, word := range words {
			wordsInNewFuncNames = append(wordsInNewFuncNames, strings.ToLower(word))
		}
	}
	idxInst.Update(sw.InvertedId, idtable.DecodeId(docId), wordsInNewFuncNames)

	s, err := GetSymbolTable(workspaceid)
	if err != nil {
		log.Println("[Symbols] Error: failed to get symbol table:", err)
		return
	}
	idxInst.Update(s.InvertedId, idtable.DecodeId(docId), newFuncNames)
}

func DeleteDocument(workspaceId int, docId string) error {
	if !conf.Get().Symbols.EnableFeature {
		return nil
	}

	return mpsc.RunFunc(func() error {
		if db.IsClosed() {
			log.Println("[Symbols] Database is closed, skip deleting document")
			return nil
		}
		s, err := GetSymbolTable(workspaceId)
		if err != nil {
			log.Println("[Symbols] Error: failed to get symbols:", err)
			return err
		}

		idxInst.Delete(s.InvertedId, idtable.DecodeId(docId))

		batch := NewBatch(db)
		batch.Delete(EncodeDocFunctionsKey(workspaceId, docId))
		err = batch.Commit()
		if err != nil {
			log.Println("[Symbols] Failed to delete document:", err)
		}

		return err
	})
}

func AddFunctions(workspaceid int, functions []DocFunction) error {
	return mpsc.RunFunc(func() error {
		if db.IsClosed() {
			log.Println("[Symbols] Database is closed, skip saving new functions")
			return nil
		}

		batch := NewBatch(db)

		for _, df := range functions {
			newFuncNames := getUniqueFunctionNames(df.Functions)
			updateSymbolWordsInverseIndex(workspaceid, df.ID, newFuncNames)

			saveDocFunctions(batch, workspaceid, &df)
		}

		err := batch.Commit()
		if err != nil {
			log.Println("[Symbols] Error: failed to save new documents:", err)
		}

		return err
	})
}
