package fstcjk

import (
	"fmt"
	"unicode/utf8"
)

// noneAddrVellum mirrors vellum's internal `noneAddr = 1` sentinel returned by
// FST.Accept when there is no transition for a byte. vellum does not export it;
// the value is stable across v1.0.x (builder.go: const noneAddr = 1) and is a
// reserved sentinel distinct from any real state address. A sanity test asserts
// that a known-absent byte from Start() yields exactly this value.
const noneAddrVellum = 1

// encodeRune writes the UTF-8 bytes of r into buf and returns the count.
func encodeRune(buf []byte, r rune) int {
	return utf8.EncodeRune(buf, r)
}

// fmtSscan is a thin wrapper so segmenter.go can parse the totalFreq sidecar
// without importing fmt at its top (keeps imports tidy).
func fmtSscan(s string, v *float64) (int, error) {
	return fmt.Sscan(s, v)
}
