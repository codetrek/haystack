package engine

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
)

// ScanOptions are the per-file caps for ScanContent. The caller pre-resolves
// them from config and the request (preserving the existing "request value when
// >0, else config" rule) and pre-clamps BeforeAfter to 0..5. MaxResultsPerFile
// and MaxResults are expected to be > 0.
type ScanOptions struct {
	BeforeAfter       int // context lines on each side of a match
	MaxResultsPerFile int // truncate a file after this many hits
	MaxResults        int // global hit budget (checked against the running total)
}

// Line is a single content line: a matched line carries Match byte offsets, a
// context line leaves Match nil.
type Line struct {
	LineNumber int
	Content    string
	Match      []int
}

// LineMatch is one emitted hit: the matched Line plus its before/after context.
type LineMatch struct {
	Before []Line
	Line   Line
	After  []Line
}

// ContentMatch is the result of scanning one file: the cleaned path, the emitted
// hits (one per regex match per line), and whether the file was truncated.
type ContentMatch struct {
	File     string
	Lines    []LineMatch
	Truncate bool
}

// ScanContent reads reader line by line, applies the compiled query via
// IsLineMatch, and collects hits (one LineMatch per match per line) with
// before/after context, honoring the per-file and global caps. totalHits is the
// caller-owned cross-file running counter; ScanContent increments it and stops
// mid-file once it reaches opts.MaxResults, preserving the searcher's behavior.
//
// This is the pure line-scan logic extracted from the server's searcher; the
// shell (file open/close, config/limit resolution, DTO mapping) stays in the
// caller.
func (e *Engine) ScanContent(relPath string, reader io.Reader, opts ScanOptions, totalHits *int) (ContentMatch, error) {
	result := ContentMatch{
		File:  filepath.Clean(relPath),
		Lines: []LineMatch{},
	}

	scanner := bufio.NewScanner(reader)

	var lines []string
	lineNumber := 1
	fileHits := 0

	for scanner.Scan() {
		line := scanner.Text()
		if opts.BeforeAfter > 0 {
			lines = append(lines, line)
		}
		matches := e.IsLineMatch(line)
		if len(matches) > 0 {
			for _, match := range matches {
				result.Lines = append(result.Lines, LineMatch{
					Line: Line{
						LineNumber: lineNumber,
						Content:    line,
						Match:      match,
					},
				})

				(*totalHits)++
				fileHits++
				if fileHits >= opts.MaxResultsPerFile {
					result.Truncate = true
					break
				}
			}
			if fileHits >= opts.MaxResultsPerFile || *totalHits >= opts.MaxResults {
				break
			}
		}
		lineNumber++
	}

	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("error scanning content for %s: %w", relPath, err)
	}

	// Populate before/after context in a second pass.
	if opts.BeforeAfter > 0 {
		for i := 0; i < len(result.Lines); i++ {
			lm := &result.Lines[i]
			lineNum := lm.Line.LineNumber

			for j := lineNum - opts.BeforeAfter; j < lineNum; j++ {
				if j > 0 && j <= len(lines) {
					lm.Before = append(lm.Before, Line{LineNumber: j, Content: lines[j-1]})
				}
			}

			for j := lineNum + 1; j <= lineNum+opts.BeforeAfter; j++ {
				if j <= len(lines) {
					lm.After = append(lm.After, Line{LineNumber: j, Content: lines[j-1]})
				}
			}
		}
	}

	return result, nil
}
