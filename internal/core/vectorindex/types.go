package vectorindex

// Node represents a node in the HNSW graph.
type Node struct {
	ID     uint64
	Level  int
	Vector []float32
}

// Vector is a float32 slice representing a point in vector space.
type Vector = []float32

// SearchResult holds the result of a nearest-neighbor search.
type SearchResult struct {
	ID       uint64
	DocID    string
	Distance float32
}
