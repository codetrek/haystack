package workspace

import (
	"testing"
)

func TestStartIndexing_IdleState(t *testing.T) {
	ws := &Workspace{Id: 1, Path: "/test"}

	// Fresh workspace should be in Idle state
	if ws.GetIndexingState() != IndexingIdle {
		t.Fatalf("new workspace should be in IndexingIdle state, got %d", ws.GetIndexingState())
	}

	err := ws.StartIndexing()
	if err != nil {
		t.Fatalf("StartIndexing on idle workspace should succeed: %v", err)
	}

	if ws.GetIndexingState() != IndexingScanning {
		t.Errorf("state should be IndexingScanning after StartIndexing, got %d", ws.GetIndexingState())
	}

	status := ws.GetIndexingStatus()
	if status == nil {
		t.Fatal("indexing status should not be nil after StartIndexing")
	}
	if status.StartedAt == nil {
		t.Error("indexing status StartedAt should be set")
	}
}

func TestStartIndexing_ScanningState_Rejected(t *testing.T) {
	ws := &Workspace{Id: 1, Path: "/test"}

	err := ws.StartIndexing()
	if err != nil {
		t.Fatalf("first StartIndexing should succeed: %v", err)
	}

	// Second call while scanning should be rejected
	err = ws.StartIndexing()
	if err == nil {
		t.Fatal("StartIndexing should fail when already scanning")
	}

	if ws.GetIndexingState() != IndexingScanning {
		t.Errorf("state should still be IndexingScanning, got %d", ws.GetIndexingState())
	}
}

func TestStartIndexing_AfterFailure_Succeeds(t *testing.T) {
	ws := &Workspace{Id: 1, Path: "/test"}

	// Start indexing
	if err := ws.StartIndexing(); err != nil {
		t.Fatalf("StartIndexing failed: %v", err)
	}

	// Simulate failure
	ws.SetIndexingFailed()

	if ws.GetIndexingState() != IndexingFailed {
		t.Fatalf("state should be IndexingFailed, got %d", ws.GetIndexingState())
	}
	if ws.GetIndexingStatus() != nil {
		t.Error("indexing status should be nil after SetIndexingFailed")
	}

	// Should be able to start indexing again
	err := ws.StartIndexing()
	if err != nil {
		t.Fatalf("StartIndexing after failure should succeed: %v", err)
	}

	if ws.GetIndexingState() != IndexingScanning {
		t.Errorf("state should be IndexingScanning, got %d", ws.GetIndexingState())
	}
	if ws.GetIndexingStatus() == nil {
		t.Error("indexing status should not be nil after restart")
	}
}

func TestStartIndexing_AfterDone_Succeeds(t *testing.T) {
	ws := &Workspace{Id: 1, Path: "/test"}

	// Start indexing
	if err := ws.StartIndexing(); err != nil {
		t.Fatalf("StartIndexing failed: %v", err)
	}

	// Add some files during indexing

	// Complete successfully
	ws.UpdateLastFullSync()

	if ws.GetIndexingState() != IndexingDone {
		t.Fatalf("state should be IndexingDone after UpdateLastFullSync, got %d", ws.GetIndexingState())
	}
	if ws.GetIndexingStatus() != nil {
		t.Error("indexing status should be nil after UpdateLastFullSync")
	}

	// Should be able to start indexing again
	err := ws.StartIndexing()
	if err != nil {
		t.Fatalf("StartIndexing after Done should succeed: %v", err)
	}

	if ws.GetIndexingState() != IndexingScanning {
		t.Errorf("state should be IndexingScanning, got %d", ws.GetIndexingState())
	}
}

func TestResetIndexingState(t *testing.T) {
	ws := &Workspace{Id: 1, Path: "/test"}

	// Test reset from Scanning state
	if err := ws.StartIndexing(); err != nil {
		t.Fatalf("StartIndexing failed: %v", err)
	}
	if ws.GetIndexingState() != IndexingScanning {
		t.Fatalf("expected IndexingScanning, got %d", ws.GetIndexingState())
	}

	ws.ResetIndexingState()
	if ws.GetIndexingState() != IndexingIdle {
		t.Errorf("state should be IndexingIdle after reset, got %d", ws.GetIndexingState())
	}
	if ws.GetIndexingStatus() != nil {
		t.Error("indexing status should be nil after reset")
	}

	// Test reset from Failed state
	if err := ws.StartIndexing(); err != nil {
		t.Fatalf("StartIndexing failed: %v", err)
	}
	ws.SetIndexingFailed()
	if ws.GetIndexingState() != IndexingFailed {
		t.Fatalf("expected IndexingFailed, got %d", ws.GetIndexingState())
	}

	ws.ResetIndexingState()
	if ws.GetIndexingState() != IndexingIdle {
		t.Errorf("state should be IndexingIdle after reset from Failed, got %d", ws.GetIndexingState())
	}

	// Test reset from Done state
	if err := ws.StartIndexing(); err != nil {
		t.Fatalf("StartIndexing failed: %v", err)
	}
	ws.UpdateLastFullSync()
	if ws.GetIndexingState() != IndexingDone {
		t.Fatalf("expected IndexingDone, got %d", ws.GetIndexingState())
	}

	ws.ResetIndexingState()
	if ws.GetIndexingState() != IndexingIdle {
		t.Errorf("state should be IndexingIdle after reset from Done, got %d", ws.GetIndexingState())
	}
}

func TestScanInterruptionRecovery(t *testing.T) {
	ws := &Workspace{Id: 1, Path: "/test"}

	// 1. Start indexing (simulates scanner.Add)
	err := ws.StartIndexing()
	if err != nil {
		t.Fatalf("initial StartIndexing should succeed: %v", err)
	}
	if ws.GetIndexingState() != IndexingScanning {
		t.Fatalf("expected IndexingScanning, got %d", ws.GetIndexingState())
	}

	// 2. Simulate some work happening
	ws.AddIndexingFiles(25)

	// 3. Simulate scan failure (error from processWorkspace)
	ws.SetIndexingFailed()
	if ws.GetIndexingState() != IndexingFailed {
		t.Fatalf("expected IndexingFailed, got %d", ws.GetIndexingState())
	}

	// 4. Verify re-indexing is rejected while not reset (shouldn't be - Failed allows restart)
	// Actually, Failed state should allow restart per the design
	err = ws.StartIndexing()
	if err != nil {
		t.Fatalf("StartIndexing after failure should succeed: %v", err)
	}

	// 5. Verify the new indexing status is fresh
	status := ws.GetIndexingStatus()
	if status == nil {
		t.Fatal("indexing status should not be nil after restart")
	}
	if status.TotalFiles != 0 {
		t.Errorf("restarted indexing should have TotalFiles=0, got %d", status.TotalFiles)
	}
	if status.IndexedFiles != 0 {
		t.Errorf("restarted indexing should have IndexedFiles=0, got %d", status.IndexedFiles)
	}

	// 6. Complete successfully this time
	ws.UpdateLastFullSync()

	if ws.GetIndexingState() != IndexingDone {
		t.Errorf("expected IndexingDone after successful completion, got %d", ws.GetIndexingState())
	}

	if ws.GetLastFullSync().IsZero() {
		t.Error("LastFullSync should be set after successful completion")
	}
}
