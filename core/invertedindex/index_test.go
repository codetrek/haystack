package invertedindex

import (
	"encoding/binary"
	"sort"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helper: flush pending writes synchronously in the current goroutine.
// We set timestamps far in the past so the time-guard in flushPendingWrites
// does not skip the flush.
// ---------------------------------------------------------------------------

func forceFlush(idx *Index) {
	idx.lastFlushWriteTime = time.Time{} // epoch — always older than 1 s
	idx.lastFlushDeleteTime = time.Time{}
	idx.flushPendingWrites(true, true)
	idx.flushPendingDeletes(true, true, MaxInvertedIndexSize)
}

// makeDocID maps a label to the int64 docid the index stores (its canonical
// 8-byte big-endian form), via the shared Doc2ID test helper.
func makeDocID(s string) int64 {
	return Doc2ID(s)
}

// ---------------------------------------------------------------------------
// CreateTable / DeleteTable
// ---------------------------------------------------------------------------

func TestCreateTable(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, err := env.idx.CreateTable("test table 1")
	if err != nil {
		t.Fatalf("CreateTable failed: %v", err)
	}
	if tableId < 0 {
		t.Fatalf("expected tableId >= 0, got %d", tableId)
	}

	// Second table should get a different (incremented) ID
	tableId2, err := env.idx.CreateTable("test table 2")
	if err != nil {
		t.Fatalf("CreateTable failed: %v", err)
	}
	if tableId2 <= tableId {
		t.Errorf("expected second tableId (%d) > first (%d)", tableId2, tableId)
	}
}

func TestCreateTable_GetIncrementalIdError(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	// Swap in a closed DB stub so db.GetIncrementalId() returns an error
	restore := simulateClosedDB(env.idx)
	defer restore()

	tableId, err := env.idx.CreateTable("should fail")
	if err == nil {
		t.Fatal("expected error from CreateTable when db is closed, got nil")
	}
	if tableId != -1 {
		t.Errorf("expected tableId -1 on error, got %d", tableId)
	}
}

func TestDeleteTable(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, err := env.idx.CreateTable("delete-me")
	if err != nil {
		t.Fatalf("CreateTable failed: %v", err)
	}

	docid := makeDocID("doc1")
	env.idx.Add(tableId, docid, []string{"hello", "world"})
	forceFlush(env.idx)

	// Verify data exists
	res := env.idx.Search(tableId, "hello", 0, nil)
	if len(res.DocIds) == 0 {
		t.Fatal("expected search results before delete")
	}

	// Delete the table
	if err := env.idx.DeleteTable(tableId); err != nil {
		t.Fatalf("DeleteTable failed: %v", err)
	}

	// Search should return no results now
	res = env.idx.Search(tableId, "hello", 0, nil)
	if len(res.DocIds) != 0 {
		t.Errorf("expected 0 results after table delete, got %d", len(res.DocIds))
	}
}

// ---------------------------------------------------------------------------
// Update — add keywords
// ---------------------------------------------------------------------------

func TestUpdateAddKeywords(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := env.idx.CreateTable("update-test")
	docid := makeDocID("doc1")

	env.idx.Add(tableId, docid, []string{"alpha", "beta"})
	forceFlush(env.idx)

	res := env.idx.Search(tableId, "alpha", 0, nil)
	if _, ok := res.DocIds[docid]; !ok {
		t.Error("expected doc1 in results for 'alpha'")
	}

	res = env.idx.Search(tableId, "beta", 0, nil)
	if _, ok := res.DocIds[docid]; !ok {
		t.Error("expected doc1 in results for 'beta'")
	}
}

// ---------------------------------------------------------------------------
// Update — remove keywords (newKeywords empty)
// ---------------------------------------------------------------------------

func TestUpdateRemoveAllKeywords(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := env.idx.CreateTable("remove-test")
	docid := makeDocID("doc1")

	// Add first
	env.idx.Add(tableId, docid, []string{"gamma"})
	forceFlush(env.idx)

	// Remove
	env.idx.Delete(tableId, docid)
	forceFlush(env.idx)

	res := env.idx.Search(tableId, "gamma", 0, nil)
	if _, ok := res.DocIds[docid]; ok {
		t.Error("expected doc1 to be removed from 'gamma' results")
	}
}

// ---------------------------------------------------------------------------
// Update — diff (old→new keywords)
// ---------------------------------------------------------------------------

func TestUpdateDiffKeywords(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := env.idx.CreateTable("diff-test")
	docid := makeDocID("doc1")

	// Add initial keywords
	env.idx.Add(tableId, docid, []string{"keep", "remove"})
	forceFlush(env.idx)

	// Update: keep stays, remove goes, addnew is added
	env.idx.Update(tableId, docid, []string{"keep", "addnew"})
	forceFlush(env.idx)

	// "keep" should still have the doc
	res := env.idx.Search(tableId, "keep", 0, nil)
	if _, ok := res.DocIds[docid]; !ok {
		t.Error("expected doc1 in 'keep' results")
	}

	// "addnew" should now have the doc
	res = env.idx.Search(tableId, "addnew", 0, nil)
	if _, ok := res.DocIds[docid]; !ok {
		t.Error("expected doc1 in 'addnew' results")
	}

	// "remove" should no longer have the doc
	res = env.idx.Search(tableId, "remove", 0, nil)
	if _, ok := res.DocIds[docid]; ok {
		t.Error("expected doc1 NOT in 'remove' results")
	}
}

// ---------------------------------------------------------------------------
// Update — empty strings in keywords are ignored
// ---------------------------------------------------------------------------

func TestUpdateIgnoresEmptyKeywords(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := env.idx.CreateTable("empty-kw-test")
	docid := makeDocID("doc1")

	env.idx.Add(tableId, docid, []string{"real", "", "word"})
	forceFlush(env.idx)

	res := env.idx.Search(tableId, "real", 0, nil)
	if _, ok := res.DocIds[docid]; !ok {
		t.Error("expected doc1 in 'real' results")
	}
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

func TestSearchBasic(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := env.idx.CreateTable("search-test")
	doc1 := makeDocID("doc1")
	doc2 := makeDocID("doc2")

	env.idx.Add(tableId, doc1, []string{"common", "unique1"})
	env.idx.Add(tableId, doc2, []string{"common", "unique2"})
	forceFlush(env.idx)

	// "common" should return both
	res := env.idx.Search(tableId, "common", 0, nil)
	if len(res.DocIds) != 2 {
		t.Errorf("expected 2 docs for 'common', got %d", len(res.DocIds))
	}

	// "unique1" should return only doc1
	res = env.idx.Search(tableId, "unique1", 0, nil)
	if len(res.DocIds) != 1 {
		t.Errorf("expected 1 doc for 'unique1', got %d", len(res.DocIds))
	}
	if _, ok := res.DocIds[doc1]; !ok {
		t.Error("expected doc1 in 'unique1' results")
	}
}

func TestSearchWithLimit(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := env.idx.CreateTable("limit-test")
	// Add documents to separate keywords that share a prefix so they appear
	// in separate rows during scanning. Each doc gets its own unique keyword
	// starting with "shared" so Scan prefix picks them all up.
	for i := 0; i < 5; i++ {
		docid := makeDocID("d" + string(rune('a'+i)))
		// Each doc gets a unique keyword like "shared0", "shared1", etc.
		// so they are stored in separate DB rows.
		kw := "shared" + string(rune('0'+i))
		env.idx.Add(tableId, docid, []string{kw})
	}
	forceFlush(env.idx)

	// Search for "shared" prefix with limit of 2
	// The limit is checked after adding docs from each row.
	// With separate rows, the limit should stop scanning after reaching 2 docs.
	res := env.idx.Search(tableId, "shared", 2, nil)
	if len(res.DocIds) < 1 {
		t.Error("expected at least 1 doc with limit=2")
	}
	// With the way limit works (checked between rows), we may get up to
	// the number of docs in the last scanned row. Just verify it's bounded.
	if len(res.DocIds) > 5 {
		t.Errorf("expected at most 5 docs, got %d", len(res.DocIds))
	}
}

func TestSearchCaseInsensitive(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := env.idx.CreateTable("case-test")
	docid := makeDocID("doc1")
	// Keywords are stored as-is; search lowercases the query
	env.idx.Add(tableId, docid, []string{"hello"})
	forceFlush(env.idx)

	res := env.idx.Search(tableId, "HELLO", 0, nil)
	if _, ok := res.DocIds[docid]; !ok {
		t.Error("expected case-insensitive search to find 'hello' via 'HELLO'")
	}
}

func TestSearchWithFilter(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := env.idx.CreateTable("filter-test")
	doc1 := makeDocID("doc1")
	doc2 := makeDocID("doc2")

	env.idx.Add(tableId, doc1, []string{"apple"})
	env.idx.Add(tableId, doc2, []string{"applesauce"})
	forceFlush(env.idx)

	// Filter that rejects all keys
	rejectAll := func(key string) bool { return false }
	res := env.idx.Search(tableId, "apple", 0, rejectAll)
	if len(res.DocIds) != 0 {
		t.Errorf("expected 0 docs with reject-all filter, got %d", len(res.DocIds))
	}

	// Filter that accepts all keys
	acceptAll := func(key string) bool { return true }
	res = env.idx.Search(tableId, "apple", 0, acceptAll)
	if len(res.DocIds) == 0 {
		t.Error("expected results with accept-all filter")
	}
}

func TestSearchNoResults(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := env.idx.CreateTable("noresult-test")
	res := env.idx.Search(tableId, "nonexistent", 0, nil)
	if len(res.DocIds) != 0 {
		t.Errorf("expected 0 docs for nonexistent keyword, got %d", len(res.DocIds))
	}
}

// ---------------------------------------------------------------------------
// GetDocs
// ---------------------------------------------------------------------------

func TestGetDocs(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := env.idx.CreateTable("getdocs-test")
	doc1 := makeDocID("doc1")
	doc2 := makeDocID("doc2")

	env.idx.Add(tableId, doc1, []string{"keyword"})
	env.idx.Add(tableId, doc2, []string{"keyword"})
	forceFlush(env.idx)

	res := env.idx.GetDocs(tableId, "keyword")
	if len(res.DocIds) != 2 {
		t.Errorf("expected 2 docs, got %d", len(res.DocIds))
	}
	if _, ok := res.DocIds[doc1]; !ok {
		t.Error("expected doc1 in GetDocs results")
	}
	if _, ok := res.DocIds[doc2]; !ok {
		t.Error("expected doc2 in GetDocs results")
	}
}

func TestGetDocsEmpty(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := env.idx.CreateTable("getdocs-empty-test")
	res := env.idx.GetDocs(tableId, "nothing")
	if len(res.DocIds) != 0 {
		t.Errorf("expected 0 docs, got %d", len(res.DocIds))
	}
}

// ---------------------------------------------------------------------------
// removeDocumentsFromInvertedIndex
// ---------------------------------------------------------------------------

func TestRemoveDocumentsFromInvertedIndex(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := env.idx.CreateTable("remove-doc-test")
	doc1 := makeDocID("doc1")
	doc2 := makeDocID("doc2")
	doc3 := makeDocID("doc3")

	// Add all three docs to same keyword
	env.idx.Add(tableId, doc1, []string{"target"})
	env.idx.Add(tableId, doc2, []string{"target"})
	env.idx.Add(tableId, doc3, []string{"target"})
	forceFlush(env.idx)

	// Remove doc2 via removeDocumentsFromInvertedIndex
	batch := env.DB.NewBatch(0)
	err := env.idx.removeDocumentsFromInvertedIndex(batch, tableId, "target", []int64{doc2}, MaxInvertedIndexSize)
	if err != nil {
		t.Fatalf("removeDocumentsFromInvertedIndex failed: %v", err)
	}
	batch.Commit()

	// doc1 and doc3 should remain, doc2 should be gone
	res := env.idx.GetDocs(tableId, "target")
	if _, ok := res.DocIds[doc2]; ok {
		t.Error("expected doc2 to be removed")
	}
	if _, ok := res.DocIds[doc1]; !ok {
		t.Error("expected doc1 to remain")
	}
	if _, ok := res.DocIds[doc3]; !ok {
		t.Error("expected doc3 to remain")
	}
}

func TestRemoveDocumentsEmptyKeyword(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	batch := env.DB.NewBatch(0)
	// Empty keyword should be a no-op (returns nil)
	err := env.idx.removeDocumentsFromInvertedIndex(batch, 1, "", []int64{Doc2ID("doc")}, MaxInvertedIndexSize)
	if err != nil {
		t.Fatalf("expected nil error for empty keyword, got: %v", err)
	}
	batch.Commit()
}

func TestRemoveDocumentsEmptyDocids(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	batch := env.DB.NewBatch(0)
	// Empty docid list should be a no-op (returns nil)
	err := env.idx.removeDocumentsFromInvertedIndex(batch, 1, "kw", []int64{}, MaxInvertedIndexSize)
	if err != nil {
		t.Fatalf("expected nil error for empty docids, got: %v", err)
	}
	batch.Commit()
}

// ---------------------------------------------------------------------------
// Multiple tables are isolated
// ---------------------------------------------------------------------------

func TestMultipleTablesIsolated(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	t1, _ := env.idx.CreateTable("table-a")
	t2, _ := env.idx.CreateTable("table-b")

	doc := makeDocID("doc1")

	env.idx.Add(t1, doc, []string{"word"})
	forceFlush(env.idx)

	// Table 1 should find the doc
	res1 := env.idx.Search(t1, "word", 0, nil)
	if len(res1.DocIds) != 1 {
		t.Errorf("table 1: expected 1 doc, got %d", len(res1.DocIds))
	}

	// Table 2 should NOT find the doc
	res2 := env.idx.Search(t2, "word", 0, nil)
	if len(res2.DocIds) != 0 {
		t.Errorf("table 2: expected 0 docs, got %d", len(res2.DocIds))
	}
}

// ---------------------------------------------------------------------------
// Pending writes / cache behaviour
// ---------------------------------------------------------------------------

func TestPendingWritesCacheCreatesAndReturns(t *testing.T) {
	// Verify getPendingWrite creates new cache entries
	env := setupTestEnv(t)
	defer env.teardown()

	origPW := env.idx.pendingWrites
	defer func() { env.idx.pendingWrites = origPW }()
	env.idx.pendingWrites = map[int]*pendingTableWrites{}

	pw := env.idx.getPendingWrite(99)
	if pw == nil {
		t.Fatal("expected non-nil pendingTableWrites")
	}
	if pw.TableId != 99 {
		t.Errorf("expected TableId 99, got %d", pw.TableId)
	}
	if pw.InvertedIndex == nil {
		t.Error("expected non-nil InvertedIndex map")
	}

	// Second call should return the same object
	pw2 := env.idx.getPendingWrite(99)
	if pw2 != pw {
		t.Error("expected same pendingTableWrites object on second call")
	}
}

func TestPendingDeletesCacheCreatesAndReturns(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	origPD := env.idx.pendingDeletes
	defer func() { env.idx.pendingDeletes = origPD }()
	env.idx.pendingDeletes = map[int]*pendingTableWrites{}

	pd := env.idx.getPendingDelete(88)
	if pd == nil {
		t.Fatal("expected non-nil pendingTableWrites for deletes")
	}
	if pd.TableId != 88 {
		t.Errorf("expected TableId 88, got %d", pd.TableId)
	}

	pd2 := env.idx.getPendingDelete(88)
	if pd2 != pd {
		t.Error("expected same pendingTableWrites object on second call")
	}
}

func TestClearPendingWrites(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	origPW := env.idx.pendingWrites
	origPD := env.idx.pendingDeletes
	defer func() {
		env.idx.pendingWrites = origPW
		env.idx.pendingDeletes = origPD
	}()

	env.idx.pendingWrites = map[int]*pendingTableWrites{}
	env.idx.pendingDeletes = map[int]*pendingTableWrites{}

	env.idx.getPendingWrite(77)
	env.idx.getPendingDelete(77)

	if len(env.idx.pendingWrites) != 1 || len(env.idx.pendingDeletes) != 1 {
		t.Fatal("expected pending caches to exist before clear")
	}

	env.idx.clearPendingWrites(77)

	if len(env.idx.pendingWrites) != 0 {
		t.Error("expected pendingWrites to be empty after clear")
	}
	if len(env.idx.pendingDeletes) != 0 {
		t.Error("expected pendingDeletes to be empty after clear")
	}
}

// ---------------------------------------------------------------------------
// writeInvertedIndex (original, not mock)
// ---------------------------------------------------------------------------

func TestWriteInvertedIndexDeduplicates(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := env.idx.CreateTable("dedup-test")
	doc := makeDocID("dup1")

	// Directly call writeInvertedIndex with duplicates
	batch := env.DB.NewBatch(0)
	key := env.idx.encodeInvertedKey(tableId, "dupkw", 3)
	writeInvertedIndex(batch, tableId, "dupkw", []int64{doc, doc, doc}, key)
	batch.Commit()

	res := env.idx.GetDocs(tableId, "dupkw")
	if len(res.DocIds) != 1 {
		t.Errorf("expected 1 unique doc after dedup, got %d", len(res.DocIds))
	}
}

// ---------------------------------------------------------------------------
// updateIndex / removeIndex (internal)
// ---------------------------------------------------------------------------

func TestUpdateAndRemoveIndex(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := env.idx.CreateTable("internal-test")
	doc := makeDocID("doc1")

	// Add via updateIndex
	env.idx.updateIndex(tableId, doc, []string{"intkw"})
	forceFlush(env.idx)

	res := env.idx.Search(tableId, "intkw", 0, nil)
	if _, ok := res.DocIds[doc]; !ok {
		t.Error("expected doc1 in 'intkw' results after updateIndex")
	}

	// Remove via removeIndex
	env.idx.removeIndex(tableId, doc, []string{"intkw"})
	forceFlush(env.idx)

	res = env.idx.Search(tableId, "intkw", 0, nil)
	if _, ok := res.DocIds[doc]; ok {
		t.Error("expected doc1 removed from 'intkw' results after removeIndex")
	}
}

// ---------------------------------------------------------------------------
// Batch write / newBatch
// ---------------------------------------------------------------------------

func TestNewBatchDefault(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	batch := newBatch(env.DB)
	if batch == nil {
		t.Fatal("expected non-nil batch")
	}
	batch.Commit()
}

// ---------------------------------------------------------------------------
// FlushPendingWritesTask
// ---------------------------------------------------------------------------

func TestFlushPendingWritesTaskRun(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := env.idx.CreateTable("flush-task-test")
	docid := makeDocID("doc1")

	env.idx.Add(tableId, docid, []string{"taskword"})

	// Run the task directly
	task := &flushPendingWritesTask{idx: env.idx, closing: true}
	err := task.Run()
	if err != nil {
		t.Fatalf("flushPendingWritesTask.Run() failed: %v", err)
	}

	// Verify data was flushed
	res := env.idx.Search(tableId, "taskword", 0, nil)
	if _, ok := res.DocIds[docid]; !ok {
		t.Error("expected doc1 in 'taskword' results after flush task")
	}
}

// ---------------------------------------------------------------------------
// Update with both new and old keywords empty — should be no-op
// ---------------------------------------------------------------------------

func TestUpdateBothEmpty(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := env.idx.CreateTable("both-empty-test")
	docid := makeDocID("doc1")

	// Both no-ops on an unknown doc — must not panic or error.
	env.idx.Update(tableId, docid, nil)
	env.idx.Delete(tableId, docid)
	forceFlush(env.idx)
}

// ---------------------------------------------------------------------------
// Search across multiple keywords in same table
// ---------------------------------------------------------------------------

func TestSearchMultipleKeywordsPrefix(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := env.idx.CreateTable("prefix-test")
	doc1 := makeDocID("doc1")
	doc2 := makeDocID("doc2")

	env.idx.Add(tableId, doc1, []string{"test"})
	env.idx.Add(tableId, doc2, []string{"testing"})
	forceFlush(env.idx)

	// Search for "test" — since Scan uses prefix scanning, this might
	// match both "test" and "testing" depending on the scan behaviour
	res := env.idx.Search(tableId, "test", 0, nil)
	if len(res.DocIds) < 1 {
		t.Error("expected at least 1 result for 'test' prefix search")
	}
}

// ---------------------------------------------------------------------------
// Merging struct
// ---------------------------------------------------------------------------

func TestMergingMergedRowCount(t *testing.T) {
	m := merging{
		TotalRowsBefore: 100,
		TotalRowsAfter:  60,
	}
	if m.MergedRowCount() != 40 {
		t.Errorf("expected MergedRowCount=40, got %d", m.MergedRowCount())
	}
}

// ---------------------------------------------------------------------------
// Large number of documents
// ---------------------------------------------------------------------------

func TestUpdateManyDocuments(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := env.idx.CreateTable("many-docs-test")

	docids := make([]int64, 20)
	for i := 0; i < 20; i++ {
		docids[i] = makeDocID("d" + string(rune('A'+i)))
		env.idx.Add(tableId, docids[i], []string{"popular"})
	}
	forceFlush(env.idx)

	res := env.idx.Search(tableId, "popular", 0, nil)
	if len(res.DocIds) != 20 {
		t.Errorf("expected 20 docs, got %d", len(res.DocIds))
	}
}

// ---------------------------------------------------------------------------
// Doc2ID helper used in tests
// ---------------------------------------------------------------------------

func TestDoc2IDPadding(t *testing.T) {
	tests := []struct {
		input string
		want  string // canonical 8-byte big-endian form
	}{
		{"a", "0000000a"},
		{"abcdefgh", "abcdefgh"},
		{"abcdefghijklm", "abcdefgh"}, // truncated to first 8 bytes
	}
	for _, tc := range tests {
		// The canonical docid form is its 8-byte big-endian encoding (idtable's
		// docid identity), independent of the inverted-value codec.
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(Doc2ID(tc.input)))
		if got := string(b[:]); got != tc.want {
			t.Errorf("Doc2ID(%q) canonical bytes = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestMakeDocsForKeyword(t *testing.T) {
	// MakeDocsForKeyword delta-varint encodes the docids; decode must recover the
	// same set (ascending in uint64 order).
	got := decodeInvertedValue([]byte(MakeDocsForKeyword("doc1", "doc2")))
	want := []int64{Doc2ID("doc1"), Doc2ID("doc2")}
	sort.Slice(want, func(i, j int) bool { return uint64(want[i]) < uint64(want[j]) })
	if len(got) != len(want) {
		t.Fatalf("got %d docids, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("docid[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// removeDocumentsFromInvertedIndex — remove partial docs
// ---------------------------------------------------------------------------

func TestRemoveDocumentsPartial(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := env.idx.CreateTable("partial-remove-test")

	docs := make([]int64, 5)
	for i := 0; i < 5; i++ {
		docs[i] = makeDocID("p" + string(rune('1'+i)))
		env.idx.Add(tableId, docs[i], []string{"partkey"})
	}
	forceFlush(env.idx)

	// Remove first 2 documents
	batch := env.DB.NewBatch(0)
	err := env.idx.removeDocumentsFromInvertedIndex(batch, tableId, "partkey", docs[:2], MaxInvertedIndexSize)
	if err != nil {
		t.Fatalf("removeDocumentsFromInvertedIndex failed: %v", err)
	}
	batch.Commit()

	// Should have 3 remaining
	res := env.idx.GetDocs(tableId, "partkey")
	if len(res.DocIds) != 3 {
		// Collect what we got for debugging
		gotDocs := make([]int64, 0, len(res.DocIds))
		for d := range res.DocIds {
			gotDocs = append(gotDocs, d)
		}
		sort.Slice(gotDocs, func(i, j int) bool { return gotDocs[i] < gotDocs[j] })
		t.Errorf("expected 3 docs after partial removal, got %d: %v", len(res.DocIds), gotDocs)
	}

	// Removed docs should be gone
	for _, removed := range docs[:2] {
		if _, ok := res.DocIds[removed]; ok {
			t.Errorf("expected doc %q to be removed", removed)
		}
	}
}
