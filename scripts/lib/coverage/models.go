package main

import (
	"fmt"
	"os"
)

// Block represents a single coverage block from the coverage file
type Block struct {
	File      string
	StartLine int
	StartCol  int
	EndLine   int
	EndCol    int
	Count     int
}

// MergedBlock represents a merged uncovered code block
type MergedBlock struct {
	File           string
	StartLine      int
	StartCol       int
	EndLine        int
	EndCol         int
	NumLines       int
	EffectiveLines int
	Level          string
	FixAction      string
}

// ShouldPrint returns true if the block should be printed (non-LOW level)
func (b *MergedBlock) ShouldPrint() bool {
	return b.Level != "LOW"
}

// Print outputs the block information with formatting
func (b *MergedBlock) Print(locWidth int) {
	if b.ShouldPrint() {
		rangeStr := fmt.Sprintf("%s:(%d:%d)-(%d:%d)", b.File, b.StartLine, b.StartCol, b.EndLine, b.EndCol)
		linesStr := fmt.Sprintf("%d", b.NumLines)

		level := b.Level
		switch level {
		case "CRITICAL":
			level = "\033[31mCRITICAL\033[0m"
		case "HIGH":
			level = "\033[33mHIGH\033[0m"
		case "MEDIUM":
			level = "\033[34mMEDIUM\033[0m"
		case "LOW":
			level = "\033[32mLOW\033[0m"
		}
		if os.Getenv("CI") == "true" && b.Level == "CRITICAL" {
			fmt.Printf("::error file=%s,line=%d::", b.File, b.StartLine)
		}
		fmt.Printf("%-*s %-6s %-6d %-10s %s\n", locWidth, rangeStr, linesStr, b.EffectiveLines, level, b.FixAction)
	}
}
