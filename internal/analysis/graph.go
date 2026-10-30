package analysis

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/ivp/ivp/internal/domain"
)

func AnalyzeImpact(changes []domain.ChangedFile, nodes []domain.GraphNode, edges []domain.GraphEdge, policy domain.Policy, now time.Time, complete, missing, declaredConflict, declaredCycle bool) ImpactResult {
	result := ImpactResult{ImpactedNodeIDs: make(map[domain.UUID]bool), MissingEdges: missing}
	nodeByID := make(map[domain.UUID]domain.GraphNode, len(nodes))
	nodesByPath := make(map[string][]domain.GraphNode)
	for _, n := range nodes {
		nodeByID[n.ID] = n
		if n.Path != "" {
			nodesByPath[n.Path] = append(nodesByPath[n.Path], n)
		}
	}
	changed := make(map[domain.UUID]bool)
	entityMap := make(map[string]ImpactEntity)
	for _, f := range changes {
		paths := []string{f.Path}
		if f.OldPath != "" {
			paths = append(paths, f.OldPath)
		}
		for _, p := range paths {
			key := "file:" + p
			entityMap[key] = ImpactEntity{StableKey: key, Path: p, NodeType: domain.NodeSourceFile, Labels: append([]string(nil), f.DetectedRiskLabels...)}
			for _, n := range nodesByPath[p] {
				if n.NodeType == domain.NodeSourceFile || n.NodeType == domain.NodeConfig || n.NodeType == domain.NodeAPISchema || n.NodeType == domain.NodeMigration || n.NodeType == domain.NodeGeneratedOutput {
					changed[n.ID] = true
					result.ImpactedNodeIDs[n.ID] = true
					entity := entityMap[key]
					entity.NodeType = n.NodeType
					entity.Labels = mergeStrings(entity.Labels, n.Labels)
					entityMap[key] = entity
				}
			}
		}
	}

	reverseDeps := make(map[domain.UUID][]domain.UUID)
	reverseCoverage := make(map[domain.UUID][]domain.UUID)
	depAdj := make(map[domain.UUID][]domain.UUID)
	for _, e := range edges {
		if e.Conflict {
			result.HasConflict = true
		}
		if e.EdgeType == domain.EdgeDependsOn {
			// consumer -> dependency; use reverseDeps to find affected consumers.
			depAdj[e.FromNodeID] = append(depAdj[e.FromNodeID], e.ToNodeID)
			reverseDeps[e.ToNodeID] = append(reverseDeps[e.ToNodeID], e.FromNodeID)
		}
		if e.EdgeType == domain.EdgeTestCovers {
			reverseCoverage[e.ToNodeID] = append(reverseCoverage[e.ToNodeID], e.FromNodeID)
		}
	}
	if declaredConflict {
		result.HasConflict = true
	}
	queue := make([]domain.UUID, 0, len(changed))
	for id := range changed {
		queue = append(queue, id)
	}
	sort.Slice(queue, func(i, j int) bool { return queue[i].String() < queue[j].String() })
	seen := make(map[domain.UUID]bool, len(changed))
	for _, id := range queue {
		seen[id] = true
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, consumer := range reverseDeps[id] {
			if !seen[consumer] {
				seen[consumer] = true
				result.ImpactedNodeIDs[consumer] = true
				queue = append(queue, consumer)
			}
		}
		for _, test := range reverseCoverage[id] {
			result.ImpactedNodeIDs[test] = true
		}
	}
	impactedDepAdj := make(map[domain.UUID][]domain.UUID)
	for from, deps := range depAdj {
		if !result.ImpactedNodeIDs[from] {
			continue
		}
		for _, dep := range deps {
			if result.ImpactedNodeIDs[dep] {
				impactedDepAdj[from] = append(impactedDepAdj[from], dep)
			}
		}
	}
	result.HasCycle = declaredCycle || hasCycle(impactedDepAdj)

	fanoutByChanged := make(map[domain.UUID]int)
	for id := range changed {
		vis := make(map[domain.UUID]bool)
		q := []domain.UUID{id}
		vis[id] = true
		for len(q) > 0 {
			cur := q[0]
			q = q[1:]
			for _, consumer := range reverseDeps[cur] {
				if !vis[consumer] {
					vis[consumer] = true
					q = append(q, consumer)
					fanoutByChanged[id]++
				}
			}
		}
		result.ReverseFanout += fanoutByChanged[id]
	}

	for key, entity := range entityMap {
		for id := range changed {
			n, ok := nodeByID[id]
			if ok && (n.StableKey == key || n.Path == entity.Path) {
				entity.ReverseFanout += fanoutByChanged[id]
			}
		}
		entity.Criticality = criticality(entity.Labels, entity.Path, policy.CriticalPathPatterns)
		entity.Critical = entity.Criticality == "CRITICAL"
		critValue := map[string]float64{"CRITICAL": 4, "HIGH": 3, "NORMAL": 2, "LOW": 1}[entity.Criticality]
		cap := policy.DependencyCap
		if cap < 1 {
			cap = 100
		}
		fanout := float64(entity.ReverseFanout)
		if fanout > float64(cap) {
			fanout = float64(cap)
		}
		depFactor := 1 + math.Log1p(fanout)
		severity := severityForChange(changes, entity.Path)
		entity.Weight = critValue * depFactor * severity
		entityMap[key] = entity
	}
	total := 0.0
	keys := make([]string, 0, len(entityMap))
	for key := range entityMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		total += entityMap[key].Weight
	}
	if total > 0 {
		for _, key := range keys {
			entity := entityMap[key]
			entity.Weight /= total
			entityMap[key] = entity
		}
	}
	for _, entity := range entityMap {
		result.Entities = append(result.Entities, entity)
	}
	sort.Slice(result.Entities, func(i, j int) bool { return result.Entities[i].StableKey < result.Entities[j].StableKey })
	result.GraphConfidence = graphConfidence(edges, result.ImpactedNodeIDs, complete, now)
	return result
}

func hasCycle(adj map[domain.UUID][]domain.UUID) bool {
	state := make(map[domain.UUID]uint8)
	var visit func(domain.UUID) bool
	visit = func(id domain.UUID) bool {
		if state[id] == 1 {
			return true
		}
		if state[id] == 2 {
			return false
		}
		state[id] = 1
		for _, next := range adj[id] {
			if visit(next) {
				return true
			}
		}
		state[id] = 2
		return false
	}
	for id := range adj {
		if visit(id) {
			return true
		}
	}
	return false
}

func graphConfidence(edges []domain.GraphEdge, impacted map[domain.UUID]bool, complete bool, now time.Time) float64 {
	if !complete {
		return 0
	}
	var values []float64
	for _, e := range edges {
		if !impacted[e.FromNodeID] && !impacted[e.ToNodeID] {
			continue
		}
		confidence := e.Confidence
		if e.ValidUntil != nil && now.After(*e.ValidUntil) {
			confidence *= 0.25
		}
		if e.Conflict {
			confidence = 0
		}
		values = append(values, confidence)
	}
	if len(values) == 0 {
		return 0.75 // Explicit file/target mappings can support a lower-confidence plan.
	}
	sort.Float64s(values)
	sum := 0.0
	for _, confidence := range values {
		sum += confidence
	}
	return sum / float64(len(values))
}

func criticality(labels []string, file string, patterns []string) string {
	set := make(map[string]bool, len(labels))
	for _, label := range labels {
		set[strings.ToUpper(label)] = true
	}
	for _, label := range []string{"SECURITY_CRITICAL", "AUTHENTICATION", "AUTHORIZATION", "CRYPTOGRAPHY", "PAYMENT", "DATA_INTEGRITY", "DEPLOYMENT", "RELEASE"} {
		if set[label] {
			return "CRITICAL"
		}
	}
	if set["PUBLIC_CONTRACT"] || set["HIGH"] {
		return "HIGH"
	}
	for _, pattern := range patterns {
		if GlobMatch(pattern, file) {
			return "CRITICAL"
		}
	}
	if strings.HasSuffix(strings.ToLower(file), ".md") || strings.HasPrefix(file, "docs/") {
		return "LOW"
	}
	// Unknown criticality is deliberately HIGH.
	return "HIGH"
}

func severityForChange(changes []domain.ChangedFile, file string) float64 {
	for _, f := range changes {
		if f.Path != file && f.OldPath != file {
			continue
		}
		switch f.Status {
		case domain.ChangeDeleted, domain.ChangeRenamed, domain.ChangeCopied:
			return 1.2
		case domain.ChangeTypeChanged:
			return 1.4
		default:
			if f.IsGenerated {
				return 1.3
			}
			return 1
		}
	}
	return 1
}

func mergeStrings(a, b []string) []string {
	set := make(map[string]bool, len(a)+len(b))
	var out []string
	for _, s := range append(append([]string(nil), a...), b...) {
		if !set[s] {
			set[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
