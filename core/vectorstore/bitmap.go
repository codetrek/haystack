package vectorstore

import "math/bits"

// bitmap is a growable packed bitset used for per-slot tombstones. Bit i lives
// in word i/64 at offset i%64; get on an out-of-range bit reports false (clear).
type bitmap struct {
	words []uint64
}

func (b *bitmap) set(i int) {
	w := i >> 6
	for w >= len(b.words) {
		b.words = append(b.words, 0)
	}
	b.words[w] |= 1 << uint(i&63)
}

func (b *bitmap) get(i int) bool {
	w := i >> 6
	if w >= len(b.words) {
		return false
	}
	return b.words[w]&(1<<uint(i&63)) != 0
}

func (b *bitmap) count() int {
	n := 0
	for _, w := range b.words {
		n += bits.OnesCount64(w)
	}
	return n
}

// and intersects b into the receiver in place (receiver ← receiver ∩ o).
func (b *bitmap) and(o *bitmap) {
	for w := range b.words {
		if w < len(o.words) {
			b.words[w] &= o.words[w]
		} else {
			b.words[w] = 0
		}
	}
}

// andNotWords clears every bit set in the raw tomb word array (receiver ←
// receiver ∧ ¬tomb). tomb is the dense uint64[] tombstone words of a sealed
// segment; word w covers slots [w*64, w*64+64). This is the "member AND live"
// composition (architecture §6: member 位图 AND 段 live 位).
func (b *bitmap) andNotWords(tomb []uint64) {
	for w := range b.words {
		if w < len(tomb) {
			b.words[w] &^= tomb[w]
		}
	}
}

// iterate calls fn for every set bit in ascending order.
func (b *bitmap) iterate(fn func(i int)) {
	for w, word := range b.words {
		for word != 0 {
			t := word & -word
			i := w*64 + bits.TrailingZeros64(word)
			fn(i)
			word ^= t
		}
	}
}

// collect returns all set bits ascending (test/helper use).
func (b *bitmap) collect() []int {
	var out []int
	b.iterate(func(i int) { out = append(out, i) })
	return out
}

// clone returns a deep copy.
func (b *bitmap) clone() bitmap {
	cp := make([]uint64, len(b.words))
	copy(cp, b.words)
	return bitmap{words: cp}
}
