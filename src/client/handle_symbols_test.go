package client

import (
	"net/http"
	"strings"
	"testing"

	"github.com/codetrek/haystack/shared/types"
)

func TestHandleSymbols_Help(t *testing.T) {
	output := captureStdout(t, func() {
		handleSymbols([]string{"-h"})
	})
	if !strings.Contains(output, "Usage:") {
		t.Error("handleSymbols -h should show usage")
	}
	if !strings.Contains(output, "symbols") {
		t.Error("should mention symbols")
	}
}

func TestHandleSymbols_NoArgs(t *testing.T) {
	output := captureStdout(t, func() {
		handleSymbols(nil)
	})
	if !strings.Contains(output, "Usage:") {
		t.Error("handleSymbols with no args should show usage")
	}
}

func TestHandleSymbols_EmptyQueryAfterParse(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called with empty query")
	})
	output := captureStdout(t, func() {
		handleSymbols([]string{"-limit", "10"})
	})
	if !strings.Contains(output, "Error: Search query cannot be empty") {
		t.Errorf("expected empty query error, got: %s", output)
	}
}

func TestHandleSymbols_WithQuery_Success(t *testing.T) {
	results := types.SymbolsContentResults{
		Query: "main",
		Symbols: []types.SymbolContent{
			{
				Name: "main",
				Files: []types.SymbolsFileMatch{
					{Path: "main.go", Line: 5},
					{Path: "cmd/main.go", Line: 10},
				},
			},
		},
	}

	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/search/symbols" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", results))
	})

	output := captureStdout(t, func() {
		handleSymbols([]string{"main"})
	})
	if !strings.Contains(output, "Searching for: main") {
		t.Errorf("expected search message, got: %s", output)
	}
	if !strings.Contains(output, "main") {
		t.Errorf("expected symbol name, got: %s", output)
	}
	if !strings.Contains(output, "main.go:5") {
		t.Errorf("expected file match, got: %s", output)
	}
}

func TestHandleSymbols_WithFuzzy(t *testing.T) {
	results := types.SymbolsContentResults{
		Query:   "mn",
		Symbols: []types.SymbolContent{},
	}

	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", results))
	})

	output := captureStdout(t, func() {
		handleSymbols([]string{"-fuzzy", "mn"})
	})
	if !strings.Contains(output, "fuzzy: true") {
		t.Errorf("expected fuzzy flag in output, got: %s", output)
	}
}

func TestHandleSymbols_ServerError(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 1, "server error", nil))
	})

	output := captureStdout(t, func() {
		handleSymbols([]string{"test"})
	})
	if !strings.Contains(output, "Error") {
		t.Errorf("expected error message, got: %s", output)
	}
}

func TestSendSearchSymbolsRequest_Success(t *testing.T) {
	expected := types.SymbolsContentResults{
		Query: "test",
		Symbols: []types.SymbolContent{
			{Name: "TestFunc"},
		},
	}

	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", expected))
	})

	result, err := sendSearchSymbolsRequest(types.SearchSymbolsRequest{Query: "test"})
	if err != nil {
		t.Fatalf("sendSearchSymbolsRequest error: %v", err)
	}
	if len(result.Symbols) != 1 {
		t.Errorf("expected 1 symbol, got %d", len(result.Symbols))
	}
	if result.Symbols[0].Name != "TestFunc" {
		t.Errorf("expected symbol name 'TestFunc', got %q", result.Symbols[0].Name)
	}
}

func TestSendSearchSymbolsRequest_ServerError(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 1, "fail", nil))
	})

	_, err := sendSearchSymbolsRequest(types.SearchSymbolsRequest{Query: "test"})
	if err == nil {
		t.Fatal("expected error from sendSearchSymbolsRequest")
	}
}
