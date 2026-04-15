package vectorindex

import (
	"container/list"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sync"

	"github.com/cockroachdb/pebble"
	"github.com/viterin/vek/vek32"
)

// Key type prefixes for vector index data in Pebble.
const (
	prefixMeta  = byte(40)
	prefixVec   = byte(41)
	prefixNode  = byte(42)
	prefixNb    = byte(43)
	prefixMap   = byte(44)
	prefixIDSeq = byte(45)
	prefixNorm  = byte(46)
)

// NodeStore defines the persistence interface for HNSW graph nodes.
type NodeStore interface {
	GetVector(id uint64) ([]float32, error)
	// GetVectorRef returns the vector without copying. The caller MUST NOT
	// modify the returned slice. Use GetVector when a mutable copy is needed.
	GetVectorRef(id uint64) ([]float32, error)
	PutNode(id uint64, level int, vector []float32) error
	DeleteNode(id uint64) error
	GetNeighbors(id uint64, layer int) ([]uint64, error)
	SetNeighbors(id uint64, layer int, neighbors []uint64) error
	GetEntryPoint() (uint64, int, error)
	SetEntryPoint(id uint64, maxLayer int) error
	GetNodeLevel(id uint64) (int, error)
	GetNodeId(docId string) (uint64, bool, error)
	SetNodeMapping(docId string, nodeId uint64) error
	DeleteNodeMapping(docId string) error
	NextNodeId() (uint64, error)
	// GetNorm returns the precomputed L2 norm for a node's vector.
	GetNorm(id uint64) (float32, error)
	// SetNorm stores a precomputed L2 norm for a node's vector.
	SetNorm(id uint64, norm float32) error
	Close() error
}

// BatchableStore defines optional batching support for NodeStore implementations.
type BatchableStore interface {
	BeginBatch()
	CommitBatch(sync bool) error
	DiscardBatch()
	BatchDepth() int
}

// pebbleReader is satisfied by both *pebble.DB and *pebble.Batch (indexed).
type pebbleReader interface {
	Get(key []byte) ([]byte, io.Closer, error)
}

// pebbleWriter is satisfied by both *pebble.DB and *pebble.Batch.
type pebbleWriter interface {
	Set(key, value []byte, opts *pebble.WriteOptions) error
	Delete(key []byte, opts *pebble.WriteOptions) error
}

// vectorCacheEntry stores the key and value for the LRU cache.
type vectorCacheEntry struct {
	id  uint64
	vec []float32
}

// PebbleNodeStore implements NodeStore backed by a Pebble database.
type PebbleNodeStore struct {
	db      *pebble.DB
	tableId int
	idMu    sync.Mutex // protects NextNodeId read-modify-write

	// Batch support
	pendingBatch *pebble.Batch
	batchDepth   int
	batchMu      sync.Mutex

	// LRU vector cache
	cacheMap   map[uint64]*list.Element
	cacheOrder *list.List
	cacheSize  int
}

// NewPebbleNodeStore creates a new PebbleNodeStore for the given table.
func NewPebbleNodeStore(db *pebble.DB, tableId int) *PebbleNodeStore {
	return &PebbleNodeStore{
		db:         db,
		tableId:    tableId,
		cacheMap:   make(map[uint64]*list.Element),
		cacheOrder: list.New(),
		cacheSize:  10000,
	}
}

// --- batch operations ---

// BeginBatch starts or nests a Pebble batch. The first call creates an indexed
// batch so that reads can see pending writes. Nested calls increment depth.
func (s *PebbleNodeStore) BeginBatch() {
	s.batchMu.Lock()
	defer s.batchMu.Unlock()
	s.batchDepth++
	if s.batchDepth == 1 {
		s.pendingBatch = s.db.NewIndexedBatch()
	}
}

// CommitBatch commits the pending batch when depth reaches zero.
// doSync controls whether the commit uses Sync or NoSync write options.
func (s *PebbleNodeStore) CommitBatch(doSync bool) error {
	s.batchMu.Lock()
	if s.batchDepth <= 0 {
		s.batchMu.Unlock()
		return fmt.Errorf("CommitBatch called with no active batch")
	}
	s.batchDepth--
	if s.batchDepth > 0 {
		s.batchMu.Unlock()
		return nil
	}
	// depth == 0: actually commit
	batch := s.pendingBatch
	s.pendingBatch = nil
	s.batchMu.Unlock()

	wo := pebble.NoSync
	if doSync {
		wo = pebble.Sync
	}
	if err := batch.Commit(wo); err != nil {
		batch.Close()
		return fmt.Errorf("failed to commit batch: %v", err)
	}
	return batch.Close()
}

// DiscardBatch discards the pending batch when depth reaches zero.
func (s *PebbleNodeStore) DiscardBatch() {
	s.batchMu.Lock()
	defer s.batchMu.Unlock()
	if s.batchDepth <= 0 {
		return
	}
	s.batchDepth--
	if s.batchDepth == 0 && s.pendingBatch != nil {
		s.pendingBatch.Close()
		s.pendingBatch = nil
	}
}

// BatchDepth returns the current batch nesting depth.
func (s *PebbleNodeStore) BatchDepth() int {
	s.batchMu.Lock()
	defer s.batchMu.Unlock()
	return s.batchDepth
}

// activeBatch returns the pending batch if one is active, or nil.
func (s *PebbleNodeStore) activeBatch() *pebble.Batch {
	s.batchMu.Lock()
	defer s.batchMu.Unlock()
	return s.pendingBatch
}

// reader returns the pending indexed batch (if active) for reads, otherwise
// the underlying DB. Both satisfy pebbleReader.
func (s *PebbleNodeStore) reader() pebbleReader {
	if ab := s.activeBatch(); ab != nil {
		return ab
	}
	return s.db
}

// --- LRU vector cache ---

func (s *PebbleNodeStore) cacheGet(id uint64) ([]float32, bool) {
	elem, ok := s.cacheMap[id]
	if !ok {
		return nil, false
	}
	s.cacheOrder.MoveToFront(elem)
	entry := elem.Value.(*vectorCacheEntry)
	// Return a copy to avoid mutation.
	cp := make([]float32, len(entry.vec))
	copy(cp, entry.vec)
	return cp, true
}

// cacheGetRef returns the cached vector without copying. The caller MUST NOT
// modify the returned slice.
func (s *PebbleNodeStore) cacheGetRef(id uint64) ([]float32, bool) {
	elem, ok := s.cacheMap[id]
	if !ok {
		return nil, false
	}
	s.cacheOrder.MoveToFront(elem)
	entry := elem.Value.(*vectorCacheEntry)
	return entry.vec, true
}

func (s *PebbleNodeStore) cachePut(id uint64, vec []float32) {
	if elem, ok := s.cacheMap[id]; ok {
		s.cacheOrder.MoveToFront(elem)
		entry := elem.Value.(*vectorCacheEntry)
		entry.vec = make([]float32, len(vec))
		copy(entry.vec, vec)
		return
	}
	cp := make([]float32, len(vec))
	copy(cp, vec)
	elem := s.cacheOrder.PushFront(&vectorCacheEntry{id: id, vec: cp})
	s.cacheMap[id] = elem

	// Evict oldest if over capacity.
	for s.cacheOrder.Len() > s.cacheSize {
		oldest := s.cacheOrder.Back()
		if oldest == nil {
			break
		}
		entry := oldest.Value.(*vectorCacheEntry)
		delete(s.cacheMap, entry.id)
		s.cacheOrder.Remove(oldest)
	}
}

func (s *PebbleNodeStore) cacheEvict(id uint64) {
	if elem, ok := s.cacheMap[id]; ok {
		delete(s.cacheMap, id)
		s.cacheOrder.Remove(elem)
	}
}

// --- key builders ---

func (s *PebbleNodeStore) metaEntryKey() []byte {
	return []byte(fmt.Sprintf("%c%d:meta:entry", prefixMeta, s.tableId))
}

func (s *PebbleNodeStore) metaCountKey() []byte {
	return []byte(fmt.Sprintf("%c%d:meta:count", prefixMeta, s.tableId))
}

func (s *PebbleNodeStore) vecKey(id uint64) []byte {
	return []byte(fmt.Sprintf("%c%d:vec:%d", prefixVec, s.tableId, id))
}

func (s *PebbleNodeStore) nodeLevelKey(id uint64) []byte {
	return []byte(fmt.Sprintf("%c%d:node:%d:level", prefixNode, s.tableId, id))
}

func (s *PebbleNodeStore) neighborsKey(id uint64, layer int) []byte {
	return []byte(fmt.Sprintf("%c%d:node:%d:nb:%d", prefixNb, s.tableId, id, layer))
}

func (s *PebbleNodeStore) neighborsPrefix(id uint64) []byte {
	return []byte(fmt.Sprintf("%c%d:node:%d:nb:", prefixNb, s.tableId, id))
}

func (s *PebbleNodeStore) docToNodeKey(docId string) []byte {
	return []byte(fmt.Sprintf("%c%d:map:doc:%s", prefixMap, s.tableId, docId))
}

func (s *PebbleNodeStore) nodeToDocKey(id uint64) []byte {
	return []byte(fmt.Sprintf("%c%d:map:node:%d", prefixMap, s.tableId, id))
}

func (s *PebbleNodeStore) idSeqKey() []byte {
	return []byte(fmt.Sprintf("%c%d:id_seq", prefixIDSeq, s.tableId))
}

func (s *PebbleNodeStore) normKey(id uint64) []byte {
	return []byte(fmt.Sprintf("%c%d:norm:%d", prefixNorm, s.tableId, id))
}

// --- encoding helpers ---

func encodeFloat32s(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

func decodeFloat32s(data []byte) []float32 {
	n := len(data) / 4
	v := make([]float32, n)
	for i := 0; i < n; i++ {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return v
}

func encodeUint64s(ids []uint64) []byte {
	buf := make([]byte, len(ids)*8)
	for i, id := range ids {
		binary.LittleEndian.PutUint64(buf[i*8:], id)
	}
	return buf
}

func decodeUint64s(data []byte) []uint64 {
	n := len(data) / 8
	ids := make([]uint64, n)
	for i := 0; i < n; i++ {
		ids[i] = binary.LittleEndian.Uint64(data[i*8:])
	}
	return ids
}

func encodeUint64(v uint64) []byte {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, v)
	return buf
}

func decodeUint64(data []byte) uint64 {
	return binary.LittleEndian.Uint64(data)
}

func encodeFloat32(v float32) []byte {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, math.Float32bits(v))
	return buf
}

func decodeFloat32Single(data []byte) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(data))
}

// --- NodeStore implementation ---

func (s *PebbleNodeStore) GetVector(id uint64) ([]float32, error) {
	// Check LRU cache first.
	if v, ok := s.cacheGet(id); ok {
		return v, nil
	}

	val, closer, err := s.reader().Get(s.vecKey(id))
	if err == pebble.ErrNotFound {
		return nil, fmt.Errorf("node %d not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get vector for node %d: %v", id, err)
	}
	defer closer.Close()

	vec := decodeFloat32s(val)
	s.cachePut(id, vec)
	return vec, nil
}

// GetVectorRef returns the vector without an extra copy. For PebbleNodeStore
// this returns the cached reference directly. Caller MUST NOT modify.
func (s *PebbleNodeStore) GetVectorRef(id uint64) ([]float32, error) {
	if v, ok := s.cacheGetRef(id); ok {
		return v, nil
	}

	val, closer, err := s.reader().Get(s.vecKey(id))
	if err == pebble.ErrNotFound {
		return nil, fmt.Errorf("node %d not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get vector for node %d: %v", id, err)
	}
	defer closer.Close()

	vec := decodeFloat32s(val)
	s.cachePut(id, vec)
	return vec, nil
}

func (s *PebbleNodeStore) PutNode(id uint64, level int, vector []float32) error {
	// Compute norm upfront for both paths.
	norm := vek32.Norm(vector)

	if ab := s.activeBatch(); ab != nil {
		// Write into the active batch — no local batch, no commit.
		if err := ab.Set(s.vecKey(id), encodeFloat32s(vector), nil); err != nil {
			return fmt.Errorf("failed to put vector for node %d: %v", id, err)
		}
		if err := ab.Set(s.nodeLevelKey(id), []byte{uint8(level)}, nil); err != nil {
			return fmt.Errorf("failed to put level for node %d: %v", id, err)
		}
		if err := ab.Set(s.normKey(id), encodeFloat32(norm), nil); err != nil {
			return fmt.Errorf("failed to put norm for node %d: %v", id, err)
		}
		// Increment count (read through batch).
		countKey := s.metaCountKey()
		count := uint64(0)
		val, closer, err := ab.Get(countKey)
		if err == nil {
			count = decodeUint64(val)
			closer.Close()
		} else if err != pebble.ErrNotFound {
			return fmt.Errorf("failed to get node count: %v", err)
		}
		if err := ab.Set(countKey, encodeUint64(count+1), nil); err != nil {
			return fmt.Errorf("failed to update node count: %v", err)
		}
		s.cachePut(id, vector)
		return nil
	}

	// No active batch — create a local batch and commit immediately.
	batch := s.db.NewBatch()
	defer batch.Close()

	// Store vector
	if err := batch.Set(s.vecKey(id), encodeFloat32s(vector), nil); err != nil {
		return fmt.Errorf("failed to put vector for node %d: %v", id, err)
	}

	// Store level
	if err := batch.Set(s.nodeLevelKey(id), []byte{uint8(level)}, nil); err != nil {
		return fmt.Errorf("failed to put level for node %d: %v", id, err)
	}

	// Store precomputed norm
	if err := batch.Set(s.normKey(id), encodeFloat32(norm), nil); err != nil {
		return fmt.Errorf("failed to put norm for node %d: %v", id, err)
	}

	// Increment count
	countKey := s.metaCountKey()
	count := uint64(0)
	val, closer, err := s.db.Get(countKey)
	if err == nil {
		count = decodeUint64(val)
		closer.Close()
	} else if err != pebble.ErrNotFound {
		return fmt.Errorf("failed to get node count: %v", err)
	}
	if err := batch.Set(countKey, encodeUint64(count+1), nil); err != nil {
		return fmt.Errorf("failed to update node count: %v", err)
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("failed to commit PutNode batch for node %d: %v", id, err)
	}
	s.cachePut(id, vector)
	return nil
}

func (s *PebbleNodeStore) DeleteNode(id uint64) error {
	r := s.reader()

	// Read the node level to know how many neighbor layers to delete
	levelVal, levelCloser, err := r.Get(s.nodeLevelKey(id))
	if err == pebble.ErrNotFound {
		return nil // nothing to delete
	}
	if err != nil {
		return fmt.Errorf("failed to get level for node %d: %v", id, err)
	}
	nodeLevel := int(levelVal[0])
	levelCloser.Close()

	if ab := s.activeBatch(); ab != nil {
		// Write deletes into the active batch.
		if err := ab.Delete(s.vecKey(id), nil); err != nil {
			return fmt.Errorf("failed to delete vector for node %d: %v", id, err)
		}
		if err := ab.Delete(s.nodeLevelKey(id), nil); err != nil {
			return fmt.Errorf("failed to delete level for node %d: %v", id, err)
		}
		if err := ab.Delete(s.normKey(id), nil); err != nil {
			return fmt.Errorf("failed to delete norm for node %d: %v", id, err)
		}
		for l := 0; l <= nodeLevel; l++ {
			if err := ab.Delete(s.neighborsKey(id, l), nil); err != nil {
				return fmt.Errorf("failed to delete neighbors for node %d layer %d: %v", id, l, err)
			}
		}
		// Delete doc mappings (node→doc direction)
		nodeToDocKey := s.nodeToDocKey(id)
		docIdVal, docCloser, err := ab.Get(nodeToDocKey)
		if err == nil {
			docId := string(docIdVal)
			docCloser.Close()
			if err := ab.Delete(s.docToNodeKey(docId), nil); err != nil {
				return fmt.Errorf("failed to delete doc→node mapping for node %d: %v", id, err)
			}
		} else if err != pebble.ErrNotFound {
			return fmt.Errorf("failed to get doc mapping for node %d: %v", id, err)
		}
		if err := ab.Delete(nodeToDocKey, nil); err != nil {
			return fmt.Errorf("failed to delete node→doc mapping for node %d: %v", id, err)
		}
		// Decrement count
		countKey := s.metaCountKey()
		countVal, countCloser, err := ab.Get(countKey)
		if err == nil {
			count := decodeUint64(countVal)
			countCloser.Close()
			if count > 0 {
				if err := ab.Set(countKey, encodeUint64(count-1), nil); err != nil {
					return fmt.Errorf("failed to update node count: %v", err)
				}
			}
		} else if err != pebble.ErrNotFound {
			return fmt.Errorf("failed to get node count: %v", err)
		}
		s.cacheEvict(id)
		return nil
	}

	// No active batch — create a local batch.
	batch := s.db.NewBatch()
	defer batch.Close()

	// Delete vector
	if err := batch.Delete(s.vecKey(id), nil); err != nil {
		return fmt.Errorf("failed to delete vector for node %d: %v", id, err)
	}

	// Delete level
	if err := batch.Delete(s.nodeLevelKey(id), nil); err != nil {
		return fmt.Errorf("failed to delete level for node %d: %v", id, err)
	}

	// Delete norm
	if err := batch.Delete(s.normKey(id), nil); err != nil {
		return fmt.Errorf("failed to delete norm for node %d: %v", id, err)
	}

	// Delete all neighbor layers
	for l := 0; l <= nodeLevel; l++ {
		if err := batch.Delete(s.neighborsKey(id, l), nil); err != nil {
			return fmt.Errorf("failed to delete neighbors for node %d layer %d: %v", id, l, err)
		}
	}

	// Delete doc mappings (node→doc direction)
	nodeToDocKey := s.nodeToDocKey(id)
	docIdVal, docCloser, err := s.db.Get(nodeToDocKey)
	if err == nil {
		docId := string(docIdVal)
		docCloser.Close()
		if err := batch.Delete(s.docToNodeKey(docId), nil); err != nil {
			return fmt.Errorf("failed to delete doc→node mapping for node %d: %v", id, err)
		}
	} else if err != pebble.ErrNotFound {
		return fmt.Errorf("failed to get doc mapping for node %d: %v", id, err)
	}
	if err := batch.Delete(nodeToDocKey, nil); err != nil {
		return fmt.Errorf("failed to delete node→doc mapping for node %d: %v", id, err)
	}

	// Decrement count
	countKey := s.metaCountKey()
	countVal, countCloser, err := s.db.Get(countKey)
	if err == nil {
		count := decodeUint64(countVal)
		countCloser.Close()
		if count > 0 {
			if err := batch.Set(countKey, encodeUint64(count-1), nil); err != nil {
				return fmt.Errorf("failed to update node count: %v", err)
			}
		}
	} else if err != pebble.ErrNotFound {
		return fmt.Errorf("failed to get node count: %v", err)
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("failed to commit DeleteNode batch for node %d: %v", id, err)
	}
	s.cacheEvict(id)
	return nil
}

func (s *PebbleNodeStore) GetNeighbors(id uint64, layer int) ([]uint64, error) {
	val, closer, err := s.reader().Get(s.neighborsKey(id, layer))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get neighbors for node %d layer %d: %v", id, layer, err)
	}
	defer closer.Close()

	return decodeUint64s(val), nil
}

func (s *PebbleNodeStore) SetNeighbors(id uint64, layer int, neighbors []uint64) error {
	if ab := s.activeBatch(); ab != nil {
		if err := ab.Set(s.neighborsKey(id, layer), encodeUint64s(neighbors), nil); err != nil {
			return fmt.Errorf("failed to set neighbors for node %d layer %d: %v", id, layer, err)
		}
		return nil
	}
	if err := s.db.Set(s.neighborsKey(id, layer), encodeUint64s(neighbors), pebble.Sync); err != nil {
		return fmt.Errorf("failed to set neighbors for node %d layer %d: %v", id, layer, err)
	}
	return nil
}

func (s *PebbleNodeStore) GetEntryPoint() (uint64, int, error) {
	val, closer, err := s.reader().Get(s.metaEntryKey())
	if err == pebble.ErrNotFound {
		return 0, 0, fmt.Errorf("entry point not set")
	}
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get entry point: %v", err)
	}
	defer closer.Close()

	if len(val) < 12 {
		return 0, 0, fmt.Errorf("invalid entry point data")
	}
	nodeId := binary.LittleEndian.Uint64(val[:8])
	maxLayer := int(binary.LittleEndian.Uint32(val[8:12]))
	return nodeId, maxLayer, nil
}

func (s *PebbleNodeStore) SetEntryPoint(id uint64, maxLayer int) error {
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint64(buf[:8], id)
	binary.LittleEndian.PutUint32(buf[8:12], uint32(maxLayer))

	if ab := s.activeBatch(); ab != nil {
		if err := ab.Set(s.metaEntryKey(), buf, nil); err != nil {
			return fmt.Errorf("failed to set entry point: %v", err)
		}
		return nil
	}
	if err := s.db.Set(s.metaEntryKey(), buf, pebble.Sync); err != nil {
		return fmt.Errorf("failed to set entry point: %v", err)
	}
	return nil
}

func (s *PebbleNodeStore) GetNodeLevel(id uint64) (int, error) {
	val, closer, err := s.reader().Get(s.nodeLevelKey(id))
	if err == pebble.ErrNotFound {
		return 0, fmt.Errorf("node %d not found", id)
	}
	if err != nil {
		return 0, fmt.Errorf("failed to get level for node %d: %v", id, err)
	}
	defer closer.Close()

	return int(val[0]), nil
}

func (s *PebbleNodeStore) GetNodeId(docId string) (uint64, bool, error) {
	val, closer, err := s.reader().Get(s.docToNodeKey(docId))
	if err == pebble.ErrNotFound {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("failed to get node id for doc %q: %v", docId, err)
	}
	defer closer.Close()

	return decodeUint64(val), true, nil
}

func (s *PebbleNodeStore) SetNodeMapping(docId string, nodeId uint64) error {
	if ab := s.activeBatch(); ab != nil {
		if err := ab.Set(s.docToNodeKey(docId), encodeUint64(nodeId), nil); err != nil {
			return fmt.Errorf("failed to set doc→node mapping for %q: %v", docId, err)
		}
		if err := ab.Set(s.nodeToDocKey(nodeId), []byte(docId), nil); err != nil {
			return fmt.Errorf("failed to set node→doc mapping for node %d: %v", nodeId, err)
		}
		return nil
	}

	batch := s.db.NewBatch()
	defer batch.Close()

	if err := batch.Set(s.docToNodeKey(docId), encodeUint64(nodeId), nil); err != nil {
		return fmt.Errorf("failed to set doc→node mapping for %q: %v", docId, err)
	}
	if err := batch.Set(s.nodeToDocKey(nodeId), []byte(docId), nil); err != nil {
		return fmt.Errorf("failed to set node→doc mapping for node %d: %v", nodeId, err)
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("failed to commit SetNodeMapping batch: %v", err)
	}
	return nil
}

func (s *PebbleNodeStore) DeleteNodeMapping(docId string) error {
	r := s.reader()

	// Look up the nodeId first
	val, closer, err := r.Get(s.docToNodeKey(docId))
	if err == pebble.ErrNotFound {
		return nil // nothing to delete
	}
	if err != nil {
		return fmt.Errorf("failed to get node id for doc %q: %v", docId, err)
	}
	nodeId := decodeUint64(val)
	closer.Close()

	if ab := s.activeBatch(); ab != nil {
		if err := ab.Delete(s.docToNodeKey(docId), nil); err != nil {
			return fmt.Errorf("failed to delete doc→node mapping for %q: %v", docId, err)
		}
		if err := ab.Delete(s.nodeToDocKey(nodeId), nil); err != nil {
			return fmt.Errorf("failed to delete node→doc mapping for node %d: %v", nodeId, err)
		}
		return nil
	}

	batch := s.db.NewBatch()
	defer batch.Close()

	if err := batch.Delete(s.docToNodeKey(docId), nil); err != nil {
		return fmt.Errorf("failed to delete doc→node mapping for %q: %v", docId, err)
	}
	if err := batch.Delete(s.nodeToDocKey(nodeId), nil); err != nil {
		return fmt.Errorf("failed to delete node→doc mapping for node %d: %v", nodeId, err)
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("failed to commit DeleteNodeMapping batch: %v", err)
	}
	return nil
}

func (s *PebbleNodeStore) NextNodeId() (uint64, error) {
	s.idMu.Lock()
	defer s.idMu.Unlock()

	r := s.reader()
	key := s.idSeqKey()
	var nextId uint64

	val, closer, err := r.Get(key)
	if err == pebble.ErrNotFound {
		nextId = 1
	} else if err != nil {
		return 0, fmt.Errorf("failed to get id sequence: %v", err)
	} else {
		nextId = decodeUint64(val) + 1
		closer.Close()
	}

	if ab := s.activeBatch(); ab != nil {
		if err := ab.Set(key, encodeUint64(nextId), nil); err != nil {
			return 0, fmt.Errorf("failed to update id sequence: %v", err)
		}
		return nextId, nil
	}

	batch := s.db.NewBatch()
	defer batch.Close()

	if err := batch.Set(key, encodeUint64(nextId), nil); err != nil {
		return 0, fmt.Errorf("failed to update id sequence: %v", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, fmt.Errorf("failed to commit NextNodeId batch: %v", err)
	}

	return nextId, nil
}

func (s *PebbleNodeStore) GetNorm(id uint64) (float32, error) {
	val, closer, err := s.reader().Get(s.normKey(id))
	if err == pebble.ErrNotFound {
		return 0, fmt.Errorf("norm for node %d not found", id)
	}
	if err != nil {
		return 0, fmt.Errorf("failed to get norm for node %d: %v", id, err)
	}
	defer closer.Close()
	return decodeFloat32Single(val), nil
}

func (s *PebbleNodeStore) SetNorm(id uint64, norm float32) error {
	if ab := s.activeBatch(); ab != nil {
		if err := ab.Set(s.normKey(id), encodeFloat32(norm), nil); err != nil {
			return fmt.Errorf("failed to set norm for node %d: %v", id, err)
		}
		return nil
	}
	if err := s.db.Set(s.normKey(id), encodeFloat32(norm), pebble.Sync); err != nil {
		return fmt.Errorf("failed to set norm for node %d: %v", id, err)
	}
	return nil
}

func (s *PebbleNodeStore) Close() error {
	// PebbleNodeStore does not own the database; the caller is responsible
	// for closing it. This method exists to satisfy the NodeStore interface.
	return nil
}
