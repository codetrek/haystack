//go:build tools

package fstcjk

import (
	"testing"
)

// TestGenerateDictFST is the offline generator: it builds dict.fst +
// dict.totalfreq INTO the package directory from gse's full dictionary, so they
// can be committed and //go:embed-ed by segmenter.go.
//
// Run with:
//
//	GOWORK=off go test -tags tools -run TestGenerateDictFST -v ./tokenizer/fstcjk/
//
// It is tools-tagged so neither the generator nor gse's loader links into the
// runtime build.
func TestGenerateDictFST(t *testing.T) {
	res, err := BuildFromGse("dict.fst", "dict.totalfreq")
	if err != nil {
		t.Fatalf("BuildFromGse: %v", err)
	}
	t.Logf("GENERATED dict.fst: keys=%d totalFreq=%g fstBytes=%d (%.2f MB)",
		res.NumKeys, res.TotalFreq, res.FSTBytes, float64(res.FSTBytes)/(1<<20))
	if res.NumKeys == 0 || res.TotalFreq == 0 {
		t.Fatalf("empty build: keys=%d totalFreq=%g", res.NumKeys, res.TotalFreq)
	}
}
