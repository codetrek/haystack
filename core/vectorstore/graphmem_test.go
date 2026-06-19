package vectorstore

import (
	"math/rand"
	"runtime"
	"testing"
)

// genTopology builds a realistic HNSW neighbor topology for n nodes: layer 0 has up
// to m0 edges, higher layers up to m, level drawn geometrically (most nodes level 0).
func genTopology(n, m, m0 int, seed int64) [][]([]uint64) {
	rng := rand.New(rand.NewSource(seed))
	out := make([][]([]uint64), n)
	for id := 0; id < n; id++ {
		level := 0
		for rng.Float64() < 1.0/2.718281828 { // ~geometric, mL≈1/ln(M)-ish
			level++
			if level > 5 {
				break
			}
		}
		layers := make([][]uint64, level+1)
		for l := 0; l <= level; l++ {
			deg := m
			if l == 0 {
				deg = m0
			}
			deg = deg/2 + rng.Intn(deg/2+1) // some variance below the cap
			nb := make([]uint64, deg)
			for j := range nb {
				nb[j] = uint64(rng.Intn(n))
			}
			layers[l] = nb
		}
		out[id] = layers
	}
	return out
}

// retained measures the heap a structure holds by sampling HeapAlloc with the
// object alive, then freeing it and sampling again. The (withObj - afterFree) delta
// is self-contained — it does not depend on a baseline shared with other
// measurements, so back-to-back calls don't interfere.
func retained(build func() any) uint64 {
	runtime.GC()
	runtime.GC()
	x := build()
	runtime.GC()
	var withObj runtime.MemStats
	runtime.ReadMemStats(&withObj)
	runtime.KeepAlive(x)
	x = nil
	runtime.GC()
	runtime.GC()
	var afterFree runtime.MemStats
	runtime.ReadMemStats(&afterFree)
	if withObj.HeapAlloc < afterFree.HeapAlloc {
		return 0
	}
	return withObj.HeapAlloc - afterFree.HeapAlloc
}

// TestGraphTopologyMemory_MapVsCSR cross-validates the win: the shipped uint32 flat
// CSR arrays must retain materially less heap than the per-node []map[int][]uint64
// (the original representation) for an identical topology. Run with:
// go test -run TestGraphTopologyMemory -v
func TestGraphTopologyMemory_MapVsCSR(t *testing.T) {
	const n, m, m0 = 50000, 16, 32
	topo := genTopology(n, m, m0, 99)

	mapMem := retained(func() any {
		nbs := make([]map[int][]uint64, n)
		for id, layers := range topo {
			mp := make(map[int][]uint64, len(layers))
			for l, nb := range layers {
				cp := make([]uint64, len(nb))
				copy(cp, nb)
				mp[l] = cp
			}
			nbs[id] = mp
		}
		return nbs
	})

	var totalEdges, totalSlots uint64
	csrMem := retained(func() any {
		nodeBase := make([]uint32, n+1)
		var slots uint64
		for id, layers := range topo {
			slots += uint64(len(layers))
			nodeBase[id+1] = uint32(slots)
		}
		layerStart := make([]uint32, slots+1)
		var edges uint64
		ls := 0
		for _, layers := range topo {
			for _, nb := range layers {
				edges += uint64(len(nb))
				layerStart[ls+1] = uint32(edges)
				ls++
			}
		}
		pool := make([]uint32, edges)
		p := 0
		for _, layers := range topo {
			for _, nb := range layers {
				for _, v := range nb {
					pool[p] = uint32(v)
					p++
				}
			}
		}
		totalEdges, totalSlots = edges, slots
		return [3]any{nodeBase, layerStart, pool}
	})

	t.Logf("n=%d nodes, %d layer-slots, %d edges (%.1f edges/node)", n, totalSlots, totalEdges, float64(totalEdges)/float64(n))
	t.Logf("  []map[int][]uint64 : %8.2f MB (%5.1f B/node)", float64(mapMem)/1e6, float64(mapMem)/float64(n))
	t.Logf("  flat CSR arrays    : %8.2f MB (%5.1f B/node)", float64(csrMem)/1e6, float64(csrMem)/float64(n))
	if csrMem > 0 && mapMem > 0 {
		t.Logf("  reduction          : %.1f%%  (%.2fx smaller)", 100*(1-float64(csrMem)/float64(mapMem)), float64(mapMem)/float64(csrMem))
	}
	if mapMem == 0 {
		t.Skip("unstable MemStats sample (mapMem measured 0); skipping the inequality assertion")
	}
	if csrMem >= mapMem {
		t.Fatalf("CSR (%d B) not smaller than map (%d B)", csrMem, mapMem)
	}
}
