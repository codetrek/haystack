package vectorstore

import "sort"

// segAttrIndex is the per-segment, derived attr index (architecture §4.1/§6):
// for each DECLARED Keyword property a value → slot-bitmap map (equality/set);
// for each DECLARED Numeric property an ordered structure (sorted distinct values
// + parallel slot bitmaps) supporting Range by binary-searching the [lo,hi] span
// and OR-ing the spanned bitmaps. It is built by scanning a segment's payloads
// and is fully rebuildable from them (NOT a source of truth) — so merge/compact
// rewrite it with the segment and a missing attr.dat is rebuilt on open.
//
// v1 stores DENSE bitmaps (the extended bitmap type), a deliberate, documented
// deviation from architecture §6's "roaring": the per-query member set is over
// one segment's ≤ maxSegSize dense slots where a flat []uint64 beats roaring on
// the per-candidate traversal gate, and per-value postings cardinality is bounded
// by the segment size. Roaring is deferred (no new module dependency in v1).
type segAttrIndex struct {
	decls   map[string]AttrKind
	keyword map[string]map[Value]*bitmap // prop → value → slot bitmap
	numeric map[string]*numericIndex     // prop → ordered structure
}

// numericIndex is the ordered structure for a Numeric property: sorted distinct
// values + a parallel slot bitmap per value. Range(lo,hi) binary-searches the
// value bounds and ORs the spanned bitmaps.
type numericIndex struct {
	values []float64 // sorted ascending, distinct
	posts  []*bitmap // posts[i] = slots whose value == values[i]
}

// buildSegAttr scans n slots' payloads (via the payloadAt accessor) and builds
// the index for the declared properties only.
func buildSegAttr(decls map[string]AttrKind, n int, payloadAt func(slot int) Payload) *segAttrIndex {
	ai := &segAttrIndex{
		decls:   decls,
		keyword: make(map[string]map[Value]*bitmap),
		numeric: make(map[string]*numericIndex),
	}
	// temp numeric accumulators: prop → value → bitmap
	numAcc := make(map[string]map[float64]*bitmap)
	for prop, kind := range decls {
		if kind == Keyword {
			ai.keyword[prop] = make(map[Value]*bitmap)
		} else {
			numAcc[prop] = make(map[float64]*bitmap)
		}
	}
	for slot := 0; slot < n; slot++ {
		pl := payloadAt(slot)
		for prop, kind := range decls {
			v, ok := pl[prop]
			if !ok {
				continue
			}
			if kind == Keyword {
				m := ai.keyword[prop]
				bm := m[v]
				if bm == nil {
					bm = &bitmap{}
					m[v] = bm
				}
				bm.set(slot)
			} else {
				x, okn := v.numeric()
				if !okn {
					continue
				}
				m := numAcc[prop]
				bm := m[x]
				if bm == nil {
					bm = &bitmap{}
					m[x] = bm
				}
				bm.set(slot)
			}
		}
	}
	for prop, acc := range numAcc {
		ni := &numericIndex{}
		for x := range acc {
			ni.values = append(ni.values, x)
		}
		sort.Float64s(ni.values)
		ni.posts = make([]*bitmap, len(ni.values))
		for i, x := range ni.values {
			ni.posts[i] = acc[x]
		}
		ai.numeric[prop] = ni
	}
	return ai
}

// evalSeg produces S_seg: the bitmap of slots matching pred. Declared leaves use
// the index; a leaf on a NON-declared property (or a residual that the index
// cannot answer) falls back to a payload scan. Returns ok=false only if pred is
// structurally unusable here (it never is for the closed set). n + payloadAt are
// passed so the residual/non-declared scan can read payloads.
func (ai *segAttrIndex) evalSeg(pred Predicate, n int, payloadAt func(slot int) Payload) (*bitmap, bool) {
	switch p := pred.(type) {
	case eqPred:
		return ai.evalEq(p.prop, p.val, n, payloadAt), true
	case inPred:
		out := &bitmap{}
		for _, v := range p.vals {
			out.or(ai.evalEq(p.prop, v, n, payloadAt))
		}
		return out, true
	case rangePred:
		return ai.evalRange(p, n, payloadAt), true
	case andPred:
		var acc *bitmap
		for _, c := range p.preds {
			bm, ok := ai.evalSeg(c, n, payloadAt)
			if !ok {
				return nil, false
			}
			if acc == nil {
				cp := bm.clone()
				acc = &cp
			} else {
				acc.and(bm)
			}
		}
		if acc == nil {
			acc = ai.allBits(n)
		}
		return acc, true
	default:
		return nil, false
	}
}

func (ai *segAttrIndex) evalEq(prop string, v Value, n int, payloadAt func(slot int) Payload) *bitmap {
	if m, ok := ai.keyword[prop]; ok {
		if bm, ok := m[v]; ok {
			cp := bm.clone()
			return &cp
		}
		return &bitmap{}
	}
	if ni, ok := ai.numeric[prop]; ok {
		if x, okn := v.numeric(); okn {
			i := sort.SearchFloat64s(ni.values, x)
			if i < len(ni.values) && ni.values[i] == x {
				cp := ni.posts[i].clone()
				return &cp
			}
		}
		return &bitmap{}
	}
	return ai.residualScan(func(pl Payload) bool { return eqPred{prop, v}.evalPayload(pl) }, n, payloadAt)
}

func (ai *segAttrIndex) evalRange(p rangePred, n int, payloadAt func(slot int) Payload) *bitmap {
	ni, ok := ai.numeric[p.prop]
	if !ok {
		// non-declared (or Keyword) Numeric range → residual scan.
		return ai.residualScan(func(pl Payload) bool { return p.evalPayload(pl) }, n, payloadAt)
	}
	lo, okl := p.lo.numeric()
	hi, okh := p.hi.numeric()
	out := &bitmap{}
	if !okl || !okh {
		return out
	}
	i := sort.SearchFloat64s(ni.values, lo) // first value >= lo
	for ; i < len(ni.values) && ni.values[i] <= hi; i++ {
		out.or(ni.posts[i])
	}
	return out
}

func (ai *segAttrIndex) residualScan(match func(Payload) bool, n int, payloadAt func(slot int) Payload) *bitmap {
	out := &bitmap{}
	for slot := 0; slot < n; slot++ {
		if match(payloadAt(slot)) {
			out.set(slot)
		}
	}
	return out
}

func (ai *segAttrIndex) allBits(n int) *bitmap {
	out := &bitmap{}
	for slot := 0; slot < n; slot++ {
		out.set(slot)
	}
	return out
}

// headAttr is the head segment's in-memory attr index over declared fields,
// maintained incrementally by segment.append (the head is mutable; sealed
// segments get the immutable segAttrIndex instead). It is rebuilt from scratch
// when CreateAttrIndex declares a new field over an existing head.
//
// The head filter path keeps a brute evalPayload over s.seg.payloads as the
// correctness floor; headAttr is the fast path consulted when a field is
// declared, so a missing/partial headAttr is never wrong (architecture §6).
type headAttr struct {
	decls   map[string]AttrKind
	keyword map[string]map[Value]*bitmap
	numeric map[string]map[float64]*bitmap
}

func newHeadAttr(decls map[string]AttrKind) *headAttr {
	ha := &headAttr{decls: decls, keyword: map[string]map[Value]*bitmap{}, numeric: map[string]map[float64]*bitmap{}}
	for prop, kind := range decls {
		if kind == Keyword {
			ha.keyword[prop] = map[Value]*bitmap{}
		} else {
			ha.numeric[prop] = map[float64]*bitmap{}
		}
	}
	return ha
}

func (ha *headAttr) index(slot int, pl Payload) {
	for prop, kind := range ha.decls {
		v, ok := pl[prop]
		if !ok {
			continue
		}
		if kind == Keyword {
			m := ha.keyword[prop]
			bm := m[v]
			if bm == nil {
				bm = &bitmap{}
				m[v] = bm
			}
			bm.set(slot)
		} else if x, okn := v.numeric(); okn {
			m := ha.numeric[prop]
			bm := m[x]
			if bm == nil {
				bm = &bitmap{}
				m[x] = bm
			}
			bm.set(slot)
		}
	}
}

func (ha *headAttr) eq(prop string, v Value) *bitmap {
	if m, ok := ha.keyword[prop]; ok {
		if bm, ok := m[v]; ok {
			cp := bm.clone()
			return &cp
		}
	}
	return &bitmap{}
}
