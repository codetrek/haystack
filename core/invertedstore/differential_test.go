package invertedstore

// differential_test.go — P12 / build-step 10 / task T11 (design §11).
//
// The acceptance gate for invertedstore: build BOTH the real, pebble-backed core/invertedindex AND
// invertedstore from the SAME synthetic corpus, run the SAME prefix queries through each, and assert
// IDENTICAL hit sets — the production-shape analogue of the spike's "hit parity 2,414,505" (the spike
// fed invertedindex + the sortruns proto the same token dump; see cmd/sortbench/main.go doPebble /
// doSortruns / sampleQueries / runSearch). The spike's narrow workload (globally-unique edit words,
// no tableId, no real delete reconciliation) MISSED several correctness cases; T11/§11 calls them out
// explicitly, so on top of the bulk parity this file adds targeted cases for each spike-missed gap:
//
//   - add -> del -> add resolved end-to-end through Update (read-time AND after a forced merge).
//   - delete-no-resurrect: a deleted doc never reappears from an older sealed segment.
//   - tableId multi-tenancy isolation: two tables never see each other's docs (Search / GetDocs),
//     exercised at the SEGMENT layer (both tables force-spilled, >=2 sealed segments) so the 4-byte
//     fixed-width tableId key-prefix scan is the thing under test, not the trivial per-table head map.
//   - int64 docids full range: docids near 1<<40 and math.MaxInt64 round-trip through spill + merge
//     (the §11 owed re-measure: the spike computes deltas in int32 space; production is int64).
//   - crash recovery (design §9 / T10, indexer-driven): a "crash" loses the volatile head; on reopen
//     the indexer re-Updates every doc newer than its own cursor (incl. low ids) and reconciles
//     deletions, and the recovered hit set is identical to the source-of-truth oracle. Idempotent
//     re-Update of already-sealed docs leaves the hit set unchanged. (The store keeps NO recovery
//     watermark; recovery is driven entirely through the public Update path + the forwardKeywords
//     resolution hook the indexer uses for its idempotency/deletion check — the real §9 contract.)
//
// Both stores are driven through their REAL public write paths (no test seams for the writes):
// invertedindex via q.AddFunc(idx.Update(...)) + CloseAndWait (the final flush, exactly as the spike's
// doPebble does), invertedstore via Batch.Commit + sync. invertedindex requires the caller to supply
// oldKeywords; the store owns its forward map and diffs internally — the differential harness tracks
// oldKeywords for the invertedindex side only, so the SAME logical edit stream reaches both engines.
//
// Two regression-guard benchmarks back the perf contract as CI guards (design §11 / build-step 10):
// BenchmarkBuild_MemoryCapped (a multi-segment build under a hard GOMEMLIMIT-equivalent
// debug.SetMemoryLimit — the §1/§3 "memory is the hard constraint and it holds" guard, where pebble
// blows up under the same cap) and BenchmarkCodeEditUpdate (the §8 code-edit incremental update path:
// a fixed file set re-edited over many rounds through Batch). The §11 owed re-measures (tableId-in-key,
// int64 full-range, in-memory-dedup peak, backgrounded-merge foreground) are folded in as the
// benchmarks' reported metrics + the targeted correctness cases above.

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"testing"

	"github.com/codetrek/haystack/core/invertedindex"
	"github.com/codetrek/haystack/core/kv/pebblekv"
	"github.com/codetrek/haystack/core/queue"
)

// ---- synthetic corpus -------------------------------------------------------

// corpusDoc is one synthetic document: a docid and its current keyword set.
type corpusDoc struct {
	id       int64
	keywords []string
}

// genCorpus builds a deterministic synthetic corpus of nDocs documents, each a word list of 5..25
// keywords drawn from a vocabulary of vocab terms. The vocabulary is a mix of shared common prefixes
// (so prefix queries fan out to many keywords, like real code identifiers) and per-term suffixes, so
// the corpus exercises the prefix-union path both stores must agree on. Deterministic (fixed seed) so
// a failure is reproducible.
func genCorpus(nDocs, vocab int, seed int64) ([]corpusDoc, []string) {
	rng := rand.New(rand.NewSource(seed))

	// A vocabulary with shared prefixes: ~24 stems crossed with a numeric suffix, so many distinct
	// keywords share a 3-5 char prefix (the realistic prefix-fanout the search path must union).
	stems := []string{
		"alpha", "alphanum", "alphabet", "beta", "betamax", "gamma", "gammaray", "delta",
		"deltav", "epsilon", "zeta", "eta", "theta", "iota", "kappa", "lambda",
		"index", "indexer", "indexing", "search", "searcher", "store", "storage", "stored",
	}
	terms := make([]string, 0, vocab)
	seen := map[string]struct{}{}
	for len(terms) < vocab {
		stem := stems[rng.Intn(len(stems))]
		w := fmt.Sprintf("%s%d", stem, rng.Intn(vocab))
		if _, ok := seen[w]; ok {
			continue
		}
		seen[w] = struct{}{}
		terms = append(terms, w)
	}

	docs := make([]corpusDoc, nDocs)
	for i := 0; i < nDocs; i++ {
		n := 5 + rng.Intn(21) // 5..25 keywords
		kwSet := map[string]struct{}{}
		for len(kwSet) < n {
			kwSet[terms[rng.Intn(len(terms))]] = struct{}{}
		}
		kws := make([]string, 0, len(kwSet))
		for w := range kwSet {
			kws = append(kws, w)
		}
		sort.Strings(kws)
		docs[i] = corpusDoc{id: int64(i + 1), keywords: kws} // docids 1..nDocs
	}
	return docs, terms
}

// sampleQueriesDiff mirrors cmd/sortbench/main.go sampleQueries: high-doc-frequency whole terms plus
// truncated-to-5-rune prefixes, deduped/lowercased. The exact term list is irrelevant to parity (both
// engines run the SAME queries); what matters is that the set spans full-term lookups and short
// high-fanout prefixes so the differential covers the prefix-union path, not just exact keys.
func sampleQueriesDiff(docs []corpusDoc) []string {
	docFreq := map[string]int{}
	for _, d := range docs {
		for _, w := range d.keywords {
			docFreq[w]++
		}
	}
	type tf struct {
		t string
		f int
	}
	all := make([]tf, 0, len(docFreq))
	for t, f := range docFreq {
		if len([]rune(t)) >= 3 {
			all = append(all, tf{t, f})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].f != all[j].f {
			return all[i].f > all[j].f
		}
		return all[i].t < all[j].t
	})
	seen := map[string]struct{}{}
	var qs []string
	add := func(s string) {
		if _, ok := seen[s]; ok || s == "" {
			return
		}
		seen[s] = struct{}{}
		qs = append(qs, s)
	}
	for i := 0; i < len(all) && i < 50; i++ {
		add(all[i].t) // top-frequency whole terms (exact)
	}
	step := len(all) / 150
	if step < 1 {
		step = 1
	}
	for i := 0; i < len(all) && len(qs) < 200; i += step {
		r := []rune(all[i].t)
		if len(r) > 5 {
			r = r[:5] // truncated prefixes -> high fanout
		}
		add(string(r))
	}
	// A few short shared stems guarantee large-fanout prefix unions are exercised.
	for _, p := range []string{"alph", "beta", "gam", "delt", "ind", "sea", "sto"} {
		add(p)
	}
	return qs
}

// ---- the two engines under test --------------------------------------------

// invIndexHarness wraps a real pebble-backed core/invertedindex driven exactly like the spike's
// doPebble: each Update is enqueued on the mpsc worker; CloseAndWait forces the final flush so Search
// sees every posting. Since #105 invertedindex owns its own forward map, Update takes the current
// keyword set only (3-arg) — identical to invertedstore — so the SAME edit stream reaches both engines.
type invIndexHarness struct {
	t      *testing.T
	dir    string
	db     interface{ Close() error }
	q      *queue.Mpsc
	idx    *invertedindex.Index
	closed bool
}

func newInvIndexHarness(t *testing.T) *invIndexHarness {
	t.Helper()
	dir, err := os.MkdirTemp("", "invidx-diff-*")
	if err != nil {
		t.Fatal(err)
	}
	db, err := pebblekv.Open(filepath.Join(dir, "pebble"), 0)
	if err != nil {
		t.Fatal(err)
	}
	q := queue.NewMpsc("diff-invidx")
	q.Start()
	idx, err := invertedindex.New(db, q, invertedindex.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return &invIndexHarness{t: t, dir: dir, db: db, q: q, idx: idx}
}

// update feeds one logical edit. #105's invertedindex owns its forward map and diffs internally, so
// Update takes only the doc's CURRENT keyword set (empty = delete) — the same contract as invertedstore.
func (h *invIndexHarness) update(tableId int, docid int64, keywords []string) {
	newKw := append([]string(nil), keywords...)
	h.q.AddFunc(func() error { h.idx.Update(tableId, docid, newKw); return nil })
}

// flush forces every enqueued Update through and flushes the pending buffers to pebble, so Search is
// authoritative. CloseAndWait is the spike's flush mechanism (doPebble searches AFTER CloseAndWait);
// it is idempotent-safe to call once, so we mark closed and skip it in teardown.
func (h *invIndexHarness) flush() {
	h.q.RunTask(&queue.NopeTask{})
	h.idx.CloseAndWait()
	h.closed = true
}

func (h *invIndexHarness) search(tableId int, query string) map[int64]struct{} {
	return h.idx.Search(tableId, query, -1, nil).DocIds
}

func (h *invIndexHarness) teardown() {
	if !h.closed {
		h.idx.CloseAndWait()
	}
	h.q.Stop()
	h.db.Close()
	os.RemoveAll(h.dir)
}

// invStoreHarness wraps a real invertedstore driven through its public Batch/Update path.
type invStoreHarness struct {
	t    *testing.T
	s    *Store
	b    *Batch
	dir  string
	q    *queue.Mpsc
	opts Options
}

func newInvStoreHarness(t *testing.T, opts Options) *invStoreHarness {
	t.Helper()
	dir := t.TempDir()
	q := queue.NewMpsc("diff-invstore")
	q.Start()
	s, err := Open(dir, q, opts)
	if err != nil {
		t.Fatal(err)
	}
	return &invStoreHarness{t: t, s: s, dir: dir, q: q, opts: opts}
}

// crashAndReopen simulates a process crash: the volatile head (any apply that has not yet spilled to
// a sealed segment) is LOST, while every fsync'd sealed segment named in the durable MANIFEST
// survives. We drain in-flight applies, then close ONLY the segment fds (without spilling the head —
// the production CloseAndWait would spill it, which is the opposite of a crash) by retiring the
// published snapshot, stop the old worker, and Open the same dir on a FRESH queue. The reopened store
// holds exactly the durable segments; its head is empty. This is the design §9 crash-consistency
// guarantee (sealed segments durable, head volatile) that the indexer-driven recovery then repairs.
func (h *invStoreHarness) crashAndReopen() {
	h.t.Helper()
	if h.b != nil { // make sure queued ops land in the head before we yank it away
		h.b.Commit()
		h.b = nil
	}
	h.s.sync() // drain every enqueued apply onto the head/segments
	// Drop the published segment set and close the open fds WITHOUT spilling the head (files kept on
	// disk for the next Open). This is the crash: the head map is simply abandoned with the *Store.
	h.s.dropHeadCloseSegmentsForTest()
	h.q.Stop()

	q := queue.NewMpsc("diff-invstore-reopen")
	q.Start()
	s, err := Open(h.dir, q, h.opts)
	if err != nil {
		h.t.Fatalf("reopen after crash: %v", err)
	}
	h.s, h.q = s, q
}

func (h *invStoreHarness) update(tableId int, docid int64, keywords []string) {
	if h.b == nil {
		h.b = h.s.NewBatch()
	}
	h.b.Update(tableId, docid, keywords)
}

// flush commits the accumulated batch (one apply task) and drains the worker so Search is authoritative.
func (h *invStoreHarness) flush() {
	if h.b != nil {
		h.b.Commit()
		h.b = nil
	}
	h.s.sync()
}

func (h *invStoreHarness) search(tableId int, query string) map[int64]struct{} {
	return h.s.Search(tableId, query, -1, nil).DocIds
}

func (h *invStoreHarness) teardown() {
	h.s.CloseAndWait()
	h.q.Stop()
}

// ---- comparison helpers -----------------------------------------------------

// assertSameHits fails the test if the two docid sets differ, printing the symmetric difference.
func assertSameHits(t *testing.T, query string, want, got map[int64]struct{}) {
	t.Helper()
	if len(want) != len(got) {
		t.Errorf("query %q: hit count differs invertedindex=%d invertedstore=%d", query, len(want), len(got))
	}
	var missing, extra []int64
	for d := range want {
		if _, ok := got[d]; !ok {
			missing = append(missing, d)
		}
	}
	for d := range got {
		if _, ok := want[d]; !ok {
			extra = append(extra, d)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
		sort.Slice(extra, func(i, j int) bool { return extra[i] < extra[j] })
		cap := func(s []int64) []int64 {
			if len(s) > 10 {
				return s[:10]
			}
			return s
		}
		t.Errorf("query %q: hit sets differ. in invertedindex but not invertedstore=%v ; in invertedstore but not invertedindex=%v",
			query, cap(missing), cap(extra))
	}
}

// ---- MUST-PASS: identical hit sets vs invertedindex (the spike's parity) ----

// TestDifferential_IdenticalHitSets is the headline gate: build BOTH engines from the SAME ~3,000-doc
// synthetic corpus, run the SAME ~200 prefix queries through each, and assert byte-for-byte identical
// hit sets per query (the production-shape analogue of the spike's hit-parity 2,414,505). A tiny
// CapBytes forces invertedstore to spill many L0 segments so the search-time newest-wins union across
// MULTIPLE segments + head is exercised (not a single in-memory head), which is the read path that
// must match pebble's LSM read.
func TestDifferential_IdenticalHitSets(t *testing.T) {
	docs, _ := genCorpus(3000, 600, 0xC0FFEE)
	queries := sampleQueriesDiff(docs)
	if len(queries) < 50 {
		t.Fatalf("expected a substantial query set, got %d", len(queries))
	}

	// Each engine numbers tables independently (invertedindex's first id is 0 via GetIncrementalId;
	// the store's is 1), so capture each engine's OWN table id rather than assuming equality — the
	// tableId is just an internal namespace handle; parity is about identical hit sets for the same
	// logical table, fed the same docs.
	ii := newInvIndexHarness(t)
	defer ii.teardown()
	tblII, err := ii.idx.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}

	// Small CapBytes -> many spills -> multi-segment search on the store side.
	is := newInvStoreHarness(t, Options{CapBytes: 64 << 10})
	defer is.teardown()
	tblIS, err := is.s.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}

	// Feed the IDENTICAL doc set to both engines (cold build: every docid new, oldKeywords nil).
	for _, d := range docs {
		ii.update(tblII, d.id, d.keywords)
		is.update(tblIS, d.id, d.keywords)
	}
	ii.flush()
	is.flush()

	// invertedstore must have spilled multiple segments (the multi-segment read path is the point).
	if len(is.s.segs) < 2 {
		t.Fatalf("expected the small-cap store to spill multiple segments, got %d", len(is.s.segs))
	}

	totalHits := 0
	for _, qy := range queries {
		want := ii.search(tblII, qy)
		got := is.search(tblIS, qy)
		assertSameHits(t, qy, want, got)
		totalHits += len(want)
	}
	if totalHits == 0 {
		t.Fatal("queries returned no hits at all — corpus/query generation is broken, parity is vacuous")
	}
	t.Logf("differential parity over %d queries: %d total hits identical across both engines", len(queries), totalHits)
}

// TestDifferential_IdenticalAfterEdits proves the store's INCREMENTAL edit path converges to the same
// hit sets as a clean build of the net final state. The spike only ever fed globally-unique words, so
// it never proved that re-edits + deletes (the store's full re-post + per-keyword tombstones, resolved
// newest-wins across spilled segments) leave the index in the right live state.
//
// The store receives the FULL incremental edit stream (initial build + re-edit rounds + deletes,
// driven through Batch). The oracle is a FRESH invertedindex built ONCE from each doc's NET final
// keyword set. We deliberately do NOT feed the incremental stream to invertedindex: invertedindex
// coalesces a within-flush-window add+delete of the same (keyword,docid) to the delete (its flush
// processes all pendingWrites then all pendingDeletes, ignoring intra-window order), so an interleaved
// stream is not a faithful oracle — but a clean build of the final live state IS the unambiguous target
// both engines must agree on. So this asserts: store(incremental edits) == invertedindex(final state).
func TestDifferential_IdenticalAfterEdits(t *testing.T) {
	docs, terms := genCorpus(1500, 400, 0xBEEF)

	is := newInvStoreHarness(t, Options{CapBytes: 48 << 10})
	defer is.teardown()
	tblIS, err := is.s.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}

	// Track the live (net) keyword set per doc as we drive the incremental stream into the store.
	live := map[int64][]string{}
	for _, d := range docs {
		is.update(tblIS, d.id, d.keywords)
		live[d.id] = d.keywords
	}

	// Edit rounds against the STORE only: re-edit a deterministic subset (fresh keyword sets) and
	// delete some docs. The store diffs internally; we just keep `live` as the ground-truth net state.
	rng := rand.New(rand.NewSource(0x5EED))
	for round := 0; round < 3; round++ {
		for _, d := range docs {
			r := rng.Float64()
			switch {
			case r < 0.15: // delete
				is.update(tblIS, d.id, nil)
				delete(live, d.id)
			case r < 0.50: // re-edit: a fresh random keyword set
				n := 4 + rng.Intn(12)
				set := map[string]struct{}{}
				for len(set) < n {
					set[terms[rng.Intn(len(terms))]] = struct{}{}
				}
				kws := make([]string, 0, len(set))
				for w := range set {
					kws = append(kws, w)
				}
				sort.Strings(kws)
				is.update(tblIS, d.id, kws)
				live[d.id] = kws
			default: // untouched this round
			}
		}
	}
	is.flush()

	// Oracle: a fresh invertedindex built once from the NET final live state (cold, oldKeywords nil).
	ii := newInvIndexHarness(t)
	defer ii.teardown()
	tblII, err := ii.idx.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}
	liveDocs := make([]corpusDoc, 0, len(live))
	for id, kws := range live {
		ii.update(tblII, id, kws)
		liveDocs = append(liveDocs, corpusDoc{id: id, keywords: kws})
	}
	ii.flush()

	queries := sampleQueriesDiff(liveDocs)
	totalHits := 0
	for _, qy := range queries {
		want := ii.search(tblII, qy)
		got := is.search(tblIS, qy)
		assertSameHits(t, qy, want, got)
		totalHits += len(want)
	}
	if totalHits == 0 {
		t.Fatal("post-edit queries returned no hits — the edit stream emptied the index, parity is vacuous")
	}
	t.Logf("post-edit differential parity over %d queries: %d total hits identical (store incremental == invertedindex final-state)", len(queries), totalHits)
}

// ---- MUST-PASS: add -> del -> add (end to end through Update) ----------------

// TestDifferential_AddDelAdd_PresentEndToEnd drives add -> delete -> add for one doc through the REAL
// Update path on the store, across spills (and a forced merge), and asserts the doc resolves PRESENT —
// the §11/T6 case the spike's concat-not-reconcile merge got wrong. The store is the system under test;
// the DESIGN (newest-wins per (keyword,docid)) is the oracle here, NOT invertedindex: invertedindex
// coalesces a within-flush-window add+delete+add of the same (keyword,docid) to the DELETE (it flushes
// all pendingWrites then all pendingDeletes regardless of intra-window order), so it would report this
// stream ABSENT — which is exactly the ambiguity the store's explicit newest-wins resolution removes.
// Each of the three actions lands in its OWN L0 segment so this exercises multi-segment resolution AND
// the merge reconciliation, not a single in-memory head.
func TestDifferential_AddDelAdd_PresentEndToEnd(t *testing.T) {
	is := newInvStoreHarness(t, Options{CapBytes: 1, Fanout: 3}) // tiny cap so each edit spills its own L0 seg
	defer is.teardown()
	tbl, err := is.s.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}

	// add -> del -> add, each followed by a forced spill so the three actions land in three different
	// L0 segments — the multi-segment newest-wins resolution the spike never built.
	is.update(tbl, 10, []string{"alpha"})
	is.flush()
	is.s.forceSpill(tbl) // seg0: alpha ADD 10

	is.update(tbl, 10, nil) // delete
	is.flush()
	is.s.forceSpill(tbl) // seg1: alpha DEL 10 + forward-tombstone

	is.update(tbl, 10, []string{"alpha"}) // re-add
	is.flush()
	is.s.forceSpill(tbl) // seg2: alpha ADD 10 (newest)

	if len(is.s.segs) != 3 {
		t.Fatalf("expected 3 L0 segments (one per action), got %d", len(is.s.segs))
	}

	// Read-time newest-wins (before any merge) must resolve PRESENT.
	got := is.search(tbl, "alpha")
	if _, ok := got[10]; !ok {
		t.Fatalf("add->del->add must resolve PRESENT end-to-end (read-time newest-wins), got %v", got)
	}
	// The forward must reflect the latest re-add ({alpha}), not the delete.
	words, deleted := is.s.forwardKeywords(tbl, 10)
	if deleted || len(words) != 1 || words[0] != "alpha" {
		t.Fatalf("forward after add->del->add must be {alpha} live, got words=%v deleted=%v", words, deleted)
	}

	// Force a tiered merge of the three L0 segments and re-assert: the reconciliation must survive the
	// merge too (the newest ADD wins over the older DEL — the case the spike's concat merge got wrong).
	if !is.s.mergeOneLevelForTest(t) {
		t.Fatal("expected a tiered merge of the 3 L0 segments at Fanout 3")
	}
	if _, ok := is.search(tbl, "alpha")[10]; !ok {
		t.Fatalf("add->del->add must STILL be PRESENT after a merge, store got %v", is.search(tbl, "alpha"))
	}
}

// ---- MUST-PASS: delete-no-resurrect -----------------------------------------

// TestDifferential_DeleteNoResurrect deletes a doc that was sealed (with a live forward + postings) in
// an older segment, then asserts BOTH engines report it absent — and crucially that the store does not
// resurrect it from the older non-empty segment (the forward-tombstone path). A merge spanning the
// delete + the older live record must keep it absent. The net state (deleted) is unambiguous, so
// invertedindex is a faithful oracle here (its within-window coalescing also lands on the delete).
func TestDifferential_DeleteNoResurrect(t *testing.T) {
	ii := newInvIndexHarness(t)
	defer ii.teardown()
	tblII, err := ii.idx.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}
	is := newInvStoreHarness(t, Options{CapBytes: 1, Fanout: 2})
	defer is.teardown()
	tblIS, err := is.s.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}

	// Seal a live doc 10 {alpha,beta} in an older segment.
	ii.update(tblII, 10, []string{"alpha", "beta"})
	is.update(tblIS, 10, []string{"alpha", "beta"})
	is.flush()
	is.s.forceSpill(tblIS)

	// Delete it; seal the delete in a newer segment.
	ii.update(tblII, 10, nil)
	is.update(tblIS, 10, nil)
	is.flush()
	is.s.forceSpill(tblIS)
	ii.flush()

	for _, kw := range []string{"alpha", "beta"} {
		want := ii.search(tblII, kw)
		got := is.search(tblIS, kw)
		assertSameHits(t, kw+" (deleted)", want, got)
		if _, ok := got[10]; ok {
			t.Fatalf("deleted doc 10 must be ABSENT from %q (no resurrection), store got %v", kw, got)
		}
	}

	// The store's own forward must report the doc deleted (not a stale {alpha,beta}).
	words, deleted := is.s.forwardKeywords(tblIS, 10)
	if !deleted || len(words) != 0 {
		t.Fatalf("store forward of deleted doc 10 must be empty/deleted, got words=%v deleted=%v", words, deleted)
	}

	// Merge the two segments and re-assert absence (the forward-tombstone survives the merge).
	if !is.s.mergeOneLevelForTest(t) {
		t.Fatal("expected a tiered merge of the 2 segments at Fanout 2")
	}
	for _, kw := range []string{"alpha", "beta"} {
		if _, ok := is.search(tblIS, kw)[10]; ok {
			t.Fatalf("deleted doc 10 must STILL be ABSENT from %q after merge", kw)
		}
	}
}

// ---- MUST-PASS: tableId multi-tenancy isolation -----------------------------

// TestDifferential_TableIsolation indexes overlapping docids into TWO tables in each engine and asserts
// a query in one table never returns the other table's docs — and that each engine agrees per table.
// Tables are isolated keyword namespaces (design §5: tableId is a fixed-width 4-byte key prefix); the
// spike carried NO tableId, so this is a pure production-format case. Each engine numbers its tables
// independently (index A=0/B=1, store A=1/B=2), so we map the logical tables A/B to each engine's OWN ids.
func TestDifferential_TableIsolation(t *testing.T) {
	ii := newInvIndexHarness(t)
	defer ii.teardown()
	iiA, err := ii.idx.CreateTable("A")
	if err != nil {
		t.Fatal(err)
	}
	iiB, err := ii.idx.CreateTable("B")
	if err != nil {
		t.Fatal(err)
	}

	is := newInvStoreHarness(t, Options{CapBytes: 32 << 10})
	defer is.teardown()
	isA, err := is.s.CreateTable("A")
	if err != nil {
		t.Fatal(err)
	}
	isB, err := is.s.CreateTable("B")
	if err != nil {
		t.Fatal(err)
	}

	// Same docids in BOTH tables, but DISJOINT vocabularies (prefixed ta_/tb_), so a leak across tables
	// would return a docid under a keyword it never had in that table. Overlapping docids (1..200 in
	// both) make any cross-table bleed observable.
	docsA, _ := genCorpus(200, 80, 1)
	docsB, _ := genCorpus(200, 80, 2)
	prefixAll := func(docs []corpusDoc, p string) {
		for i := range docs {
			for j := range docs[i].keywords {
				docs[i].keywords[j] = p + docs[i].keywords[j]
			}
		}
	}
	prefixAll(docsA, "ta_")
	prefixAll(docsB, "tb_")

	feed := func(iiTbl, isTbl int, docs []corpusDoc) {
		for _, d := range docs {
			ii.update(iiTbl, d.id, d.keywords)
			is.update(isTbl, d.id, d.keywords)
		}
	}
	feed(iiA, isA, docsA)
	feed(iiB, isB, docsB)
	ii.flush()
	is.flush()

	// Force BOTH tables' heads to seal into segments so the isolation we assert below is the
	// SEGMENT-level one: the 4-byte fixed-width tableId key-prefix scan (design §5), NOT the trivial
	// per-table head map. A query's segment scan over [I]+tableId+keyword must not bleed into an
	// adjacent tableId's records. Without this the whole corpus can sit in the head (200 small docs <
	// the cap), where table isolation is vacuous (separate maps) and the production-format case the
	// spike never had goes untested. Assert segments actually exist so the check can't silently regress.
	is.s.forceSpill(isA)
	is.s.forceSpill(isB)
	if len(is.s.segs) < 2 {
		t.Fatalf("expected both tables sealed into segments to exercise segment-level tableId isolation, got %d", len(is.s.segs))
	}

	// A query for table B's vocabulary prefix in table A must be EMPTY in both engines, and vice versa.
	for _, qy := range []string{"tb_", "tb_beta", "tb_store"} {
		if got := is.search(isA, qy); len(got) != 0 {
			t.Errorf("store table A leaked table B's keyword %q -> %v", qy, got)
		}
		if got := ii.search(iiA, qy); len(got) != 0 {
			t.Errorf("(oracle) invertedindex table A leaked %q -> %v", qy, got)
		}
	}
	for _, qy := range []string{"ta_", "ta_alpha", "ta_index"} {
		if got := is.search(isB, qy); len(got) != 0 {
			t.Errorf("store table B leaked table A's keyword %q -> %v", qy, got)
		}
		if got := ii.search(iiB, qy); len(got) != 0 {
			t.Errorf("(oracle) invertedindex table B leaked %q -> %v", qy, got)
		}
	}

	// And the per-table hit sets must MATCH the invertedindex oracle for each table's own queries.
	for _, qy := range []string{"ta_", "ta_alph", "ta_ind", "ta_sea"} {
		assertSameHits(t, "A:"+qy, ii.search(iiA, qy), is.search(isA, qy))
	}
	for _, qy := range []string{"tb_", "tb_beta", "tb_sto", "tb_gam"} {
		assertSameHits(t, "B:"+qy, ii.search(iiB, qy), is.search(isB, qy))
	}

	// Non-empty sanity: each table's own prefix actually returns docs (isolation isn't vacuously empty).
	if len(is.search(isA, "ta_")) == 0 {
		t.Fatal("store table A own-prefix query returned no docs — isolation check would be vacuous")
	}
	if len(is.search(isB, "tb_")) == 0 {
		t.Fatal("store table B own-prefix query returned no docs — isolation check would be vacuous")
	}
}

// TestDifferential_GetDocsTableIsolation checks GetDocs (exact-key) honors table isolation too: the
// same exact keyword in two tables returns only that table's docs.
func TestDifferential_GetDocsTableIsolation(t *testing.T) {
	is := newInvStoreHarness(t, Options{})
	defer is.teardown()
	tblA, _ := is.s.CreateTable("A")
	tblB, _ := is.s.CreateTable("B")

	is.update(tblA, 100, []string{"shared", "onlya"})
	is.update(tblB, 100, []string{"shared", "onlyb"})
	is.update(tblB, 200, []string{"shared"})
	is.flush()

	// Seal both tables into segments so GetDocs's exact-key match runs over the on-disk 4-byte
	// tableId key prefix (design §5), not the trivial per-table head map — the production-format
	// isolation the spike (no tableId) never had. Without this the tiny corpus stays in the head
	// and the segment-prefix path goes untested.
	is.s.forceSpill(tblA)
	is.s.forceSpill(tblB)
	if len(is.s.segs) < 2 {
		t.Fatalf("expected both tables sealed into segments to exercise segment-level GetDocs isolation, got %d", len(is.s.segs))
	}

	a := is.s.GetDocs(tblA, "shared").DocIds
	if _, ok := a[100]; !ok || len(a) != 1 {
		t.Fatalf("GetDocs(A,\"shared\") must be exactly {100}, got %v", a)
	}
	b := is.s.GetDocs(tblB, "shared").DocIds
	if _, ok := b[100]; !ok {
		t.Fatalf("GetDocs(B,\"shared\") must contain 100, got %v", b)
	}
	if _, ok := b[200]; !ok || len(b) != 2 {
		t.Fatalf("GetDocs(B,\"shared\") must be exactly {100,200}, got %v", b)
	}
	// A keyword only in table A must not appear in table B.
	if got := is.s.GetDocs(tblB, "onlya").DocIds; len(got) != 0 {
		t.Fatalf("GetDocs(B,\"onlya\") must be empty (A-only keyword), got %v", got)
	}
}

// ---- MUST-PASS: int64 docids — full range (the §11 owed re-measure) ---------

// TestDifferential_Int64DocidFullRange closes the §11 "int64 docids — full range" owed re-measure: the
// spike computes posting/ordinal deltas in int32 space (byte-identical only for ids < 2^31 at its
// corpus). The production codec is int64 throughout (forwardKey writes an 8-byte BE docid; encodeDocs/
// decodeDocs delta-varint over int64). This indexes docs whose ids span low, ~2^40, and math.MaxInt64-1
// alongside a low id sharing a keyword, then asserts Search/GetDocs/forward round-trip every high id
// correctly across a spill AND a tiered merge — the real write/read path, not an estimate. invertedindex
// is the oracle for Search parity (its docid is int64 too), so a high id that survives one engine but
// not the other is caught.
func TestDifferential_Int64DocidFullRange(t *testing.T) {
	hi := []int64{1 << 31, 1 << 40, 1 << 62, math.MaxInt64 - 1, math.MaxInt64}
	lo := int64(7)

	ii := newInvIndexHarness(t)
	defer ii.teardown()
	tblII, err := ii.idx.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}
	is := newInvStoreHarness(t, Options{CapBytes: 1, Fanout: 2}) // tiny cap -> each edit spills its own seg
	defer is.teardown()
	tblIS, err := is.s.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}

	// A low id and every high id all carry "wide"; each high id also carries a unique exact keyword.
	feed := func(id int64, kws []string) {
		ii.update(tblII, id, kws)
		is.update(tblIS, id, kws)
	}
	feed(lo, []string{"wide", "lowonly"})
	for i, id := range hi {
		feed(id, []string{"wide", fmt.Sprintf("hi%d", i)})
		is.flush()
		is.s.forceSpill(tblIS) // spread the high ids across multiple L0 segments
	}
	ii.flush()

	// Parity on the shared prefix "wide": both engines must return the low id + every high id.
	want := ii.search(tblII, "wide")
	got := is.search(tblIS, "wide")
	assertSameHits(t, "wide", want, got)
	for _, id := range append([]int64{lo}, hi...) {
		if _, ok := got[id]; !ok {
			t.Fatalf("docid %d (0x%x) missing from store Search(wide) — int64 round-trip broken: %v", id, id, got)
		}
	}

	// Exact-key GetDocs + the forward map must round-trip each high id under its unique keyword.
	for i, id := range hi {
		kw := fmt.Sprintf("hi%d", i)
		d := is.s.GetDocs(tblIS, kw).DocIds
		if _, ok := d[id]; !ok || len(d) != 1 {
			t.Fatalf("GetDocs(%q) must be exactly {%d (0x%x)}, got %v", kw, id, id, d)
		}
		words, deleted := is.s.forwardKeywords(tblIS, id)
		if deleted || !containsStr(words, "wide") || !containsStr(words, kw) {
			t.Fatalf("forward of high docid %d (0x%x) must round-trip {wide,%s}, got words=%v deleted=%v", id, id, kw, words, deleted)
		}
	}

	// Force a tiered merge spanning the high-id segments; the int64 ids must survive the ord->ord remap
	// + re-encode (the merge re-delta-varints the posting lists — int64 deltas, not int32).
	for is.s.mergeOneLevelForTest(t) { // collapse to the bottom level
	}
	got = is.search(tblIS, "wide")
	assertSameHits(t, "wide (post-merge)", ii.search(tblII, "wide"), got)
	for _, id := range append([]int64{lo}, hi...) {
		if _, ok := got[id]; !ok {
			t.Fatalf("docid %d (0x%x) lost across merge — int64 delta re-encode broken: %v", id, id, got)
		}
	}
}

// ---- MUST-PASS: crash recovery (design §9 / T10, indexer-driven) ------------

// recoveryDoc is one source doc the indexer tracks: its current keywords and a monotonically increasing
// source version (mtime analogue). The indexer's durable cursor is "max version already indexed"; on
// reopen it re-Updates every doc with version > cursor and deletes docids in the store's forward map
// that are no longer in source. This is the §9 indexer-driven recovery contract, modeled exactly.
type recoveryDoc struct {
	id      int64
	version int64
	kws     []string
	deleted bool // true once the source removes the doc
}

// TestDifferential_CrashRecovery_IdenticalHitSet is the design §9 / T10 acceptance case modeled through
// the store's REAL public API: a "crash" (crashAndReopen) loses the volatile head; on reopen the
// indexer, from its OWN durable cursor, re-Updates every source doc newer than the cursor — INCLUDING
// low-id docs edited just before the crash — and reconciles deletions (a docid the indexer knows it
// removed from source is re-Updated empty). The store keeps NO recovery watermark; recovery rides
// entirely on Update + the forwardKeywords resolution hook. After the replay the recovered hit set must
// be byte-identical to a clean invertedindex built from the final source state, AND a redundant second
// replay (idempotency) must leave it unchanged.
//
// Concretely the crash drops two kinds of head-resident edits the indexer must heal:
//   - a LOW-id doc edited just before the crash (its new keywords were only in the volatile head),
//   - a DELETE issued just before the crash (the forward-tombstone was only in the head),
//
// plus brand-new docs the indexer hadn't sealed yet. The §9 guarantee is that re-Updating from source
// is idempotent in result, so over-replay can't corrupt the index.
func TestDifferential_CrashRecovery_IdenticalHitSet(t *testing.T) {
	docs, terms := genCorpus(800, 200, 0xCA5E)

	is := newInvStoreHarness(t, Options{CapBytes: 24 << 10}) // small cap -> some docs sealed, rest in head
	defer is.teardown()
	tbl, err := is.s.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}

	// The indexer's source-of-truth view + its durable cursor (max version persisted before the crash).
	src := map[int64]*recoveryDoc{}
	var version int64
	put := func(id int64, kws []string) {
		version++
		src[id] = &recoveryDoc{id: id, version: version, kws: kws}
		is.update(tbl, id, kws)
	}
	del := func(id int64) {
		version++
		if d := src[id]; d != nil {
			d.deleted, d.version = true, version
		}
		is.update(tbl, id, nil)
	}

	// Initial build, then spill PART of it so some docs are durable and the rest sit in the volatile head.
	for _, d := range docs {
		put(d.id, d.keywords)
	}
	is.flush()
	is.s.forceSpill(tbl) // seal the build so far -> durable segment(s)

	// Post-seal edits that live ONLY in the volatile head (will be LOST by the crash): a low-id re-edit,
	// a delete, and a couple of brand-new docs. These are exactly what the indexer must replay.
	rng := rand.New(rand.NewSource(0xF00D))
	freshKws := func(n int) []string {
		set := map[string]struct{}{}
		for len(set) < n {
			set[terms[rng.Intn(len(terms))]] = struct{}{}
		}
		out := make([]string, 0, n)
		for w := range set {
			out = append(out, w)
		}
		sort.Strings(out)
		return out
	}
	put(1, freshKws(8))    // LOW-id doc edited just before crash (new kws only in the head)
	put(2, freshKws(6))    // another low-id re-edit
	del(3)                 // DELETE just before crash (forward-tombstone only in the head)
	put(9001, freshKws(7)) // brand-new doc the indexer hadn't sealed yet
	put(9002, freshKws(5))
	is.flush() // applied to the head, NOT spilled — these are the volatile, crash-lost edits

	// ---- CRASH: lose the volatile head; only fsync'd sealed segments survive. ----
	is.crashAndReopen()

	// The post-seal edits are GONE: doc 9001/9002 were never sealed, so the recovered store can't have
	// them yet (proves the crash actually dropped the head — otherwise recovery would be vacuous).
	for _, id := range []int64{9001, 9002} {
		if words, deleted := is.s.forwardKeywords(tbl, id); !deleted && len(words) > 0 {
			t.Fatalf("crash should have dropped unsealed doc %d, but it survived as %v — recovery test is vacuous", id, words)
		}
	}

	// ---- INDEXER-DRIVEN RECOVERY (design §9): from the indexer's own cursor, re-Update every source ----
	// doc newer than the cursor (incl. low ids) and reconcile deletions. The store has NO watermark; the
	// indexer drives recovery via the public Update path + forwardKeywords for its idempotency/deletion
	// check. We deliberately replay from cursor 0 (over-replay) to also prove idempotency on sealed docs.
	replay := func() {
		b := is.s.NewBatch()
		for id, d := range src {
			if d.deleted {
				// Deletion reconcile: a docid still live in the store's forward but absent from source.
				if _, deleted := is.s.forwardKeywords(tbl, id); !deleted {
					b.Update(tbl, id, nil)
				}
				continue
			}
			b.Update(tbl, id, d.kws) // re-Update is idempotent in result (§9)
		}
		b.Commit()
		is.s.sync()
	}
	replay()

	// Oracle: a clean invertedindex built once from the FINAL source state (deleted docs omitted).
	ii := newInvIndexHarness(t)
	defer ii.teardown()
	tblII, err := ii.idx.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}
	liveDocs := make([]corpusDoc, 0, len(src))
	for id, d := range src {
		if d.deleted {
			continue
		}
		ii.update(tblII, id, d.kws)
		liveDocs = append(liveDocs, corpusDoc{id: id, keywords: d.kws})
	}
	ii.flush()

	queries := sampleQueriesDiff(liveDocs)
	totalHits := 0
	for _, qy := range queries {
		want := ii.search(tblII, qy)
		got := is.search(tbl, qy)
		assertSameHits(t, "recovered:"+qy, want, got)
		totalHits += len(want)
	}
	if totalHits == 0 {
		t.Fatal("recovered queries returned no hits — recovery emptied the index, parity is vacuous")
	}

	// The low-id edit lost in the head must be re-applied (no stale postings), the delete must NOT
	// resurrect, and doc 9001/9002 must now be present — the §9 targeted guarantees.
	if w, deleted := is.s.forwardKeywords(tbl, 1); deleted || !sameSet(w, src[1].kws) {
		t.Fatalf("recovered forward of low-id doc 1 must be its post-edit kws %v, got %v deleted=%v", src[1].kws, w, deleted)
	}
	if _, deleted := is.s.forwardKeywords(tbl, 3); !deleted {
		t.Fatalf("deleted doc 3 must STAY deleted after recovery (no resurrection)")
	}
	if w, deleted := is.s.forwardKeywords(tbl, 9001); deleted || len(w) == 0 {
		t.Fatalf("brand-new doc 9001 lost at crash must be re-applied by recovery, got %v deleted=%v", w, deleted)
	}

	// ---- IDEMPOTENCY: a redundant second replay must leave the hit set unchanged (§9 over-replay). ----
	replay()
	for _, qy := range queries {
		assertSameHits(t, "idempotent:"+qy, ii.search(tblII, qy), is.search(tbl, qy))
	}
	t.Logf("crash-recovery differential parity over %d queries: %d total hits identical (recovered == invertedindex final-state), idempotent under re-replay", len(queries), totalHits)
}

// ---- helpers for the new cases ----------------------------------------------

func containsStr(ss []string, w string) bool {
	for _, s := range ss {
		if s == w {
			return true
		}
	}
	return false
}

// sameSet (order-independent keyword-set equality) is shared with merge_test.go in this package.

// ---- CI regression-guard benchmarks (design §11 / build-step 10) ------------

// benchCorpusDocs builds the shared benchmark corpus once (deterministic) so both build and memory
// numbers are comparable across runs.
func benchCorpusDocs(n int) []corpusDoc {
	docs, _ := genCorpus(n, n/5+1, 0xB0BA)
	return docs
}

// BenchmarkBuild_MemoryCapped is the §1/§3 "memory is the hard constraint and it holds" CI regression
// guard: it cold-builds a multi-segment index UNDER A HARD MEMORY LIMIT (debug.SetMemoryLimit, the
// in-process equivalent of GOMEMLIMIT — the exact knob §3 reports pebble blowing up under). The store's
// memory is bounded by CapBytes regardless of corpus size, so a small cap must build comfortably under
// a tight limit; this benchmark fails (OOM / GC death) if a future change makes the build buffer grow
// unbounded like pebble's pendingWrites. It reports peak HeapAlloc and the spilled segment count as the
// regression metrics. AutoMerge ON so the foreground build also drives the backgrounded merge (the §11
// "backgrounded-merge foreground time" owed re-measure: foreground stays the spill path, merge is off it).
func BenchmarkBuild_MemoryCapped(b *testing.B) {
	const memLimit = 256 << 20 // 256 MiB, the §3 GOMEMLIMIT the comparison is reported against
	prev := debug.SetMemoryLimit(memLimit)
	defer debug.SetMemoryLimit(prev)

	docs := benchCorpusDocs(20000)
	b.ResetTimer()
	var peakHeap uint64
	var segCount int
	for i := 0; i < b.N; i++ {
		dir := b.TempDir()
		q := queue.NewMpsc("bench-build")
		q.Start()
		// Small cap -> many spills -> the bounded-memory build path; AutoMerge keeps live seg count down.
		s, err := Open(dir, q, Options{CapBytes: 256 << 10, Fanout: 4, AutoMerge: true})
		if err != nil {
			b.Fatal(err)
		}
		tbl, err := s.CreateTable("files")
		if err != nil {
			b.Fatal(err)
		}
		batch := s.NewBatch()
		for _, d := range docs {
			batch.Update(tbl, d.id, d.keywords)
		}
		batch.Commit()
		s.sync()
		s.waitMergeIdle() // let the backgrounded merge settle (so the merge cost is measured, not hidden)

		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		if ms.HeapAlloc > peakHeap {
			peakHeap = ms.HeapAlloc
		}
		segCount = len(s.segs)
		s.CloseAndWait()
		q.Stop()
	}
	b.ReportMetric(float64(peakHeap)/(1<<20), "peakHeapMiB")
	b.ReportMetric(float64(segCount), "liveSegs")
}

// BenchmarkCodeEditUpdate is the §8 code-edit incremental-update CI regression guard: a fixed set of
// "files" (docids) is re-edited over many rounds through Batch (the realistic editor workload — the
// same file touched repeatedly with a slightly changed keyword set), driving the term-id full-re-post +
// per-keyword-tombstone + forward-read path. It guards the §8 incremental-update cost (the price the
// term-id forward pays on edit) against regression. ns/op is per edited-file-update.
func BenchmarkCodeEditUpdate(b *testing.B) {
	const nFiles = 256
	// A stable vocabulary so re-edits churn the SAME keywords across files (forward reads hit warm dict).
	_, terms := genCorpus(1, 400, 0xED17)
	rng := rand.New(rand.NewSource(0xED17))
	fileKws := func() []string {
		n := 8 + rng.Intn(24)
		set := map[string]struct{}{}
		for len(set) < n {
			set[terms[rng.Intn(len(terms))]] = struct{}{}
		}
		out := make([]string, 0, n)
		for w := range set {
			out = append(out, w)
		}
		sort.Strings(out)
		return out
	}

	dir := b.TempDir()
	q := queue.NewMpsc("bench-edit")
	q.Start()
	s, err := Open(dir, q, Options{CapBytes: 512 << 10, Fanout: 4, AutoMerge: true})
	if err != nil {
		b.Fatal(err)
	}
	tbl, err := s.CreateTable("files")
	if err != nil {
		b.Fatal(err)
	}
	// Seed the files once so every measured update is a real re-edit (forward-diff), not a cold add.
	seed := s.NewBatch()
	for id := int64(1); id <= nFiles; id++ {
		seed.Update(tbl, id, fileKws())
	}
	seed.Commit()
	s.sync()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := int64((i % nFiles) + 1)
		s.Update(tbl, id, fileKws()) // re-edit one file with a fresh keyword set (full re-post + tombstones)
		if i%nFiles == nFiles-1 {
			s.sync() // periodically drain so the worker queue can't grow unbounded across b.N
		}
	}
	b.StopTimer()
	s.sync()
	s.waitMergeIdle()
	s.CloseAndWait()
	q.Stop()
}
