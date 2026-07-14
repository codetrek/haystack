package invertedstore

import (
	"testing"

	"github.com/codetrek/haystack/core/queue"
)

// newForwardSkipStore mirrors newMergeStore: a started queue + Open + one table (AutoMerge off).
func newForwardSkipStore(t *testing.T, opts Options) (*Store, int) {
	t.Helper()
	q := queue.NewMpsc("fwdskip")
	q.Start()
	s, err := Open(t.TempDir(), q, opts)
	if err != nil {
		t.Fatal(err)
	}
	tid, err := s.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}
	// Release every open segment fd at test end (retireKeepFile via CloseAndWait). Without this the
	// read fds a spill opens stay live, and on Windows t.TempDir's RemoveAll cannot delete seg-*.dat
	// while a handle is open. No caller of this helper closes the store itself, so one close here is safe.
	t.Cleanup(func() { s.CloseAndWait() })
	return s, tid
}

// Three sealed segments with DISJOINT ascending docid ranges (one table). A docid above all ranges
// probes 0 segments; an in-range docid probes only the covering segment.
func TestForwardSkip_ProbesOnlyCoveringSegment(t *testing.T) {
	s, tid := newForwardSkipStore(t, Options{CapBytes: 1 << 20})
	// Seal three segments: docids [1..3], [10..12], [20..22].
	for _, base := range []int64{1, 10, 20} {
		for d := base; d < base+3; d++ {
			s.applyForTest(tid, d, []string{uniqWord(int(d))})
		}
		s.spillForTest(tid)
	}
	if got := len(s.SegmentsForTest()); got != 3 {
		t.Fatalf("want 3 segments, got %d", got)
	}

	probes := s.installForwardProbeCounter(t)

	// A brand-new high docid (cold-build shape) is above every range → 0 probes.
	probes.Store(0)
	s.forwardKeywordsForTest(tid, 999)
	if n := probes.Load(); n != 0 {
		t.Fatalf("new high docid probed %d segments, want 0 (all range-skipped)", n)
	}

	// An in-range docid (11) probes ONLY the [10..12] segment → exactly 1 probe.
	probes.Store(0)
	words, _ := s.forwardKeywordsForTest(tid, 11)
	if n := probes.Load(); n != 1 {
		t.Fatalf("in-range docid probed %d segments, want 1", n)
	}
	if len(words) != 1 || words[0] != uniqWord(11) {
		t.Fatalf("forward for docid 11 = %v, want [%s]", words, uniqWord(11))
	}
}

// An [I]-present, [F]-absent segment (a head that only added postings via the test stub never sets a
// forward — but for the real path: a spill of only deletes emits forward-tombstones; here assert the
// empty-range case always-skips). Build a segment with NO forward records and confirm it is skipped.
func TestForwardSkip_EmptyForwardRangeAlwaysSkips(t *testing.T) {
	s, tid := newForwardSkipStore(t, Options{CapBytes: 1 << 20})
	// addPosting without setForward → [I] present, [F] absent (exercised via a worker task).
	s.q.RunFunc(func() error {
		s.mu.Lock()
		h := newHeadTable()
		h.addPosting("orphanKw", 7)
		s.head[tid] = h
		s.mu.Unlock()
		return s.spill(tid)
	})
	sm := s.SegmentsForTest()
	if len(sm) != 1 || sm[0].MinDocid <= sm[0].MaxDocid {
		t.Fatalf("forward-absent segment should have an empty (min>max) range, got %+v", sm)
	}
	probes := s.installForwardProbeCounter(t)
	s.forwardKeywordsForTest(tid, 7)
	if n := probes.Load(); n != 0 {
		t.Fatalf("empty-range segment probed %d times, want 0", n)
	}
}

// A pre-B (FormatVersion 2) manifest has no docid range — the segMeta MinDocid/MaxDocid unmarshal to
// [0,0], a VALID-looking range that would mis-skip every docid != 0. Open of a < 3 manifest must
// recompute each segment's range from its forward records and rewrite at v3. Build a real segment,
// crash-close, hand-craft the on-disk MANIFEST back to v2 with a stale [0,0] range, then reopen and
// assert the range is corrected, FormatVersion == 3, and an in-range read still resolves+skips right.
func TestForwardSkip_LegacyManifestUpgrade(t *testing.T) {
	dir := t.TempDir()
	s := openAt(t, dir, Options{AutoMerge: false, CapBytes: 1 << 20})
	tid, err := s.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}
	for d := int64(10); d <= 12; d++ {
		s.applyForTest(tid, d, []string{uniqWord(int(d))})
	}
	s.spillForTest(tid)
	if got := len(s.SegmentsForTest()); got != 1 {
		t.Fatalf("want 1 segment, got %d", got)
	}
	s.dropHeadCloseSegmentsForTest() // close fds, keep the segment file + MANIFEST on disk

	// Hand-craft the on-disk MANIFEST back to a pre-B v2 with a stale [0,0] range on every segment.
	man, err := readManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	man.FormatVersion = 2
	for i := range man.Segments {
		man.Segments[i].MinDocid = 0
		man.Segments[i].MaxDocid = 0
	}
	if err := writeManifest(dir, man); err != nil {
		t.Fatal(err)
	}

	// Reopen: Open must detect FormatVersion < 3 and recompute each segment's range.
	s2 := openAt(t, dir, Options{AutoMerge: false, CapBytes: 1 << 20})
	defer s2.CloseAndWait() // release the reopened store's segment fds (Windows RemoveAll)
	sm := s2.SegmentsForTest()
	if len(sm) != 1 {
		t.Fatalf("want 1 segment after reopen, got %d", len(sm))
	}
	if sm[0].MinDocid != 10 || sm[0].MaxDocid != 12 {
		t.Fatalf("upgraded range = [%d,%d], want [10,12]", sm[0].MinDocid, sm[0].MaxDocid)
	}

	// The on-disk MANIFEST must now be at v3 with the corrected range persisted.
	man2, err := readManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if man2.FormatVersion != 3 {
		t.Fatalf("on-disk FormatVersion = %d, want 3 after upgrade", man2.FormatVersion)
	}
	if man2.Segments[0].MinDocid != 10 || man2.Segments[0].MaxDocid != 12 {
		t.Fatalf("persisted range = [%d,%d], want [10,12]", man2.Segments[0].MinDocid, man2.Segments[0].MaxDocid)
	}

	// An in-range read still resolves through the corrected range (1 probe), and an out-of-range
	// docid is skipped (0 probes) — the stale [0,0] would have mis-skipped docid 11 entirely.
	probes := s2.installForwardProbeCounter(t)
	probes.Store(0)
	words, _ := s2.forwardKeywordsForTest(tid, 11)
	if n := probes.Load(); n != 1 {
		t.Fatalf("in-range docid 11 probed %d segments, want 1", n)
	}
	if len(words) != 1 || words[0] != uniqWord(11) {
		t.Fatalf("forward for docid 11 = %v, want [%s]", words, uniqWord(11))
	}
	probes.Store(0)
	s2.forwardKeywordsForTest(tid, 999)
	if n := probes.Load(); n != 0 {
		t.Fatalf("out-of-range docid 999 probed %d segments, want 0", n)
	}
}
