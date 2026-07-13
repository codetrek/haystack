//go:build amd64

#include "textflag.h"

// func dotAVX2FMA(a, b []float32) float32
//
// Multi-accumulator AVX2+FMA dot product. Eight independent ymm accumulators
// (Y0-Y7) keep enough FMAs in flight to saturate Zen 3's two FMA units (4-cycle
// latency → need ≥8 in flight to be throughput- not latency-bound). Main loop does
// 64 floats/iter (b loaded into Y8-Y15, a via the FMA memory operand), then an
// 8-float loop and a scalar tail. Caller guarantees len(a)==len(b) and AVX2+FMA.
TEXT ·dotAVX2FMA(SB), NOSPLIT, $0-52
	MOVQ a_base+0(FP), AX
	MOVQ b_base+24(FP), BX
	MOVQ a_len+8(FP), CX

	VXORPS Y0, Y0, Y0
	VXORPS Y1, Y1, Y1
	VXORPS Y2, Y2, Y2
	VXORPS Y3, Y3, Y3
	VXORPS Y4, Y4, Y4
	VXORPS Y5, Y5, Y5
	VXORPS Y6, Y6, Y6
	VXORPS Y7, Y7, Y7

blk64:
	CMPQ CX, $64
	JL   reduce8
	VMOVUPS 0(BX), Y8
	VFMADD231PS 0(AX), Y8, Y0
	VMOVUPS 32(BX), Y9
	VFMADD231PS 32(AX), Y9, Y1
	VMOVUPS 64(BX), Y10
	VFMADD231PS 64(AX), Y10, Y2
	VMOVUPS 96(BX), Y11
	VFMADD231PS 96(AX), Y11, Y3
	VMOVUPS 128(BX), Y12
	VFMADD231PS 128(AX), Y12, Y4
	VMOVUPS 160(BX), Y13
	VFMADD231PS 160(AX), Y13, Y5
	VMOVUPS 192(BX), Y14
	VFMADD231PS 192(AX), Y14, Y6
	VMOVUPS 224(BX), Y15
	VFMADD231PS 224(AX), Y15, Y7
	ADDQ $256, AX
	ADDQ $256, BX
	SUBQ $64, CX
	JMP  blk64

reduce8:
	VADDPS Y1, Y0, Y0
	VADDPS Y3, Y2, Y2
	VADDPS Y5, Y4, Y4
	VADDPS Y7, Y6, Y6
	VADDPS Y2, Y0, Y0
	VADDPS Y6, Y4, Y4
	VADDPS Y4, Y0, Y0

blk8:
	CMPQ CX, $8
	JL   hsum
	VMOVUPS 0(BX), Y8
	VFMADD231PS 0(AX), Y8, Y0
	ADDQ $32, AX
	ADDQ $32, BX
	SUBQ $8, CX
	JMP  blk8

hsum:
	VEXTRACTF128 $1, Y0, X1
	VADDPS X1, X0, X0
	VHADDPS X0, X0, X0
	VHADDPS X0, X0, X0

tail:
	CMPQ CX, $0
	JLE  done
	VMOVSS 0(AX), X2
	VMOVSS 0(BX), X3
	VMULSS X2, X3, X2
	VADDSS X2, X0, X0
	ADDQ $4, AX
	ADDQ $4, BX
	SUBQ $1, CX
	JMP  tail

done:
	VMOVSS X0, ret+48(FP)
	VZEROUPPER
	RET
