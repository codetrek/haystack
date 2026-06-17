package vectorstore

import "unsafe"

// segPageSize is the page-aligned header reservation for sealed-segment data
// files: each file's fixed header lives in the first segPageSize bytes, data
// follows. A fixed page-size header prefix.
const segPageSize = 4096

// File magic constants for sealed-segment data files (4 bytes each). There is no
// tomb.dat magic since incr 3: the durable tombstone form is the bbolt tomb bucket,
// not a per-segment mmap'd file.
var (
	magicVectors = [4]byte{'V', 'S', 'V', 'C'} // vectors.dat
	magicSlotDoc = [4]byte{'V', 'S', 'S', 'D'} // slotdoc.dat
	magicPayload = [4]byte{'V', 'S', 'P', 'L'} // payload.dat
	magicAttr    = [4]byte{'V', 'S', 'A', 'T'} // attr.dat
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

// payloadHeader is the on-disk header for payload.dat (16 bytes). Data is Count
// uint32 lengths (slot→payload length) followed by the concatenated payload
// bytes; offsets are derived by prefix-sum of the lengths at open time.
type payloadHeader struct {
	Magic [4]byte
	_     [4]byte
	Count uint64
}

var _ [16]byte = [unsafe.Sizeof(payloadHeader{})]byte{}

// attrHeader is the on-disk header for attr.dat (16 bytes). Body is a
// self-describing serialization of the per-property postings (see attrfile.go).
// Count is the row count this index was built over; it must match the segment's
// vector count, else the file is stale and the index is rebuilt from payload.
type attrHeader struct {
	Magic [4]byte
	_     [4]byte
	Count uint64
}

var _ [16]byte = [unsafe.Sizeof(attrHeader{})]byte{}
