package vectorstore

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

// maxSealRows caps the row count an opener will trust from a (possibly corrupt)
// vectors.dat header before allocating slices sized from it. A real segment is
// the dumped in-memory head, so this is far above any legitimate seal while
// still rejecting a garbage Count that would otherwise drive a bogus-size make()
// or overflow the required-size arithmetic. ~1.07e9 rows.
const maxSealRows = 1 << 30

// writeSealedSegment dumps a (frozen) head segment into segDir as four fsynced
// data files: vectors.dat, slotdoc.dat, tomb.dat, payload.dat. Files are written
// and fsynced individually, then the directory is fsynced so the entries are
// durable before any sentinel (manifest) references the segment (audit #76:
// data durable LAST-relative-to-itself, sentinel later). The dir is created here.
func writeSealedSegment(segDir string, head *segment) error {
	if err := os.MkdirAll(segDir, 0755); err != nil {
		return fmt.Errorf("seal: mkdir %s: %w", segDir, err)
	}
	n := len(head.slotDoc)
	dim := head.dim

	if err := writeVectorsFile(segFilePath(segDir, "vectors.dat"), head, dim, n); err != nil {
		return err
	}
	if err := writeSlotDocFile(segFilePath(segDir, "slotdoc.dat"), head, n); err != nil {
		return err
	}
	if err := writeTombFile(segFilePath(segDir, "tomb.dat"), head, n); err != nil {
		return err
	}
	if err := writePayloadFile(segFilePath(segDir, "payload.dat"), head, n); err != nil {
		return err
	}
	// Make the four directory entries durable.
	return fsyncDir(segDir)
}

func writeVectorsFile(path string, head *segment, dim, n int) error {
	f, err := fsCreate(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hdr := make([]byte, segPageSize)
	copy(hdr[0:4], magicVectors[:])
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(dim))
	binary.LittleEndian.PutUint64(hdr[8:16], uint64(n))
	if _, err := f.Write(hdr); err != nil {
		return err
	}
	row := make([]byte, (dim+1)*4)
	for slot := 0; slot < n; slot++ {
		v := head.vectors[slot]
		for i := 0; i < dim; i++ {
			binary.LittleEndian.PutUint32(row[i*4:], math.Float32bits(v[i]))
		}
		binary.LittleEndian.PutUint32(row[dim*4:], math.Float32bits(head.norms[slot]))
		if _, err := f.Write(row); err != nil {
			return err
		}
	}
	return f.Sync()
}

func writeSlotDocFile(path string, head *segment, n int) error {
	f, err := fsCreate(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hdr := make([]byte, segPageSize)
	copy(hdr[0:4], magicSlotDoc[:])
	binary.LittleEndian.PutUint64(hdr[8:16], uint64(n))
	if _, err := f.Write(hdr); err != nil {
		return err
	}
	buf := make([]byte, 8)
	for slot := 0; slot < n; slot++ {
		binary.LittleEndian.PutUint64(buf, uint64(head.slotDoc[slot]))
		if _, err := f.Write(buf); err != nil {
			return err
		}
	}
	return f.Sync()
}

func writeTombFile(path string, head *segment, n int) error {
	f, err := fsCreate(path)
	if err != nil {
		return err
	}
	defer f.Close()
	words := (n + 63) / 64
	if words == 0 {
		words = 1 // always at least one word so the data region is non-empty
	}
	hdr := make([]byte, segPageSize)
	copy(hdr[0:4], magicTomb[:])
	binary.LittleEndian.PutUint64(hdr[8:16], uint64(words))
	if _, err := f.Write(hdr); err != nil {
		return err
	}
	out := make([]byte, words*8)
	for slot := 0; slot < n; slot++ {
		if head.tomb.get(slot) {
			w := slot >> 6
			cur := binary.LittleEndian.Uint64(out[w*8:])
			cur |= 1 << uint(slot&63)
			binary.LittleEndian.PutUint64(out[w*8:], cur)
		}
	}
	if _, err := f.Write(out); err != nil {
		return err
	}
	return f.Sync()
}

func writePayloadFile(path string, head *segment, n int) error {
	f, err := fsCreate(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hdr := make([]byte, segPageSize)
	copy(hdr[0:4], magicPayload[:])
	binary.LittleEndian.PutUint64(hdr[8:16], uint64(n))
	if _, err := f.Write(hdr); err != nil {
		return err
	}
	lens := make([]byte, n*4)
	var total int
	for slot := 0; slot < n; slot++ {
		l := len(head.payloads[slot])
		binary.LittleEndian.PutUint32(lens[slot*4:], uint32(l))
		total += l
	}
	if _, err := f.Write(lens); err != nil {
		return err
	}
	for slot := 0; slot < n; slot++ {
		if len(head.payloads[slot]) > 0 {
			if _, err := f.Write(head.payloads[slot]); err != nil {
				return err
			}
		}
	}
	return f.Sync()
}

// openSealedSegment mmaps a sealed segment's files. vectors/slotdoc/payload are
// read-only; tomb.dat is mapped read-write (the only mutable file). The metric
// must match the store's (vectors are in its natural stored form).
func openSealedSegment(segDir string, metric Metric) (*sealedSegment, error) {
	s := &sealedSegment{dir: segDir, metric: metric}

	// vectors.dat
	vf, err := fsOpenFile(segFilePath(segDir, "vectors.dat"), os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	vSize, err := fileSize(vf)
	if err != nil {
		vf.Close()
		return nil, err
	}
	vmap, err := mmapAlloc(vf.Fd(), 0, int(vSize), mmapRead)
	vf.Close()
	if err != nil {
		return nil, err
	}
	// Header guard: need >=4 bytes for the magic slice and >=16 bytes to read
	// Dim (uint32) + Count (uint64). A torn/short file must error, not panic.
	if len(vmap) < 16 {
		_ = mmapFree(vmap)
		return nil, fmt.Errorf("seal: vectors.dat too short (%d bytes, need >=16) in %s", len(vmap), segDir)
	}
	if string(vmap[0:4]) != string(magicVectors[:]) {
		_ = mmapFree(vmap)
		return nil, fmt.Errorf("seal: bad vectors magic in %s", segDir)
	}
	dim := int(binary.LittleEndian.Uint32(vmap[4:8]))
	nRows := binary.LittleEndian.Uint64(vmap[8:16])
	// Reject bogus dim/count that would overflow or index past the map. Compute
	// the required size in uint64 so a corrupt huge Count cannot wrap on int.
	need := uint64(segPageSize) + nRows*uint64(dim+1)*4
	if dim < 0 || nRows > uint64(maxSealRows) || need < uint64(segPageSize) || uint64(len(vmap)) < need {
		_ = mmapFree(vmap)
		return nil, fmt.Errorf("seal: vectors.dat truncated/corrupt in %s (dim=%d count=%d size=%d need=%d)", segDir, dim, nRows, len(vmap), need)
	}
	s.vecMap = vmap
	s.dim = dim
	s.n = int(nRows)
	s.rowF32 = s.dim + 1
	s.vecBase = segPageSize

	// slotdoc.dat (decode into a slice; it is small and queried per-result)
	sd, err := readWholeFile(segFilePath(segDir, "slotdoc.dat"))
	if err != nil {
		s.close()
		return nil, err
	}
	if len(sd) < 16 {
		s.close()
		return nil, fmt.Errorf("seal: slotdoc.dat too short (%d bytes, need >=16) in %s", len(sd), segDir)
	}
	if string(sd[0:4]) != string(magicSlotDoc[:]) {
		s.close()
		return nil, fmt.Errorf("seal: bad slotdoc magic in %s", segDir)
	}
	// slotdoc declares its own Count; it must agree with vectors' count and the
	// file must be long enough to hold n int64 docIds after the header.
	sdCount := binary.LittleEndian.Uint64(sd[8:16])
	if sdCount != nRows {
		s.close()
		return nil, fmt.Errorf("seal: slotdoc.dat count %d != vectors count %d in %s", sdCount, nRows, segDir)
	}
	if uint64(len(sd)) < uint64(segPageSize)+nRows*8 {
		s.close()
		return nil, fmt.Errorf("seal: slotdoc.dat truncated in %s (size=%d need=%d)", segDir, len(sd), uint64(segPageSize)+nRows*8)
	}
	s.slotDocs = make([]int64, s.n)
	for i := 0; i < s.n; i++ {
		s.slotDocs[i] = int64(binary.LittleEndian.Uint64(sd[segPageSize+i*8:]))
	}

	// tomb.dat (RW mmap)
	tf, err := fsOpenFile(segFilePath(segDir, "tomb.dat"), os.O_RDWR, 0)
	if err != nil {
		s.close()
		return nil, err
	}
	tSize, err := fileSize(tf)
	if err != nil {
		tf.Close()
		s.close()
		return nil, err
	}
	tmap, err := mmapAlloc(tf.Fd(), 0, int(tSize), mmapRead|mmapWrite)
	tf.Close()
	if err != nil {
		s.close()
		return nil, err
	}
	if len(tmap) < 16 {
		_ = mmapFree(tmap)
		s.close()
		return nil, fmt.Errorf("seal: tomb.dat too short (%d bytes, need >=16) in %s", len(tmap), segDir)
	}
	if string(tmap[0:4]) != string(magicTomb[:]) {
		_ = mmapFree(tmap)
		s.close()
		return nil, fmt.Errorf("seal: bad tomb magic in %s", segDir)
	}
	tWords := binary.LittleEndian.Uint64(tmap[8:16])
	// The bitmap must cover every slot AND the mapped file must actually hold the
	// declared words, else tombGet would index past the map and panic.
	needWords := (nRows + 63) / 64
	if needWords == 0 {
		needWords = 1
	}
	tNeed := uint64(segPageSize) + tWords*8
	if tWords < needWords || tNeed < uint64(segPageSize) || uint64(len(tmap)) < tNeed {
		_ = mmapFree(tmap)
		s.close()
		return nil, fmt.Errorf("seal: tomb.dat truncated/corrupt in %s (words=%d needWords=%d size=%d need=%d)", segDir, tWords, needWords, len(tmap), tNeed)
	}
	s.tombMap = tmap
	s.tombWords = int(tWords)

	// payload.dat (read-only mmap)
	pf, err := fsOpenFile(segFilePath(segDir, "payload.dat"), os.O_RDONLY, 0)
	if err != nil {
		s.close()
		return nil, err
	}
	pSize, err := fileSize(pf)
	if err != nil {
		pf.Close()
		s.close()
		return nil, err
	}
	pmap, err := mmapAlloc(pf.Fd(), 0, int(pSize), mmapRead)
	pf.Close()
	if err != nil {
		s.close()
		return nil, err
	}
	if len(pmap) < 16 {
		_ = mmapFree(pmap)
		s.close()
		return nil, fmt.Errorf("seal: payload.dat too short (%d bytes, need >=16) in %s", len(pmap), segDir)
	}
	if string(pmap[0:4]) != string(magicPayload[:]) {
		_ = mmapFree(pmap)
		s.close()
		return nil, fmt.Errorf("seal: bad payload magic in %s", segDir)
	}
	plCount := binary.LittleEndian.Uint64(pmap[8:16])
	if plCount != nRows {
		_ = mmapFree(pmap)
		s.close()
		return nil, fmt.Errorf("seal: payload.dat count %d != vectors count %d in %s", plCount, nRows, segDir)
	}
	// The lengths array (n uint32) must fit after the header before we prefix-sum.
	lensNeed := uint64(segPageSize) + nRows*4
	if lensNeed < uint64(segPageSize) || uint64(len(pmap)) < lensNeed {
		_ = mmapFree(pmap)
		s.close()
		return nil, fmt.Errorf("seal: payload.dat truncated lens in %s (size=%d need=%d)", segDir, len(pmap), lensNeed)
	}
	s.plMap = pmap
	s.plLens = make([]uint32, s.n)
	s.plOffsets = make([]int, s.n)
	var off uint64
	for i := 0; i < s.n; i++ {
		l := binary.LittleEndian.Uint32(pmap[segPageSize+i*4:])
		s.plLens[i] = l
		s.plOffsets[i] = int(off)
		off += uint64(l)
	}
	s.plBase = segPageSize + s.n*4
	// The concatenated payload bytes must actually be present: data region must
	// hold sum(lens) bytes after the lens array, else payload() panics on a short
	// or torn file.
	plNeed := uint64(s.plBase) + off
	if plNeed < uint64(s.plBase) || uint64(len(pmap)) < plNeed {
		s.close()
		return nil, fmt.Errorf("seal: payload.dat truncated bytes in %s (size=%d need=%d)", segDir, len(pmap), plNeed)
	}

	// Build the derived docId→slot index over LIVE slots (architecture §4.6;
	// appendix #6/#17/#20/#24 — keeps slotOfDoc/Get/Delete and the Search
	// tombstone post-filter O(1) instead of an O(n) scan). Done last so tombGet
	// (which reads the now-mapped tomb bitmap) is valid.
	s.docToSlot = make(map[int64]int, s.n)
	for slot := 0; slot < s.n; slot++ {
		if !s.tombGet(slot) {
			s.docToSlot[s.slotDocs[slot]] = slot
		}
	}
	return s, nil
}

func fileSize(f osFile) (int64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// readWholeFile reads an entire file into memory (used for the small slotdoc.dat).
func readWholeFile(path string) ([]byte, error) {
	f, err := fsOpen(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sz, err := fileSize(f)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, sz)
	if _, err := f.ReadAt(buf, 0); err != nil {
		return nil, err
	}
	return buf, nil
}
