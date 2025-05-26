package invertedindex

import (
	"log"
	"time"
)

var (
	pendingWrites      = map[int]*PendingTableWrites{}
	lastFlushWriteTime = time.Now()

	pendingDeletes      = map[int]*PendingTableWrites{}
	lastFlushDeleteTime = time.Now()
)

var (
	FlushTicker        = 1 * time.Second
	FlushWaitTimeout   = 2 * time.Second
	FlushWaitBatchSize = 50
)

type BufferedWrites struct {
}

type RelatedDocs struct {
	DocIds    []string
	UpdatedAt time.Time
}

type PendingTableWrites struct {
	TableId int

	// Map of keyword to document ids
	InvertedIndex map[string]RelatedDocs
}

type flushPendingWritesTask struct {
	closing bool
}

func (t *flushPendingWritesTask) Run() error {
	// Flush pending writes to the database
	flushPendingWrites(t.closing)
	flushPendingDeletes(t.closing, MaxInvertedIndexSize)
	return nil
}

// getPendingWrite returns the pending write cache for the table
// It will create a new cache if it does not exist
func getPendingWrite(tableId int) *PendingTableWrites {
	wp := pendingWrites[tableId]
	if wp == nil {
		wp = &PendingTableWrites{
			TableId:       tableId,
			InvertedIndex: make(map[string]RelatedDocs),
		}
		pendingWrites[tableId] = wp
	}

	return wp
}

// flushPendingWrites flushes the pending writes to the database
func flushPendingWrites(closing bool) {
	if !closing && time.Since(lastFlushWriteTime) < 1*time.Second {
		return
	}
	lastFlushWriteTime = time.Now()

	if closing {
		log.Println("[Inverted] Flushing pending writes...")
		defer func() {
			log.Println("[Inverted] Flushed pending writes")
		}()
	}

	batch := NewBatch(db)

	wordsCount := 0
	docsCount := 0
	for _, wp := range pendingWrites {
		for kw, relatedDocs := range wp.InvertedIndex {
			// Skip the keyword if it has been updated in the last 2 seconds
			// and has less than 50 documents
			if !closing && len(relatedDocs.DocIds) < FlushWaitBatchSize &&
				time.Since(relatedDocs.UpdatedAt) < FlushWaitTimeout {
				continue
			}

			wordsCount++
			docsCount += len(relatedDocs.DocIds)

			writeInvertedIndex(batch, wp.TableId, kw, relatedDocs.DocIds, nil)
			delete(wp.InvertedIndex, kw)

			// delete empty table
			if len(wp.InvertedIndex) == 0 {
				delete(pendingWrites, wp.TableId)
			}
		}
	}

	batch.Commit()
}

// getPendingDelete returns the pending delete cache for the table
// It will create a new cache if it does not exist
func getPendingDelete(tableId int) *PendingTableWrites {
	wp := pendingDeletes[tableId]
	if wp == nil {
		wp = &PendingTableWrites{
			TableId:       tableId,
			InvertedIndex: make(map[string]RelatedDocs),
		}
		pendingDeletes[tableId] = wp
	}

	return wp
}

func flushPendingDeletes(closing bool, maxKeywordIndexSize int) {
	if !closing && time.Since(lastFlushDeleteTime) < 1*time.Second {
		return
	}
	lastFlushDeleteTime = time.Now()

	if closing {
		log.Println("[Inverted] Flushing pending deletes...")
		defer func() {
			log.Println("[Inverted] Flushed pending deletes")
		}()
	}

	batch := NewBatch(db)

	for _, wp := range pendingDeletes {
		for kw, relatedDocs := range wp.InvertedIndex {
			// Skip the keyword if it has been updated in the last 2 seconds
			// and has less than 50 documents
			if !closing && len(relatedDocs.DocIds) < 50 && time.Since(relatedDocs.UpdatedAt) < 5*time.Second {
				continue
			}

			removeDocumentsFromInvertedIndex(batch, wp.TableId, kw, relatedDocs.DocIds, maxKeywordIndexSize)
			delete(wp.InvertedIndex, kw)

			// delete empty table
			if len(wp.InvertedIndex) == 0 {
				delete(pendingDeletes, wp.TableId)
			}
		}
	}

	batch.Commit()
}

// clearPendingWrites clears the pending writes for the table
// This function is not thread-safe and should be called in database mpsc queue
// It is used when the table is deleted
func clearPendingWrites(tableId int) {
	delete(pendingWrites, tableId)
	delete(pendingDeletes, tableId)
}
