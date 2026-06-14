//go:build arm64

#include "textflag.h"

// func dotNEONPartial(a, b *float32, n int, out *float32)
// n must be a multiple of 4. Accumulates a[i]*b[i] using FOUR independent NEON
// accumulators (V0..V3), folds them to 4 partial sums, and stores those to
// out[0..3]; the Go caller folds the 4 and does the scalar tail.
//
// Four accumulators (vs one) break the VFMLA dependency chain: a single
// accumulator serializes on FMLA latency (~1/4 of FMA throughput on Apple
// Silicon). Measured on Apple M1, four accumulators are 1.8×–3.6× faster than
// the single-accumulator loop, the gain growing with dim (64→1536). Moving the
// fold/tail into asm as well measured at +0–4% (noise), so it stays in Go.
TEXT ·dotNEONPartial(SB), NOSPLIT, $0-32
	MOVD a+0(FP), R0
	MOVD b+8(FP), R1
	MOVD n+16(FP), R2
	MOVD out+24(FP), R3

	VEOR V0.B16, V0.B16, V0.B16
	VEOR V1.B16, V1.B16, V1.B16
	VEOR V2.B16, V2.B16, V2.B16
	VEOR V3.B16, V3.B16, V3.B16

	CMP $16, R2
	BLT cleanup4

	// Main loop: 16 floats/iter into 4 independent accumulators.
main16:
	VLD1.P 64(R0), [V16.S4, V17.S4, V18.S4, V19.S4]
	VLD1.P 64(R1), [V20.S4, V21.S4, V22.S4, V23.S4]
	VFMLA  V20.S4, V16.S4, V0.S4
	VFMLA  V21.S4, V17.S4, V1.S4
	VFMLA  V22.S4, V18.S4, V2.S4
	VFMLA  V23.S4, V19.S4, V3.S4
	SUB    $16, R2
	CMP    $16, R2
	BGE    main16

	// Cleanup: remaining multiples of 4 (caller guarantees n%4==0) into V0.
cleanup4:
	CBZ R2, fold

loop4:
	VLD1.P 16(R0), [V16.S4]
	VLD1.P 16(R1), [V20.S4]
	VFMLA  V20.S4, V16.S4, V0.S4
	SUB    $4, R2
	CBNZ   R2, loop4

	// V0 += V1 + V2 + V3 via FMLA against a ones-vector (×1.0 is exact), then
	// store the 4 lane-wise partials for the Go caller to fold.
fold:
	MOVD  $0x3F800000, R4
	VDUP  R4, V28.S4
	VFMLA V28.S4, V1.S4, V0.S4
	VFMLA V28.S4, V2.S4, V0.S4
	VFMLA V28.S4, V3.S4, V0.S4
	VST1  [V0.S4], (R3)
	RET
