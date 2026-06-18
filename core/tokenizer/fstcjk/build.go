//go:build tools

package fstcjk

// This file builds the prebuilt FST + totalFreq sidecar OFFLINE. It is tagged
// "tools" so it (and its gse dictionary-loader / cobra / pflag dependencies)
// NEVER link into the runtime binary — only segmenter.go + util.go ship.
//
// Fidelity strategy: we load gse exactly once (the normal LoadDict() path that
// reads s_1.txt then t_1.txt with the same Size()/AddToken dedup/totalFreq
// semantics), then dump the resulting in-RAM dictionary verbatim into a vellum
// FST keyed by the word's UTF-8 bytes with value = the dictionary freq. Because
// the keys/values/totalFreq are copied straight out of gse's own Dictionary,
// the FST is byte-for-byte the same word->freq map gse would use at runtime.
//
// At runtime (segmenter.go) we never touch gse's loader; we load this embedded
// FST via vellum.Load.

import (
	"bufio"
	"fmt"
	"os"
	"sort"

	"github.com/blevesearch/vellum"
	"github.com/go-ego/gse"
)

// BuildResult reports build artifacts/measurements.
type BuildResult struct {
	NumKeys   int
	TotalFreq float64
	FSTBytes  int64
}

// BuildFromGse loads gse's full dictionary (s_1 + t_1, default LoadDict path),
// extracts every token's word + freq + the accumulated totalFreq, and writes a
// vellum FST (keys sorted lexicographically by bytes) plus a totalfreq sidecar.
func BuildFromGse(fstPath, totalFreqPath string) (BuildResult, error) {
	var res BuildResult

	var seg gse.Segmenter
	seg.SkipLog = true
	if err := seg.LoadDict(); err != nil {
		return res, fmt.Errorf("gse LoadDict: %w", err)
	}

	dict := seg.Dictionary()
	res.TotalFreq = dict.TotalFreq()

	// Collect word->freq from gse's own tokens. gse already deduped (first
	// occurrence wins) and applied Size() (single-char->2, freq<2 dropped).
	type kv struct {
		word []byte
		freq uint64
	}
	toks := dict.Tokens
	pairs := make([]kv, 0, len(toks))
	seen := make(map[string]struct{}, len(toks))
	for i := range toks {
		w := toks[i].Text()
		if w == "" {
			continue
		}
		// gse's trie can only hold one value per key; Tokens may technically
		// contain a dup if two source rows mapped to the same key but the
		// second was rejected by AddToken. Guard with seen to mirror the trie.
		if _, ok := seen[w]; ok {
			continue
		}
		seen[w] = struct{}{}
		f := toks[i].Freq()
		if f < 0 {
			f = 0
		}
		pairs = append(pairs, kv{word: []byte(w), freq: uint64(f)})
	}

	// vellum requires keys inserted in lexicographic byte order.
	sort.Slice(pairs, func(i, j int) bool {
		return string(pairs[i].word) < string(pairs[j].word)
	})

	f, err := os.Create(fstPath)
	if err != nil {
		return res, err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	b, err := vellum.New(w, nil)
	if err != nil {
		f.Close()
		return res, err
	}
	for _, p := range pairs {
		if err := b.Insert(p.word, p.freq); err != nil {
			f.Close()
			return res, fmt.Errorf("insert %q: %w", p.word, err)
		}
	}
	if err := b.Close(); err != nil {
		f.Close()
		return res, err
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return res, err
	}
	if err := f.Close(); err != nil {
		return res, err
	}

	st, err := os.Stat(fstPath)
	if err != nil {
		return res, err
	}
	res.FSTBytes = st.Size()
	res.NumKeys = len(pairs)

	if err := os.WriteFile(totalFreqPath,
		[]byte(fmt.Sprintf("%g\n", res.TotalFreq)), 0o644); err != nil {
		return res, err
	}

	return res, nil
}
