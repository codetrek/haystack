//go:build arm64 && dotbench

package vectorindex

// Benchmark-only wrappers around the multi-accumulator NEON kernels in
// dot_variants_arm64.s. Compiled only under -tags=dotbench.

//go:noescape
func dotNEON4Acc(a, b *float32, n int, out *float32)

//go:noescape
func dotNEON4AccPure(a, b *float32, n int) float32

// dot4AccGoTail uses the 4-accumulator kernel but folds the 4 partial sums and
// runs the scalar tail in Go — identical Go-side work to the shipped dot(), so
// a delta vs dot() isolates the accumulator count alone.
func dot4AccGoTail(a, b []float32) float32 {
	n := len(a)
	nn := n &^ 3
	var sum float32
	if nn > 0 {
		var partials [4]float32
		dotNEON4Acc(&a[0], &b[0], nn, &partials[0])
		sum = partials[0] + partials[1] + partials[2] + partials[3]
	}
	for i := nn; i < n; i++ {
		sum += a[i] * b[i]
	}
	return sum
}

// dot4AccPure does everything (kernel, fold, scalar tail, reduction) in asm and
// returns the scalar — no Go-side fold. A delta vs dot4AccGoTail isolates the
// value (if any) of moving the fold/tail into assembly.
func dot4AccPure(a, b []float32) float32 {
	if len(a) == 0 {
		return 0
	}
	return dotNEON4AccPure(&a[0], &b[0], len(a))
}
