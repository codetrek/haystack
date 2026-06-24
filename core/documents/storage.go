// Package documents provides per-collection document storage that composes an
// inverted index, persisting document metadata, keywords, and path information
// in a kv.Store with async writes serialized through a shared queue.
package documents

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/codetrek/haystack/core/idtable"
	"github.com/codetrek/haystack/core/invertedindex"
	"github.com/codetrek/haystack/core/kv"
	"github.com/codetrek/haystack/core/queue"
)

// CollectionInfo holds per-collection metadata persisted in the key-value store.
// It is returned by GetCollection and exposes the collection's identifiers and
// description as JSON-tagged fields. The json tags are stable on-disk keys and
// must not change.
type CollectionInfo struct {
	CollectionID int        `json:"workspace_id"`
	InvertedId   int        `json:"inverted_id"`
	Desc         string     `json:"desc"`
	CreateAt     *time.Time `json:"create_at"`
}

// Options holds tunables for Store. Zero values select production defaults.
//
// The KeyType* fields select the single on-disk key-type prefix byte for each
// kind of key. Byte 0 (NUL) is reserved and cannot be selected by a consumer:
// a zero field means "use the default" (NUL is a poor key prefix, and
// reserving it lets the zero value double as the default sentinel). To use a
// custom prefix, pick any non-zero byte.
//
// Changing any KeyType* field after data has been written is a breaking
// on-disk change.
type Options struct {
	// KeyTypeDocCollection is the on-disk prefix byte for collection metadata keys.
	// Zero selects DefaultKeyTypeDocCollection (10).
	KeyTypeDocCollection byte

	// KeyTypeDocMeta is the on-disk prefix byte for document metadata keys.
	// Zero selects DefaultKeyTypeDocMeta (12).
	KeyTypeDocMeta byte

	// KeyTypeDocPath is the on-disk prefix byte for document path keys.
	// Zero selects DefaultKeyTypeDocPath (13).
	KeyTypeDocPath byte
}

// Store is the instance-based document store. It persists document metadata
// and path information in a kv.Store, and optionally maintains a linked
// inverted index (any invertedindex.Indexer) for full-text search.
//
// The document's tokenized keywords are NOT persisted here: the inverted index
// owns the forward map (its source of truth for a doc's current keywords), so
// the store passes the doc's CURRENT keyword set to the index on every mutation
// and lets the index diff against its own forward map.
type Store struct {
	db  kv.Store
	q   queue.Queue
	idx invertedindex.Indexer

	// resolved on-disk key-type bytes (set in New from opts with defaults applied)
	keyTypeDocCollection byte
	keyTypeDocMeta       byte
	keyTypeDocPath       byte

	collectionsMu      sync.Mutex
	collections        map[int]*CollectionInfo
	deletedCollections map[int]struct{}

	docCountMu sync.RWMutex
	docCount   map[int]int // collectionID -> document count
}

// New creates a new Store backed by the given kv.Store and queue.Queue.
// idx may be nil in tests or configurations that do not exercise index-linked
// paths; when non-nil it is notified of all document mutations so it stays in
// sync with the kv.Store. idx is the storage-agnostic invertedindex.Indexer
// seam, so the store runs unchanged on either the pebble-backed invertedindex
// (via invertedindex.NewIndexerAdapter) or the segment-based invertedstore.Store.
func New(store kv.Store, q queue.Queue, idx invertedindex.Indexer, opts Options) (*Store, error) {
	// Apply key-type defaults (zero means "use default").
	if opts.KeyTypeDocCollection == 0 {
		opts.KeyTypeDocCollection = DefaultKeyTypeDocCollection
	}
	if opts.KeyTypeDocMeta == 0 {
		opts.KeyTypeDocMeta = DefaultKeyTypeDocMeta
	}
	if opts.KeyTypeDocPath == 0 {
		opts.KeyTypeDocPath = DefaultKeyTypeDocPath
	}

	s := &Store{
		db:                   store,
		q:                    q,
		idx:                  idx,
		keyTypeDocCollection: opts.KeyTypeDocCollection,
		keyTypeDocMeta:       opts.KeyTypeDocMeta,
		keyTypeDocPath:       opts.KeyTypeDocPath,
		collections:          make(map[int]*CollectionInfo),
		deletedCollections:   make(map[int]struct{}),
		docCount:             make(map[int]int),
	}
	log.Println("[Documents] Initialized")
	return s, nil
}

func (s *Store) isCollectionDeleted(collectionID int) bool {
	s.collectionsMu.Lock()
	defer s.collectionsMu.Unlock()

	_, ok := s.deletedCollections[collectionID]
	return ok
}

func (s *Store) markCollectionDeleted(collectionID int) {
	s.collectionsMu.Lock()
	defer s.collectionsMu.Unlock()

	s.deletedCollections[collectionID] = struct{}{}
}

// CloseAndWait flushes any pending work through the queue and releases
// in-memory collection state. The caller must not use the Store after this
// returns.
func (s *Store) CloseAndWait() {
	s.q.RunTask(&queue.NopeTask{})

	s.collectionsMu.Lock()
	s.collections = nil
	s.deletedCollections = nil
	s.collectionsMu.Unlock()

	s.docCountMu.Lock()
	s.docCount = nil
	s.docCountMu.Unlock()

	log.Println("[Documents] Closed")
}

// Create initialises a new collection in the store, allocating an inverted-index
// table for it and seeding the in-memory document counter.
func (s *Store) Create(collectionID int, desc string) error {
	inverted, err := s.indexCreateTable(fmt.Sprintf("workspace:%d,desc:%s", collectionID, desc))
	if err != nil {
		return fmt.Errorf("failed to create inverted index table: %w", err)
	}

	ft := CollectionInfo{
		CollectionID: collectionID,
		InvertedId:   inverted,
		Desc:         desc,
	}

	err = s.db.Put(s.encodeMetaKey(collectionID), encodeFTMetaValue(ft))
	if err != nil {
		return err
	}

	// Initialize the in-memory document count by scanning the DB once
	prefix := s.encodeDocumentMetaKey(collectionID, "")
	count := 0
	s.db.Scan(prefix, func(key, value []byte) bool {
		count++
		return true
	})

	s.docCountMu.Lock()
	s.docCount[collectionID] = count
	s.docCountMu.Unlock()

	return nil
}

// Delete deletes a collection and all of its documents and keywords.
//
// indexDeleteTable runs OUTSIDE the queue task, exactly like Create runs
// indexCreateTable outside any task: an Indexer's DeleteTable may itself block
// on the same mpsc worker (invertedstore.DeleteTable does q.RunFunc), so calling
// it from inside s.q.RunFunc would nest RunFunc-in-RunFunc and deadlock the
// single worker when the store and the index share a queue (the production
// wiring does). The kv cleanup + count update stay serialized on the queue.
func (s *Store) Delete(collectionID int) error {
	ft, err := s.GetCollection(collectionID)
	if err != nil {
		return fmt.Errorf("failed to get collection: %w", err)
	}
	s.markCollectionDeleted(collectionID)

	s.indexDeleteTable(ft.InvertedId)

	return s.q.RunFunc(func() error {
		batch := s.db.NewBatch(0)
		batch.DeletePrefix(s.encodeDocumentMetaKey(collectionID, ""))

		err := batch.Commit()
		if err != nil {
			return err
		}

		// Clean up in-memory document count for this collection
		s.docCountMu.Lock()
		delete(s.docCount, collectionID)
		s.docCountMu.Unlock()

		return nil
	})
}

// CountByCollection returns the number of documents for a given collection ID
// using an in-memory counter maintained by document mutations. O(1).
func (s *Store) CountByCollection(collectionID int) int {
	s.docCountMu.RLock()
	defer s.docCountMu.RUnlock()
	return s.docCount[collectionID]
}

// GetCollection retrieves the collection information for a given collection ID.
// Results are cached in memory after the first lookup.
func (s *Store) GetCollection(collectionID int) (*CollectionInfo, error) {
	s.collectionsMu.Lock()
	defer s.collectionsMu.Unlock()
	if f, ok := s.collections[collectionID]; ok {
		return f, nil
	}

	meta, err := s.db.Get(s.encodeMetaKey(collectionID))
	if err != nil {
		return nil, fmt.Errorf("failed to get collection meta, collection: %d, error: %w", collectionID, err)
	}

	f, err := decodeFTMetaValue(meta)
	if err != nil {
		return nil, fmt.Errorf("failed to decode collection meta, collection: %d, error: %w", collectionID, err)
	}

	s.collections[collectionID] = f

	return f, nil
}

// indexCreateTable is the seam that isolates the inverted-index notification
// for collection creation. A future second index type is an additive change here.
func (s *Store) indexCreateTable(name string) (int, error) {
	if s.idx == nil {
		return 0, nil
	}
	return s.idx.CreateTable(name)
}

// indexDeleteTable is the seam for collection deletion notification.
func (s *Store) indexDeleteTable(tableId int) {
	if s.idx == nil {
		return
	}
	s.idx.DeleteTable(tableId)
}

// indexDocument is the seam for a single per-document index update. words is the
// doc's CURRENT full keyword set (empty/nil ⇒ delete the doc from the index). The
// index owns the forward map and diffs against it, so NO oldWords is passed —
// this is the invertedstore contract (design §4) that lets the store drop its
// doc-words machinery. It MUST be called OUTSIDE any s.q worker task (Update
// enqueues onto the shared queue; see indexDocuments).
func (s *Store) indexDocument(tableId int, docId string, words []string) {
	if s.idx == nil {
		return
	}
	// The inverted index keys postings by the docid's int64 value; docId here is
	// its canonical 8-byte string form (as produced by idtable.GetId and used for
	// the document-store keys), so decode it at this boundary.
	s.idx.Update(tableId, idtable.DecodeId(docId), words)
}
