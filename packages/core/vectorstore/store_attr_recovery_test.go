package vectorstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStore_CreateAttrIndex_SurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, Metric: Cosine})
	requireNoError(t, err)
	b := s.NewBatch()
	for i := 0; i < 30; i++ {
		b.Put("k"+itoa(i), []float32{float32(i + 1), 0, 0},
			Payload{"color": StringValue(map[bool]string{true: "red", false: "blue"}[i%2 == 0])})
	}
	requireNoError(t, b.Commit())
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	requireNoError(t, s.CreateAttrIndex("color", Keyword))
	requireNoError(t, s.Close())

	// Reopen: the declared index must persist (manifest v3) and filter correctly.
	s2, err := Open(Options{Dir: dir, Metric: Cosine})
	requireNoError(t, err)
	defer s2.Close()
	requireNoError(t, s2.WaitForIndex())
	got, err := s2.Search("default", []float32{1, 0, 0}, 5, Eq("color", StringValue("red")))
	requireNoError(t, err)
	if len(got) == 0 {
		t.Fatal("declared filter lost across reopen → no results")
	}
}

func TestStore_AttrFile_Corrupt_RebuildsOnOpen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, Metric: Cosine})
	requireNoError(t, err)
	b := s.NewBatch()
	for i := 0; i < 20; i++ {
		b.Put("k"+itoa(i), []float32{float32(i + 1), 0, 0},
			Payload{"color": StringValue("red")})
	}
	requireNoError(t, b.Commit())
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	requireNoError(t, s.CreateAttrIndex("color", Keyword))
	requireNoError(t, s.Close())

	// Corrupt the sealed segment's attr.dat, then reopen — must rebuild from payload.
	matches, _ := filepath.Glob(filepath.Join(dir, "seg-*", "attr.dat"))
	if len(matches) == 0 {
		t.Fatal("expected an attr.dat to corrupt")
	}
	requireNoError(t, os.WriteFile(matches[0], []byte("garbage"), 0644))

	s2, err := Open(Options{Dir: dir, Metric: Cosine})
	requireNoError(t, err)
	defer s2.Close()
	requireNoError(t, s2.WaitForIndex())
	got, err := s2.Search("default", []float32{1, 0, 0}, 5, Eq("color", StringValue("red")))
	requireNoError(t, err)
	if len(got) == 0 {
		t.Fatal("corrupt attr.dat must be rebuilt from payload → filter still works")
	}
}
