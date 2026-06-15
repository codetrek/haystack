package invertedindex

import (
	"context"
	"log"
	"time"

	"github.com/codetrek/haystack/core/kv"
	"github.com/dustin/go-humanize"
)

type keywordsMerger struct {
	idx            *Index
	shutdown       context.Context
	shutdownFn     context.CancelFunc
	mergerDone     chan struct{}
	merging        merging
	InitialDelay   time.Duration // delay before the first merge scan (default 300s)
	CompletedDelay time.Duration // delay after a full scan completes (default 8h)
}

const (
	defaultInitialDelay   = 300 * time.Second
	defaultCompletedDelay = 8 * 3600 * time.Second
)

type merging struct {
	WaitingForFlushCache bool
	NextIter             string
	TotalKeywords        int
	TotalRowsBefore      int
	TotalRowsAfter       int
}

func (m *merging) MergedRowCount() int {
	return m.TotalRowsBefore - m.TotalRowsAfter
}

type mergeKeywordTask struct {
	idx     *Index
	merging merging
}

func (m *mergeKeywordTask) Run() error {
	if len(m.idx.pendingWrites) != 0 {
		// There are pending writes, so we need to wait
		// for them to be flushed before merging
		// the keywords index
		m.merging.WaitingForFlushCache = true
		return nil
	}
	m.merging.WaitingForFlushCache = false
	m.merging = m.idx.mergeKeywordsIndex(m.merging, m.idx.opts.maxInvertedIndexSize())
	return nil
}

func (km *keywordsMerger) Shutdown() {
	km.shutdownFn()
}

func (km *keywordsMerger) Wait() {
	<-km.mergerDone
}

func (km *keywordsMerger) GetWait() <-chan struct{} {
	return km.mergerDone
}

func (km *keywordsMerger) Start() {
	km.merging = merging{
		NextIter: string(km.idx.keyTypeRow),
	}

	km.shutdown, km.shutdownFn = context.WithCancel(context.Background())
	km.mergerDone = make(chan struct{})
	go km.run()
}

func (km *keywordsMerger) run() {
	log.Printf("[Inverted] Keywords merger: started")

	// Capture locals to avoid reading mutable struct fields after Shutdown.
	// km.merging is intentionally NOT captured as a local here: it is owned
	// exclusively by this single run() goroutine, so direct field access is
	// safe. Post-Wait reads by callers (e.g. tests) are safe because the
	// close(mergerDone) below provides the required happens-before guarantee.
	shutdown := km.shutdown
	idx := km.idx
	mergerDone := km.mergerDone

	nextDelay := km.InitialDelay
	if nextDelay == 0 {
		nextDelay = defaultInitialDelay
	}

	for {
		select {
		case <-shutdown.Done():
			log.Printf("[Inverted] Keywords merger: shutdown")
			close(mergerDone)
			return
		case <-time.After(nextDelay):
			if km.merging.NextIter == "" {
				km.merging = merging{
					NextIter: string(idx.keyTypeRow),
				}

				log.Printf("[Inverted] Keywords merger: new scan started.")
			}
		}

		m := &mergeKeywordTask{
			idx:     idx,
			merging: km.merging,
		}

		before := km.merging
		idx.q.RunTask(m)
		km.merging = m.merging

		if !km.merging.WaitingForFlushCache && before.MergedRowCount() != km.merging.MergedRowCount() {
			// we've reached the end of the database
			log.Printf("[Inverted] Keywords merger: merged %s keywords, row reduced: %s, total row reduced: %s\n",
				humanize.Comma(int64(km.merging.TotalKeywords)),
				humanize.Comma(int64(km.merging.MergedRowCount()-before.MergedRowCount())),
				humanize.Comma(int64(km.merging.MergedRowCount())))
		}

		nextDelay = 500 * time.Millisecond
		if km.merging.WaitingForFlushCache {
			nextDelay = 5 * time.Second
		}

		if km.merging.NextIter == "" {
			m := km.merging
			if float32(m.TotalRowsBefore-m.TotalRowsAfter)/float32(m.TotalRowsBefore) > 0.25 {
				// we've merged a lot of keywords, so we need to
				// compact the database to free up space
				log.Printf("[Inverted] Keywords merger: scheduling compact")
				idx.db.ScheduleCompact()
			}

			log.Printf("[Inverted] Keywords merge done, total keywords: %s", humanize.Comma(int64(km.merging.TotalKeywords)))
			// we've reached the end of the database
			// reset the nextIter to the beginning
			// and set a longer delay time
			nextDelay = km.CompletedDelay
			if nextDelay == 0 {
				nextDelay = defaultCompletedDelay
			}
		}
	}
}

type recordRow struct {
	Key      string
	Value    string
	DocCount int
}
type invertedIndexEntry struct {
	TableId  int
	Keyword  string
	Rows     []recordRow
	DocCount int
}

var rewriteIndex = func(batch kv.Batch, idx *Index, index *invertedIndexEntry, maxKeywordIndexSize int) int {
	if len(index.Rows) < 2 ||
		index.DocCount/len(index.Rows) > maxKeywordIndexSize {
		// We've already have a well batched keyword
		// so we don't need to merge it
		return len(index.Rows)
	}

	mergedCount := 0
	// Merge the keyword docids in old
	// and write the new keyword to the database
	rows := index.Rows
	remainingDocCount := index.DocCount
	for len(rows) > 1 {
		docids := map[string]struct{}{}
		for len(rows) > 0 && (len(docids) < maxKeywordIndexSize /* docs batched */ || remainingDocCount < max(maxKeywordIndexSize/5, 4) /* docs left */) {
			row := rows[0]
			rows = rows[1:]
			remainingDocCount -= row.DocCount

			batch.Delete([]byte(row.Key))
			for _, docid := range decodeInvertedValueStr(row.Value) {
				docids[docid] = struct{}{}
			}
		}

		ids := make([]string, 0, len(docids))
		for id := range docids {
			ids = append(ids, id)
		}

		key := idx.encodeInvertedKey(index.TableId, index.Keyword, len(ids))
		writeInvertedIndex(batch, index.TableId, index.Keyword, ids, key)
		mergedCount++
	}

	return mergedCount
}

func (idx *Index) mergeKeywordsIndex(m merging, maxKeywordIndexSize int) merging {
	now := time.Now()
	var isTimeout = func() bool {
		return time.Since(now) > 300*time.Millisecond
	}

	batch := newBatch(idx.db)
	lastTableId := -1
	current := &invertedIndexEntry{Rows: []recordRow{}}
	nextIter := m.NextIter
	pending := []*invertedIndexEntry{}
	for {
		var next *invertedIndexEntry
		idx.db.ScanRange([]byte(nextIter), append([]byte{idx.keyTypeRow}, 0xff), func(key []byte, value []byte) bool {
			tableId, keyword, doccount, _ := idx.decodeInvertedKey(string(key))
			if lastTableId == -1 {
				lastTableId = tableId
				current.Keyword = keyword
				current.TableId = tableId
			}

			if lastTableId != tableId {
				lastTableId = tableId
			} else {
				if doccount > maxKeywordIndexSize/2 {
					m.TotalRowsBefore += len(current.Rows)
					m.TotalRowsAfter += len(current.Rows)
					// Already well batched
					return true
				}

				if current.Keyword == keyword {
					// we've reached the same keyword
					// add the current key and value to the current keyword
					current.Rows = append(current.Rows, recordRow{
						Key:      string(key),
						Value:    string(value),
						DocCount: doccount,
					})
					current.DocCount += doccount
					return true
				}
			}

			next = &invertedIndexEntry{
				TableId: tableId,
				Keyword: keyword,
				Rows: append([]recordRow{}, recordRow{
					Key:      string(key),
					Value:    string(value),
					DocCount: doccount,
				}),
				DocCount: doccount,
			}

			pending = append(pending, current)
			if !isTimeout() && len(pending) < 500 {
				current = next
				next = nil
				return true
			}

			current = nil
			nextIter = string(append(key, 0x01))
			return false
		})

		if current != nil && current.Keyword != "" {
			// we've reached the end of the database
			// so we need to merge the current keyword
			pending = append(pending, current)
		}

		for _, c := range pending {
			m.TotalRowsBefore += len(c.Rows)
			m.TotalRowsAfter += rewriteIndex(batch, idx, c, maxKeywordIndexSize)
			m.TotalKeywords++
		}
		pending = []*invertedIndexEntry{}

		if next == nil {
			// we've reached the end of the database
			m.NextIter = ""
			break
		}

		if isTimeout() {
			// We'll reset NextIter to the next keyword
			m.NextIter = string(idx.encodeInvertedKeyPrefix(next.TableId, next.Keyword))
			break
		}

		current = next
		next = nil
	}

	batch.Commit()
	return m
}
