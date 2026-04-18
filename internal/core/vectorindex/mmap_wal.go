package vectorindex

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

// WalRecordType identifies the kind of WAL record.
type WalRecordType uint8

const (
	WalInsert       WalRecordType = 1
	WalDelete       WalRecordType = 2
	WalSetNeighbors WalRecordType = 3
	WalSetEntry     WalRecordType = 4
	WalSetNorm      WalRecordType = 5
)

// WAL record disk layout: LSN(8) + Length(4) + Type(1) + Payload(var) + CRC32(4)
const walHeaderSize = 8 + 4 + 1 // LSN + Length + Type
const walCRCSize = 4

// maxWalPayloadSize caps the payload length we accept during WAL replay.
// Anything larger is treated as corruption (prevents OOM on bogus lengths).
// 64 MiB is well above any legitimate record.
const maxWalPayloadSize = 64 << 20

// WAL is an append-only write-ahead log with CRC32 integrity checks.
type WAL struct {
	file *os.File
	lsn  uint64
	mu   sync.Mutex
	buf  *bufio.Writer
}

// OpenWAL opens or creates wal.bin in the given directory.
// It scans the file to determine the current LSN.
func OpenWAL(dir string) (*WAL, error) {
	path := filepath.Join(dir, "wal.bin")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("WAL: open: %w", err)
	}

	w := &WAL{
		file: f,
		buf:  bufio.NewWriter(f),
	}

	// Scan to find the last valid LSN.
	if err := w.scanLSN(); err != nil {
		f.Close()
		return nil, fmt.Errorf("WAL: scan: %w", err)
	}

	return w, nil
}

// scanLSN scans the WAL file to determine the current max LSN and seeks to the
// end of the last valid record.
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
			break // EOF or incomplete header
		}
		lsn := binary.LittleEndian.Uint64(header[0:8])
		length := binary.LittleEndian.Uint32(header[8:12])
		if length > maxWalPayloadSize {
			break // corrupted record — payload too large
		}

		payload := make([]byte, length)
		if _, err := io.ReadFull(r, payload); err != nil {
			break // incomplete payload
		}
		crcBuf := make([]byte, walCRCSize)
		if _, err := io.ReadFull(r, crcBuf); err != nil {
			break // incomplete CRC
		}

		// Verify CRC over LSN+Length+Type+Payload.
		h := crc32.NewIEEE()
		h.Write(header)
		h.Write(payload)
		expected := h.Sum32()
		stored := binary.LittleEndian.Uint32(crcBuf)
		if stored != expected {
			break // CRC mismatch — truncate here
		}

		maxLSN = lsn
		lastValidOffset += int64(walHeaderSize) + int64(length) + int64(walCRCSize)
	}

	// Truncate at last valid position and seek there.
	if err := w.file.Truncate(lastValidOffset); err != nil {
		return err
	}
	if _, err := w.file.Seek(lastValidOffset, io.SeekStart); err != nil {
		return err
	}
	w.lsn = maxLSN
	return nil
}

// Append writes a WAL record and returns the assigned LSN.
// In non-batch mode, it fsyncs immediately.
func (w *WAL) Append(typ WalRecordType, payload []byte, batchMode bool) (uint64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.lsn++
	lsn := w.lsn

	header := make([]byte, walHeaderSize)
	binary.LittleEndian.PutUint64(header[0:8], lsn)
	binary.LittleEndian.PutUint32(header[8:12], uint32(len(payload)))
	header[12] = byte(typ)

	h := crc32.NewIEEE()
	h.Write(header)
	h.Write(payload)
	crcVal := h.Sum32()
	crcBuf := make([]byte, walCRCSize)
	binary.LittleEndian.PutUint32(crcBuf, crcVal)

	if _, err := w.buf.Write(header); err != nil {
		return 0, fmt.Errorf("WAL: write header: %w", err)
	}
	if _, err := w.buf.Write(payload); err != nil {
		return 0, fmt.Errorf("WAL: write payload: %w", err)
	}
	if _, err := w.buf.Write(crcBuf); err != nil {
		return 0, fmt.Errorf("WAL: write crc: %w", err)
	}

	if !batchMode {
		if err := w.buf.Flush(); err != nil {
			return 0, fmt.Errorf("WAL: flush: %w", err)
		}
		if err := w.file.Sync(); err != nil {
			return 0, fmt.Errorf("WAL: sync: %w", err)
		}
	}

	return lsn, nil
}

// Flush flushes the buffered writer.
func (w *WAL) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Flush()
}

// Sync flushes the buffer and fsyncs the file.
func (w *WAL) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.buf.Flush(); err != nil {
		return err
	}
	return w.file.Sync()
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

// Replay scans the WAL and invokes fn for each valid record with LSN > afterLSN.
// Records with CRC mismatches or incomplete data are discarded (file truncated).
// Returns recovered metadata from the replayed records.
func (w *WAL) Replay(afterLSN uint64, fn func(lsn uint64, typ WalRecordType, payload []byte) error) error {
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
		lsn := binary.LittleEndian.Uint64(header[0:8])
		length := binary.LittleEndian.Uint32(header[8:12])
		typ := WalRecordType(header[12])

		if length > maxWalPayloadSize {
			return fmt.Errorf("wal: payload length %d exceeds max %d — possible corruption", length, maxWalPayloadSize)
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

		if lsn <= afterLSN {
			continue
		}

		if fn != nil {
			if err := fn(lsn, typ, payload); err != nil {
				return err
			}
		}
	}

	// Truncate at last valid record and seek to end.
	if err := w.file.Truncate(lastValidOffset); err != nil {
		return err
	}
	if _, err := w.file.Seek(lastValidOffset, io.SeekStart); err != nil {
		return err
	}
	// Reset buffered writer since we seeked.
	w.buf.Reset(w.file)
	return nil
}

// --- Payload encode/decode helpers ---

// EncodeInsert encodes an INSERT WAL payload.
func EncodeInsert(nodeId uint64, level int, vec []float32, norm float32, docId string) []byte {
	// nodeId(8) + level(4) + norm(4) + vecLen(4) + vec(N*4) + docIdLen(2) + docId
	docBytes := []byte(docId)
	size := 8 + 4 + 4 + 4 + len(vec)*4 + 2 + len(docBytes)
	buf := make([]byte, size)
	off := 0
	binary.LittleEndian.PutUint64(buf[off:], nodeId)
	off += 8
	binary.LittleEndian.PutUint32(buf[off:], uint32(level))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(norm))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], uint32(len(vec)))
	off += 4
	for _, v := range vec {
		binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(v))
		off += 4
	}
	binary.LittleEndian.PutUint16(buf[off:], uint16(len(docBytes)))
	off += 2
	copy(buf[off:], docBytes)
	return buf
}

// DecodeInsert decodes an INSERT WAL payload.
func DecodeInsert(payload []byte) (nodeId uint64, level int, vec []float32, norm float32, docId string) {
	off := 0
	nodeId = binary.LittleEndian.Uint64(payload[off:])
	off += 8
	level = int(binary.LittleEndian.Uint32(payload[off:]))
	off += 4
	norm = math.Float32frombits(binary.LittleEndian.Uint32(payload[off:]))
	off += 4
	vecLen := int(binary.LittleEndian.Uint32(payload[off:]))
	off += 4
	vec = make([]float32, vecLen)
	for i := 0; i < vecLen; i++ {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(payload[off:]))
		off += 4
	}
	docIdLen := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2
	docId = string(payload[off : off+docIdLen])
	return
}

// EncodeDelete encodes a DELETE WAL payload.
func EncodeDelete(nodeId uint64, docId string) []byte {
	docBytes := []byte(docId)
	buf := make([]byte, 8+2+len(docBytes))
	binary.LittleEndian.PutUint64(buf[0:], nodeId)
	binary.LittleEndian.PutUint16(buf[8:], uint16(len(docBytes)))
	copy(buf[10:], docBytes)
	return buf
}

// DecodeDelete decodes a DELETE WAL payload.
func DecodeDelete(payload []byte) (nodeId uint64, docId string) {
	nodeId = binary.LittleEndian.Uint64(payload[0:])
	docIdLen := int(binary.LittleEndian.Uint16(payload[8:]))
	docId = string(payload[10 : 10+docIdLen])
	return
}

// EncodeSetNeighbors encodes a SET_NEIGHBORS WAL payload.
func EncodeSetNeighbors(nodeId uint64, layer int, neighbors []uint64) []byte {
	// nodeId(8) + layer(4) + count(4) + neighbors(N*8)
	buf := make([]byte, 8+4+4+len(neighbors)*8)
	binary.LittleEndian.PutUint64(buf[0:], nodeId)
	binary.LittleEndian.PutUint32(buf[8:], uint32(layer))
	binary.LittleEndian.PutUint32(buf[12:], uint32(len(neighbors)))
	for i, n := range neighbors {
		binary.LittleEndian.PutUint64(buf[16+i*8:], n)
	}
	return buf
}

// DecodeSetNeighbors decodes a SET_NEIGHBORS WAL payload.
func DecodeSetNeighbors(payload []byte) (nodeId uint64, layer int, neighbors []uint64) {
	nodeId = binary.LittleEndian.Uint64(payload[0:])
	layer = int(binary.LittleEndian.Uint32(payload[8:]))
	count := int(binary.LittleEndian.Uint32(payload[12:]))
	neighbors = make([]uint64, count)
	for i := 0; i < count; i++ {
		neighbors[i] = binary.LittleEndian.Uint64(payload[16+i*8:])
	}
	return
}

// EncodeSetEntry encodes a SET_ENTRY WAL payload.
func EncodeSetEntry(entryId uint64, maxLevel int) []byte {
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint64(buf[0:], entryId)
	binary.LittleEndian.PutUint32(buf[8:], uint32(maxLevel))
	return buf
}

// DecodeSetEntry decodes a SET_ENTRY WAL payload.
func DecodeSetEntry(payload []byte) (entryId uint64, maxLevel int) {
	entryId = binary.LittleEndian.Uint64(payload[0:])
	maxLevel = int(binary.LittleEndian.Uint32(payload[8:]))
	return
}

// EncodeSetNorm encodes a SET_NORM WAL payload.
func EncodeSetNorm(nodeId uint64, norm float32) []byte {
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint64(buf[0:], nodeId)
	binary.LittleEndian.PutUint32(buf[8:], math.Float32bits(norm))
	return buf
}

// DecodeSetNorm decodes a SET_NORM WAL payload.
func DecodeSetNorm(payload []byte) (nodeId uint64, norm float32) {
	nodeId = binary.LittleEndian.Uint64(payload[0:])
	norm = math.Float32frombits(binary.LittleEndian.Uint32(payload[8:]))
	return
}
