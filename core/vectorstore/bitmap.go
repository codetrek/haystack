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
