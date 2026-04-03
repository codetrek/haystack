package invertedindex

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/codetrek/haystack/server/core/pebble"
)

const (
	testTable1 = 1
	testTable2 = 2
)

// mockBatchWrite implements BatchWrite interface for testing
type mockBatchWrite struct {
	deleted     []string
	writtenKeys []string
	writtenData [][]string
	tableIds    []int
	keywords    []string
}

func (m *mockBatchWrite) Delete(key []byte) error {
	m.deleted = append(m.deleted, string(key))
	return nil
}

func (m *mockBatchWrite) Put(key, value []byte) error {
	m.writtenKeys = append(m.writtenKeys, string(key))
	return nil
}

func (m *mockBatchWrite) Commit() error { return nil }
func (m *mockBatchWrite) Reset()        {}
func (m *mockBatchWrite) Close() error  { return nil }
func (m *mockBatchWrite) Count() int32  { return 0 }
func (m *mockBatchWrite) DeleteRange(start, end []byte) error {
	return nil
}
func (m *mockBatchWrite) DeletePrefix(prefix []byte) error {
	return nil
}

// setupTestMocks sets up the mocks used by all rewrite index tests
func setupTestMocks() func() {
	// Override the writeKeywordIndex function for testing
	originalWriteKeywordIndex := writeInvertedIndex
	writeInvertedIndex = func(batch pebble.Batch, tableId int, keyword string, docIDs []string, data []byte) {
		mockBatch := batch.(*mockBatchWrite)
		mockBatch.tableIds = append(mockBatch.tableIds, tableId)
		mockBatch.keywords = append(mockBatch.keywords, keyword)
		mockBatch.writtenData = append(mockBatch.writtenData, docIDs)
	}

	// Return restore functions
	restoreWriteKeywordIndex := func() { writeInvertedIndex = originalWriteKeywordIndex }

	return restoreWriteKeywordIndex
}

// Helper function to create a new mock batch
func newMockBatch(db pebble.DB) *mockBatchWrite {
	return &mockBatchWrite{
		deleted:     []string{},
		writtenKeys: []string{},
		writtenData: [][]string{},
		tableIds:    []int{},
		keywords:    []string{},
	}
}

// Helper function to verify write operations and document IDs
func verifyWriteOperations(t *testing.T, mockBatch *mockBatchWrite, index *InvertedIndex, expectedWriteCount int, expectedDocIDs int) {
	t.Helper()

	// Check write calls
	if len(mockBatch.tableIds) != expectedWriteCount {
		t.Errorf("Expected %d writes, got %d", expectedWriteCount, len(mockBatch.tableIds))
	}

	// Check table ID and keyword are correctly passed
	for i, wsID := range mockBatch.tableIds {
		if wsID != index.TableId {
			t.Errorf("Expected table %d, got %d", index.TableId, wsID)
		}
		if mockBatch.keywords[i] != index.Keyword {
			t.Errorf("Expected keyword %s, got %s", index.Keyword, mockBatch.keywords[i])
		}
	}

	// For cases with merged data, check unique doc count
	if len(mockBatch.writtenData) > 0 && expectedDocIDs > 0 {
		uniqueDocs := make(map[string]struct{})
		for _, docs := range mockBatch.writtenData {
			for _, doc := range docs {
				uniqueDocs[doc] = struct{}{}
			}
		}
		if len(uniqueDocs) != expectedDocIDs {
			t.Errorf("Expected %d unique doc IDs, got %d", expectedDocIDs, len(uniqueDocs))
		}
	}
}

// TestRewriteIndexSingleRow tests the case where there's only a single row
// so no merging should happen
func TestRewriteIndexSingleRow(t *testing.T) {
	restoreWriteKeywordIndex := setupTestMocks()
	defer restoreWriteKeywordIndex()

	// Use actual encoded document IDs
	encodedValue := "doc1|doc2"

	index := &InvertedIndex{
		TableId: testTable1,
		Keyword: "keyword1",
		Rows: []RecordRow{
			{Key: "key1", Value: encodedValue, DocCount: 2},
		},
		DocCount: 2,
	}
	maxSize := 10

	mockBatch := newMockBatch(nil)
	mergedCount := rewriteIndex(mockBatch, index, maxSize)

	// No operations should be performed
	if len(mockBatch.deleted) != 0 || len(mockBatch.writtenKeys) != 0 {
		t.Errorf("Expected no database operations, got %d deletes and %d writes",
			len(mockBatch.deleted), len(mockBatch.writtenKeys))
	}

	// Should return original count (1)
	if mergedCount != 1 {
		t.Errorf("Expected mergedCount 1, got %d", mergedCount)
	}
}

// TestRewriteIndexMultipleRows tests merging multiple rows into one
func TestRewriteIndexMultipleRows(t *testing.T) {
	restoreWriteKeywordIndex := setupTestMocks()
	defer restoreWriteKeywordIndex()

	// Use actual encoded document IDs
	index := &InvertedIndex{
		TableId: testTable1,
		Keyword: "keyword1",
		Rows: []RecordRow{
			{Key: "key1", Value: "cdocdoc1cdocdoc2", DocCount: 2},
			{Key: "key2", Value: "cdocdoc3cdocdoc4", DocCount: 2},
			{Key: "key3", Value: "cdocdoc5cdocdoc6", DocCount: 2},
		},
		DocCount: 6,
	}
	maxSize := 10
	expectedDeleted := 3
	expectedWritten := 1
	expectedDocIDs := 6

	mockBatch := newMockBatch(nil)
	mergedCount := rewriteIndex(mockBatch, index, maxSize)

	// Check deletion count
	if len(mockBatch.deleted) != expectedDeleted {
		t.Errorf("Expected %d deletes, got %d", expectedDeleted, len(mockBatch.deleted))
	}

	// Check write operations
	verifyWriteOperations(t, mockBatch, index, expectedWritten, expectedDocIDs)

	// Should return merged count (1)
	if mergedCount != 1 {
		t.Errorf("Expected mergedCount 1, got %d", mergedCount)
	}
}

// TestRewriteIndexWellBatched tests that rows with high doc counts aren't merged
func TestRewriteIndexWellBatched(t *testing.T) {
	restoreWriteKeywordIndex := setupTestMocks()
	defer restoreWriteKeywordIndex()

	// Use actual encoded document IDs
	index := &InvertedIndex{
		TableId: testTable1,
		Keyword: "keyword1",
		Rows: []RecordRow{
			{Key: "key1", Value: "doc1|doc2|doc3|doc4|doc5|doc6|doc7|doc8|doc9|doc10", DocCount: 10},
			{Key: "key2", Value: "doc11|doc12|doc13|doc14|doc15|doc16|doc17|doc18|doc19|doc20", DocCount: 10},
		},
		DocCount: 20,
	}
	maxSize := 5

	mockBatch := newMockBatch(nil)
	mergedCount := rewriteIndex(mockBatch, index, maxSize)

	// No operations should be performed
	if len(mockBatch.deleted) != 0 || len(mockBatch.writtenKeys) != 0 {
		t.Errorf("Expected no database operations, got %d deletes and %d writes",
			len(mockBatch.deleted), len(mockBatch.writtenKeys))
	}

	// Should return original count (2)
	if mergedCount != 2 {
		t.Errorf("Expected mergedCount 2, got %d", mergedCount)
	}
}

func Doc2ID(doc string) string {
	// docid MUST be a 16 character string
	if len(doc) > 8 {
		return doc[0:8]
	}

	return fmt.Sprintf("%s%s", strings.Repeat("0", 8-len(doc)), doc)
}

func MakeDocsForKeyword(docs ...string) string {
	result := ""
	for _, doc := range docs {
		result += Doc2ID(doc)
	}

	return result
}

// TestRewriteIndexMultipleBatches tests when multiple merge operations are needed
func TestRewriteIndexMultipleBatches(t *testing.T) {
	restoreWriteKeywordIndex := setupTestMocks()
	defer restoreWriteKeywordIndex()

	// Use actual encoded document IDs
	index := &InvertedIndex{
		TableId: testTable1,
		Keyword: "keyword1",
		Rows: []RecordRow{
			{Key: "key1", Value: MakeDocsForKeyword("doc1", "doc2"), DocCount: 2},
			{Key: "key2", Value: MakeDocsForKeyword("doc3", "doc4"), DocCount: 2},
			{Key: "key3", Value: MakeDocsForKeyword("doc5", "doc6"), DocCount: 2},
			{Key: "key4", Value: MakeDocsForKeyword("doc7", "doc8"), DocCount: 2},
			{Key: "key5", Value: MakeDocsForKeyword("doc9", "doc10"), DocCount: 2},
		},
		DocCount: 10,
	}
	maxSize := 4
	// The rewriteIndex function deletes all 5 rows when merging
	expectedDeleted := 5
	expectedWritten := 2
	expectedDocIDs := 10

	mockBatch := newMockBatch(nil)
	mergedCount := rewriteIndex(mockBatch, index, maxSize)

	// Check deletion count
	if len(mockBatch.deleted) != expectedDeleted {
		t.Errorf("Expected %d deletes, got %d", expectedDeleted, len(mockBatch.deleted))
	}

	// Check write operations
	verifyWriteOperations(t, mockBatch, index, expectedWritten, expectedDocIDs)

	// Should return merged count (2)
	if mergedCount != 2 {
		t.Errorf("Expected mergedCount 2, got %d", mergedCount)
	}
}

// mockDB implements a mock database for testing
type mockDB struct {
	scanRangeFunc func(start, end []byte, fn func(k, v []byte) bool)
	batch         func() pebble.Batch
	batchCommits  int
}

func (m *mockDB) ScanRange(start, end []byte, fn func(k, v []byte) bool) error {
	if m.scanRangeFunc != nil {
		m.scanRangeFunc(start, end, fn)
	}

	return nil
}

func (m *mockDB) Commit() error {
	m.batchCommits++
	return nil
}

func (m *mockDB) IsClosed() bool {
	return false
}

func (m *mockDB) Close() error {
	return nil
}

func (d *mockDB) NewBatch(int32) pebble.Batch {
	if d.batch != nil {
		return d.batch()
	}
	return newMockBatch(nil)
}

func (m *mockDB) Delete(key []byte) error {
	return nil
}
func (m *mockDB) Put(key, value []byte) error {
	return nil
}

func (m *mockDB) Get(key []byte) ([]byte, error) {
	return nil, nil
}

func (d *mockDB) Scan(prefix []byte, cb func(key, value []byte) bool) error {
	return nil
}

func (d *mockDB) ScheduleCompact() {
}

func (d *mockDB) GetIncrementalId(key []byte) (int, error) {
	return 0, nil
}

// TestMergeKeywordsIndexEmptyInput tests merging with an empty initial state
func TestMergeKeywordsIndexEmptyInput(t *testing.T) {
	// Save and defer-restore original functions
	originalDB := db
	defer func() { db = originalDB }()

	// Create a mock database for testing
	mockDB := &mockDB{
		scanRangeFunc: func(start, end []byte, fn func(k, v []byte) bool) {
			// Empty database, no keys to scan
		},
	}
	db = mockDB

	// Set up test data
	input := Merging{
		NextIter:        string(KeyTypeRow),
		TotalKeywords:   0,
		TotalRowsBefore: 0,
		TotalRowsAfter:  0,
	}

	// Run the function
	result := mergeKeywordsIndex(input, 10)

	// Validate results
	if result.NextIter != "" {
		t.Errorf("Expected NextIter to be empty, got %q", result.NextIter)
	}
	if result.TotalKeywords != 0 {
		t.Errorf("Expected TotalKeywords to be 0, got %d", result.TotalKeywords)
	}
	if result.TotalRowsBefore != 0 {
		t.Errorf("Expected TotalRowsBefore to be 0, got %d", result.TotalRowsBefore)
	}
	if result.TotalRowsAfter != 0 {
		t.Errorf("Expected TotalRowsAfter to be 0, got %d", result.TotalRowsAfter)
	}
}

// TestMergeKeywordsIndexSingleTable tests merging keywords from a single table
func TestMergeKeywordsIndexSingleTable(t *testing.T) {
	// Save and defer-restore original functions
	originalDB := db
	originalWriteKeywordIndex := writeInvertedIndex
	originalNewBatch := NewBatch
	defer func() {
		db = originalDB
		writeInvertedIndex = originalWriteKeywordIndex
		NewBatch = originalNewBatch
	}()

	writtenTables := []int{}
	writtenKeywords := []string{}
	writtenDocIDs := [][]string{}

	batch := newMockBatch(nil)
	// Mock only the writeKeywordIndex function
	writeInvertedIndex = func(batch pebble.Batch, tableId int, keyword string, docIDs []string, data []byte) {
		writtenTables = append(writtenTables, tableId)
		writtenKeywords = append(writtenKeywords, keyword)
		writtenDocIDs = append(writtenDocIDs, docIDs)
	}

	// Mock database with test data - using real key/value formats
	db = &mockDB{
		scanRangeFunc: func(start, end []byte, fn func(k, v []byte) bool) {
			// Create real formatted keys and values
			keys := [][]byte{
				encodeInvertedKey(testTable1, "keyword1", 2),
				encodeInvertedKey(testTable1, "keyword1", 3),
			}
			values := []string{
				MakeDocsForKeyword("doc1", "doc2"),
				MakeDocsForKeyword("doc3", "doc4", "doc5"),
			}

			for i, key := range keys {
				if !fn(key, []byte(values[i])) {
					return
				}
			}
		},
		batch: func() pebble.Batch {
			return batch
		},
	}

	// Set up test data
	input := Merging{
		NextIter:        string(KeyTypeRow),
		TotalKeywords:   0,
		TotalRowsBefore: 0,
		TotalRowsAfter:  0,
	}

	// Run function
	result := mergeKeywordsIndex(input, 6)

	// Validate results
	if result.NextIter != "" {
		t.Errorf("Expected NextIter to be empty, got %q", result.NextIter)
	}
	if result.TotalKeywords != 1 {
		t.Errorf("Expected TotalKeywords to be 1, got %d", result.TotalKeywords)
	}
	if result.TotalRowsBefore != 2 {
		t.Errorf("Expected TotalRowsBefore to be 2, got %d", result.TotalRowsBefore)
	}
	if result.TotalRowsAfter != 1 {
		t.Errorf("Expected TotalRowsAfter to be 1, got %d", result.TotalRowsAfter)
	}

	// Validate writes
	if len(writtenTables) != 1 {
		t.Errorf("Expected 1 table write, got %d", len(writtenTables))
	} else if writtenTables[0] != testTable1 {
		t.Errorf("Expected table 'table1', got %q", writtenTables[0])
	}

	if len(writtenKeywords) != 1 {
		t.Errorf("Expected 1 keyword write, got %d", len(writtenKeywords))
	} else if writtenKeywords[0] != "keyword1" {
		t.Errorf("Expected keyword 'keyword1', got %q", writtenKeywords[0])
	}

	if len(batch.deleted) != 2 {
		t.Errorf("Expected 2 deleted keys, got %d", len(batch.deleted))
	}

	// Check if we have all expected doc IDs
	if len(writtenDocIDs) != 1 {
		t.Errorf("Expected 1 docID group, got %d", len(writtenDocIDs))
	} else {
		uniqueDocs := make(map[string]struct{})
		for _, docID := range writtenDocIDs[0] {
			uniqueDocs[docID] = struct{}{}
		}

		if len(uniqueDocs) != 5 {
			t.Errorf("Expected 5 unique doc IDs, got %d", len(uniqueDocs))
		}

		expectedDocs := []string{Doc2ID("doc1"), Doc2ID("doc2"), Doc2ID("doc3"), Doc2ID("doc4"), Doc2ID("doc5")}
		for _, doc := range expectedDocs {
			if _, ok := uniqueDocs[doc]; !ok {
				t.Errorf("Expected to find doc ID %q but it was missing", doc)
			}
		}
	}
}

// TestMergeKeywordsIndexMultipleTables tests merging keywords from multiple tables
func TestMergeKeywordsIndexMultipleTables(t *testing.T) {
	// Save and defer-restore original functions
	originalDB := db
	originalWriteKeywordIndex := writeInvertedIndex
	originalNewBatch := NewBatch
	defer func() {
		db = originalDB
		writeInvertedIndex = originalWriteKeywordIndex
		NewBatch = originalNewBatch
	}()

	// Track writes by table
	writtenData := make(map[int]map[string][]string) // tableId -> keyword -> docIDs
	deletedKeys := []string{}

	// Create mock batch
	mockBatch := &struct {
		pebble.Batch
	}{
		Batch: &mockBatchWriteWithFuncs{
			deleteFunc: func(key []byte) error {
				deletedKeys = append(deletedKeys, string(key))
				return nil
			},
			putFunc: func(key, value []byte) error {
				return nil
			},
			commitFunc: func() error {
				return nil
			},
		},
	}

	// Mock only the writeKeywordIndex function
	writeInvertedIndex = func(batch pebble.Batch, tableId int, keyword string, docIDs []string, data []byte) {
		if _, ok := writtenData[tableId]; !ok {
			writtenData[tableId] = make(map[string][]string)
		}
		writtenData[tableId][keyword] = docIDs
	}

	NewBatch = func(db pebble.DB) pebble.Batch {
		return mockBatch
	}

	// Mock database with test data for multiple tables - using real key/value formats
	db = &mockDB{
		scanRangeFunc: func(start, end []byte, fn func(k, v []byte) bool) {
			// Create real formatted keys and values
			keys := [][]byte{
				encodeInvertedKey(testTable1, "keyword1", 2),
				encodeInvertedKey(testTable1, "keyword1", 3),
				encodeInvertedKey(testTable2, "keyword1", 2),
				encodeInvertedKey(testTable2, "keyword1", 3),
			}
			values := []string{
				MakeDocsForKeyword("doc1", "doc2"),
				MakeDocsForKeyword("doc3", "doc4", "doc5"),
				MakeDocsForKeyword("doc6", "doc7"),
				MakeDocsForKeyword("doc8", "doc9", "doc10"),
			}

			for i, key := range keys {
				if !fn([]byte(key), []byte(values[i])) {
					return
				}
			}
		},
	}

	// Set up test data
	input := Merging{
		NextIter:        string(KeyTypeRow),
		TotalKeywords:   0,
		TotalRowsBefore: 0,
		TotalRowsAfter:  0,
	}

	// Run function
	result := mergeKeywordsIndex(input, 6)

	// Validate results
	if result.NextIter != "" {
		t.Errorf("Expected NextIter to be empty, got %q", result.NextIter)
	}
	if result.TotalKeywords != 2 {
		t.Errorf("Expected TotalKeywords to be 2, got %d", result.TotalKeywords)
	}
	if result.TotalRowsBefore != 4 {
		t.Errorf("Expected TotalRowsBefore to be 4, got %d", result.TotalRowsBefore)
	}
	if result.TotalRowsAfter != 2 {
		t.Errorf("Expected TotalRowsAfter to be 2, got %d", result.TotalRowsAfter)
	}

	// Validate writes
	if len(writtenData) != 2 {
		t.Errorf("Expected writes for 2 tables, got %d", len(writtenData))
	}

	// Check table1 data
	if ws1Data, ok := writtenData[testTable1]; !ok {
		t.Errorf("Expected data for table1 but found none")
	} else {
		if len(ws1Data) != 1 {
			t.Errorf("Expected 1 keyword for table1, got %d", len(ws1Data))
		}

		if docs, ok := ws1Data["keyword1"]; !ok {
			t.Errorf("Expected keyword1 data for table1 but found none")
		} else {
			uniqueDocs := make(map[string]struct{})
			for _, doc := range docs {
				uniqueDocs[doc] = struct{}{}
			}

			if len(uniqueDocs) != 5 {
				t.Errorf("Expected 5 unique docs for table1, got %d", len(uniqueDocs))
			}

			expectedDocs := []string{Doc2ID("doc1"), Doc2ID("doc2"), Doc2ID("doc3"), Doc2ID("doc4"), Doc2ID("doc5")}
			for _, doc := range expectedDocs {
				if _, ok := uniqueDocs[doc]; !ok {
					t.Errorf("Expected doc %s in table1 but it was missing", doc)
				}
			}
		}
	}

	// Check table2 data
	if ws2Data, ok := writtenData[testTable2]; !ok {
		t.Errorf("Expected data for table2 but found none")
	} else {
		if len(ws2Data) != 1 {
			t.Errorf("Expected 1 keyword for table2, got %d", len(ws2Data))
		}

		if docs, ok := ws2Data["keyword1"]; !ok {
			t.Errorf("Expected keyword1 data for table2 but found none")
		} else {
			uniqueDocs := make(map[string]struct{})
			for _, doc := range docs {
				uniqueDocs[doc] = struct{}{}
			}

			if len(uniqueDocs) != 5 {
				t.Errorf("Expected 5 unique docs for table2, got %d", len(uniqueDocs))
			}

			expectedDocs := []string{Doc2ID("doc6"), Doc2ID("doc7"), Doc2ID("doc8"), Doc2ID("doc9"), Doc2ID("doc10")}
			for _, doc := range expectedDocs {
				if _, ok := uniqueDocs[doc]; !ok {
					t.Errorf("Expected doc %s in table2 but it was missing", doc)
				}
			}
		}
	}

	// Check deleted keys
	if len(deletedKeys) != 4 {
		t.Errorf("Expected 4 deleted keys, got %d", len(deletedKeys))
	}
}

// For compatibility with mockBatchWrite in existing tests
type mockBatchWriteWithFuncs struct {
	deleteFunc func(key []byte) error
	putFunc    func(key, value []byte) error
	commitFunc func() error
}

func (m *mockBatchWriteWithFuncs) Delete(key []byte) error {
	if m.deleteFunc != nil {
		return m.deleteFunc(key)
	}
	return nil
}

func (m *mockBatchWriteWithFuncs) Put(key, value []byte) error {
	if m.putFunc != nil {
		return m.putFunc(key, value)
	}
	return nil
}

func (m *mockBatchWriteWithFuncs) Commit() error {
	if m.commitFunc != nil {
		return m.commitFunc()
	}
	return nil
}

func (m *mockBatchWriteWithFuncs) Reset() {}

func (m *mockBatchWriteWithFuncs) Close() error {
	return nil
}
func (m *mockBatchWriteWithFuncs) Count() int32 { return 0 }

func (m *mockBatchWriteWithFuncs) DeleteRange(start, end []byte) error {
	return nil
}

func (m *mockBatchWriteWithFuncs) DeletePrefix(prefix []byte) error {
	return nil
}

// TestMergeKeywordsIndexTimeout tests the timeout behavior of mergeKeywordsIndex
func TestMergeKeywordsIndexTimeout(t *testing.T) {
	// Save and defer-restore original functions
	originalDB := db
	originalWriteKeywordIndex := writeInvertedIndex
	originalNewBatch := NewBatch
	defer func() {
		db = originalDB
		writeInvertedIndex = originalWriteKeywordIndex
		NewBatch = originalNewBatch
	}()

	// Create a lot of entries with properly formatted keys to trigger timeout
	keyCount := 1000
	keys := make([][]byte, keyCount)
	values := make([]string, keyCount)

	// Generate properly formatted keys and values
	for i := 0; i < keyCount; i++ {
		keyword := "keyword" + string(rune('a'+i%26))
		keys[i] = encodeInvertedKey(testTable1, keyword, 2)
		values[i] = fmt.Sprintf("doc%d|doc%d", i*2, i*2+1)
	}

	// Set up scanner to process a lot of entries
	var iterationCount int
	db = &mockDB{
		scanRangeFunc: func(start, end []byte, fn func(k, v []byte) bool) {
			// Process items slowly to trigger timeout
			for i := 0; i < 100; i++ {
				if iterationCount >= len(keys) {
					break
				}

				key := keys[iterationCount]
				value := values[iterationCount]
				iterationCount++

				// Sleep a tiny bit to ensure timeout triggers
				time.Sleep(5 * time.Millisecond)

				if !fn(key, []byte(value)) {
					break
				}
			}
		},
	}

	// Mock only the writeKeywordIndex function
	writeInvertedIndex = func(batch pebble.Batch, tableId int, keyword string, docIDs []string, data []byte) {
		// No-op for this test
	}

	NewBatch = func(db pebble.DB) pebble.Batch {
		return &mockBatchWriteWithFuncs{
			deleteFunc: func(key []byte) error { return nil },
			putFunc:    func(key, value []byte) error { return nil },
			commitFunc: func() error { return nil },
		}
	}

	// Run the function
	input := Merging{
		NextIter:        string(KeyTypeRow),
		TotalKeywords:   0,
		TotalRowsBefore: 0,
		TotalRowsAfter:  0,
	}

	result := mergeKeywordsIndex(input, 5)

	// Verify we hit the timeout (NextIter should be non-empty)
	if result.NextIter == "" {
		t.Errorf("Expected timeout with non-empty NextIter, but got empty NextIter")
	}

	// We should have some processed keywords
	if result.TotalKeywords == 0 {
		t.Errorf("Expected non-zero TotalKeywords after timeout")
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// mergeKeywordTask.Run – cover both branches (pendingWrites empty / non-empty)
// ---------------------------------------------------------------------------

func TestMergeKeywordTask_Run_NoPendingWrites(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	// pendingWrites is reset to empty in setupTestEnv
	task := &mergeKeywordTask{
		merging: Merging{
			NextIter: string(KeyTypeRow),
		},
	}

	err := task.Run()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.merging.WaitingForFlushCache {
		t.Error("expected WaitingForFlushCache to be false")
	}
}

func TestMergeKeywordTask_Run_WithPendingWrites(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	// Add a pending write to trigger the waiting path
	pendingWrites[1] = &PendingTableWrites{}

	task := &mergeKeywordTask{
		merging: Merging{
			NextIter: string(KeyTypeRow),
		},
	}

	err := task.Run()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !task.merging.WaitingForFlushCache {
		t.Error("expected WaitingForFlushCache to be true")
	}

	// Clean up
	pendingWrites = map[int]*PendingTableWrites{}
}

// ---------------------------------------------------------------------------
// KeywordsMerger.GetWait – verify it returns the mergerDone channel
// ---------------------------------------------------------------------------

func TestKeywordsMerger_GetWait(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	km := &KeywordsMerger{}
	km.Start()

	ch := km.GetWait()
	if ch == nil {
		t.Fatal("expected non-nil channel from GetWait")
	}

	km.Shutdown()

	select {
	case <-ch:
		// success — channel closed after shutdown
	case <-time.After(5 * time.Second):
		t.Fatal("GetWait channel not closed after shutdown")
	}
}

// ---------------------------------------------------------------------------
// KeywordsMerger.run – exercise the merge loop via mpsc queue
// This test creates a KeywordsMerger, immediately shuts it down,
// and verifies the shutdown path. The 300s initial timer makes it
// impractical to test the merge-loop path without modifying prod code.
// ---------------------------------------------------------------------------

func TestKeywordsMerger_StartShutdownWait(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	km := &KeywordsMerger{}
	km.Start()

	// Verify mergerDone is not closed yet
	select {
	case <-km.mergerDone:
		t.Fatal("mergerDone should not be closed before shutdown")
	default:
		// expected
	}

	km.Shutdown()
	km.Wait()
}

// ---------------------------------------------------------------------------
// mergeKeywordTask.Run via mpscQueue – exercises Run through the queue
// ---------------------------------------------------------------------------

func TestMergeKeywordTask_RunViaQueue(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	task := &mergeKeywordTask{
		merging: Merging{
			NextIter: string(KeyTypeRow),
		},
	}

	err := mpscQueue.RunTask(task)
	if err != nil {
		t.Fatalf("unexpected error from RunTask: %v", err)
	}
	if task.merging.WaitingForFlushCache {
		t.Error("expected WaitingForFlushCache to be false with empty pendingWrites")
	}
}
