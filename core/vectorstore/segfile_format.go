package vectorstore

import "unsafe"

// segPageSize is the page-aligned header reservation for sealed-segment data
// files: each file's fixed header lives in the first segPageSize bytes, data
// follows. Mirrors vectorindex's pageSize convention.
const segPageSize = 4096

// File magic constants for sealed-segment data files (4 bytes each).
var (
	magicVectors = [4]byte{'V', 'S', 'V', 'C'} // vectors.dat
	magicSlotDoc = [4]byte{'V', 'S', 'S', 'D'} // slotdoc.dat
	magicTomb    = [4]byte{'V', 'S', 'T', 'B'} // tomb.dat
	magicPayload = [4]byte{'V', 'S', 'P', 'L'} // payload.dat
)

// vectorsHeader is the on-disk header for vectors.dat (24 bytes). After the
// segPageSize header region, data is Count rows of (Dim float32 + 1 float32
// norm) = (Dim+1)*4 bytes each, in metric-natural stored form.
type vectorsHeader struct {
	Magic [4]byte
	Dim   uint32
	Count uint64
	_     uint64 // reserved; keeps header 8-byte aligned and stable-offset
}

var _ [24]byte = [unsafe.Sizeof(vectorsHeader{})]byte{}

// slotDocHeader is the on-disk header for slotdoc.dat (16 bytes). Data is Count
// int64 docIds (slot→docId, the on-disk source of truth).
type slotDocHeader struct {
	Magic [4]byte
	_     [4]byte
	Count uint64
}

var _ [16]byte = [unsafe.Sizeof(slotDocHeader{})]byte{}

// tombHeader is the on-disk header for tomb.dat (16 bytes). Data is Words uint64
// words of the tombstone bitmap (the ONLY mutable file in a sealed segment).
type tombHeader struct {
	Magic [4]byte
	_     [4]byte
	Words uint64
}

var _ [16]byte = [unsafe.Sizeof(tombHeader{})]byte{}

// payloadHeader is the on-disk header for payload.dat (16 bytes). Data is Count
// uint32 lengths (slot→payload length) followed by the concatenated payload
// bytes; offsets are derived by prefix-sum of the lengths at open time.
type payloadHeader struct {
	Magic [4]byte
	_     [4]byte
	Count uint64
}

var _ [16]byte = [unsafe.Sizeof(payloadHeader{})]byte{}
