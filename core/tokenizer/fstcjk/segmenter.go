package fstcjk

// Runtime FST-backed CJK segmenter that is byte-identical to gse Cut(text,true).
//
// It ports gse v1.0.2's exact algorithm (dag.go getDag/calc/cutDAG/hmm and
// gse.go Cut) but replaces the cedar-trie Find() with a vellum FST prefix walk.
// OOV runs are handed to gse's own HMM package (github.com/go-ego/gse/hmm) so
// out-of-vocabulary segmentation matches gse bit-for-bit.
//
// The dictionary FST and its totalFreq sidecar are embedded via //go:embed and
// loaded at runtime through vellum.Load (anonymous memory). The offline builder
// that produces dict.fst from gse's source dictionaries lives in build.go behind
// a "//go:build tools" tag, so gse's dictionary LOADER (and cobra/pflag from
// vellum's CLI) never link into the runtime binary. Only go-ego/gse/hmm remains
// a runtime dependency.

import (
	_ "embed"
	"math"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/blevesearch/vellum"
	"github.com/go-ego/gse/hmm"
)

// dictFST is the prebuilt vellum FST (word UTF-8 bytes -> freq) generated offline
// by build.go from gse's s_1.txt + t_1.txt. Embedded so the runtime never loads
// gse's dictionary.
//
//go:embed dict.fst
var dictFST []byte

// dictTotalFreq is the gse Dict.TotalFreq() sidecar matching dictFST.
//
//go:embed dict.totalfreq
var dictTotalFreqStr string

// reEng mirrors gse dag.go: `[[:alnum:]]`. Only used by the NoHMM path; kept for
// completeness so this package can reproduce both Cut modes.
var reEng = regexp.MustCompile(`[[:alnum:]]`)

var hmmOnce sync.Once

// embeddedOnce builds the shared embedded segmenter exactly once.
var (
	embeddedOnce sync.Once
	embeddedSeg  *Segmenter
	embeddedErr  error
)

// Segmenter is an FST-backed, gse-faithful CJK segmenter.
type Segmenter struct {
	fst       *vellum.FST
	totalFreq float64
	logT      float64

	// owned indicates we mmap-opened the fst and must Close it.
	owned bool

	scratch sync.Pool
}

type route struct {
	freq  float64
	index int
}

// dagEdge is a DAG edge from a start position to end index `i` (inclusive)
// carrying the word's freq + ok (gse Find semantics), captured once during the
// prefix walk so calc() never has to re-walk the FST for the same substring.
type dagEdge struct {
	index int
	freq  float64
	ok    bool
}

// scratchBuf holds reusable per-call slices to keep allocs near gse parity.
type scratchBuf struct {
	dag    map[int][]dagEdge
	rs     map[int]route
	result []string
	buf    []rune
}

// Open returns the process-wide segmenter backed by the embedded FST. It builds
// the segmenter (vellum.Load over the embedded bytes + hmm.LoadModel) exactly
// once and returns the cached instance thereafter. This is the production entry
// point; callers must NOT Close it.
func Open() (*Segmenter, error) {
	embeddedOnce.Do(func() {
		embeddedSeg, embeddedErr = buildEmbedded(dictFST, dictTotalFreqStr)
	})
	return embeddedSeg, embeddedErr
}

// buildEmbedded parses the totalFreq sidecar string and loads the FST bytes into
// a Segmenter. It is the body of Open()'s once-init, factored out so its parse
// and load error paths are reachable without resetting the package-level once.
func buildEmbedded(fstBytes []byte, totalFreqStr string) (*Segmenter, error) {
	var tf float64
	if _, err := fmtSscan(strings.TrimSpace(totalFreqStr), &tf); err != nil {
		return nil, err
	}
	return LoadBytes(fstBytes, tf)
}

// OpenMmap opens a prebuilt FST via mmap (vellum.Open) and reads totalFreq.
// Used by resource/footprint tests; production uses Open (embedded).
func OpenMmap(fstPath, totalFreqPath string) (*Segmenter, error) {
	f, err := vellum.Open(fstPath)
	if err != nil {
		return nil, err
	}
	tf, err := readTotalFreq(totalFreqPath)
	if err != nil {
		f.Close()
		return nil, err
	}
	return newSeg(f, tf, true), nil
}

// LoadBytes loads a prebuilt FST from an in-memory []byte (vellum.Load) — the
// embed path (anonymous memory, no file mmap).
func LoadBytes(data []byte, totalFreq float64) (*Segmenter, error) {
	f, err := vellum.Load(data)
	if err != nil {
		return nil, err
	}
	return newSeg(f, totalFreq, false), nil
}

func newSeg(f *vellum.FST, totalFreq float64, owned bool) *Segmenter {
	s := &Segmenter{
		fst:       f,
		totalFreq: totalFreq,
		logT:      math.Log(totalFreq),
		owned:     owned,
	}
	s.scratch.New = func() any {
		return &scratchBuf{
			dag:    make(map[int][]dagEdge, 64),
			rs:     make(map[int]route, 64),
			result: make([]string, 0, 64),
			buf:    make([]rune, 0, 16),
		}
	}
	// Load gse's HMM emission/transition tables once (same model gse uses).
	hmmOnce.Do(func() { hmm.LoadModel() })
	return s
}

// TotalFreq exposes the loaded totalFreq (for cross-check vs gse).
func (s *Segmenter) TotalFreq() float64 { return s.totalFreq }

// Close releases the mmap if we own it.
func (s *Segmenter) Close() error {
	if s.owned && s.fst != nil {
		return s.fst.Close()
	}
	return nil
}

func readTotalFreq(path string) (float64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var v float64
	_, err = fmtSscan(strings.TrimSpace(string(b)), &v)
	return v, err
}

// acceptRune walks the FST for the bytes of one rune appended to a prior state,
// accumulating vellum's per-transition outputs into sum. In vellum a key's value
// is sum(transition outputs) + final output, so we MUST thread sum through the
// walk (using AcceptWithVal) — Accept alone drops the outputs and corrupts freq.
//
// Given state `addr` reached after consuming a prefix (with running output
// `sum`), it appends the UTF-8 bytes of `r` and returns the new state, the new
// running output, and whether the path still exists (== gse Find ok).
func (s *Segmenter) acceptRune(addr int, sum uint64, r rune) (int, uint64, bool) {
	var buf [4]byte
	n := encodeRune(buf[:], r)
	for i := 0; i < n; i++ {
		naddr, out := s.fst.AcceptWithVal(addr, buf[i])
		if naddr == noneAddrVellum {
			return naddr, sum, false
		}
		addr = naddr
		sum += out
	}
	return addr, sum, true
}

// getDag replicates gse dag.go getDag exactly, FST-backed, while caching the
// (freq, ok) of every edge so calc() needs no FST re-walk.
//
// gse: for each k, extend frag rune by rune while Find(prefix) ok; append i to
// dag[k] whenever freq>0; if dag[k] empty, dag[k]=[k]. In calc, each edge's
// freq/ok come from Find(runes[idx:i+1]); for the fallback [k] edge that is
// Find of the single rune (which may be a non-word node: freq=0, ok=true, or
// absent: ok=false). We capture all of that here in a single walk.
func (s *Segmenter) getDag(runes []rune, dag map[int][]dagEdge) {
	n := len(runes)
	for k := range dag {
		delete(dag, k)
	}
	for k := 0; k < n; k++ {
		lst := dag[k]
		if lst == nil {
			lst = make([]dagEdge, 0, 4)
		} else {
			lst = lst[:0]
		}
		addr := s.fst.Start()
		var sum uint64
		i := k

		// Capture the single-rune (runes[k:k+1]) Find result for the fallback.
		var firstFreq float64
		var firstOK bool

		for {
			naddr, nsum, ok := s.acceptRune(addr, sum, runes[i])
			if !ok {
				if i == k {
					firstFreq, firstOK = 0, false
				}
				break
			}
			addr = naddr
			sum = nsum
			final, fout := s.fst.IsMatchWithVal(addr)
			freq := float64(sum + fout)
			if i == k {
				// Single rune: gse Find ok=true (path exists); freq is the
				// final value or 0 for a non-word intermediate node.
				firstOK = true
				if final {
					firstFreq = freq
				} else {
					firstFreq = 0
				}
			}
			if final && freq > 0 {
				lst = append(lst, dagEdge{index: i, freq: freq, ok: true})
			}
			i++
			if i >= n {
				break
			}
		}
		if len(lst) == 0 {
			lst = append(lst, dagEdge{index: k, freq: firstFreq, ok: firstOK})
		}
		dag[k] = lst
	}
}

// findFreq returns (freq, ok) for an exact word — gse Find semantics: ok=true if
// the byte path exists (even intermediate); freq is the final value or 0.
func (s *Segmenter) findFreq(runes []rune) (float64, bool) {
	addr := s.fst.Start()
	var sum uint64
	for _, r := range runes {
		naddr, nsum, ok := s.acceptRune(addr, sum, r)
		if !ok {
			return 0, false
		}
		addr = naddr
		sum = nsum
	}
	final, fout := s.fst.IsMatchWithVal(addr)
	if final {
		return float64(sum + fout), true
	}
	// Path exists but not a final word: gse returns ok=true, freq=0.
	return 0, true
}

// calc replicates gse dag.go calc exactly, reading edge freq/ok from the cached
// DAG (no FST re-walk).
func (s *Segmenter) calc(runes []rune, dag map[int][]dagEdge, rs map[int]route) {
	n := len(runes)
	for k := range rs {
		delete(rs, k)
	}
	rs[n] = route{freq: 0.0, index: 0}
	var r route
	logT := s.logT
	for idx := n - 1; idx >= 0; idx-- {
		for _, e := range dag[idx] {
			i := e.index
			if e.ok {
				f := math.Log(e.freq) - logT + rs[i+1].freq
				r = route{freq: f, index: i}
			} else {
				f := math.Log(1.0) - logT + rs[i+1].freq
				r = route{freq: f, index: i}
			}
			if v, ok := rs[idx]; !ok {
				rs[idx] = r
			} else {
				fEq := v.freq == r.freq && v.index < r.index
				if v.freq < r.freq || fEq {
					rs[idx] = r
				}
			}
		}
	}
}

// hmm replicates gse dag.go hmm: if the buffer string is a known word with
// freq>0 keep it whole-but-split-to-chars... actually gse splits to runes only
// when found; otherwise HMMCut. We mirror exactly.
func (s *Segmenter) hmm(bufString string, buf []rune, result []string) []string {
	v, ok := s.findFreq([]rune(bufString))
	if !ok || v == 0 {
		return append(result, hmm.Cut(bufString)...)
	}
	for _, elem := range buf {
		result = append(result, string(elem))
	}
	return result
}

// CutDAG replicates gse dag.go cutDAG (the hmm=true path of Cut). ToLower=true
// in gse, so we lowercase first.
func (s *Segmenter) CutDAG(str string) []string {
	sb := s.scratch.Get().(*scratchBuf)
	defer s.scratch.Put(sb)

	str = strings.ToLower(str)
	runes := []rune(str)

	s.getDag(runes, sb.dag)
	s.calc(runes, sb.dag, sb.rs)
	routes := sb.rs

	result := sb.result[:0]
	buf := sb.buf[:0]

	length := len(runes)
	var y int
	for x := 0; x < length; {
		y = routes[x].index + 1
		frag := runes[x:y]
		if y-x == 1 {
			buf = append(buf, frag...)
		} else {
			if len(buf) > 0 {
				bufString := string(buf)
				if len(buf) == 1 {
					result = append(result, bufString)
				} else {
					result = s.hmm(bufString, buf, result)
				}
				buf = buf[:0]
			}
			result = append(result, string(frag))
		}
		x = y
	}
	if len(buf) > 0 {
		bufString := string(buf)
		if len(buf) == 1 {
			result = append(result, bufString)
		} else {
			result = s.hmm(bufString, buf, result)
		}
	}

	// Copy out so the pooled slice can be reused safely by the caller.
	out := make([]string, len(result))
	copy(out, result)
	sb.result = result
	sb.buf = buf
	return out
}

// Cut is the public entry mirroring gse Cut(str, true).
func (s *Segmenter) Cut(str string) []string {
	return s.CutDAG(str)
}

// keep reEng referenced (NoHMM-path completeness; not on the hmm=true path).
var _ = reEng
