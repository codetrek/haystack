package vectorstore

import (
	"encoding/binary"
	"fmt"
	"math"
)

// graphFileName is the per-index graph file within a shared segment dir. Each
// named index gets its own file (graph-<name>.dat) so N indexes coexist in one
// seg dir and DropVectorIndex deletes only one index's files (architecture §4.7).
func graphFileName(name string) string { return "graph-" + name + ".dat" }

// graphMetaRecSize is the fixed size of a META record: level(int32) + slot(int32)
// + docId(int64). 16 is a multiple of 8 so the META section stays 8-aligned.
const graphMetaRecSize = 16

// writeGraphFile serializes a built segGraphStore to segDir/graph-<name>.dat in the
// flat/CSR VSG2 layout (see graphHeader) and fsyncs it, then fsyncs segDir. The
// graph is immutable after this (built once). The store's mutable build-time
// representation (neighbors []map[int][]uint32) is the source; the file is the flat
// form the read path (openGraphFile) decodes into compact heap CSR arrays.
func writeGraphFile(segDir, name string, g *segGraphStore) error {
	n := len(g.nodeSlot)

	// Pass 1: per-node layer counts → NODE_BASE prefix sums. A live node (its
	// neighbor map is non-nil) owns level+1 layer slots; a tombstone owns 0.
	nodeBase := make([]uint32, n+1)
	var layerSlots uint64
	for id := 0; id < n; id++ {
		lc := 0
		if g.neighbors[id] != nil {
			lc = g.levels[id] + 1
		}
		layerSlots += uint64(lc)
		if layerSlots > math.MaxUint32 {
			return fmt.Errorf("graphfile: layer-slot count %d exceeds uint32 range in %s", layerSlots, segDir)
		}
		nodeBase[id+1] = uint32(layerSlots)
	}

	// Pass 2: per-(node,layer) edge counts → LAYER_START prefix sums.
	layerStart := make([]uint32, layerSlots+1)
	ls := 0
	var poolLen uint64
	for id := 0; id < n; id++ {
		if g.neighbors[id] == nil {
			continue
		}
		nLayers := g.levels[id] + 1
		for layer := 0; layer < nLayers; layer++ {
			poolLen += uint64(len(g.neighbors[id][layer]))
			if poolLen > math.MaxUint32 {
				return fmt.Errorf("graphfile: edge count %d exceeds uint32 offset range in %s", poolLen, segDir)
			}
			layerStart[ls+1] = uint32(poolLen)
			ls++
		}
	}

	// Pass 3: fill POOL (node ascending, then layer ascending).
	pool := make([]uint32, poolLen)
	p := 0
	for id := 0; id < n; id++ {
		if g.neighbors[id] == nil {
			continue
		}
		nLayers := g.levels[id] + 1
		for layer := 0; layer < nLayers; layer++ {
			for _, x := range g.neighbors[id][layer] {
				pool[p] = x
				p++
			}
		}
	}

	// Section offsets. META/NODE_BASE/LAYER_START are packed right after the header;
	// POOL is page-aligned so a future change can mmap it in place.
	metaOff := uint64(segPageSize)
	nodeBaseOff := metaOff + uint64(n)*graphMetaRecSize
	layerStartOff := nodeBaseOff + uint64(n+1)*4
	indexEnd := layerStartOff + (layerSlots+1)*4
	poolOff := (indexEnd + segPageSize - 1) &^ (segPageSize - 1)

	hdr := make([]byte, segPageSize)
	copy(hdr[0:4], magicGraph[:])
	binary.LittleEndian.PutUint32(hdr[4:8], graphFormatVersion)
	binary.LittleEndian.PutUint32(hdr[8:12], uint32(defaultMaxLayers))
	if g.hasEntry {
		binary.LittleEndian.PutUint32(hdr[12:16], 1)
		binary.LittleEndian.PutUint64(hdr[24:32], g.entryID)
		binary.LittleEndian.PutUint32(hdr[32:36], uint32(g.maxLayer))
	}
	binary.LittleEndian.PutUint64(hdr[16:24], uint64(n))
	binary.LittleEndian.PutUint64(hdr[40:48], layerSlots)
	binary.LittleEndian.PutUint64(hdr[48:56], poolLen)
	binary.LittleEndian.PutUint64(hdr[56:64], poolOff)

	meta := make([]byte, uint64(n)*graphMetaRecSize)
	for id := 0; id < n; id++ {
		off := id * graphMetaRecSize
		binary.LittleEndian.PutUint32(meta[off:off+4], uint32(int32(g.levels[id])))
		binary.LittleEndian.PutUint32(meta[off+4:off+8], uint32(int32(g.nodeSlot[id])))
		binary.LittleEndian.PutUint64(meta[off+8:off+16], uint64(g.nodeDoc[id]))
	}

	f, err := fsCreate(segFilePath(segDir, graphFileName(name)))
	if err != nil {
		return err
	}
	defer f.Close()

	pad := make([]byte, poolOff-indexEnd)
	for _, chunk := range [][]byte{hdr, meta, u32sToBytes(nodeBase), u32sToBytes(layerStart), pad, u32sToBytes(pool)} {
		if _, err := f.Write(chunk); err != nil {
			return err
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return fsyncDir(segDir)
}

// openGraphFile loads segDir/graph-<name>.dat into a fresh segGraphStore bound to
// seg, reconstructing topology + nodeId↔slot/docId. Vectors are still resolved from
// seg (graph stores no vectors). The returned store is read-only for search: its
// neighbors live in compact heap CSR arrays (csrNodeBase/csrLayerStart/csrPool), not
// the build-time []map. Every section offset and prefix-sum array is bounds- and
// monotonicity-checked before use, so a truncated or corrupt file produces a clean
// error instead of a slice-bounds panic at open or during search.
func openGraphFile(segDir, name string, seg *sealedSegment) (*segGraphStore, error) {
	data, err := readWholeFile(segFilePath(segDir, graphFileName(name)))
	if err != nil {
		return nil, err
	}
	if len(data) < segPageSize {
		return nil, fmt.Errorf("graphfile: file shorter than header in %s (%d bytes)", segDir, len(data))
	}
	if string(data[0:4]) != string(magicGraph[:]) {
		return nil, fmt.Errorf("graphfile: bad magic in %s", segDir)
	}
	if v := binary.LittleEndian.Uint32(data[4:8]); v != graphFormatVersion {
		return nil, fmt.Errorf("graphfile: unsupported version %d in %s", v, segDir)
	}

	nodeCount := binary.LittleEndian.Uint64(data[16:24])
	if nodeCount > uint64(maxSealRows) {
		return nil, fmt.Errorf("graphfile: node count %d exceeds cap %d in %s", nodeCount, maxSealRows, segDir)
	}
	layerSlots := binary.LittleEndian.Uint64(data[40:48])
	poolLen := binary.LittleEndian.Uint64(data[48:56])
	poolOff := binary.LittleEndian.Uint64(data[56:64])
	// Bound the index/pool sizes so the offset arithmetic below cannot overflow
	// uint64 and so a corrupt header cannot demand an absurd allocation.
	if layerSlots > math.MaxUint32 || poolLen > math.MaxUint32 {
		return nil, fmt.Errorf("graphfile: index sizes (%d slots, %d edges) exceed uint32 range in %s", layerSlots, poolLen, segDir)
	}

	dataLen := uint64(len(data))
	metaOff := uint64(segPageSize)
	nodeBaseOff := metaOff + nodeCount*graphMetaRecSize
	layerStartOff := nodeBaseOff + (nodeCount+1)*4
	indexEnd := layerStartOff + (layerSlots+1)*4
	// poolOff is read raw from the header (untrusted). Keep every comparison's
	// arithmetic within file bounds: a near-MaxUint64 poolOff would wrap
	// poolOff+poolLen*4 back below dataLen and slip past the guard into a
	// slice-bounds panic at readU32s below. The short-circuit order guarantees
	// poolOff <= dataLen before the division, so dataLen-poolOff cannot underflow.
	if indexEnd > dataLen || poolOff < indexEnd || poolOff > dataLen || poolLen > (dataLen-poolOff)/4 {
		return nil, fmt.Errorf("graphfile: section layout overruns file in %s (len=%d)", segDir, dataLen)
	}

	n := int(nodeCount)
	g := newSegGraphStore(seg)
	g.levels = make([]int, n)
	g.nodeSlot = make([]int, n)
	g.nodeDoc = make([]int64, n)
	g.nextID = nodeCount
	if binary.LittleEndian.Uint32(data[12:16]) == 1 {
		g.hasEntry = true
		g.entryID = binary.LittleEndian.Uint64(data[24:32])
		g.maxLayer = int(binary.LittleEndian.Uint32(data[32:36]))
	}

	for id := 0; id < n; id++ {
		off := int(metaOff) + id*graphMetaRecSize
		g.levels[id] = int(int32(binary.LittleEndian.Uint32(data[off : off+4])))
		slot := int(int32(binary.LittleEndian.Uint32(data[off+4 : off+8])))
		// A live slot indexes the segment's vectors via GetVectorRef→getVectorRef,
		// which does an unbounded unsafe.Slice into the mmap; reject any slot past the
		// segment's row count (a tombstone's -1 stays < seg.n) so a corrupt META can't
		// drive an OOB read / SIGSEGV at search.
		if slot >= seg.n {
			return nil, fmt.Errorf("graphfile: node %d slot %d out of range (segment has %d rows) in %s", id, slot, seg.n, segDir)
		}
		g.nodeSlot[id] = slot
		doc := int64(binary.LittleEndian.Uint64(data[off+8 : off+16]))
		g.nodeDoc[id] = doc
		if slot >= 0 {
			g.docToNode[doc] = uint64(id)
		}
	}

	nodeBase := make([]uint32, n+1)
	readU32s(data[nodeBaseOff:], nodeBase)
	prev := uint32(0)
	for _, v := range nodeBase {
		if v < prev {
			return nil, fmt.Errorf("graphfile: non-monotonic node base in %s", segDir)
		}
		prev = v
	}
	if uint64(nodeBase[n]) != layerSlots {
		return nil, fmt.Errorf("graphfile: node base tail %d != layer slots %d in %s", nodeBase[n], layerSlots, segDir)
	}

	layerStart := make([]uint32, layerSlots+1)
	readU32s(data[layerStartOff:], layerStart)
	prev = 0
	for _, v := range layerStart {
		if v < prev {
			return nil, fmt.Errorf("graphfile: non-monotonic layer start in %s", segDir)
		}
		prev = v
	}
	if uint64(layerStart[layerSlots]) != poolLen {
		return nil, fmt.Errorf("graphfile: layer-start tail %d != edge count %d in %s", layerStart[layerSlots], poolLen, segDir)
	}

	pool := make([]uint32, poolLen)
	readU32s(data[poolOff:], pool)
	// Neighbor ids index visited.mark and GetVectorRef during search; reject any
	// outside [0, NodeCount) so a corrupt pool can't drive an OOB index or a runaway
	// visited-set allocation at search time.
	for _, v := range pool {
		if uint64(v) >= nodeCount {
			return nil, fmt.Errorf("graphfile: neighbor id %d >= node count %d in %s", v, nodeCount, segDir)
		}
	}

	g.csrNodeBase = nodeBase
	g.csrLayerStart = layerStart
	g.csrPool = pool
	return g, nil
}

// u32sToBytes flattens a slice to LittleEndian bytes for writing.
func u32sToBytes(s []uint32) []byte {
	b := make([]byte, len(s)*4)
	for i, v := range s {
		binary.LittleEndian.PutUint32(b[i*4:], v)
	}
	return b
}

// readU32s decodes dst-many LittleEndian words from b (which the caller
// has already bounds-checked to be long enough).
func readU32s(b []byte, dst []uint32) {
	for i := range dst {
		dst[i] = binary.LittleEndian.Uint32(b[i*4:])
	}
}
