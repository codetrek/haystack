// Package collection provides an instance-based Catalog that manages named
// collections of documents. It composes a documents.Store for per-collection
// document storage and an invertedindex.Index (via documents.Store) for
// full-text indexing.
//
// # On-disk layout
//
// Two key-type bytes control the catalog's own keys (the documents.Store uses
// separate key-type bytes, 10–13, for its own records):
//
//   - KeyTypeIncrId  (default 1): single key holding the auto-increment counter.
//   - KeyTypeRecord  (default 2): one key per collection, value is JSON-encoded Record.
//
// These defaults match the legacy haystack workspace registry so existing data
// is readable without migration.
package collection

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/codetrek/haystack/searchcore/documents"
	"github.com/codetrek/haystack/searchcore/kv"
)

// Record is the persisted metadata for one collection.
// All fields are JSON-encoded and stored under a single key-value entry.
type Record struct {
	// ID is the numeric identifier assigned by the Catalog at creation time.
	ID int `json:"id"`

	// Name is the unique logical name for this collection. Haystack uses the
	// workspace's absolute path as the name.
	Name string `json:"name"`

	// Desc is an optional human-readable description.
	Desc string `json:"desc,omitempty"`

	// CreatedAt is the wall-clock time when the collection was created.
	CreatedAt time.Time `json:"created_at"`

	// LastAccessed is updated each time the collection is opened or accessed.
	LastAccessed time.Time `json:"last_accessed"`

	// LastFullSync is updated by the caller when a full re-index is completed.
	LastFullSync time.Time `json:"last_full_sync"`

	// Extra is an opaque, consumer-defined payload. Haystack uses this to store
	// filter configuration (e.g. file glob patterns).
	Extra []byte `json:"extra,omitempty"`
}

// Options configures the Catalog's on-disk key-type bytes. Zero values select
// production defaults (1 and 2). Byte 0 is reserved as the "use default"
// sentinel; it cannot be used as a real key-type prefix.
//
// Changing any KeyType* field after data has been written is a breaking
// on-disk change.
type Options struct {
	// KeyTypeIncrId is the on-disk prefix byte for the auto-increment id key.
	// Zero selects DefaultKeyTypeIncrId (1).
	KeyTypeIncrId byte

	// KeyTypeRecord is the on-disk prefix byte for collection record keys.
	// Zero selects DefaultKeyTypeRecord (2).
	KeyTypeRecord byte
}

// Catalog manages named collections by composing a documents.Store. It
// maintains an in-memory index (name→id and id→*Record) that is populated at
// construction time and kept in sync with all mutations.
type Catalog struct {
	db   kv.Store
	docs *documents.Store

	keyTypeIncrId byte
	keyTypeRecord byte

	mu     sync.RWMutex
	byID   map[int]*Record
	byName map[string]int
}

// New creates a Catalog backed by the given kv.Store and documents.Store.
// It scans all persisted records (key-type 2 by default) into the in-memory
// index so that Get/GetByName/List are immediately available without additional
// disk reads.
func New(store kv.Store, docs *documents.Store, opts Options) (*Catalog, error) {
	if opts.KeyTypeIncrId == 0 {
		opts.KeyTypeIncrId = DefaultKeyTypeIncrId
	}
	if opts.KeyTypeRecord == 0 {
		opts.KeyTypeRecord = DefaultKeyTypeRecord
	}
	if opts.KeyTypeIncrId == opts.KeyTypeRecord {
		return nil, fmt.Errorf("collection.New: KeyTypeIncrId and KeyTypeRecord must differ (both %d)", opts.KeyTypeIncrId)
	}

	c := &Catalog{
		db:            store,
		docs:          docs,
		keyTypeIncrId: opts.KeyTypeIncrId,
		keyTypeRecord: opts.KeyTypeRecord,
		byID:          make(map[int]*Record),
		byName:        make(map[string]int),
	}

	if err := c.loadFromStore(); err != nil {
		return nil, fmt.Errorf("collection.New: failed to load existing records: %w", err)
	}

	return c, nil
}

// loadFromStore scans all persisted collection records and populates the
// in-memory index. Called once from New; must not be called after
// concurrent access begins.
func (c *Catalog) loadFromStore() error {
	return c.db.Scan(c.encodeRecordScanPrefix(), func(key, value []byte) bool {
		r, err := unmarshalRecord(value)
		if err != nil {
			// Skip corrupted records; they won't appear in the in-memory index.
			log.Printf("[Collection] Skipping corrupted record key %q: %v", string(key), err)
			return true
		}
		if r.Name == "" {
			// Defensive: a record with an empty name is either corrupt or an
			// un-migrated legacy record. Indexing it would poison byName[""]
			// and let an unrelated GetByName("") collide. Skip it.
			log.Printf("[Collection] Skipping record key %q with empty name (id=%d)", string(key), r.ID)
			return true
		}
		c.byID[r.ID] = r
		c.byName[r.Name] = r.ID
		return true
	})
}

// Create allocates a new collection with the given name, persists its Record,
// and creates the backing document table in the documents.Store. Returns an
// error if the name is already in use.
func (c *Catalog) Create(name string) (*Collection, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.byName[name]; exists {
		return nil, fmt.Errorf("collection %q already exists", name)
	}

	id, err := c.db.GetIncrementalId(c.encodeIncrIdKey())
	if err != nil {
		return nil, fmt.Errorf("collection.Create: failed to allocate id: %w", err)
	}

	now := time.Now()
	r := &Record{
		ID:           id,
		Name:         name,
		CreatedAt:    now,
		LastAccessed: now,
	}

	if err := c.persistRecord(r); err != nil {
		return nil, fmt.Errorf("collection.Create: failed to persist record: %w", err)
	}

	if err := c.docs.Create(id, name); err != nil {
		// Best-effort cleanup: remove the persisted key so the id isn't orphaned.
		if delErr := c.db.Delete(c.encodeRecordKey(id)); delErr != nil {
			log.Printf("[Collection] Create rollback failed to delete orphaned record %d: %v", id, delErr)
		}
		return nil, fmt.Errorf("collection.Create: failed to create document table: %w", err)
	}

	c.byID[id] = r
	c.byName[name] = id

	return &Collection{catalog: c, id: id}, nil
}

// Get returns a Collection handle for the given id. It uses the in-memory
// index and does not read from disk. Returns an error if the id is not found.
func (c *Catalog) Get(id int) (*Collection, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if _, ok := c.byID[id]; !ok {
		return nil, fmt.Errorf("collection.Get: id %d not found", id)
	}
	return &Collection{catalog: c, id: id}, nil
}

// GetByName returns a Collection handle for the given name. It uses the
// in-memory index and does not read from disk. Returns an error if the name
// is not found.
func (c *Catalog) GetByName(name string) (*Collection, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	id, ok := c.byName[name]
	if !ok {
		return nil, fmt.Errorf("collection.GetByName: %q not found", name)
	}
	return &Collection{catalog: c, id: id}, nil
}

// List returns a snapshot of all collection records. The returned slice and
// every Record in it are independent copies; mutating them does not affect the
// Catalog's internal state. The returned slice is always non-nil.
func (c *Catalog) List() []*Record {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]*Record, 0, len(c.byID))
	for _, r := range c.byID {
		out = append(out, copyRecord(r))
	}
	return out
}

// Delete removes a collection by id. It first removes the on-disk Record and
// de-indexes the collection from the in-memory maps (under the lock), then
// purges the backing document data via documents.Store.Delete (outside the
// lock, since that call blocks on a queue). Removing the record first ensures a
// partial failure cannot leave a live record pointing at deleted document data;
// if the document purge fails the orphaned data is logged. Returns an error if
// the id is not found.
func (c *Catalog) Delete(id int) error {
	c.mu.Lock()

	r, ok := c.byID[id]
	if !ok {
		c.mu.Unlock()
		return fmt.Errorf("collection.Delete: id %d not found", id)
	}

	if err := c.db.Delete(c.encodeRecordKey(id)); err != nil {
		c.mu.Unlock()
		return fmt.Errorf("collection.Delete: failed to delete record key: %w", err)
	}

	delete(c.byName, r.Name)
	delete(c.byID, id)
	c.mu.Unlock()

	// Purge document data outside the lock: documents.Store.Delete blocks on the
	// shared queue, and the record is already gone from the catalog.
	if err := c.docs.Delete(id); err != nil {
		log.Printf("[Collection] Delete: record %d removed but document data orphaned: %v", id, err)
		return fmt.Errorf("collection.Delete: failed to delete document data: %w", err)
	}

	return nil
}

// Save updates an existing collection record (e.g. changed Name, Extra, or
// timestamps). The record's ID must already exist in the Catalog. If the Name
// changed, it must not collide with another collection's name. It re-persists
// the record and updates the in-memory index.
func (c *Catalog) Save(r *Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	old, ok := c.byID[r.ID]
	if !ok {
		return fmt.Errorf("collection.Save: id %d not found", r.ID)
	}

	// Guard against renaming into a name already owned by another collection.
	if old.Name != r.Name {
		if _, taken := c.byName[r.Name]; taken {
			return fmt.Errorf("collection.Save: name %q already in use", r.Name)
		}
	}

	if err := c.persistRecord(r); err != nil {
		return fmt.Errorf("collection.Save: failed to persist record: %w", err)
	}

	// Update in-memory name index if the name changed.
	if old.Name != r.Name {
		delete(c.byName, old.Name)
		c.byName[r.Name] = r.ID
	}

	// Make an independent copy so future mutations to r don't corrupt the index.
	saved := copyRecord(r)
	c.byID[r.ID] = saved

	return nil
}

// persistRecord JSON-encodes r and writes it to the kv.Store.
func (c *Catalog) persistRecord(r *Record) error {
	// Record contains only JSON-safe fields (ints, strings, time.Time, []byte),
	// so json.Marshal cannot fail; ignoring the error keeps this fully testable.
	data, _ := json.Marshal(r)
	return c.db.Put(c.encodeRecordKey(r.ID), data)
}

// copyRecord returns a deep copy of r (the Extra slice is copied).
func copyRecord(r *Record) *Record {
	cp := *r
	if r.Extra != nil {
		cp.Extra = make([]byte, len(r.Extra))
		copy(cp.Extra, r.Extra)
	}
	return &cp
}

// Collection is a lightweight handle bound to a single collection id.
// Document operations delegate to the composed documents.Store scoped to that id.
type Collection struct {
	catalog *Catalog
	id      int
}

// ID returns the numeric collection identifier.
func (c *Collection) ID() int {
	return c.id
}

// Meta returns a snapshot copy of the collection's Record. The returned value
// is safe to mutate; it does not alias the Catalog's internal state.
func (c *Collection) Meta() *Record {
	c.catalog.mu.RLock()
	r := c.catalog.byID[c.id]
	c.catalog.mu.RUnlock()
	if r == nil {
		return &Record{ID: c.id}
	}
	return copyRecord(r)
}

// Save saves new documents to this collection.
// It delegates to documents.Store.SaveNewDocuments scoped to this collection's id.
func (c *Collection) Save(docs []*documents.Document) error {
	return c.catalog.docs.SaveNewDocuments(c.id, docs)
}

// Update updates existing documents in this collection.
// It delegates to documents.Store.UpdateDocuments scoped to this collection's id.
func (c *Collection) Update(docs []*documents.Document) error {
	return c.catalog.docs.UpdateDocuments(c.id, docs)
}

// DeleteDocument removes a single document from this collection.
// It delegates to documents.Store.DeleteDocument scoped to this collection's id.
func (c *Collection) DeleteDocument(docID string) error {
	return c.catalog.docs.DeleteDocument(c.id, docID)
}

// GetDocument retrieves a document from this collection.
// If includeWords is true, the document's Words field is populated.
// Returns nil, nil if the document does not exist.
func (c *Collection) GetDocument(docID string, includeWords bool) (*documents.Document, error) {
	return c.catalog.docs.GetDocument(c.id, docID, includeWords)
}

// Count returns the number of documents in this collection.
// It delegates to documents.Store.CountByCollection scoped to this collection's id.
func (c *Collection) Count() int {
	return c.catalog.docs.CountByCollection(c.id)
}

// ScanFiles iterates over all document path entries in this collection, calling
// cb(docid, relPath) for each one. Returning false from cb stops the scan.
func (c *Collection) ScanFiles(cb func(docid, relPath string) bool) {
	c.catalog.docs.ScanFiles(c.id, cb)
}
