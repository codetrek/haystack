//go:build arm64 && dotbench

#include "textflag.h"

// Benchmark-only NEON dot variants used to answer "is a multi-accumulator /
// pure-asm kernel faster than the shipped single-accumulator hybrid?". Built
// only under -tags=dotbench so they never enter normal builds.
//
// The shipped kernel (dot_arm64.s) uses ONE accumulator, so each VFMLA depends
// on the previous one — the loop is latency-bound (~1/4 of FMA throughput on
// Apple Silicon). These use FOUR independent accumulators to break that chain.

// func dotNEON4Acc(a, b *float32, n int, out *float32)
// n must be a multiple of 4. Four-accumulator kernel; folds the accumulators to
// 4 partial sums and stores them to out[0..3]. The Go caller sums + tails —
// identical Go-side work to the shipped dotNEONPartial, isolating the kernel.
TEXT ·dotNEON4Acc(SB), NOSPLIT, $0-32
	MOVD a+0(FP), R0
	MOVD b+8(FP), R1
	MOVD n+16(FP), R2
	MOVD out+24(FP), R3

	VEOR V0.B16, V0.B16, V0.B16
	VEOR V1.B16, V1.B16, V1.B16
	VEOR V2.B16, V2.B16, V2.B16
	VEOR V3.B16, V3.B16, V3.B16

	CMP $16, R2
	BLT cleanup4a

main16a:
	VLD1.P 64(R0), [V16.S4, V17.S4, V18.S4, V19.S4]
	VLD1.P 64(R1), [V20.S4, V21.S4, V22.S4, V23.S4]
	VFMLA  V20.S4, V16.S4, V0.S4
	VFMLA  V21.S4, V17.S4, V1.S4
	VFMLA  V22.S4, V18.S4, V2.S4
	VFMLA  V23.S4, V19.S4, V3.S4
	SUB    $16, R2
	CMP    $16, R2
	BGE    main16a

cleanup4a:
	CBZ R2, folda

loop4a:
	VLD1.P 16(R0), [V16.S4]
	VLD1.P 16(R1), [V20.S4]
	VFMLA  V20.S4, V16.S4, V0.S4
	SUB    $4, R2
	CBNZ   R2, loop4a

folda:
	// V0 += V1 + V2 + V3 via FMLA against a ones-vector (mul by 1.0 is exact).
	MOVD  $0x3F800000, R4
	VDUP  R4, V28.S4
	VFMLA V28.S4, V1.S4, V0.S4
	VFMLA V28.S4, V2.S4, V0.S4
	VFMLA V28.S4, V3.S4, V0.S4
	VST1  [V0.S4], (R3)
	RET

// func dotNEON4AccPure(a, b *float32, n int) float32
// Full length n (any value). Four-accumulator kernel + a 4-wide cleanup loop +
// a scalar tail, with the horizontal reduction done in-asm. Returns the scalar
// directly: nothing folds in Go. This is the "pure asm" end of the comparison.
TEXT ·dotNEON4AccPure(SB), NOSPLIT, $0-28
	MOVD a+0(FP), R0
	MOVD b+8(FP), R1
	MOVD n+16(FP), R2

	VEOR V0.B16, V0.B16, V0.B16
	VEOR V1.B16, V1.B16, V1.B16
	VEOR V2.B16, V2.B16, V2.B16
	VEOR V3.B16, V3.B16, V3.B16

	CMP $16, R2
	BLT cleanup4p

main16p:
	VLD1.P 64(R0), [V16.S4, V17.S4, V18.S4, V19.S4]
	VLD1.P 64(R1), [V20.S4, V21.S4, V22.S4, V23.S4]
	VFMLA  V20.S4, V16.S4, V0.S4
	VFMLA  V21.S4, V17.S4, V1.S4
	VFMLA  V22.S4, V18.S4, V2.S4
	VFMLA  V23.S4, V19.S4, V3.S4
	SUB    $16, R2
	CMP    $16, R2
	BGE    main16p

cleanup4p:
	CMP $4, R2
	BLT reducep

loop4p:
	VLD1.P 16(R0), [V16.S4]
	VLD1.P 16(R1), [V20.S4]
	VFMLA  V20.S4, V16.S4, V0.S4
	SUB    $4, R2
	CMP    $4, R2
	BGE    loop4p

reducep:
	// Fold the four accumulators into V0 (V0 += V1 + V2 + V3).
	MOVD  $0x3F800000, R4
	VDUP  R4, V28.S4
	VFMLA V28.S4, V1.S4, V0.S4
	VFMLA V28.S4, V2.S4, V0.S4
	VFMLA V28.S4, V3.S4, V0.S4

	// Horizontal sum of V0.S4 into F0 via lane extracts + scalar adds (FP-safe,
	// avoids the ambiguous VADDP). F0 already aliases V0 lane 0.
	VMOV  V0.S[1], V4
	VMOV  V0.S[2], V5
	VMOV  V0.S[3], V6
	FADDS F4, F0, F0
	FADDS F6, F5, F5
	FADDS F5, F0, F0

	// Scalar tail: 0..3 leftover elements.
	CBZ R2, donep

tailp:
	FMOVS (R0), F10
	FMOVS (R1), F11
	FMULS F11, F10, F10
	FADDS F10, F0, F0
	ADD   $4, R0
	ADD   $4, R1
	SUB   $1, R2
	CBNZ  R2, tailp

donep:
	FMOVS F0, ret+24(FP)
	RET
