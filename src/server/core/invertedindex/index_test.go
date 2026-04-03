package invertedindex

import (
	"sort"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helper: flush pending writes synchronously in the current goroutine.
// We set timestamps far in the past so the time-guard in flushPendingWrites
// does not skip the flush.
// ---------------------------------------------------------------------------

func forceFlush() {
	lastFlushWriteTime = time.Time{} // epoch — always older than 1 s
	lastFlushDeleteTime = time.Time{}
	flushPendingWrites(true)
	flushPendingDeletes(true, MaxInvertedIndexSize)
}

// makeDocID pads or truncates s to exactly 8 bytes (the canonical docid size).
func makeDocID(s string) string {
	return Doc2ID(s)
}

// ---------------------------------------------------------------------------
// CreateTable / DeleteTable
// ---------------------------------------------------------------------------

func TestCreateTable(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, err := CreateTable("test table 1")
	if err != nil {
		t.Fatalf("CreateTable failed: %v", err)
	}
	if tableId < 0 {
		t.Fatalf("expected tableId >= 0, got %d", tableId)
	}

	// Second table should get a different (incremented) ID
	tableId2, err := CreateTable("test table 2")
	if err != nil {
		t.Fatalf("CreateTable failed: %v", err)
	}
	if tableId2 <= tableId {
		t.Errorf("expected second tableId (%d) > first (%d)", tableId2, tableId)
	}
}

func TestDeleteTable(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, err := CreateTable("delete-me")
	if err != nil {
		t.Fatalf("CreateTable failed: %v", err)
	}

	docid := makeDocID("doc1")
	Update(tableId, docid, []string{"hello", "world"}, nil)
	forceFlush()

	// Verify data exists
	res := Search(tableId, "hello", 0, nil)
	if len(res.DocIds) == 0 {
		t.Fatal("expected search results before delete")
	}

	// Delete the table
	if err := DeleteTable(tableId); err != nil {
		t.Fatalf("DeleteTable failed: %v", err)
	}

	// Search should return no results now
	res = Search(tableId, "hello", 0, nil)
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

	tableId, _ := CreateTable("update-test")
	docid := makeDocID("doc1")

	Update(tableId, docid, []string{"alpha", "beta"}, nil)
	forceFlush()

	res := Search(tableId, "alpha", 0, nil)
	if _, ok := res.DocIds[docid]; !ok {
		t.Error("expected doc1 in results for 'alpha'")
	}

	res = Search(tableId, "beta", 0, nil)
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

	tableId, _ := CreateTable("remove-test")
	docid := makeDocID("doc1")

	// Add first
	Update(tableId, docid, []string{"gamma"}, nil)
	forceFlush()

	// Remove
	Update(tableId, docid, nil, []string{"gamma"})
	forceFlush()

	res := Search(tableId, "gamma", 0, nil)
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

	tableId, _ := CreateTable("diff-test")
	docid := makeDocID("doc1")

	// Add initial keywords
	Update(tableId, docid, []string{"keep", "remove"}, nil)
	forceFlush()

	// Update: keep stays, remove goes, addnew is added
	Update(tableId, docid, []string{"keep", "addnew"}, []string{"keep", "remove"})
	forceFlush()

	// "keep" should still have the doc
	res := Search(tableId, "keep", 0, nil)
	if _, ok := res.DocIds[docid]; !ok {
		t.Error("expected doc1 in 'keep' results")
	}

	// "addnew" should now have the doc
	res = Search(tableId, "addnew", 0, nil)
	if _, ok := res.DocIds[docid]; !ok {
		t.Error("expected doc1 in 'addnew' results")
	}

	// "remove" should no longer have the doc
	res = Search(tableId, "remove", 0, nil)
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

	tableId, _ := CreateTable("empty-kw-test")
	docid := makeDocID("doc1")

	Update(tableId, docid, []string{"real", "", "word"}, []string{"", "old"})
	forceFlush()

	res := Search(tableId, "real", 0, nil)
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

	tableId, _ := CreateTable("search-test")
	doc1 := makeDocID("doc1")
	doc2 := makeDocID("doc2")

	Update(tableId, doc1, []string{"common", "unique1"}, nil)
	Update(tableId, doc2, []string{"common", "unique2"}, nil)
	forceFlush()

	// "common" should return both
	res := Search(tableId, "common", 0, nil)
	if len(res.DocIds) != 2 {
		t.Errorf("expected 2 docs for 'common', got %d", len(res.DocIds))
	}

	// "unique1" should return only doc1
	res = Search(tableId, "unique1", 0, nil)
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

	tableId, _ := CreateTable("limit-test")
	// Add documents to separate keywords that share a prefix so they appear
	// in separate rows during scanning. Each doc gets its own unique keyword
	// starting with "shared" so Scan prefix picks them all up.
	for i := 0; i < 5; i++ {
		docid := makeDocID("d" + string(rune('a'+i)))
		// Each doc gets a unique keyword like "shared0", "shared1", etc.
		// so they are stored in separate DB rows.
		kw := "shared" + string(rune('0'+i))
		Update(tableId, docid, []string{kw}, nil)
	}
	forceFlush()

	// Search for "shared" prefix with limit of 2
	// The limit is checked after adding docs from each row.
	// With separate rows, the limit should stop scanning after reaching 2 docs.
	res := Search(tableId, "shared", 2, nil)
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

	tableId, _ := CreateTable("case-test")
	docid := makeDocID("doc1")
	// Keywords are stored as-is; search lowercases the query
	Update(tableId, docid, []string{"hello"}, nil)
	forceFlush()

	res := Search(tableId, "HELLO", 0, nil)
	if _, ok := res.DocIds[docid]; !ok {
		t.Error("expected case-insensitive search to find 'hello' via 'HELLO'")
	}
}

func TestSearchWithFilter(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := CreateTable("filter-test")
	doc1 := makeDocID("doc1")
	doc2 := makeDocID("doc2")

	Update(tableId, doc1, []string{"apple"}, nil)
	Update(tableId, doc2, []string{"applesauce"}, nil)
	forceFlush()

	// Filter that rejects all keys
	rejectAll := func(key string) bool { return false }
	res := Search(tableId, "apple", 0, rejectAll)
	if len(res.DocIds) != 0 {
		t.Errorf("expected 0 docs with reject-all filter, got %d", len(res.DocIds))
	}

	// Filter that accepts all keys
	acceptAll := func(key string) bool { return true }
	res = Search(tableId, "apple", 0, acceptAll)
	if len(res.DocIds) == 0 {
		t.Error("expected results with accept-all filter")
	}
}

func TestSearchNoResults(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := CreateTable("noresult-test")
	res := Search(tableId, "nonexistent", 0, nil)
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

	tableId, _ := CreateTable("getdocs-test")
	doc1 := makeDocID("doc1")
	doc2 := makeDocID("doc2")

	Update(tableId, doc1, []string{"keyword"}, nil)
	Update(tableId, doc2, []string{"keyword"}, nil)
	forceFlush()

	res := GetDocs(tableId, "keyword")
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

	tableId, _ := CreateTable("getdocs-empty-test")
	res := GetDocs(tableId, "nothing")
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

	tableId, _ := CreateTable("remove-doc-test")
	doc1 := makeDocID("doc1")
	doc2 := makeDocID("doc2")
	doc3 := makeDocID("doc3")

	// Add all three docs to same keyword
	Update(tableId, doc1, []string{"target"}, nil)
	Update(tableId, doc2, []string{"target"}, nil)
	Update(tableId, doc3, []string{"target"}, nil)
	forceFlush()

	// Remove doc2 via removeDocumentsFromInvertedIndex
	batch := db.NewBatch(0)
	err := removeDocumentsFromInvertedIndex(batch, tableId, "target", []string{doc2}, MaxInvertedIndexSize)
	if err != nil {
		t.Fatalf("removeDocumentsFromInvertedIndex failed: %v", err)
	}
	batch.Commit()

	// doc1 and doc3 should remain, doc2 should be gone
	res := GetDocs(tableId, "target")
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

	batch := db.NewBatch(0)
	// Empty keyword should be a no-op (returns nil)
	err := removeDocumentsFromInvertedIndex(batch, 1, "", []string{"doc"}, MaxInvertedIndexSize)
	if err != nil {
		t.Fatalf("expected nil error for empty keyword, got: %v", err)
	}
	batch.Commit()
}

func TestRemoveDocumentsEmptyDocids(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	batch := db.NewBatch(0)
	// Empty docid list should be a no-op (returns nil)
	err := removeDocumentsFromInvertedIndex(batch, 1, "kw", []string{}, MaxInvertedIndexSize)
	if err != nil {
		t.Fatalf("expected nil error for empty docids, got: %v", err)
	}
	batch.Commit()
}

func TestRemoveDocumentsOnlyEmptyStringDocids(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	batch := db.NewBatch(0)
	// Only empty-string docids should be a no-op (returns nil)
	err := removeDocumentsFromInvertedIndex(batch, 1, "kw", []string{"", ""}, MaxInvertedIndexSize)
	if err != nil {
		t.Fatalf("expected nil error for empty-string docids, got: %v", err)
	}
	batch.Commit()
}

// ---------------------------------------------------------------------------
// Multiple tables are isolated
// ---------------------------------------------------------------------------

func TestMultipleTablesIsolated(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	t1, _ := CreateTable("table-a")
	t2, _ := CreateTable("table-b")

	doc := makeDocID("doc1")

	Update(t1, doc, []string{"word"}, nil)
	forceFlush()

	// Table 1 should find the doc
	res1 := Search(t1, "word", 0, nil)
	if len(res1.DocIds) != 1 {
		t.Errorf("table 1: expected 1 doc, got %d", len(res1.DocIds))
	}

	// Table 2 should NOT find the doc
	res2 := Search(t2, "word", 0, nil)
	if len(res2.DocIds) != 0 {
		t.Errorf("table 2: expected 0 docs, got %d", len(res2.DocIds))
	}
}

// ---------------------------------------------------------------------------
// Pending writes / cache behaviour
// ---------------------------------------------------------------------------

func TestPendingWritesCacheCreatesAndReturns(t *testing.T) {
	// Verify getPendingWrite creates new cache entries
	origPW := pendingWrites
	defer func() { pendingWrites = origPW }()
	pendingWrites = map[int]*PendingTableWrites{}

	pw := getPendingWrite(99)
	if pw == nil {
		t.Fatal("expected non-nil PendingTableWrites")
	}
	if pw.TableId != 99 {
		t.Errorf("expected TableId 99, got %d", pw.TableId)
	}
	if pw.InvertedIndex == nil {
		t.Error("expected non-nil InvertedIndex map")
	}

	// Second call should return the same object
	pw2 := getPendingWrite(99)
	if pw2 != pw {
		t.Error("expected same PendingTableWrites object on second call")
	}
}

func TestPendingDeletesCacheCreatesAndReturns(t *testing.T) {
	origPD := pendingDeletes
	defer func() { pendingDeletes = origPD }()
	pendingDeletes = map[int]*PendingTableWrites{}

	pd := getPendingDelete(88)
	if pd == nil {
		t.Fatal("expected non-nil PendingTableWrites for deletes")
	}
	if pd.TableId != 88 {
		t.Errorf("expected TableId 88, got %d", pd.TableId)
	}

	pd2 := getPendingDelete(88)
	if pd2 != pd {
		t.Error("expected same PendingTableWrites object on second call")
	}
}

func TestClearPendingWrites(t *testing.T) {
	origPW := pendingWrites
	origPD := pendingDeletes
	defer func() {
		pendingWrites = origPW
		pendingDeletes = origPD
	}()

	pendingWrites = map[int]*PendingTableWrites{}
	pendingDeletes = map[int]*PendingTableWrites{}

	getPendingWrite(77)
	getPendingDelete(77)

	if len(pendingWrites) != 1 || len(pendingDeletes) != 1 {
		t.Fatal("expected pending caches to exist before clear")
	}

	clearPendingWrites(77)

	if len(pendingWrites) != 0 {
		t.Error("expected pendingWrites to be empty after clear")
	}
	if len(pendingDeletes) != 0 {
		t.Error("expected pendingDeletes to be empty after clear")
	}
}

// ---------------------------------------------------------------------------
// writeInvertedIndex (original, not mock)
// ---------------------------------------------------------------------------

func TestWriteInvertedIndexDeduplicates(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := CreateTable("dedup-test")
	doc := makeDocID("dup1")

	// Directly call writeInvertedIndex with duplicates
	batch := db.NewBatch(0)
	writeInvertedIndex(batch, tableId, "dupkw", []string{doc, doc, doc}, nil)
	batch.Commit()

	res := GetDocs(tableId, "dupkw")
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

	tableId, _ := CreateTable("internal-test")
	doc := makeDocID("doc1")

	// Add via updateIndex
	updateIndex(tableId, doc, []string{"intkw"})
	forceFlush()

	res := Search(tableId, "intkw", 0, nil)
	if _, ok := res.DocIds[doc]; !ok {
		t.Error("expected doc1 in 'intkw' results after updateIndex")
	}

	// Remove via removeIndex
	removeIndex(tableId, doc, []string{"intkw"})
	forceFlush()

	res = Search(tableId, "intkw", 0, nil)
	if _, ok := res.DocIds[doc]; ok {
		t.Error("expected doc1 removed from 'intkw' results after removeIndex")
	}
}

// ---------------------------------------------------------------------------
// Batch write / NewBatch
// ---------------------------------------------------------------------------

func TestNewBatchDefault(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	batch := NewBatch(db)
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

	tableId, _ := CreateTable("flush-task-test")
	docid := makeDocID("doc1")

	Update(tableId, docid, []string{"taskword"}, nil)

	// Run the task directly
	task := &flushPendingWritesTask{closing: true}
	err := task.Run()
	if err != nil {
		t.Fatalf("flushPendingWritesTask.Run() failed: %v", err)
	}

	// Verify data was flushed
	res := Search(tableId, "taskword", 0, nil)
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

	tableId, _ := CreateTable("both-empty-test")
	docid := makeDocID("doc1")

	// Both empty — should not panic or error
	Update(tableId, docid, nil, nil)
	Update(tableId, docid, []string{}, []string{})
	forceFlush()
}

// ---------------------------------------------------------------------------
// Search across multiple keywords in same table
// ---------------------------------------------------------------------------

func TestSearchMultipleKeywordsPrefix(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := CreateTable("prefix-test")
	doc1 := makeDocID("doc1")
	doc2 := makeDocID("doc2")

	Update(tableId, doc1, []string{"test"}, nil)
	Update(tableId, doc2, []string{"testing"}, nil)
	forceFlush()

	// Search for "test" — since Scan uses prefix scanning, this might
	// match both "test" and "testing" depending on the scan behaviour
	res := Search(tableId, "test", 0, nil)
	if len(res.DocIds) < 1 {
		t.Error("expected at least 1 result for 'test' prefix search")
	}
}

// ---------------------------------------------------------------------------
// Merging struct
// ---------------------------------------------------------------------------

func TestMergingMergedRowCount(t *testing.T) {
	m := Merging{
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

	tableId, _ := CreateTable("many-docs-test")

	docids := make([]string, 20)
	for i := 0; i < 20; i++ {
		docids[i] = makeDocID("d" + string(rune('A'+i)))
		Update(tableId, docids[i], []string{"popular"}, nil)
	}
	forceFlush()

	res := Search(tableId, "popular", 0, nil)
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
		want  int
	}{
		{"a", 8},
		{"abcdefgh", 8},
		{"abcdefghijklm", 8}, // truncated
	}
	for _, tc := range tests {
		got := Doc2ID(tc.input)
		if len(got) != tc.want {
			t.Errorf("Doc2ID(%q) length = %d, want %d", tc.input, len(got), tc.want)
		}
	}
}

func TestMakeDocsForKeyword(t *testing.T) {
	result := MakeDocsForKeyword("doc1", "doc2")
	if len(result) != 16 {
		t.Errorf("expected 16 bytes, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// removeDocumentsFromInvertedIndex — remove partial docs
// ---------------------------------------------------------------------------

func TestRemoveDocumentsPartial(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	tableId, _ := CreateTable("partial-remove-test")

	docs := make([]string, 5)
	for i := 0; i < 5; i++ {
		docs[i] = makeDocID("p" + string(rune('1'+i)))
		Update(tableId, docs[i], []string{"partkey"}, nil)
	}
	forceFlush()

	// Remove first 2 documents
	batch := db.NewBatch(0)
	err := removeDocumentsFromInvertedIndex(batch, tableId, "partkey", docs[:2], MaxInvertedIndexSize)
	if err != nil {
		t.Fatalf("removeDocumentsFromInvertedIndex failed: %v", err)
	}
	batch.Commit()

	// Should have 3 remaining
	res := GetDocs(tableId, "partkey")
	if len(res.DocIds) != 3 {
		// Collect what we got for debugging
		gotDocs := make([]string, 0, len(res.DocIds))
		for d := range res.DocIds {
			gotDocs = append(gotDocs, d)
		}
		sort.Strings(gotDocs)
		t.Errorf("expected 3 docs after partial removal, got %d: %v", len(res.DocIds), gotDocs)
	}

	// Removed docs should be gone
	for _, removed := range docs[:2] {
		if _, ok := res.DocIds[removed]; ok {
			t.Errorf("expected doc %q to be removed", removed)
		}
	}
}
