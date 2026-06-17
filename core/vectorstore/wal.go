package vectorstore

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
)

// recType identifies a records-layer WAL record. (Distinct numbering from the
// HNSW vectorindex WAL; this log has no graph records.)
type recType uint8

const (
	recPut    recType = 1 // upsert: tombstone old slot (if any) + new slot
	recDelete recType = 2 // tombstone an existing docId
)

// putRecord is the durable form of an upsert. ID is the caller's string key:
// storing it in the WAL makes the id↔docId mapping crash-safe independently of
// idtable's lazily-committed batch (the WAL is the single source of truth for
// the mapping; see store.go replay). OldSlot is the slot to tombstone for this
// docId (-1 when new); the new slot index is implied by append order on apply.
type putRecord struct {
	ID      string
	DocID   int64
	OldSlot int64
	Stored  []float32
	Norm    float32
	Payload []byte

	// badVersion is set by decodePut when the frame does not carry the current
	// walRecMagic prefix (a pre-Phase-5 / corrupt putRecord). It is never encoded;
	// replay rejects any record with badVersion rather than mis-decoding it.
	badVersion bool
}

// deleteRecord tombstones Slot, which holds DocID for string key ID.
type deleteRecord struct {
	ID    string
	DocID int64
	Slot  int64
}

// --- record payload encode/decode ---

func putString(buf []byte, off int, s string) int {
	binary.LittleEndian.PutUint32(buf[off:], uint32(len(s)))
	off += 4
	copy(buf[off:], s)
	return off + len(s)
}

func getString(b []byte, off int) (string, int) {
	n := int(binary.LittleEndian.Uint32(b[off:]))
	off += 4
	s := string(b[off : off+n])
	return s, off + n
}

// walRecMagic prefixes a Phase-5 putRecord body. A pre-Phase-5 putRecord body
// began directly with idLen (a little-endian uint32 — its low bytes are a small,
// data-dependent id length), so it can never begin with these two fixed magic
// bytes (that would require an id of length 0x5AF5 = 23285). decodePut flags any
// frame lacking the magic as badVersion, and replay rejects it, so an old WAL is
// never silently mis-applied. (DEVIATION from the draft's single walRecVersion
// byte: a 1-byte version at offset 0 collides with a legacy frame whose id length
// low byte equals the version — e.g. a length-1 id → 0x01 == version 1. A 2-byte
// magic removes that collision; the format predates production data, clean break.)
var walRecMagic = [2]byte{0xF5, 0x5A}

// encodePut layout: magic(2) | idLen(4)|id | docId(8) | oldSlot(8) | norm(4) |
// vecLen(4) | vec(N*4) | payloadLen(4) | payload (a serialized Payload blob).
func encodePut(r putRecord) []byte {
	size := 2 + 4 + len(r.ID) + 8 + 8 + 4 + 4 + len(r.Stored)*4 + 4 + len(r.Payload)
	buf := make([]byte, size)
	buf[0] = walRecMagic[0]
	buf[1] = walRecMagic[1]
	off := putString(buf, 2, r.ID)
	binary.LittleEndian.PutUint64(buf[off:], uint64(r.DocID))
	off += 8
	binary.LittleEndian.PutUint64(buf[off:], uint64(r.OldSlot))
	off += 8
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(r.Norm))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], uint32(len(r.Stored)))
	off += 4
	for _, v := range r.Stored {
		binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(v))
		off += 4
	}
	binary.LittleEndian.PutUint32(buf[off:], uint32(len(r.Payload)))
	off += 4
	copy(buf[off:], r.Payload)
	return buf
}

func decodePut(b []byte) putRecord {
	r := putRecord{}
	if len(b) < 2 || b[0] != walRecMagic[0] || b[1] != walRecMagic[1] {
		// Pre-Phase-5 or corrupt record: flag it so replay rejects rather than
		// mis-decoding opaque bytes into a bogus head segment. OldSlot is set to -1
		// so an accidental apply (if a caller ignored badVersion) is at least inert.
		return putRecord{OldSlot: -1, badVersion: true}
	}
	off := 2
	r.ID, off = getString(b, off)
	r.DocID = int64(binary.LittleEndian.Uint64(b[off:]))
	off += 8
	r.OldSlot = int64(binary.LittleEndian.Uint64(b[off:]))
	off += 8
	r.Norm = math.Float32frombits(binary.LittleEndian.Uint32(b[off:]))
	off += 4
	n := int(binary.LittleEndian.Uint32(b[off:]))
	off += 4
	r.Stored = make([]float32, n)
	for i := 0; i < n; i++ {
		r.Stored[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[off:]))
		off += 4
	}
	pl := int(binary.LittleEndian.Uint32(b[off:]))
	off += 4
	if pl > 0 {
		r.Payload = make([]byte, pl)
		copy(r.Payload, b[off:off+pl])
	}
	return r
}

// encodeDelete layout: idLen(4)|id | docId(8) | slot(8)
func encodeDelete(id string, docID, slot int64) []byte {
	buf := make([]byte, 4+len(id)+8+8)
	off := putString(buf, 0, id)
	binary.LittleEndian.PutUint64(buf[off:], uint64(docID))
	off += 8
	binary.LittleEndian.PutUint64(buf[off:], uint64(slot))
	return buf
}

func decodeDelete(b []byte) deleteRecord {
	id, off := getString(b, 0)
	d := deleteRecord{ID: id}
	d.DocID = int64(binary.LittleEndian.Uint64(b[off:]))
	off += 8
	d.Slot = int64(binary.LittleEndian.Uint64(b[off:]))
	return d
}

// --- CRC WAL framing (adapted from vectorindex/mmap_wal.go) ---

const walHeaderSize = 8 + 4 + 1 // LSN + Length + Type
const walCRCSize = 4
const maxWalPayloadSize = 64 << 20

// WAL is an append-only write-ahead log with CRC32 integrity checks.
type WAL struct {
	file       osFile
	lsn        uint64
	mu         sync.Mutex
	buf        *bufio.Writer
	size       int64  // logical end offset (buffered + flushed)
	syncedSize int64  // file offset durably synced — the rollback target
	syncedLSN  uint64 // lsn durable as of syncedSize
}

// OpenWAL opens or creates records.wal in dir, scanning it to find the last
// valid LSN and truncating any torn tail.
func OpenWAL(dir string) (*WAL, error) {
	path := filepath.Join(dir, "records.wal")
	f, err := fsOpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("WAL: open: %w", err)
	}
	w := &WAL{file: f, buf: bufio.NewWriter(f)}
	if err := w.scanLSN(); err != nil {
		f.Close()
		return nil, fmt.Errorf("WAL: scan: %w", err)
	}
	return w, nil
}

func (w *WAL) scanLSN() error {
	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReader(w.file)
	var lastValidOffset int64
	var maxLSN uint64
	for {
		header := make([]byte, walHeaderSize)
		if _, err := io.ReadFull(r, header); err != nil {
			break
		}
		lsn := binary.LittleEndian.Uint64(header[0:8])
		length := binary.LittleEndian.Uint32(header[8:12])
		if length > maxWalPayloadSize {
			break
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(r, payload); err != nil {
			break
		}
		crcBuf := make([]byte, walCRCSize)
		if _, err := io.ReadFull(r, crcBuf); err != nil {
			break
		}
		h := crc32.NewIEEE()
		h.Write(header)
		h.Write(payload)
		if binary.LittleEndian.Uint32(crcBuf) != h.Sum32() {
			break
		}
		maxLSN = lsn
		lastValidOffset += int64(walHeaderSize) + int64(length) + int64(walCRCSize)
	}
	if err := w.file.Truncate(lastValidOffset); err != nil {
		return err
	}
	if _, err := w.file.Seek(lastValidOffset, io.SeekStart); err != nil {
		return err
	}
	w.lsn = maxLSN
	w.size = lastValidOffset
	w.syncedSize = lastValidOffset
	w.syncedLSN = maxLSN
	return nil
}

// Append buffers a record and returns its LSN. The caller fsyncs via Sync at the
// commit boundary. An oversize record is rejected up-front (a frame whose length
// field exceeds maxWalPayloadSize is indistinguishable from a torn tail to
// scanLSN/Replay, which would silently drop it and every record after it on
// reopen). A mid-write buffer error rolls the WAL back to the last durable state.
func (w *WAL) Append(typ recType, payload []byte) (uint64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(payload) > maxWalPayloadSize {
		return 0, fmt.Errorf("WAL: record payload %d exceeds max %d", len(payload), maxWalPayloadSize)
	}
	w.lsn++
	lsn := w.lsn
	header := make([]byte, walHeaderSize)
	binary.LittleEndian.PutUint64(header[0:8], lsn)
	binary.LittleEndian.PutUint32(header[8:12], uint32(len(payload)))
	header[12] = byte(typ)
	h := crc32.NewIEEE()
	h.Write(header)
	h.Write(payload)
	crcBuf := make([]byte, walCRCSize)
	binary.LittleEndian.PutUint32(crcBuf, h.Sum32())
	_, err := w.buf.Write(header)
	if err == nil {
		_, err = w.buf.Write(payload)
	}
	if err == nil {
		_, err = w.buf.Write(crcBuf)
	}
	if err != nil {
		w.rollbackLocked()
		return 0, fmt.Errorf("WAL: write frame: %w", err)
	}
	w.size += int64(walHeaderSize) + int64(len(payload)) + int64(walCRCSize)
	return lsn, nil
}

// Sync flushes the buffer and fsyncs the file. On any failure it rolls the WAL
// back to the last durably-synced state, so an errored Sync leaves no record
// that a later Sync/Close could resurrect.
func (w *WAL) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.buf.Flush(); err != nil {
		w.rollbackLocked()
		return err
	}
	if err := w.file.Sync(); err != nil {
		w.rollbackLocked()
		return err
	}
	w.syncedSize = w.size
	w.syncedLSN = w.lsn
	return nil
}

// rollbackLocked restores the WAL to its last durably-synced state, discarding
// any buffered or flushed-but-unsynced frames. An errored Append/Sync must leave
// no recoverable record (crash-atomicity, decision #7): a frame whose fsync
// failed is not silently resurrected on reopen by a later Sync or Close. The
// discarded LSN(s) are reused by the next Append. Caller holds w.mu.
func (w *WAL) rollbackLocked() {
	_ = w.file.Truncate(w.syncedSize)
	_, _ = w.file.Seek(w.syncedSize, io.SeekStart)
	w.buf.Reset(w.file)
	w.lsn = w.syncedLSN
	w.size = w.syncedSize
}

// Reset truncates the WAL to 0 bytes while preserving the LSN counter.
func (w *WAL) Reset() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.buf.Flush(); err != nil {
		return err
	}
	if err := w.file.Truncate(0); err != nil {
		return err
	}
	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	w.buf.Reset(w.file)
	w.size = 0
	w.syncedSize = 0
	w.syncedLSN = w.lsn
	return nil
}

// Close flushes and closes the WAL file.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.buf.Flush(); err != nil {
		w.file.Close()
		return err
	}
	return w.file.Close()
}

// Replay invokes fn for every valid record in LSN order; a torn tail is truncated.
func (w *WAL) Replay(fn func(typ recType, payload []byte) error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReader(w.file)
	var lastValidOffset int64
	for {
		header := make([]byte, walHeaderSize)
		if _, err := io.ReadFull(r, header); err != nil {
			break
		}
		length := binary.LittleEndian.Uint32(header[8:12])
		typ := recType(header[12])
		if length > maxWalPayloadSize {
			return fmt.Errorf("wal: payload length %d exceeds max %d", length, maxWalPayloadSize)
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(r, payload); err != nil {
			break
		}
		crcBuf := make([]byte, walCRCSize)
		if _, err := io.ReadFull(r, crcBuf); err != nil {
			break
		}
		h := crc32.NewIEEE()
		h.Write(header)
		h.Write(payload)
		if binary.LittleEndian.Uint32(crcBuf) != h.Sum32() {
			break
		}
		lastValidOffset += int64(walHeaderSize) + int64(length) + int64(walCRCSize)
		if fn != nil {
			if err := fn(typ, payload); err != nil {
				return err
			}
		}
	}
	if err := w.file.Truncate(lastValidOffset); err != nil {
		return err
	}
	if _, err := w.file.Seek(lastValidOffset, io.SeekStart); err != nil {
		return err
	}
	w.buf.Reset(w.file)
	return nil
}
