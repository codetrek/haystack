package vectorindex

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/cockroachdb/pebble"
)

// Key type prefixes for vector index data in Pebble.
const (
	prefixMeta   = byte(40)
	prefixVec    = byte(41)
	prefixNode   = byte(42)
	prefixNb     = byte(43)
	prefixMap    = byte(44)
	prefixIDSeq  = byte(45)
)

// NodeStore defines the persistence interface for HNSW graph nodes.
type NodeStore interface {
	GetVector(id uint64) ([]float32, error)
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
	Close() error
}

// PebbleNodeStore implements NodeStore backed by a Pebble database.
type PebbleNodeStore struct {
	db      *pebble.DB
	tableId int
}

// NewPebbleNodeStore creates a new PebbleNodeStore for the given table.
func NewPebbleNodeStore(db *pebble.DB, tableId int) *PebbleNodeStore {
	return &PebbleNodeStore{db: db, tableId: tableId}
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

// --- NodeStore implementation ---

func (s *PebbleNodeStore) GetVector(id uint64) ([]float32, error) {
	val, closer, err := s.db.Get(s.vecKey(id))
	if err == pebble.ErrNotFound {
		return nil, fmt.Errorf("node %d not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get vector for node %d: %v", id, err)
	}
	defer closer.Close()

	return decodeFloat32s(val), nil
}

func (s *PebbleNodeStore) PutNode(id uint64, level int, vector []float32) error {
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
	return nil
}

func (s *PebbleNodeStore) DeleteNode(id uint64) error {
	// Read the node level to know how many neighbor layers to delete
	levelVal, levelCloser, err := s.db.Get(s.nodeLevelKey(id))
	if err == pebble.ErrNotFound {
		return nil // nothing to delete
	}
	if err != nil {
		return fmt.Errorf("failed to get level for node %d: %v", id, err)
	}
	nodeLevel := int(levelVal[0])
	levelCloser.Close()

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
	return nil
}

func (s *PebbleNodeStore) GetNeighbors(id uint64, layer int) ([]uint64, error) {
	val, closer, err := s.db.Get(s.neighborsKey(id, layer))
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
	if err := s.db.Set(s.neighborsKey(id, layer), encodeUint64s(neighbors), pebble.Sync); err != nil {
		return fmt.Errorf("failed to set neighbors for node %d layer %d: %v", id, layer, err)
	}
	return nil
}

func (s *PebbleNodeStore) GetEntryPoint() (uint64, int, error) {
	val, closer, err := s.db.Get(s.metaEntryKey())
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

	if err := s.db.Set(s.metaEntryKey(), buf, pebble.Sync); err != nil {
		return fmt.Errorf("failed to set entry point: %v", err)
	}
	return nil
}

func (s *PebbleNodeStore) GetNodeLevel(id uint64) (int, error) {
	val, closer, err := s.db.Get(s.nodeLevelKey(id))
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
	val, closer, err := s.db.Get(s.docToNodeKey(docId))
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
	// Look up the nodeId first
	val, closer, err := s.db.Get(s.docToNodeKey(docId))
	if err == pebble.ErrNotFound {
		return nil // nothing to delete
	}
	if err != nil {
		return fmt.Errorf("failed to get node id for doc %q: %v", docId, err)
	}
	nodeId := decodeUint64(val)
	closer.Close()

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
	batch := s.db.NewBatch()
	defer batch.Close()

	key := s.idSeqKey()
	var nextId uint64

	val, closer, err := s.db.Get(key)
	if err == pebble.ErrNotFound {
		nextId = 1
	} else if err != nil {
		return 0, fmt.Errorf("failed to get id sequence: %v", err)
	} else {
		nextId = decodeUint64(val) + 1
		closer.Close()
	}

	if err := batch.Set(key, encodeUint64(nextId), nil); err != nil {
		return 0, fmt.Errorf("failed to update id sequence: %v", err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, fmt.Errorf("failed to commit NextNodeId batch: %v", err)
	}

	return nextId, nil
}

func (s *PebbleNodeStore) Close() error {
	// PebbleNodeStore does not own the database; the caller is responsible
	// for closing it. This method exists to satisfy the NodeStore interface.
	return nil
}
