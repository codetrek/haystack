package invertedindex

import (
	"log"
	"time"
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
	idx     *Index
	closing bool
}

func (t *flushPendingWritesTask) Run() error {
	// Flush pending writes to the database
	t.idx.flushPendingWrites(t.closing)
	t.idx.flushPendingDeletes(t.closing, t.idx.opts.maxInvertedIndexSize())
	return nil
}

// getPendingWrite returns the pending write cache for the table.
// It will create a new cache if it does not exist.
func (idx *Index) getPendingWrite(tableId int) *PendingTableWrites {
	wp := idx.pendingWrites[tableId]
	if wp == nil {
		wp = &PendingTableWrites{
			TableId:       tableId,
			InvertedIndex: make(map[string]RelatedDocs),
		}
		idx.pendingWrites[tableId] = wp
	}

	return wp
}

// flushPendingWrites flushes the pending writes to the database.
func (idx *Index) flushPendingWrites(closing bool) {
	if !closing && time.Since(idx.lastFlushWriteTime) < idx.opts.flushCooldown() {
		return
	}
	idx.lastFlushWriteTime = time.Now()

	if closing {
		log.Println("[Inverted] Flushing pending writes...")
		defer func() {
			log.Println("[Inverted] Flushed pending writes")
		}()
	}

	batch := NewBatch(idx.db)

	flushWaitTimeout := idx.opts.flushWaitTimeout()
	flushWaitBatchSize := idx.opts.flushWaitBatchSize()

	wordsCount := 0
	docsCount := 0
	for _, wp := range idx.pendingWrites {
		for kw, relatedDocs := range wp.InvertedIndex {
			// Skip the keyword if it has been updated in the last 2 seconds
			// and has less than 50 documents
			if !closing && len(relatedDocs.DocIds) < flushWaitBatchSize &&
				time.Since(relatedDocs.UpdatedAt) < flushWaitTimeout {
				continue
			}

			wordsCount++
			docsCount += len(relatedDocs.DocIds)

			writeInvertedIndex(batch, wp.TableId, kw, relatedDocs.DocIds, nil)
			delete(wp.InvertedIndex, kw)

			// delete empty table
			if len(wp.InvertedIndex) == 0 {
				delete(idx.pendingWrites, wp.TableId)
			}
		}
	}

	batch.Commit()
}

// getPendingDelete returns the pending delete cache for the table.
// It will create a new cache if it does not exist.
func (idx *Index) getPendingDelete(tableId int) *PendingTableWrites {
	wp := idx.pendingDeletes[tableId]
	if wp == nil {
		wp = &PendingTableWrites{
			TableId:       tableId,
			InvertedIndex: make(map[string]RelatedDocs),
		}
		idx.pendingDeletes[tableId] = wp
	}

	return wp
}

func (idx *Index) flushPendingDeletes(closing bool, maxKeywordIndexSize int) {
	if !closing && time.Since(idx.lastFlushDeleteTime) < idx.opts.flushCooldown() {
		return
	}
	idx.lastFlushDeleteTime = time.Now()

	if closing {
		log.Println("[Inverted] Flushing pending deletes...")
		defer func() {
			log.Println("[Inverted] Flushed pending deletes")
		}()
	}

	batch := NewBatch(idx.db)

	for _, wp := range idx.pendingDeletes {
		for kw, relatedDocs := range wp.InvertedIndex {
			// Skip the keyword if it has been updated in the last 2 seconds
			// and has less than 50 documents
			if !closing && len(relatedDocs.DocIds) < 50 && time.Since(relatedDocs.UpdatedAt) < 5*time.Second {
				continue
			}

			err := idx.removeDocumentsFromInvertedIndex(batch, wp.TableId, kw, relatedDocs.DocIds, maxKeywordIndexSize)
			if err != nil {
				log.Printf("[Inverted] Error removing documents from inverted index: %v", err)
			}
			delete(wp.InvertedIndex, kw)

			// delete empty table
			if len(wp.InvertedIndex) == 0 {
				delete(idx.pendingDeletes, wp.TableId)
			}
		}
	}

	batch.Commit()
}

// clearPendingWrites clears the pending writes for the table.
// This function is not thread-safe and should be called in database mpsc queue.
// It is used when the table is deleted.
func (idx *Index) clearPendingWrites(tableId int) {
	delete(idx.pendingWrites, tableId)
	delete(idx.pendingDeletes, tableId)
}
