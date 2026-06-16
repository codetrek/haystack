package vectorstore

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

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
	if string(vmap[0:4]) != string(magicVectors[:]) {
		_ = mmapFree(vmap)
		return nil, fmt.Errorf("seal: bad vectors magic in %s", segDir)
	}
	s.vecMap = vmap
	s.dim = int(binary.LittleEndian.Uint32(vmap[4:8]))
	s.n = int(binary.LittleEndian.Uint64(vmap[8:16]))
	s.rowF32 = s.dim + 1
	s.vecBase = segPageSize

	// slotdoc.dat (decode into a slice; it is small and queried per-result)
	sd, err := readWholeFile(segFilePath(segDir, "slotdoc.dat"))
	if err != nil {
		s.close()
		return nil, err
	}
	if string(sd[0:4]) != string(magicSlotDoc[:]) {
		s.close()
		return nil, fmt.Errorf("seal: bad slotdoc magic in %s", segDir)
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
	if string(tmap[0:4]) != string(magicTomb[:]) {
		_ = mmapFree(tmap)
		s.close()
		return nil, fmt.Errorf("seal: bad tomb magic in %s", segDir)
	}
	s.tombMap = tmap
	s.tombWords = int(binary.LittleEndian.Uint64(tmap[8:16]))

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
	if string(pmap[0:4]) != string(magicPayload[:]) {
		_ = mmapFree(pmap)
		s.close()
		return nil, fmt.Errorf("seal: bad payload magic in %s", segDir)
	}
	s.plMap = pmap
	s.plLens = make([]uint32, s.n)
	s.plOffsets = make([]int, s.n)
	off := 0
	for i := 0; i < s.n; i++ {
		l := binary.LittleEndian.Uint32(pmap[segPageSize+i*4:])
		s.plLens[i] = l
		s.plOffsets[i] = off
		off += int(l)
	}
	s.plBase = segPageSize + s.n*4
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
