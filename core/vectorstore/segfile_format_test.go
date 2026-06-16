package vectorstore

import (
	"testing"
	"unsafe"
)

func TestSegFileFormat_Sizes(t *testing.T) {
	if got := unsafe.Sizeof(vectorsHeader{}); got != 24 {
		t.Fatalf("vectorsHeader size = %d, want 24", got)
	}
	if got := unsafe.Sizeof(slotDocHeader{}); got != 16 {
		t.Fatalf("slotDocHeader size = %d, want 16", got)
	}
	if got := unsafe.Sizeof(tombHeader{}); got != 16 {
		t.Fatalf("tombHeader size = %d, want 16", got)
	}
	if got := unsafe.Sizeof(payloadHeader{}); got != 16 {
		t.Fatalf("payloadHeader size = %d, want 16", got)
	}
	if segPageSize != 4096 {
		t.Fatalf("segPageSize = %d, want 4096", segPageSize)
	}
}
