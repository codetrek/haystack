package prompts

import (
	"testing"

	"github.com/ai-microsoft/haystack/server/core/pebble"
	"github.com/ai-microsoft/haystack/utils/queue"
	pebbleDB "github.com/cockroachdb/pebble"
)

// MockDB implements the pebble.DB interface for testing
type MockDB struct {
	closed bool
	data   map[string][]byte
}

func (m *MockDB) Put(key, value []byte) error {
	if m.data == nil {
		m.data = make(map[string][]byte)
	}
	m.data[string(key)] = value
	return nil
}

func (m *MockDB) Get(key []byte) ([]byte, error) {
	if value, exists := m.data[string(key)]; exists {
		return value, nil
	}
	return nil, pebbleDB.ErrNotFound
}

func (m *MockDB) Delete(key []byte) error {
	if m.data != nil {
		delete(m.data, string(key))
	}
	return nil
}

func (m *MockDB) GetIncrementalId(key []byte) (int, error) {
	// Mock implementation - just return a simple incremental ID
	return 1, nil
}

func (m *MockDB) ScheduleCompact() {
	// Mock implementation - do nothing
}

func (m *MockDB) Scan(prefix []byte, cb func(key, value []byte) bool) error {
	if m.data == nil {
		return nil
	}
	prefixStr := string(prefix)
	for k, v := range m.data {
		if len(k) >= len(prefixStr) && k[:len(prefixStr)] == prefixStr {
			if !cb([]byte(k), v) {
				break
			}
		}
	}
	return nil
}

func (m *MockDB) ScanRange(begin []byte, end []byte, cb func(key, value []byte) bool) error {
	if m.data == nil {
		return nil
	}
	beginStr := string(begin)
	endStr := string(end)
	for k, v := range m.data {
		if k >= beginStr && k < endStr {
			if !cb([]byte(k), v) {
				break
			}
		}
	}
	return nil
}

func (m *MockDB) Close() error {
	m.closed = true
	return nil
}

func (m *MockDB) IsClosed() bool {
	return m.closed
}

func (m *MockDB) NewBatch(maxSize int32) pebble.Batch {
	return &MockBatch{
		db:      m,
		maxSize: int(maxSize),
		ops:     make([]batchOp, 0),
	}
}

// MockBatch implements the pebble.Batch interface for testing
type MockBatch struct {
	db      *MockDB
	maxSize int
	ops     []batchOp
}

type batchOp struct {
	key      []byte
	value    []byte
	isDelete bool
}

func (m *MockBatch) Put(key, value []byte) error {
	m.ops = append(m.ops, batchOp{
		key:   append([]byte(nil), key...),
		value: append([]byte(nil), value...),
	})
	return nil
}

func (m *MockBatch) Delete(key []byte) error {
	m.ops = append(m.ops, batchOp{
		key:      append([]byte(nil), key...),
		isDelete: true,
	})
	return nil
}

func (m *MockBatch) DeleteRange(start, end []byte) error {
	// Mock implementation - add range delete operations individually
	return nil
}

func (m *MockBatch) DeletePrefix(prefix []byte) error {
	// Mock implementation
	return nil
}

func (m *MockBatch) Commit() error {
	for _, op := range m.ops {
		if op.isDelete {
			m.db.Delete(op.key)
		} else {
			m.db.Put(op.key, op.value)
		}
	}
	return nil
}

func (m *MockBatch) Reset() {
	m.ops = m.ops[:0]
}

func (m *MockBatch) Close() error {
	m.ops = nil
	return nil
}

func TestInit(t *testing.T) {
	mockDB := &MockDB{}
	mockMpsc := &queue.Mpsc{}

	err := Init(mockDB, mockMpsc)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	// Verify that the global variables are set
	// Note: In a real test, we'd need to expose these variables or use dependency injection
	// For now, just verify Init doesn't return an error
}

func TestCloseAndWait(t *testing.T) {
	mockDB := &MockDB{}
	mockMpsc := queue.NewMpsc("test")
	mockMpsc.Start()

	// Initialize first
	Init(mockDB, mockMpsc)
	// Call CloseAndWait
	CloseAndWait()

	// Stop the queue to clean up
	mockMpsc.Stop()

	// Note: In a real test, we'd verify global variables are cleared
	// but they're not exported, so we just verify the function doesn't panic
}

func TestNewBatch(t *testing.T) {
	mockDB := &MockDB{}

	batch := NewBatch(mockDB)
	if batch == nil {
		t.Fatal("NewBatch returned nil")
	}

	// Note: We can't easily test the exact type due to interface constraints
	// but we can verify it implements the Batch interface and behaves correctly
	err := batch.Put([]byte("key"), []byte("value"))
	if err != nil {
		t.Errorf("Batch.Put failed: %v", err)
	}

	err = batch.Commit()
	if err != nil {
		t.Errorf("Batch.Commit failed: %v", err)
	}
}

func TestMaxBatchSizeConstant(t *testing.T) {
	expected := 512
	if MaxBatchSize != expected {
		t.Errorf("MaxBatchSize = %d, want %d", MaxBatchSize, expected)
	}
}

func TestScanPromptFiles(t *testing.T) {
	// Setup mock database
	mockDB := &MockDB{
		data: make(map[string][]byte),
	}

	// Initialize the package
	mockMpsc := queue.NewMpsc("test")
	mockMpsc.Start()
	Init(mockDB, mockMpsc)
	defer func() {
		CloseAndWait()
		mockMpsc.Stop()
	}()

	// Add test data
	workspaceId := 123
	testData := map[string][]byte{
		string(EncodePromptPathKey(workspaceId, "test1.prompt.md")):        []byte("embedding1"),
		string(EncodePromptPathKey(workspaceId, "test2.prompt.md")):        []byte("embedding2"),
		string(EncodePromptPathKey(workspaceId, "subdir/test3.prompt.md")): []byte("embedding3"),
		// Data from different workspace should not be included
		string(EncodePromptPathKey(456, "other.prompt.md")): []byte("other_embedding"),
	}

	for key, value := range testData {
		mockDB.data[key] = value
	}

	// Test scanning
	var results []struct {
		key   string
		value []byte
	}

	ScanPromptFiles(workspaceId, "", func(promptKey string, value []byte) bool {
		results = append(results, struct {
			key   string
			value []byte
		}{promptKey, value})
		return true // Continue scanning
	})

	// Verify results
	expectedCount := 3 // Should find 3 files from workspace 123
	if len(results) != expectedCount {
		t.Errorf("Expected %d results, got %d", expectedCount, len(results))
	}

	// Verify that results contain the expected keys
	foundKeys := make(map[string]bool)
	for _, result := range results {
		foundKeys[result.key] = true
	}

	expectedKeys := []string{
		string(EncodePromptPathKey(workspaceId, "test1.prompt.md")),
		string(EncodePromptPathKey(workspaceId, "test2.prompt.md")),
		string(EncodePromptPathKey(workspaceId, "subdir/test3.prompt.md")),
	}

	for _, expectedKey := range expectedKeys {
		if !foundKeys[expectedKey] {
			t.Errorf("Expected key %s not found in results", expectedKey)
		}
	}
}

func TestScanPromptFilesWithPath(t *testing.T) {
	// Setup mock database
	mockDB := &MockDB{
		data: make(map[string][]byte),
	}

	// Initialize the package
	mockMpsc := queue.NewMpsc("test")
	mockMpsc.Start()
	Init(mockDB, mockMpsc)
	defer func() {
		CloseAndWait()
		mockMpsc.Stop()
	}()

	// Add test data
	workspaceId := 123
	testData := map[string][]byte{
		string(EncodePromptPathKey(workspaceId, "subdir/test1.prompt.md")): []byte("embedding1"),
		string(EncodePromptPathKey(workspaceId, "subdir/test2.prompt.md")): []byte("embedding2"),
		string(EncodePromptPathKey(workspaceId, "other/test3.prompt.md")):  []byte("embedding3"),
	}

	for key, value := range testData {
		mockDB.data[key] = value
	}

	// Test scanning with specific path prefix
	var results []struct {
		key   string
		value []byte
	}

	ScanPromptFiles(workspaceId, "subdir/", func(promptKey string, value []byte) bool {
		results = append(results, struct {
			key   string
			value []byte
		}{promptKey, value})
		return true // Continue scanning
	})

	// Should only find files in subdir/
	expectedCount := 2
	if len(results) != expectedCount {
		t.Errorf("Expected %d results, got %d", expectedCount, len(results))
	}
}

func TestScanPromptFilesEarlyExit(t *testing.T) {
	// Setup mock database
	mockDB := &MockDB{
		data: make(map[string][]byte),
	}

	// Initialize the package
	mockMpsc := queue.NewMpsc("test")
	mockMpsc.Start()
	Init(mockDB, mockMpsc)
	defer func() {
		CloseAndWait()
		mockMpsc.Stop()
	}()

	// Add test data
	workspaceId := 123
	testData := map[string][]byte{
		string(EncodePromptPathKey(workspaceId, "test1.prompt.md")): []byte("embedding1"),
		string(EncodePromptPathKey(workspaceId, "test2.prompt.md")): []byte("embedding2"),
		string(EncodePromptPathKey(workspaceId, "test3.prompt.md")): []byte("embedding3"),
	}

	for key, value := range testData {
		mockDB.data[key] = value
	}

	// Test early exit from callback
	callCount := 0
	ScanPromptFiles(workspaceId, "", func(promptKey string, value []byte) bool {
		callCount++
		return callCount < 2 // Stop after first call
	})

	if callCount != 2 {
		t.Errorf("Expected callback to be called 2 times, got %d", callCount)
	}
}

func TestInitTwice(t *testing.T) {
	mockDB1 := &MockDB{}
	mockMpsc1 := &queue.Mpsc{}
	mockDB2 := &MockDB{}
	mockMpsc2 := &queue.Mpsc{}

	// First initialization
	err := Init(mockDB1, mockMpsc1)
	if err != nil {
		t.Fatalf("First Init failed: %v", err)
	}
	// Second initialization should overwrite
	err = Init(mockDB2, mockMpsc2)
	if err != nil {
		t.Fatalf("Second Init failed: %v", err)
	}

	// Note: In a real test, we'd verify the global variables are updated
	// but they're not exported, so we just verify the function succeeds
}
