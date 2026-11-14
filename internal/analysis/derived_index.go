package analysis

import (
	"hash/fnv"
	"sort"
	"sync"

	"github.com/ivp/ivp/internal/domain"
)

// DerivedIndex is the read-only derived graph index required by LLD 13.3: it
// is built once per snapshot instead of being rebuilt for every plan.
//
// It carries:
//   - adjacency lists by edge type (dependency adjacency and its reverse,
//     coverage adjacency and its reverse, contract consumers);
//   - connected target/module mappings (which targets are connected to which);
//   - an affected-target index for the common source -> target query.
//
// The immutable raw graph (nodes and edges) stays the source of truth; the
// index is derived from it and never overrides it.
type DerivedIndex struct {
	// SnapshotKey identifies the snapshot the index was derived from.
	SnapshotKey string
	// AlgorithmVersion identifies the derivation rules; an index built by a
	// different algorithm version is never reused.
	AlgorithmVersion string
	// NodeCount and EdgeCount let the cache verify that a cached index still
	// describes the graph it is asked about.
	NodeCount int
	EdgeCount int
	// Fingerprint identifies the exact raw graph the index was derived from.
	// The cache key is the snapshot hash and algorithm version; the
	// fingerprint makes sure an index is only ever served for the very graph
	// it was built from.
	Fingerprint uint64

	// ReverseDeps maps a dependency node to the consumers that depend on it.
	ReverseDeps map[domain.UUID][]domain.UUID
	// ReverseCoverage maps a covered node to the tests that cover it.
	ReverseCoverage map[domain.UUID][]domain.UUID
	// DependencyAdj maps a consumer node to the nodes it depends on.
	DependencyAdj map[domain.UUID][]domain.UUID
	// ContractConsumers maps a contract node to its consumers.
	ContractConsumers map[domain.UUID][]domain.UUID
	// AffectedTargets maps a source path to the build/test target keys that
	// depend on it (the affected-target index).
	AffectedTargets map[string][]string
	// ConnectedTargets maps a target or module key to the target keys it is
	// connected to (the connected target/module mapping).
	ConnectedTargets map[string][]string
	// HasConflict records that the source graph contains a conflicting edge.
	HasConflict bool
}

// BuildDerivedIndex derives the index from the immutable raw graph.
func BuildDerivedIndex(snapshotKey string, nodes []domain.GraphNode, edges []domain.GraphEdge) *DerivedIndex {
	index := &DerivedIndex{
		SnapshotKey:       snapshotKey,
		AlgorithmVersion:  AlgorithmVersion,
		NodeCount:         len(nodes),
		EdgeCount:         len(edges),
		Fingerprint:       graphFingerprint(nodes, edges),
		ReverseDeps:       map[domain.UUID][]domain.UUID{},
		ReverseCoverage:   map[domain.UUID][]domain.UUID{},
		DependencyAdj:     map[domain.UUID][]domain.UUID{},
		ContractConsumers: map[domain.UUID][]domain.UUID{},
		AffectedTargets:   map[string][]string{},
		ConnectedTargets:  map[string][]string{},
	}
	stableKey := make(map[domain.UUID]string, len(nodes))
	path := make(map[domain.UUID]string, len(nodes))
	nodeType := make(map[domain.UUID]domain.GraphNodeType, len(nodes))
	for _, node := range nodes {
		stableKey[node.ID] = node.StableKey
		path[node.ID] = node.Path
		nodeType[node.ID] = node.NodeType
	}
	for _, edge := range edges {
		if edge.Conflict {
			index.HasConflict = true
		}
		switch edge.EdgeType {
		case domain.EdgeDependsOn:
			// consumer -> dependency; the reverse list finds affected consumers.
			index.DependencyAdj[edge.FromNodeID] = append(index.DependencyAdj[edge.FromNodeID], edge.ToNodeID)
			index.ReverseDeps[edge.ToNodeID] = append(index.ReverseDeps[edge.ToNodeID], edge.FromNodeID)
		case domain.EdgeTestCovers:
			index.ReverseCoverage[edge.ToNodeID] = append(index.ReverseCoverage[edge.ToNodeID], edge.FromNodeID)
		case domain.EdgeContractConsumer:
			index.ContractConsumers[edge.ToNodeID] = append(index.ContractConsumers[edge.ToNodeID], edge.FromNodeID)
		}
	}
	for _, edge := range edges {
		if edge.EdgeType != domain.EdgeDependsOn {
			continue
		}
		consumerKey, dependencyKey := stableKey[edge.FromNodeID], stableKey[edge.ToNodeID]
		// Connected target/module mappings: connected targets are connected in
		// both directions so a lookup never misses a neighbour.
		if consumerKey != "" && dependencyKey != "" {
			index.ConnectedTargets[consumerKey] = append(index.ConnectedTargets[consumerKey], dependencyKey)
			index.ConnectedTargets[dependencyKey] = append(index.ConnectedTargets[dependencyKey], consumerKey)
		}
	}
	// Affected-target index: for every source path, the build/test targets
	// that depend on it (transitively). The closure is computed from the
	// target side, so the cost follows the number of targets rather than the
	// number of files.
	for _, node := range nodes {
		if !isTargetNodeType(node.NodeType) {
			continue
		}
		seen := map[domain.UUID]bool{node.ID: true}
		queue := append([]domain.UUID(nil), index.DependencyAdj[node.ID]...)
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			if seen[current] {
				continue
			}
			seen[current] = true
			if dependencyPath := path[current]; dependencyPath != "" && nodeType[current] == domain.NodeSourceFile {
				index.AffectedTargets[dependencyPath] = append(index.AffectedTargets[dependencyPath], node.StableKey)
			}
			queue = append(queue, index.DependencyAdj[current]...)
		}
	}
	index.sortAll()
	return index
}

func isTargetNodeType(nodeType domain.GraphNodeType) bool {
	switch nodeType {
	case domain.NodeBuildTarget, domain.NodeTestTarget, domain.NodeTestCase, domain.NodeCheckTarget:
		return true
	default:
		return false
	}
}

// sortAll makes every adjacency list deterministic so the derived index (and
// every plan built from it) is byte-stable for identical inputs.
func (index *DerivedIndex) sortAll() {
	for _, list := range []map[domain.UUID][]domain.UUID{
		index.ReverseDeps, index.ReverseCoverage, index.DependencyAdj, index.ContractConsumers,
	} {
		for id := range list {
			list[id] = sortedUUIDs(list[id])
		}
	}
	for key := range index.AffectedTargets {
		index.AffectedTargets[key] = sortedStrings(index.AffectedTargets[key])
	}
	for key := range index.ConnectedTargets {
		index.ConnectedTargets[key] = sortedStrings(index.ConnectedTargets[key])
	}
}

func sortedUUIDs(values []domain.UUID) []domain.UUID {
	sort.Slice(values, func(i, j int) bool { return values[i].String() < values[j].String() })
	return values
}

func sortedStrings(values []string) []string {
	sort.Strings(values)
	return values
}

// defaultDerivedIndexCacheSize bounds the number of derived indexes kept in
// memory. Snapshots are immutable, so a small cache already serves every
// plan that targets the same snapshot.
const defaultDerivedIndexCacheSize = 8

type derivedIndexCache struct {
	mu      sync.Mutex
	entries map[string]*DerivedIndex
	order   []string
	max     int
}

func newDerivedIndexCache(max int) *derivedIndexCache {
	if max < 1 {
		max = 1
	}
	return &derivedIndexCache{entries: map[string]*DerivedIndex{}, max: max}
}

func (c *derivedIndexCache) get(key string) (*DerivedIndex, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	index, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	// Refresh recency.
	for i, existing := range c.order {
		if existing == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, key)
	return index, true
}

func (c *derivedIndexCache) put(key string, index *DerivedIndex) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists {
		c.order = append(c.order, key)
	}
	c.entries[key] = index
	for len(c.order) > c.max {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

var derivedIndexes = newDerivedIndexCache(defaultDerivedIndexCacheSize)

// DerivedIndexKey is the cache key of the derived index: the snapshot hash
// and the algorithm version (LLD 13.3: "cache read-only derived index by
// snapshot hash and algorithm version").
func DerivedIndexKey(snapshotKey string) string {
	return snapshotKey + "|" + AlgorithmVersion
}

// DerivedIndexFor returns the derived index for a snapshot, building it on
// first use. A cached index is reused only when it still describes the graph
// it is asked about (identical content fingerprint), so a snapshot hash that
// cannot identify its content can never yield a wrong index.
func DerivedIndexFor(snapshotKey string, nodes []domain.GraphNode, edges []domain.GraphEdge) *DerivedIndex {
	key := DerivedIndexKey(snapshotKey)
	fingerprint := graphFingerprint(nodes, edges)
	if cached, ok := derivedIndexes.get(key); ok {
		if cached.Fingerprint == fingerprint && cached.NodeCount == len(nodes) && cached.EdgeCount == len(edges) {
			return cached
		}
	}
	index := BuildDerivedIndex(snapshotKey, nodes, edges)
	derivedIndexes.put(key, index)
	return index
}

// graphFingerprint is an order-independent fingerprint of the raw graph: the
// sum and the xor of the per-item hashes. Two graphs with the same nodes and
// edges (in any order) share a fingerprint; any difference in the nodes, the
// stable keys, or the edge endpoints and types changes it.
func graphFingerprint(nodes []domain.GraphNode, edges []domain.GraphEdge) uint64 {
	var sum, xor uint64
	for _, node := range nodes {
		hash := fnv.New64a()
		_, _ = hash.Write([]byte(node.ID.String()))
		_, _ = hash.Write([]byte(node.StableKey))
		_, _ = hash.Write([]byte(node.Path))
		value := hash.Sum64()
		sum += value
		xor ^= value
	}
	for _, edge := range edges {
		hash := fnv.New64a()
		_, _ = hash.Write([]byte(edge.FromNodeID.String()))
		_, _ = hash.Write([]byte(edge.ToNodeID.String()))
		_, _ = hash.Write([]byte(edge.EdgeType))
		value := hash.Sum64()
		sum += value
		xor ^= value
	}
	return sum ^ (xor << 1)
}

// DerivedIndexCacheEntries reports how many derived indexes are currently
// cached (used by the direct assertions of the cache behavior).
func DerivedIndexCacheEntries() int {
	derivedIndexes.mu.Lock()
	defer derivedIndexes.mu.Unlock()
	return len(derivedIndexes.entries)
}
