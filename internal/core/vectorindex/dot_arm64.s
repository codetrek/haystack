//go:build arm64

#include "textflag.h"

// func dotNEONPartial(a, b *float32, n int, out *float32)
// n must be a multiple of 4. Accumulates a[i]*b[i] into 4 lanes and stores
// the 4 partial sums to out[0..3]; the Go caller folds them and does the tail.
TEXT ·dotNEONPartial(SB), NOSPLIT, $0-32
	MOVD a+0(FP), R0
	MOVD b+8(FP), R1
	MOVD n+16(FP), R2
	MOVD out+24(FP), R3

	VEOR V0.B16, V0.B16, V0.B16 // acc = 0

loop:
	CBZ  R2, done
	VLD1.P 16(R0), [V1.S4] // load 4 floats from a, advance
	VLD1.P 16(R1), [V2.S4] // load 4 floats from b, advance
	VFMLA  V2.S4, V1.S4, V0.S4 // acc += a*b (lane-wise FMA)
	SUB    $4, R2
	B      loop

done:
	VST1 [V0.S4], (R3)
	RET
