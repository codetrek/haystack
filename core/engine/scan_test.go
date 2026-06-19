package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func newScanEngine(t *testing.T, query string) *Engine {
	t.Helper()
	e := New(nil, nil, 0, Options{MaxWildcardLength: 100, MaxKeywordDistance: 100})
	if err := e.Compile(query, false); err != nil {
		t.Fatalf("compile %q: %v", query, err)
	}
	return e
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestScanContent_MatchesAndCleansFile(t *testing.T) {
	e := newScanEngine(t, "foo")
	total := 0
	got, err := e.ScanContent("./a/../test.txt", strings.NewReader("foo\nbar\nfoo baz\n"),
		ScanOptions{BeforeAfter: 0, MaxResultsPerFile: 100, MaxResults: 100}, &total)
	assert.NoError(t, err)
	assert.Equal(t, "test.txt", got.File) // filepath.Clean
	assert.False(t, got.Truncate)
	if assert.Len(t, got.Lines, 2) {
		assert.Equal(t, 1, got.Lines[0].Line.LineNumber)
		assert.Equal(t, "foo", got.Lines[0].Line.Content)
		assert.Equal(t, 3, got.Lines[1].Line.LineNumber)
		assert.Equal(t, "foo baz", got.Lines[1].Line.Content)
	}
	assert.Equal(t, 2, total)
}

func TestScanContent_NoMatch(t *testing.T) {
	e := newScanEngine(t, "zzz")
	total := 0
	got, err := e.ScanContent("f.txt", strings.NewReader("foo\nbar\n"),
		ScanOptions{MaxResultsPerFile: 100, MaxResults: 100}, &total)
	assert.NoError(t, err)
	assert.Empty(t, got.Lines)
	assert.Equal(t, 0, total)
}

func TestScanContent_MultiMatchPerLineFansOut(t *testing.T) {
	e := newScanEngine(t, "o")
	total := 0
	got, err := e.ScanContent("f.txt", strings.NewReader("ooo\n"),
		ScanOptions{MaxResultsPerFile: 100, MaxResults: 100}, &total)
	assert.NoError(t, err)
	// One LineMatch per regex match on the line, all line 1.
	assert.Equal(t, 3, len(got.Lines))
	assert.Equal(t, 3, total)
	for _, lm := range got.Lines {
		assert.Equal(t, 1, lm.Line.LineNumber)
		assert.Equal(t, "ooo", lm.Line.Content)
	}
}

func TestScanContent_Context(t *testing.T) {
	e := newScanEngine(t, "foo")
	total := 0
	got, err := e.ScanContent("f.txt", strings.NewReader("a\nfoo\nb\n"),
		ScanOptions{BeforeAfter: 1, MaxResultsPerFile: 100, MaxResults: 100}, &total)
	assert.NoError(t, err)
	if assert.Len(t, got.Lines, 1) {
		lm := got.Lines[0]
		assert.Equal(t, 2, lm.Line.LineNumber)
		if assert.Len(t, lm.Before, 1) {
			assert.Equal(t, 1, lm.Before[0].LineNumber)
			assert.Equal(t, "a", lm.Before[0].Content)
		}
		if assert.Len(t, lm.After, 1) {
			assert.Equal(t, 3, lm.After[0].LineNumber)
			assert.Equal(t, "b", lm.After[0].Content)
		}
	}
}

func TestScanContent_TruncatePerFile(t *testing.T) {
	e := newScanEngine(t, "foo")
	total := 0
	got, err := e.ScanContent("f.txt", strings.NewReader("foo\nfoo\nfoo\n"),
		ScanOptions{MaxResultsPerFile: 2, MaxResults: 100}, &total)
	assert.NoError(t, err)
	assert.True(t, got.Truncate)
	assert.Len(t, got.Lines, 2)
}

func TestScanContent_GlobalLimitStops(t *testing.T) {
	e := newScanEngine(t, "foo")
	total := 5 // pretend prior files already produced 5 hits
	got, err := e.ScanContent("f.txt", strings.NewReader("foo\nfoo\n"),
		ScanOptions{MaxResultsPerFile: 100, MaxResults: 6}, &total)
	assert.NoError(t, err)
	// First match makes total 6 >= MaxResults -> stop; not a per-file truncate.
	assert.Len(t, got.Lines, 1)
	assert.False(t, got.Truncate)
	assert.Equal(t, 6, total)
}

func TestScanContent_ScannerError(t *testing.T) {
	e := newScanEngine(t, "foo")
	total := 0
	_, err := e.ScanContent("f.txt", errReader{},
		ScanOptions{MaxResultsPerFile: 100, MaxResults: 100}, &total)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "f.txt")
}
