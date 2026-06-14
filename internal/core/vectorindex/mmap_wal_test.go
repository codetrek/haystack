package vectorindex

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWALAppendAndReplay(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir)
	requireNoError(t, err)

	vec := []float32{1.0, 2.0, 3.0}
	lsn1, err := w.Append(WalInsert, EncodeInsert(0, 1, vec, 3.74, "doc-0"), false)
	requireNoError(t, err)
	assert.Equal(t, uint64(1), lsn1)

	lsn2, err := w.Append(WalSetNeighbors, EncodeSetNeighbors(0, 0, []uint64{1, 2, 3}), false)
	requireNoError(t, err)
	assert.Equal(t, uint64(2), lsn2)

	lsn3, err := w.Append(WalSetEntry, EncodeSetEntry(0, 1), false)
	requireNoError(t, err)
	assert.Equal(t, uint64(3), lsn3)

	lsn4, err := w.Append(WalSetNorm, EncodeSetNorm(0, 5.5), false)
	requireNoError(t, err)
	assert.Equal(t, uint64(4), lsn4)

	lsn5, err := w.Append(WalDelete, EncodeDelete(0, "doc-0"), false)
	requireNoError(t, err)
	assert.Equal(t, uint64(5), lsn5)

	requireNoError(t, w.Close())

	// Replay all records.
	w2, err := OpenWAL(dir)
	requireNoError(t, err)
	defer w2.Close()

	var records []struct {
		lsn uint64
		typ WalRecordType
	}
	err = w2.Replay(0, func(lsn uint64, typ WalRecordType, payload []byte) error {
		records = append(records, struct {
			lsn uint64
			typ WalRecordType
		}{lsn, typ})

		switch typ {
		case WalInsert:
			nid, lvl, v, norm, doc := DecodeInsert(payload)
			assert.Equal(t, uint64(0), nid)
			assert.Equal(t, 1, lvl)
			assert.InDelta(t, float32(3.74), norm, 0.01)
			assert.Equal(t, vec, v)
			assert.Equal(t, "doc-0", doc)
		case WalSetNeighbors:
			nid, layer, nbs := DecodeSetNeighbors(payload)
			assert.Equal(t, uint64(0), nid)
			assert.Equal(t, 0, layer)
			assert.Equal(t, []uint64{1, 2, 3}, nbs)
		case WalSetEntry:
			eid, ml := DecodeSetEntry(payload)
			assert.Equal(t, uint64(0), eid)
			assert.Equal(t, 1, ml)
		case WalSetNorm:
			nid, norm := DecodeSetNorm(payload)
			assert.Equal(t, uint64(0), nid)
			assert.InDelta(t, float32(5.5), norm, 0.01)
		case WalDelete:
			nid, doc := DecodeDelete(payload)
			assert.Equal(t, uint64(0), nid)
			assert.Equal(t, "doc-0", doc)
		}
		return nil
	})
	requireNoError(t, err)
	assert.Len(t, records, 5)
}

func TestWALReplayAfterLSN(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir)
	requireNoError(t, err)

	for i := 0; i < 10; i++ {
		_, err := w.Append(WalSetNorm, EncodeSetNorm(uint64(i), float32(i)), false)
		requireNoError(t, err)
	}
	requireNoError(t, w.Close())

	w2, err := OpenWAL(dir)
	requireNoError(t, err)
	defer w2.Close()

	var count int
	err = w2.Replay(5, func(lsn uint64, typ WalRecordType, payload []byte) error {
		assert.Greater(t, lsn, uint64(5))
		count++
		return nil
	})
	requireNoError(t, err)
	assert.Equal(t, 5, count)
}

func TestWALTruncatedRecord(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir)
	requireNoError(t, err)

	for i := 0; i < 5; i++ {
		_, err := w.Append(WalSetNorm, EncodeSetNorm(uint64(i), float32(i)), false)
		requireNoError(t, err)
	}
	requireNoError(t, w.Close())

	// Truncate file to simulate incomplete write.
	path := filepath.Join(dir, "wal.bin")
	info, _ := os.Stat(path)
	requireNoError(t, os.Truncate(path, info.Size()-3))

	w2, err := OpenWAL(dir)
	requireNoError(t, err)
	defer w2.Close()

	var count int
	err = w2.Replay(0, func(lsn uint64, typ WalRecordType, payload []byte) error {
		count++
		return nil
	})
	requireNoError(t, err)
	assert.Equal(t, 4, count)
}

func TestWALCorruptedCRC(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir)
	requireNoError(t, err)

	for i := 0; i < 5; i++ {
		_, err := w.Append(WalSetNorm, EncodeSetNorm(uint64(i), float32(i)), false)
		requireNoError(t, err)
	}
	requireNoError(t, w.Close())

	// Corrupt a byte in the middle of the file (record 3).
	path := filepath.Join(dir, "wal.bin")
	data, err := os.ReadFile(path)
	requireNoError(t, err)

	// Each SET_NORM record: header(13) + payload(12) + crc(4) = 29 bytes.
	corruptOffset := 29*2 + 5 // middle of third record
	data[corruptOffset] ^= 0xFF
	requireNoError(t, os.WriteFile(path, data, 0644))

	w2, err := OpenWAL(dir)
	requireNoError(t, err)
	defer w2.Close()

	var count int
	err = w2.Replay(0, func(lsn uint64, typ WalRecordType, payload []byte) error {
		count++
		return nil
	})
	requireNoError(t, err)
	assert.Equal(t, 2, count) // Stops at corrupted record
}

// ---------------------------------------------------------------------------
// Store-level WAL replay integration tests
// These exercise the replayWAL callback in MmapStore (block #1, #2).
// ---------------------------------------------------------------------------

func TestWALReplayInsertWithUpperLevel(t *testing.T) {
	// Write a node at level>0, close, reopen → replayWAL must allocate upper
	// slot and restore vectors/nodes/meta correctly.
	dir := t.TempDir()
	opts := MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4}

	s, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)

	vec := []float32{3.0, 4.0, 0.0, 0.0}
	requireNoError(t, s.PutNode(0, 2, vec))
	requireNoError(t, s.SetEntryPoint(0, 2))
	requireNoError(t, s.Close())

	// Reopen triggers replayWAL (no checkpoint, so all WAL records replayed).
	s2, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)
	defer s2.Close()

	got, err := s2.GetVector(0)
	requireNoError(t, err)
	assert.Equal(t, vec, got)

	level, err := s2.GetNodeLevel(0)
	requireNoError(t, err)
	assert.Equal(t, 2, level)

	epId, epLevel, err := s2.GetEntryPoint()
	requireNoError(t, err)
	assert.Equal(t, uint64(0), epId)
	assert.Equal(t, 2, epLevel)

	// Upper neighbors should be settable after replay (proves upper slot was allocated).
	requireNoError(t, s2.SetNeighbors(0, 1, []uint64{0}))
}

func TestWALReplaySetNeighborsUpperPath(t *testing.T) {
	// Exercises the WalSetNeighbors → setNeighborsUpper path in replayWAL.
	dir := t.TempDir()
	opts := MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4}

	s, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)

	vec := []float32{1.0, 0, 0, 0}
	requireNoError(t, s.PutNode(0, 2, vec))
	requireNoError(t, s.SetNeighbors(0, 1, []uint64{1, 2}))
	requireNoError(t, s.SetNeighbors(0, 0, []uint64{3}))
	requireNoError(t, s.Close())

	s2, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)
	defer s2.Close()

	nbs, err := s2.GetNeighbors(0, 1)
	requireNoError(t, err)
	assert.Equal(t, []uint64{1, 2}, nbs)

	nbs0, err := s2.GetNeighbors(0, 0)
	requireNoError(t, err)
	assert.Equal(t, []uint64{3}, nbs0)
}

func TestWALReplaySetNormAndDelete(t *testing.T) {
	// Exercises WalSetNorm and WalDelete record types in replayWAL.
	dir := t.TempDir()
	opts := MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4}

	s, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)

	vec := []float32{1.0, 0, 0, 0}
	requireNoError(t, s.PutNode(0, 0, vec))
	requireNoError(t, s.PutNode(1, 0, vec))
	requireNoError(t, s.SetNorm(0, 42.0))
	requireNoError(t, s.DeleteNode(1))
	requireNoError(t, s.Close())

	s2, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)
	defer s2.Close()

	norm, err := s2.GetNorm(0)
	requireNoError(t, err)
	assert.InDelta(t, float32(42.0), norm, 0.01)

	// Node 1 should be deleted after replay.
	_, err = s2.GetNodeLevel(1)
	assert.Error(t, err)

	// rebuildNodeCount should give 1 (only node 0 alive).
	assert.Equal(t, uint64(1), s2.meta.NodeCount)
}

func TestWALReplayGrowsDuringReplay(t *testing.T) {
	// Insert a node beyond initial capacity, close, reopen.
	// replayWAL must call ensureCapacity and grow files during replay.
	dir := t.TempDir()
	opts := MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4}

	s, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)

	vec := []float32{5.0, 6.0, 7.0, 8.0}
	// Insert at ID 1500 (beyond default 1024 capacity).
	requireNoError(t, s.PutNode(1500, 0, vec))
	requireNoError(t, s.Close())

	s2, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)
	defer s2.Close()

	got, err := s2.GetVector(1500)
	requireNoError(t, err)
	assert.Equal(t, vec, got)

	// Capacity should have grown during replay.
	assert.Greater(t, s2.vecCapacity, uint64(1500))
}

func TestWALContinueLSNAfterReopen(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir)
	requireNoError(t, err)

	_, err = w.Append(WalSetNorm, EncodeSetNorm(0, 1.0), false)
	requireNoError(t, err)
	_, err = w.Append(WalSetNorm, EncodeSetNorm(1, 2.0), false)
	requireNoError(t, err)
	requireNoError(t, w.Close())

	w2, err := OpenWAL(dir)
	requireNoError(t, err)
	lsn, err := w2.Append(WalSetNorm, EncodeSetNorm(2, 3.0), false)
	requireNoError(t, err)
	assert.Equal(t, uint64(3), lsn)
	requireNoError(t, w2.Close())
}
