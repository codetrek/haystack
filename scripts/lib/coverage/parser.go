package main

import (
	"strconv"
	"strings"
)

const MAX_GAP_LINES = 20

// isFileExcluded checks whether a file path matches any entry in cfg.ExcludeFiles.
func isFileExcluded(filePath string) bool {
	for _, ex := range strings.Split(cfg.ExcludeFiles, ",") {
		if ex != "" && strings.Contains(filePath, ex) {
			return true
		}
	}
	return false
}

// parseLine parses a single coverage file line into a Block
// Format: file:startLine.startCol,endLine.endCol numStmt count
func parseLine(line string) (Block, bool) {
	parts := strings.Fields(line)
	if len(parts) != 3 {
		return Block{}, false
	}

	locParts := strings.Split(parts[0], ":")
	if len(locParts) != 2 {
		return Block{}, false
	}
	filePath := locParts[0]

	// Filter out prefix
	filePath = strings.TrimPrefix(filePath, "github.com/codetrek/haystack/")

	// Skip excluded files
	if isFileExcluded(filePath) {
		return Block{}, false
	}

	rangeParts := strings.Split(locParts[1], ",")
	if len(rangeParts) != 2 {
		return Block{}, false
	}

	startParts := strings.Split(rangeParts[0], ".")
	endParts := strings.Split(rangeParts[1], ".")

	startLine, _ := strconv.Atoi(startParts[0])
	startCol, _ := strconv.Atoi(startParts[1])
	endLine, _ := strconv.Atoi(endParts[0])
	endCol, _ := strconv.Atoi(endParts[1])

	count, _ := strconv.Atoi(parts[2])

	return Block{
		File:      filePath,
		StartLine: startLine,
		StartCol:  startCol,
		EndLine:   endLine,
		EndCol:    endCol,
		Count:     count,
	}, true
}

// mergeBlocks merges adjacent uncovered blocks into larger blocks
func mergeBlocks(blocks []Block) []MergedBlock {
	var result []MergedBlock
	var current *MergedBlock
	fileCache := NewFileCache()

	for _, b := range blocks {
		if b.Count > 0 {
			if current != nil {
				// Finish current block
				current.NumLines = current.EndLine - current.StartLine + 1
				result = append(result, *current)
				current = nil
			}
			continue
		}

		// It is an uncovered block (Count == 0)
		if current == nil {
			current = &MergedBlock{
				File:      b.File,
				StartLine: b.StartLine,
				StartCol:  b.StartCol,
				EndLine:   b.EndLine,
				EndCol:    b.EndCol,
			}
		} else {
			// Try to merge
			shouldMerge := false
			if b.File == current.File {
				diff := b.StartLine - current.EndLine
				if diff <= 1 {
					// Adjacent or overlapping lines (diff <= 1 means gap is 0 lines)
					shouldMerge = true
				} else {
					// Check gap lines
					gapLinesCount := diff - 1
					if gapLinesCount <= MAX_GAP_LINES {
						// Check if gap lines are ignorable (empty or comments)
						// Gap lines are from (current.EndLine + 1) to (b.StartLine - 1)
						if fileCache.AreLinesIgnorable(b.File, current.EndLine+1, b.StartLine-1) {
							shouldMerge = true
						}
					}
				}
			}

			if shouldMerge {
				current.EndLine = b.EndLine
				current.EndCol = b.EndCol
			} else {
				// Cannot merge, save current and start new
				current.NumLines = current.EndLine - current.StartLine + 1
				result = append(result, *current)

				current = &MergedBlock{
					File:      b.File,
					StartLine: b.StartLine,
					StartCol:  b.StartCol,
					EndLine:   b.EndLine,
					EndCol:    b.EndCol,
				}
			}
		}
	}

	if current != nil {
		current.NumLines = current.EndLine - current.StartLine + 1
		result = append(result, *current)
	}

	return result
}
