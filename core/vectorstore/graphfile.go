package vectorstore

import (
	"encoding/binary"
	"fmt"
)

// writeGraphFile serializes a built segGraphStore to segDir/graph.dat and fsyncs
// it, then fsyncs segDir. The graph is immutable after this (built once).
func writeGraphFile(segDir string, g *segGraphStore) error {
	f, err := fsCreate(segFilePath(segDir, "graph.dat"))
	if err != nil {
		return err
	}
	defer f.Close()

	hdr := make([]byte, segPageSize)
	copy(hdr[0:4], magicGraph[:])
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(defaultMaxLayers))
	binary.LittleEndian.PutUint64(hdr[8:16], uint64(len(g.nodeSlot)))
	if g.hasEntry {
		binary.LittleEndian.PutUint32(hdr[16:20], 1)
		binary.LittleEndian.PutUint32(hdr[20:24], uint32(g.maxLayer))
		binary.LittleEndian.PutUint64(hdr[24:32], g.entryID)
	}
	if _, err := f.Write(hdr); err != nil {
		return err
	}

	for id := 0; id < len(g.nodeSlot); id++ {
		var rec []byte
		rec = appendI32(rec, int32(g.levels[id]))
		rec = appendI32(rec, int32(g.nodeSlot[id]))
		rec = appendU64(rec, uint64(g.nodeDoc[id]))
		layers := g.neighbors[id]
		nLayers := 0
		if layers != nil {
			nLayers = g.levels[id] + 1
		}
		rec = appendI32(rec, int32(nLayers))
		for layer := 0; layer < nLayers; layer++ {
			nb := layers[layer]
			rec = appendI32(rec, int32(len(nb)))
			for _, x := range nb {
				rec = appendU64(rec, x)
			}
		}
		if _, err := f.Write(rec); err != nil {
			return err
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return fsyncDir(segDir)
}

// graphReader is a bounds-checked cursor over graph.dat's node table. Every read
// validates that the requested bytes are within the buffer, so a truncated or
// corrupt file produces a clean error instead of a slice-bounds panic
// (adversarial-review appendix #9(3): openGraphFile must not index past EOF).
type graphReader struct {
	data []byte
	off  int
}

func (r *graphReader) i32() (int32, error) {
	if r.off+4 > len(r.data) {
		return 0, fmt.Errorf("graphfile: truncated node table at offset %d (need 4 bytes, have %d)", r.off, len(r.data)-r.off)
	}
	v := int32(binary.LittleEndian.Uint32(r.data[r.off:]))
	r.off += 4
	return v, nil
}

func (r *graphReader) u64() (uint64, error) {
	if r.off+8 > len(r.data) {
		return 0, fmt.Errorf("graphfile: truncated node table at offset %d (need 8 bytes, have %d)", r.off, len(r.data)-r.off)
	}
	v := binary.LittleEndian.Uint64(r.data[r.off:])
	r.off += 8
	return v, nil
}

// openGraphFile loads segDir/graph.dat into a fresh segGraphStore bound to seg,
// reconstructing topology + nodeId↔slot/docId. Vectors are still resolved from
// seg (graph stores no vectors). The returned store is read-only for search.
func openGraphFile(segDir string, seg *sealedSegment) (*segGraphStore, error) {
	data, err := readWholeFile(segFilePath(segDir, "graph.dat"))
	if err != nil {
		return nil, err
	}
	// The header occupies the first segPageSize bytes; reject anything shorter
	// before reading any field (appendix #9(3)).
	if len(data) < segPageSize {
		return nil, fmt.Errorf("graphfile: file shorter than header in %s (%d bytes)", segDir, len(data))
	}
	if string(data[0:4]) != string(magicGraph[:]) {
		return nil, fmt.Errorf("graphfile: bad magic in %s", segDir)
	}
	nodeCount := int(binary.LittleEndian.Uint64(data[8:16]))
	if nodeCount < 0 {
		return nil, fmt.Errorf("graphfile: invalid node count %d in %s", nodeCount, segDir)
	}
	g := newSegGraphStore(seg)
	g.levels = make([]int, nodeCount)
	g.neighbors = make([]map[int][]uint64, nodeCount)
	g.nodeSlot = make([]int, nodeCount)
	g.nodeDoc = make([]int64, nodeCount)
	g.nextID = uint64(nodeCount)
	if binary.LittleEndian.Uint32(data[16:20]) == 1 {
		g.hasEntry = true
		g.maxLayer = int(binary.LittleEndian.Uint32(data[20:24]))
		g.entryID = binary.LittleEndian.Uint64(data[24:32])
	}

	r := &graphReader{data: data, off: segPageSize}
	for id := 0; id < nodeCount; id++ {
		level, err := r.i32()
		if err != nil {
			return nil, err
		}
		slot, err := r.i32()
		if err != nil {
			return nil, err
		}
		docIdRaw, err := r.u64()
		if err != nil {
			return nil, err
		}
		nLayers, err := r.i32()
		if err != nil {
			return nil, err
		}
		if nLayers < 0 {
			return nil, fmt.Errorf("graphfile: negative layer count %d for node %d in %s", nLayers, id, segDir)
		}
		g.levels[id] = int(level)
		g.nodeSlot[id] = int(slot)
		g.nodeDoc[id] = int64(docIdRaw)
		if int(slot) >= 0 {
			g.docToNode[int64(docIdRaw)] = uint64(id)
		}
		if nLayers > 0 {
			m := make(map[int][]uint64, nLayers)
			for layer := 0; layer < int(nLayers); layer++ {
				cnt, err := r.i32()
				if err != nil {
					return nil, err
				}
				if cnt < 0 {
					return nil, fmt.Errorf("graphfile: negative neighbor count %d for node %d layer %d in %s", cnt, id, layer, segDir)
				}
				// Bounds-check the whole neighbor block before allocating, so a
				// corrupt cnt cannot over-allocate or read past EOF.
				if r.off+int(cnt)*8 > len(data) {
					return nil, fmt.Errorf("graphfile: neighbor block for node %d layer %d overruns file in %s", id, layer, segDir)
				}
				nb := make([]uint64, cnt)
				for j := 0; j < int(cnt); j++ {
					nb[j], _ = r.u64()
				}
				m[layer] = nb
			}
			g.neighbors[id] = m
		}
	}
	return g, nil
}

func appendI32(b []byte, v int32) []byte {
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], uint32(v))
	return append(b, tmp[:]...)
}
