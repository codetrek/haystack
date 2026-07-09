package invertedindex

import (
	"log"
	"time"
)

type relatedDocs struct {
	DocIds    []int64
	UpdatedAt time.Time
}

type pendingTableWrites struct {
	TableId int

	// Map of keyword to document ids
	InvertedIndex map[string]relatedDocs
}

type flushPendingWritesTask struct {
	idx     *Index
	closing bool
}

func (t *flushPendingWritesTask) Run() error {
	// Flush pending writes to the database. A closing flush forces everything out
	// (force = closing); the periodic ticker flush respects the per-keyword
	// batch/age thresholds and the cooldown.
	t.idx.flushPendingWrites(t.closing, t.closing)
	t.idx.flushPendingDeletes(t.closing, t.closing, t.idx.opts.maxInvertedIndexSize())
	return nil
}

// maybeFlushOnPressure forces a full flush of the write and/or delete caches when
// either exceeds the configured doc-id budget, bounding build-phase RSS. It runs
// on the mpsc worker (called from updateIndex/removeIndex), the same single
// thread that owns the caches and runs flush*, so the inline flush is safe.
func (idx *Index) maybeFlushOnPressure() {
	max := idx.opts.maxPendingPostings()
	if max <= 0 {
		return
	}
	if idx.pendingWritePostings >= max {
		idx.flushPendingWrites(false, true)
	}
	if idx.pendingDeletePostings >= max {
		idx.flushPendingDeletes(false, true, idx.opts.maxInvertedIndexSize())
	}
}

// getPendingWrite returns the pending write cache for the table.
// It will create a new cache if it does not exist.
func (idx *Index) getPendingWrite(tableId int) *pendingTableWrites {
	wp := idx.pendingWrites[tableId]
	if wp == nil {
		wp = &pendingTableWrites{
			TableId:       tableId,
			InvertedIndex: make(map[string]relatedDocs),
		}
		idx.pendingWrites[tableId] = wp
	}

	return wp
}

// flushPendingWrites flushes the pending writes to the database. When force is
// true (memory-pressure or closing flush) the cooldown and the per-keyword
// batch/age thresholds are bypassed so the entire write cache is drained.
func (idx *Index) flushPendingWrites(closing, force bool) {
	if !closing && !force && time.Since(idx.lastFlushWriteTime) < idx.opts.flushCooldown() {
		return
	}
	now := time.Now()
	idx.lastFlushWriteTime = now

	if closing {
		log.Println("[Inverted] Flushing pending writes...")
		defer func() {
			log.Println("[Inverted] Flushed pending writes")
		}()
	}

	batch := newBatch(idx.db)

	flushWaitTimeout := idx.opts.flushWaitTimeout()
	flushWaitBatchSize := idx.opts.flushWaitBatchSize()

	for _, wp := range idx.pendingWrites {
		for kw, relatedDocs := range wp.InvertedIndex {
			// Skip young, small keyword entries on a periodic flush; a forced or
			// closing flush drains them regardless.
			if !closing && !force && len(relatedDocs.DocIds) < flushWaitBatchSize &&
				time.Since(relatedDocs.UpdatedAt) < flushWaitTimeout {
				continue
			}

			writeInvertedIndex(batch, wp.TableId, kw, relatedDocs.DocIds, idx.encodeInvertedKey(wp.TableId, kw, len(relatedDocs.DocIds), now.UnixMicro()))
			idx.pendingWritePostings -= len(relatedDocs.DocIds)
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
func (idx *Index) getPendingDelete(tableId int) *pendingTableWrites {
	wp := idx.pendingDeletes[tableId]
	if wp == nil {
		wp = &pendingTableWrites{
			TableId:       tableId,
			InvertedIndex: make(map[string]relatedDocs),
		}
		idx.pendingDeletes[tableId] = wp
	}

	return wp
}

func (idx *Index) flushPendingDeletes(closing, force bool, maxKeywordIndexSize int) {
	if !closing && !force && time.Since(idx.lastFlushDeleteTime) < idx.opts.flushCooldown() {
		return
	}
	idx.lastFlushDeleteTime = time.Now()

	if closing {
		log.Println("[Inverted] Flushing pending deletes...")
		defer func() {
			log.Println("[Inverted] Flushed pending deletes")
		}()
	}

	batch := newBatch(idx.db)

	for _, wp := range idx.pendingDeletes {
		for kw, relatedDocs := range wp.InvertedIndex {
			// Skip young, small delete entries on a periodic flush; a forced or
			// closing flush drains them regardless.
			if !closing && !force && len(relatedDocs.DocIds) < idx.opts.flushDeleteWaitBatchSize() &&
				time.Since(relatedDocs.UpdatedAt) < idx.opts.flushDeleteWaitTimeout() {
				continue
			}

			err := idx.removeDocumentsFromInvertedIndex(batch, wp.TableId, kw, relatedDocs.DocIds, maxKeywordIndexSize)
			if err != nil {
				log.Printf("[Inverted] Error removing documents from inverted index: %v", err)
			}
			idx.pendingDeletePostings -= len(relatedDocs.DocIds)
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
	// Keep the global doc-id counters accurate: subtract whatever this table had
	// buffered before dropping its caches (these docs are never flushed).
	if wp := idx.pendingWrites[tableId]; wp != nil {
		for _, rd := range wp.InvertedIndex {
			idx.pendingWritePostings -= len(rd.DocIds)
		}
	}
	if wp := idx.pendingDeletes[tableId]; wp != nil {
		for _, rd := range wp.InvertedIndex {
			idx.pendingDeletePostings -= len(rd.DocIds)
		}
	}
	delete(idx.pendingWrites, tableId)
	delete(idx.pendingDeletes, tableId)
}
