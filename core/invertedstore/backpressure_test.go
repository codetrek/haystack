package invertedstore

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codetrek/haystack/core/queue"
)

// searchDocidsForTest returns the live docids of the EXACT keyword kw in tableId, sorted — a thin
// []int64 view over GetDocs for membership assertions. (GetDocs, not Search: exact, not prefix.)
func searchDocidsForTest(t *testing.T, s *Store, tableId int, kw string) []int64 {
	t.Helper()
	r := s.GetDocs(tableId, kw)
	out := make([]int64, 0, len(r.DocIds))
	for d := range r.DocIds {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// newBackpressureStore mirrors newForwardSkipStore: a started queue + Open + one table (AutoMerge
// off), with caller-chosen Options so a test can set a tiny MaxInflightPostings.
func newBackpressureStore(t *testing.T, opts Options) (*Store, int) {
	t.Helper()
	q := queue.NewMpsc("backpressure")
	q.Start()
	s, err := Open(t.TempDir(), q, opts)
	if err != nil {
		t.Fatal(err)
	}
	tid, err := s.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}
	return s, tid
}

// TestBackpressure_PeakInflightBoundedByBudget is the behavioral red→green discriminator. A producer
// fires far more 1-posting Updates than the budget while every apply is parked on a gate, so applies
// CANNOT drain (the single worker blocks inside the first gated apply; the rest sit buffered). With
// the producer backpressure wired, Update blocks once the in-flight postings reach the budget, so the
// producer returns from at most `cap` Updates and the peak in-flight never exceeds the cap. WITHOUT
// the wiring Update never blocks: the producer returns from all `producers` Updates, so the peak
// in-flight equals `producers` (>> cap) — this assertion fails on the BOUND (not on a compile error).
//
// In-flight = postings whose Update has returned to the producer but whose apply has not yet
// completed (released). It is counted directly (producer +1 per returned Update, apply -1 after the
// gate releases) so the red is real even though the gate keeps budget.used pinned regardless.
func TestBackpressure_PeakInflightBoundedByBudget(t *testing.T) {
	const cap = 3
	const producers = 20 // each fires 1 posting (1 keyword), 20 >> cap; < mpsc buffer (100)

	s, tid := newBackpressureStore(t, Options{CapBytes: 1 << 20, MaxInflightPostings: cap})

	// inflight = Updates returned to the producer minus applies that have completed. While the gate
	// holds, no apply completes, so inflight == the number of Updates the producer got past.
	var inflight, peak atomic.Int64
	bump := func() {
		v := inflight.Add(1)
		for {
			p := peak.Load()
			if v <= p || peak.CompareAndSwap(p, v) {
				break
			}
		}
	}

	// Gate every apply: it blocks until release, so applies cannot drain. On release each apply
	// decrements inflight (it has run to completion).
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseGate := func() { releaseOnce.Do(func() { close(release) }) }
	applyGate = func() {
		<-release
		inflight.Add(-1)
	}
	// Drain BEFORE clearing the global: release the gate so any parked apply can finish, then
	// RunFunc blocks until every prior-enqueued closure (each of which reads applyGate) has run, so
	// the last `if applyGate != nil` read happens-before this nil write. Otherwise an in-flight
	// closure on the still-alive worker races the next test reassigning the package-global.
	t.Cleanup(func() {
		releaseGate()
		s.q.RunFunc(func() error { return nil })
		applyGate = nil
	})

	// Fire the producers from one goroutine; with the wiring it blocks at the budget until applies
	// drain (which they never do here — the gate holds them), so not all Updates return.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < producers; i++ {
			s.Update(tid, int64(i), []string{uniqWord(i)})
			bump() // this Update returned: +1 in-flight posting
		}
	}()

	// Wait until the producer has either blocked at the budget (wired: inflight stalls at ~cap) or
	// fired everything (unwired: inflight reaches `producers`). Poll until it is quiescent.
	deadline := time.After(3 * time.Second)
	var last int64 = -1
	stable := 0
poll:
	for {
		cur := inflight.Load()
		if cur == last {
			stable++
			if stable >= 20 { // ~200ms of no movement → producer is quiescent
				break poll
			}
		} else {
			stable = 0
			last = cur
		}
		select {
		case <-deadline:
			break poll
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}

	if p := peak.Load(); p > int64(cap) {
		t.Fatalf("peak in-flight postings = %d exceeds budget %d (producer did not block)", p, cap)
	}

	// Releasing the gate lets every apply drain and the producer finish.
	releaseGate()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("producer never finished after the gate released")
	}
	s.q.RunFunc(func() error { return nil }) // drain
}

// TestBackpressure_OversizedSingleUpdateDoesNotDeadlock: a single Update with more keywords than the
// whole budget must cap its acquire to the budget and proceed (never self-deadlock waiting for room
// that can never exist).
func TestBackpressure_OversizedSingleUpdateDoesNotDeadlock(t *testing.T) {
	const cap = 2
	s, tid := newBackpressureStore(t, Options{CapBytes: 1 << 20, MaxInflightPostings: cap})

	kws := []string{"a", "b", "c", "d", "e"} // 5 > cap 2
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Update(tid, 1, kws)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("oversized single Update self-deadlocked (acquire not capped to the budget)")
	}
	s.q.RunFunc(func() error { return nil }) // drain
	got := searchDocidsForTest(t, s, tid, "c")
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("oversized Update did not apply: c -> %v, want {1}", got)
	}
}

// TestBackpressure_DeletesNeverBlock: a delete (0 keywords) acquires nothing, so it never blocks even
// when the budget is fully held by parked applies.
func TestBackpressure_DeletesNeverBlock(t *testing.T) {
	const cap = 1
	s, tid := newBackpressureStore(t, Options{CapBytes: 1 << 20, MaxInflightPostings: cap})

	// Park one 1-keyword apply on the gate so the budget is fully consumed.
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	applyGate = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}
	// Drain BEFORE clearing the global: close release so the parked apply unblocks and finishes, then
	// RunFunc blocks until every prior-enqueued closure (each of which reads applyGate) has run, so the
	// last `if applyGate != nil` read happens-before this nil write (no race vs the next test's write).
	t.Cleanup(func() {
		close(release)
		s.q.RunFunc(func() error { return nil })
		applyGate = nil
	})

	go s.Update(tid, 1, []string{"alpha"})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the parked apply never entered the gate")
	}

	// A delete (0 keywords) acquires 0 tokens and must NOT block on the full budget.
	deleted := make(chan struct{})
	go func() {
		defer close(deleted)
		s.Update(tid, 2, nil) // 0 keywords -> delete
	}()
	select {
	case <-deleted:
	case <-time.After(5 * time.Second):
		t.Fatal("a delete (0 keywords) blocked on the budget; deletes must never block")
	}
}

// TestBackpressure_ApplyBatchDoesNotReferenceBudget is the static guard from Step 6: the acquire MUST
// be on the PRODUCER (Update/Commit), never inside applyBatch. If applyBatch ever references s.budget,
// the acquire moved onto the worker (it would self-deadlock or defeat the bound). Parse update.go and
// assert applyBatch's body has no `budget` selector.
func TestBackpressure_ApplyBatchDoesNotReferenceBudget(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "update.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "applyBatch" {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name == "budget" {
				t.Fatalf("applyBatch references s.budget — the backpressure acquire must be on the producer, not the worker")
			}
			return true
		})
	}
}
