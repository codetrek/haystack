package main

import (
	"fmt"
	"strings"
)

// printUncoveredHeader prints the header for uncovered blocks output
func printUncoveredHeader(locWidth int, printDescription bool) {
	if printDescription {
		fmt.Println(`# Uncovered Code Ranges (from Go coverage)
#
# Format:
#   <file>:(startLine,startCol)-(endLine,endCol)
#
# Field meanings:
#   <file>              Go source file path
#   startLine,startCol  Start position of an uncovered block (inclusive)
#   endLine,endCol      End position of an uncovered block (exclusive)
#
# Range semantics:
#   [startLine.startCol , endLine.endCol)
#
# Example:
#   foo/bar.go:(10,5)-(13,1) 3
#   Means:
#     - From line 10, column 5
#     - Through lines 11 and 12
#     - Stops before line 13, column 1
#     - 3 lines uncovered
#

Details:`)
	} else {
		fmt.Println("# Uncovered Code Ranges (from Go coverage)")
	}
	separator := strings.Repeat("-", locWidth+30)
	fmt.Println(separator)
	fmt.Printf("%-*s %-6s %-6s %-10s %s\n", locWidth, "LOCATION", "LINES", "EFFECT", "STATUS", "FIX ACTION")
	fmt.Println(separator)
}

// printBlocks prints the analyzed blocks with formatting
func printBlocks(merged []MergedBlock, maxLocWidth int, limit int) {
	count := 0
	printedNonCritical := false

	for _, b := range merged {
		if !b.ShouldPrint() {
			continue
		}

		isCritical := b.Level == "CRITICAL"

		// Condition to print:
		// 1. It is CRITICAL (always print)
		// 2. We haven't reached the limit yet
		// 3. We haven't printed any non-critical yet (ensure at least one)
		shouldPrint := isCritical || count < limit || !printedNonCritical

		if shouldPrint {
			b.Print(maxLocWidth)
			count++
			if !isCritical {
				printedNonCritical = true
			}
		} else {
			// If we are here, it means:
			// - It's not critical
			// - We reached the limit
			// - We already printed at least one non-critical
			// So we can stop, because the list is sorted by Level then Lines.
			// Remaining items will also be non-critical (or lower priority).
			break
		}
	}

	remaining := 0
	for i := 0; i < len(merged); i++ {
		if merged[i].ShouldPrint() {
			remaining++
		}
	}
	remaining -= count

	if remaining > 0 {
		fmt.Printf("... %d more ...\n", remaining)
	}
}

// calculateMaxLocWidth calculates the maximum location width for formatting
func calculateMaxLocWidth(merged []MergedBlock) int {
	maxLocWidth := 0
	for _, b := range merged {
		if !b.ShouldPrint() {
			continue
		}
		loc := fmt.Sprintf("%s:(%d:%d)-(%d:%d)", b.File, b.StartLine, b.StartCol, b.EndLine, b.EndCol)
		if len(loc) > maxLocWidth {
			maxLocWidth = len(loc)
		}
	}
	if maxLocWidth < 20 {
		maxLocWidth = 20
	}
	return maxLocWidth
}
