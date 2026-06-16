package vectorstore

import (
	"os"
	"path/filepath"
	"testing"
)

// buildHeadSeg appends rows into a fresh in-memory segment (head) for sealing.
func buildHeadSeg(m Metric, rows []struct {
	doc int64
	v   []float32
	pl  []byte
}) *segment {
	seg := newSegment(m)
	for _, r := range rows {
		stored, norm := m.prepare(r.v)
		seg.append(r.doc, stored, norm, r.pl)
	}
	return seg
}

func TestSeal_WriteOpenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	head := buildHeadSeg(DotProduct, []struct {
		doc int64
		v   []float32
		pl  []byte
	}{
		{10, []float32{1, 0, 0}, []byte("p10")},
		{20, []float32{0, 1, 0}, []byte("p20")},
		{30, []float32{0, 0, 1}, nil},
	})
	// Tombstone slot 1 (docId 20) so the sealed segment carries a delete.
	head.tombstone(1)

	segDir := filepath.Join(dir, "seg-1-0")
	requireNoError(t, writeSealedSegment(segDir, head))

	ss, err := openSealedSegment(segDir, DotProduct)
	requireNoError(t, err)
	defer ss.close()

	if ss.dim != 3 {
		t.Fatalf("dim = %d, want 3", ss.dim)
	}
	if ss.count() != 3 {
		t.Fatalf("count = %d, want 3", ss.count())
	}
	// slot 0 live, docId 10, payload p10, vector {1,0,0}
	stored, _, pl, live := ss.read(0)
	if !live || ss.slotDoc(0) != 10 || string(pl) != "p10" || stored[0] != 1 {
		t.Fatalf("slot0 = live=%v doc=%d pl=%q v=%v", live, ss.slotDoc(0), pl, stored)
	}
	// slot 1 tombstoned (docId 20)
	if _, _, _, live := ss.read(1); live {
		t.Fatal("slot1 should be tombstoned")
	}
	// eachLive visits only slots 0 and 2
	var seenDocs []int64
	ss.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		seenDocs = append(seenDocs, docID)
	})
	if len(seenDocs) != 2 || seenDocs[0] != 10 || seenDocs[1] != 30 {
		t.Fatalf("eachLive docs = %v, want [10 30]", seenDocs)
	}
}

// sealFourRows writes a small sealed segment and returns its dir. The rows are
// chosen so every .dat file has a non-trivial data region (multi-byte payloads,
// >1 row) to exercise the truncation guards.
func sealFourRows(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	head := buildHeadSeg(DotProduct, []struct {
		doc int64
		v   []float32
		pl  []byte
	}{
		{10, []float32{1, 0, 0}, []byte("payload-ten")},
		{20, []float32{0, 1, 0}, []byte("payload-twenty")},
		{30, []float32{0, 0, 1}, []byte("payload-thirty")},
		{40, []float32{0, 1, 1}, nil},
	})
	segDir := filepath.Join(dir, "seg-9-0")
	requireNoError(t, writeSealedSegment(segDir, head))
	return segDir
}

// TestSeal_TruncatedFileErrorsNotPanic asserts that a torn or truncated .dat
// file makes openSealedSegment return a clean error instead of panicking on a
// slice-bounds / index-out-of-range — so recovery can quarantine the segment
// rather than crash Store.Open (adversarial findings #2/#3/#9).
func TestSeal_TruncatedFileErrorsNotPanic(t *testing.T) {
	cases := []struct {
		name string
		file string
		size int64 // truncated file length in bytes
	}{
		// vectors.dat: header is segPageSize, each row is (3+1)*4 = 16 bytes,
		// 4 rows => data ends at segPageSize+64.
		{"vectors_below_4", "vectors.dat", 3},                    // can't even read magic
		{"vectors_below_16", "vectors.dat", 8},                   // can't read Dim/Count
		{"vectors_header_only", "vectors.dat", segPageSize},      // 0 rows mapped, header says 4
		{"vectors_partial_rows", "vectors.dat", segPageSize + 8}, // less than one row short of full

		// slotdoc.dat: header then 4*8 = 32 bytes of docIds.
		{"slotdoc_below_16", "slotdoc.dat", 4},
		{"slotdoc_header_only", "slotdoc.dat", segPageSize},
		{"slotdoc_partial", "slotdoc.dat", segPageSize + 8},

		// tomb.dat: header then words*8; 4 rows => 1 word => 8 bytes.
		{"tomb_below_16", "tomb.dat", 4},
		{"tomb_header_only", "tomb.dat", segPageSize},

		// payload.dat: header, then 4*4 = 16 bytes of lens, then the bytes.
		{"payload_below_16", "payload.dat", 4},
		{"payload_lens_truncated", "payload.dat", segPageSize + 8},   // lens array short
		{"payload_bytes_truncated", "payload.dat", segPageSize + 16}, // lens ok, no payload bytes
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			segDir := sealFourRows(t)
			path := filepath.Join(segDir, tc.file)
			requireNoError(t, os.Truncate(path, tc.size))

			// Must not panic; must return an error.
			var ss *sealedSegment
			var err error
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("openSealedSegment panicked on truncated %s (%d bytes): %v", tc.file, tc.size, r)
					}
				}()
				ss, err = openSealedSegment(segDir, DotProduct)
			}()
			if err == nil {
				if ss != nil {
					ss.close()
				}
				t.Fatalf("openSealedSegment(%s truncated to %d) returned nil error, want corruption error", tc.file, tc.size)
			}
		})
	}
}

// TestSeal_CorruptCountHeaderErrors asserts a bogus huge Count in vectors.dat is
// rejected (no bogus-size make()/OOB) rather than crashing the opener.
func TestSeal_CorruptCountHeaderErrors(t *testing.T) {
	segDir := sealFourRows(t)
	path := filepath.Join(segDir, "vectors.dat")
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	requireNoError(t, err)
	// Overwrite the Count field (bytes 8..16) with an enormous value.
	huge := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x7F}
	if _, err := f.WriteAt(huge, 8); err != nil {
		t.Fatalf("writeat: %v", err)
	}
	requireNoError(t, f.Close())

	var ss *sealedSegment
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("openSealedSegment panicked on corrupt huge Count: %v", r)
			}
		}()
		ss, err = openSealedSegment(segDir, DotProduct)
	}()
	if err == nil {
		if ss != nil {
			ss.close()
		}
		t.Fatal("openSealedSegment accepted a corrupt huge Count, want error")
	}
}

// TestSeal_CountHeaderMismatchErrors asserts that a per-file Count header that
// disagrees with vectors.dat's Count is rejected. A mismatched slotdoc/payload
// count would otherwise mis-size the decode and either read garbage or index out
// of bounds; the opener must fail cleanly so recovery can quarantine the segment.
func TestSeal_CountHeaderMismatchErrors(t *testing.T) {
	cases := []struct {
		name string
		file string
	}{
		{"slotdoc_count_mismatch", "slotdoc.dat"},
		{"payload_count_mismatch", "payload.dat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			segDir := sealFourRows(t)
			path := filepath.Join(segDir, tc.file)
			f, err := os.OpenFile(path, os.O_RDWR, 0)
			requireNoError(t, err)
			// Count lives at byte offset 8 (after Magic[4] + 4 reserved). Set it to
			// a value that does not equal the vectors count (4).
			bad := []byte{99, 0, 0, 0, 0, 0, 0, 0}
			if _, err := f.WriteAt(bad, 8); err != nil {
				t.Fatalf("writeat: %v", err)
			}
			requireNoError(t, f.Close())

			var ss *sealedSegment
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("openSealedSegment panicked on %s count mismatch: %v", tc.file, r)
					}
				}()
				ss, err = openSealedSegment(segDir, DotProduct)
			}()
			if err == nil {
				if ss != nil {
					ss.close()
				}
				t.Fatalf("openSealedSegment accepted a %s count mismatch, want error", tc.file)
			}
		})
	}
}
