package fulltext

import (
	"context"
	"log"
	"time"

	"github.com/ai-microsoft/haystack/server/core/pebble"
	"github.com/dustin/go-humanize"
)

type KeywordsMerger struct {
	shutdown   context.Context
	shutdownFn context.CancelFunc
	mergerDone chan struct{}
	merging    Merging
}

type Merging struct {
	StartTime            time.Time `json:"start_time"`
	NextMergeTime        time.Time `json:"next_merge_time"`
	WaitingForFlushCache bool      `json:"-"`
	NextIter             string    `json:"next_iter"`
	TotalKeywords        int       `json:"total_keywords"`
	TotalRowsBefore      int       `json:"total_rows_before"`
	TotalRowsAfter       int       `json:"total_rows_after"`
}

func (m *Merging) MergedRowCount() int {
	return m.TotalRowsBefore - m.TotalRowsAfter
}

type mergeKeywordTask struct {
	merging Merging
	done    chan Merging
}

func (km *KeywordsMerger) Shutdown() {
	km.shutdownFn()
}

func (km *KeywordsMerger) Wait() {
	<-km.mergerDone
}

func (km *KeywordsMerger) GetWait() <-chan struct{} {
	return km.mergerDone
}

func (km *KeywordsMerger) Start() {
	km.merging = Merging{
		NextIter: KeywordPrefix,
	}

	km.shutdown, km.shutdownFn = context.WithCancel(context.Background())
	km.mergerDone = make(chan struct{})
	go km.run()
}

func (km *KeywordsMerger) run() {
	log.Printf("Keywords merger: started")

	nextDelay := 300 * time.Second

	for {
		select {
		case <-km.shutdown.Done():
			log.Printf("Keywords merger: shutdown")
			close(km.mergerDone)
			return
		case <-time.After(nextDelay):
			if km.merging.NextIter == "" {
				km.merging = Merging{
					NextIter: KeywordPrefix,
				}

				log.Printf("Keywords merger: new scan started.")
			}
		}

		m := &mergeKeywordTask{
			merging: km.merging,
			done:    make(chan Merging),
		}

		before := km.merging
		writeQueue <- m
		km.merging = m.Wait()
		if !km.merging.WaitingForFlushCache && before.MergedRowCount() != km.merging.MergedRowCount() {
			// we've reached the end of the database
			log.Printf("Keywords merger: merged %s keywords, row count reduced: %s\n",
				humanize.Comma(int64(km.merging.TotalKeywords)),
				humanize.Comma(int64(km.merging.MergedRowCount()-before.MergedRowCount())))
		}

		nextDelay = 1 * time.Second
		if km.merging.WaitingForFlushCache {
			nextDelay = 5 * time.Second
		}

		if km.merging.NextIter == "" {
			m := km.merging
			if float32(m.TotalRowsBefore-m.TotalRowsAfter)/float32(m.TotalRowsBefore) > 0.25 {
				// we've merged a lot of keywords, so we need to
				// compact the database to free up space
				log.Printf("Keywords merger: scheduling compact")
				db.ScheduleCompact()
			}

			log.Printf("Keywords merge done, total keywords: %s", humanize.Comma(int64(km.merging.TotalKeywords)))
			// we've reached the end of the database
			// reset the nextIter to the beginning
			// and set a longer delay time
			nextDelay = 3600 * time.Second
		}
	}
}

func (m *mergeKeywordTask) Run() {
	if len(pendingWrites) != 0 {
		// There are pending writes, wo we need to wait
		// for them to be flushed before merging
		// the keywords index
		m.merging.WaitingForFlushCache = true
		m.done <- m.merging
		return
	}
	m.merging.WaitingForFlushCache = false
	m.done <- mergeKeywordsIndex(m.merging, MaxKeywordIndexSize)
}

func (m *mergeKeywordTask) Wait() Merging {
	defer close(m.done)
	return <-m.done
}

type RecordRow struct {
	Key      string
	Value    string
	DocCount int
}
type InvertedIndex struct {
	WorkspaceId string
	Keyword     string
	Rows        []RecordRow
	DocCount    int
}

var rewriteIndex = func(batch pebble.Batch, index *InvertedIndex, maxKeywordIndexSize int) int {
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
			for _, docid := range DecodeKeywordIndexValue(row.Value) {
				docids[docid] = struct{}{}
			}
		}

		ids := []string{}
		for id := range docids {
			ids = append(ids, id)
		}

		writeKeywordIndex(batch, index.WorkspaceId, index.Keyword, ids, nil)
		mergedCount++
	}

	return mergedCount
}

func mergeKeywordsIndex(m Merging, maxKeywordIndexSize int) Merging {
	now := time.Now()
	var isTimeout = func() bool {
		return time.Since(now) > 300*time.Millisecond
	}

	batch := NewBatch(db)
	lastWorkspaceId := ""
	current := &InvertedIndex{Rows: []RecordRow{}}
	nextIter := m.NextIter
	pending := []*InvertedIndex{}
	for {
		var next *InvertedIndex
		db.ScanRange([]byte(nextIter), append([]byte(KeywordPrefix), 0xff), func(key []byte, value []byte) bool {
			workspaceid, keyword, doccount, _ := DecodeKeywordIndexKey(string(key))
			if lastWorkspaceId == "" {
				lastWorkspaceId = workspaceid
				current.Keyword = keyword
				current.WorkspaceId = workspaceid
			}

			if lastWorkspaceId != workspaceid {
				lastWorkspaceId = workspaceid
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
					current.Rows = append(current.Rows, RecordRow{
						Key:      string(key),
						Value:    string(value),
						DocCount: doccount,
					})
					current.DocCount += doccount
					return true
				}
			}

			next = &InvertedIndex{
				WorkspaceId: workspaceid,
				Keyword:     keyword,
				Rows: append([]RecordRow{}, RecordRow{
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
			m.TotalRowsAfter += rewriteIndex(batch, c, maxKeywordIndexSize)
			m.TotalKeywords++
		}
		pending = []*InvertedIndex{}

		if next == nil {
			// we've reached the end of the database
			m.NextIter = ""
			break
		}

		if isTimeout() {
			// We'll reset NextIter to the next keyword
			m.NextIter = string(EncodeKeywordIndexKeyPrefix(next.WorkspaceId, next.Keyword))
			break
		}

		current = next
		next = nil
	}

	batch.Commit()
	return m
}
