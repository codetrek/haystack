package invertedstore

import (
	"sort"
	"testing"
)

func TestKeyEncoding(t *testing.T) {
	// [I] keyType(1) tableId(4 BE) keyword
	ik := invertedKey(7, "return")
	if ik[0] != ktInverted || len(ik) != 5+len("return") {
		t.Fatalf("inverted key shape: % x", ik)
	}
	// [F] keyType(1) tableId(4 BE) docid(8 BE int64); [I] (0x01) must sort before [F] (0x02)
	fk := forwardKey(7, 1<<40) // a docid > 2^31 to prove int64 width
	if fk[0] != ktForward || len(fk) != 13 {
		t.Fatalf("forward key shape: % x", fk)
	}
	if string(invertedKey(7, "")) >= string(fk) {
		t.Fatal("[I] must sort before [F]")
	}
	// tableId is fixed-width so 2 vs 10 sort numerically and prefixes are unambiguous
	if string(invertedKey(2, "z")) >= string(invertedKey(10, "a")) {
		t.Fatal("fixed-width tableId mis-sorts")
	}
}

func TestPostingsRoundTrip(t *testing.T) {
	in := []int64{5, 1 << 40, 1, 1, 9} // unsorted, dup, and > 2^31
	var got []int64
	decodeDocs(encodeDocs(in), func(d int64) { got = append(got, d) })
	want := []int64{1, 5, 9, 1 << 40} // sorted + deduped
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestInvertedValueRoundTrip(t *testing.T) {
	adds, dels := []int64{1, 4, 9}, []int64{4}
	ab, db := splitInvertedValue(encodeInvertedValue(adds, dels))
	var ga, gd []int64
	decodeDocs(ab, func(d int64) { ga = append(ga, d) })
	decodeDocs(db, func(d int64) { gd = append(gd, d) })
	if len(ga) != 3 || len(gd) != 1 || gd[0] != 4 {
		t.Fatalf("inverted value split wrong: adds=%v dels=%v", ga, gd)
	}
}

func TestForwardTombstoneNoAlias(t *testing.T) {
	// The blocker: a single-keyword doc whose only term-id is ordinal 0 must NOT
	// look like a delete. forwardValue = uvarint(nKw) delta-varint(ords); tombstone = nKw 0.
	live := encodeForward([]uint32{0}) // nKw=1, ord 0  → bytes 0x01 0x00
	if ords, deleted := decodeForward(live); deleted || len(ords) != 1 || ords[0] != 0 {
		t.Fatalf("single-ord-0 doc misread: ords=%v deleted=%v bytes=% x", ords, deleted, live)
	}
	tomb := forwardTombstone()
	if len(tomb) != 1 || tomb[0] != 0x00 {
		t.Fatalf("tombstone must be a single 0x00: % x", tomb)
	}
	if _, deleted := decodeForward(tomb); !deleted {
		t.Fatal("tombstone not detected as delete")
	}
	// round-trip a multi-keyword doc, order-independent
	in := []uint32{9, 0, 4}
	ords, deleted := decodeForward(encodeForward(in))
	sort.Slice(ords, func(i, j int) bool { return ords[i] < ords[j] })
	if deleted || len(ords) != 3 || ords[0] != 0 || ords[1] != 4 || ords[2] != 9 {
		t.Fatalf("forward round-trip wrong: %v", ords)
	}
}

func TestForwardGoldenBytes(t *testing.T) {
	// nKw=1 then ord 0 → exactly 0x01 0x00 (the anti-alias guarantee, frozen)
	got := encodeForward([]uint32{0})
	if len(got) != 2 || got[0] != 0x01 || got[1] != 0x00 {
		t.Fatalf("forward golden changed: % x", got)
	}
}
