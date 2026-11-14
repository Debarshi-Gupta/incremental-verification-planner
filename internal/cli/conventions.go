package cli

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/analysis"
	"github.com/ivp/ivp/internal/domain"
)

// Convention-derived graph evidence (LLD 4.4 edge inventory). The producers
// are pure over tracked repository paths and file contents: they read only
// the CODEOWNERS files, tracked paths, and bounded file scans, and they emit
// nothing when a convention's input is absent rather than guessing. Pairing
// conventions, confidence values, and CODEOWNERS locations are fixed by
// ADR 0013.

// Schema-consumer scan bounds: one file is sniffed only when it is a regular
// text file within schemaConsumerMaxFileBytes, and the total bytes read per
// schema are capped at schemaConsumerMaxBytes.
const (
	schemaConsumerMaxFileBytes = 512 << 10
	schemaConsumerMaxBytes     = 8 << 20
)

// codeownersLocations are the standard CODEOWNERS file locations, in search
// order. All present files contribute rules; ownership is the union of every
// matching rule across the files (ADR 0013).
var codeownersLocations = []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}

// readCodeowners parses every standard CODEOWNERS file under root. A file
// that cannot be read contributes no rules: missing content only removes
// evidence, never fabricates it.
func readCodeowners(root string) []analysis.CodeownersRule {
	var rules []analysis.CodeownersRule
	for _, rel := range codeownersLocations {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		rules = append(rules, analysis.ParseCodeowners(string(data))...)
	}
	return rules
}

// buildConventionEvidence creates the owner, config, and API-schema nodes
// that convention edges need and returns them with the five convention edge
// types: BUILT_FROM (generated output -> input), CONFIGURES (build/test
// target -> configuration file), CONTRACT_CONSUMER (consumer file -> API
// schema), OWNED_BY (node -> owner), and COLOCATED_WITH (test file -> source
// file, low confidence so it can never alone justify skipping broader tests).
// Edges are deduplicated by (from, to, type) and every edge records its
// provenance and confidence (LLD 4.4, ADR 0013).
func buildConventionEvidence(root string, snapshotID uuid.UUID, now time.Time, tracked []string, baseNodes []domain.GraphNode, codeownerRules []analysis.CodeownersRule) ([]domain.GraphNode, []domain.GraphEdge) {
	sortedTracked := append([]string(nil), tracked...)
	sort.Strings(sortedTracked)
	sourceNodeByPath := make(map[string]uuid.UUID, len(baseNodes))
	for _, node := range baseNodes {
		if node.NodeType == domain.NodeSourceFile && node.Path != "" {
			sourceNodeByPath[node.Path] = node.ID
		}
	}

	var newNodes []domain.GraphNode
	var edges []domain.GraphEdge
	seenEdge := map[[3]string]bool{}
	addEdge := func(from, to uuid.UUID, kind domain.GraphEdgeType, confidence float64, evidenceSource, precision string) {
		if from == uuid.Nil || to == uuid.Nil || from == to {
			return
		}
		key := [3]string{from.String(), to.String(), string(kind)}
		if seenEdge[key] {
			return
		}
		seenEdge[key] = true
		edges = append(edges, domain.GraphEdge{ID: uuid.New(), SnapshotID: snapshotID, FromNodeID: from, ToNodeID: to, EdgeType: kind,
			Confidence: confidence, EvidenceSource: evidenceSource, ObservedAt: now, Attributes: map[string]any{"precision": precision}})
	}
	ownerNodeByHandle := map[string]uuid.UUID{}
	ensureOwner := func(handle string) uuid.UUID {
		if id, ok := ownerNodeByHandle[handle]; ok {
			return id
		}
		id := uuid.New()
		ownerNodeByHandle[handle] = id
		newNodes = append(newNodes, domain.GraphNode{ID: id, SnapshotID: snapshotID, NodeType: domain.NodeOwner,
			StableKey: analysis.StableOwnerKey(handle), DisplayName: handle, EvidenceSource: "conventions:codeowners",
			Confidence: 0.9, ObservedAt: now})
		return id
	}
	configNodeByPath := map[string]uuid.UUID{}
	ensureConfig := func(repoPath string) uuid.UUID {
		if id, ok := configNodeByPath[repoPath]; ok {
			return id
		}
		id := uuid.New()
		configNodeByPath[repoPath] = id
		newNodes = append(newNodes, domain.GraphNode{ID: id, SnapshotID: snapshotID, NodeType: domain.NodeConfig,
			StableKey: analysis.StableConfigKey(repoPath), Path: repoPath, DisplayName: repoPath,
			EvidenceSource: "conventions:build-config", Confidence: 0.9, ObservedAt: now})
		return id
	}
	apiNodeByPath := map[string]uuid.UUID{}
	ensureAPI := func(repoPath string) uuid.UUID {
		if id, ok := apiNodeByPath[repoPath]; ok {
			return id
		}
		id := uuid.New()
		apiNodeByPath[repoPath] = id
		newNodes = append(newNodes, domain.GraphNode{ID: id, SnapshotID: snapshotID, NodeType: domain.NodeAPISchema,
			StableKey: analysis.StableSchemaKey(repoPath), Path: repoPath, DisplayName: repoPath,
			EvidenceSource: "conventions:schema", Confidence: 0.9, ObservedAt: now})
		return id
	}
	isUnitNode := func(node domain.GraphNode) bool {
		return strings.HasPrefix(node.StableKey, "unit:") && node.Path != ""
	}

	// OWNED_BY: every tracked source file and every unit contributes its
	// CODEOWNERS owners. Ownership comes only from explicit CODEOWNERS files.
	if len(codeownerRules) > 0 {
		for _, repoPath := range sortedTracked {
			nodeID, ok := sourceNodeByPath[repoPath]
			if !ok {
				continue
			}
			for _, handle := range analysis.CodeownerRulesFor(codeownerRules, repoPath) {
				addEdge(nodeID, ensureOwner(handle), domain.EdgeOwnedBy, 0.9, "conventions:codeowners", "CODEOWNERS")
			}
		}
		for _, node := range baseNodes {
			if !isUnitNode(node) {
				continue
			}
			for _, handle := range analysis.CodeownerRulesFor(codeownerRules, node.Path) {
				addEdge(node.ID, ensureOwner(handle), domain.EdgeOwnedBy, 0.9, "conventions:codeowners", "CODEOWNERS")
			}
		}
	}

	// BUILT_FROM: generated output -> its tracked input file.
	for _, pair := range analysis.BuiltFromPairs(sortedTracked) {
		addEdge(sourceNodeByPath[pair[0]], sourceNodeByPath[pair[1]], domain.EdgeBuiltFrom, 0.9,
			"conventions:generated-suffix", "GENERATED_SUFFIX")
	}

	// CONFIGURES: a build/test target node -> the configuration file that
	// governs its primary source path (nearest ancestor, else repo root).
	configPaths := make([]string, 0, len(sortedTracked))
	for _, repoPath := range sortedTracked {
		if isBuildConfigPath(repoPath) {
			configPaths = append(configPaths, repoPath)
		}
	}
	if len(configPaths) > 0 {
		for _, node := range baseNodes {
			if !isUnitNode(node) {
				continue
			}
			for _, pair := range analysis.ConfiguredByPairs([]string{node.Path}, configPaths) {
				if pair[1] == "" {
					continue
				}
				addEdge(node.ID, ensureConfig(pair[1]), domain.EdgeConfigures, 0.85,
					"conventions:nearest-config", "NEAREST_ANCESTOR_CONFIG")
			}
		}
	}

	// CONTRACT_CONSUMER: consumer file -> API schema it references. The scan
	// is bounded and silently skips unreadable or oversized files.
	for _, schemaPath := range sortedTracked {
		if !analysis.IsContractPath(schemaPath) {
			continue
		}
		for _, consumer := range analysis.SchemaConsumers(root, schemaPath, sortedTracked, schemaConsumerMaxFileBytes, schemaConsumerMaxBytes) {
			consumerID, ok := sourceNodeByPath[consumer]
			if !ok {
				continue
			}
			addEdge(consumerID, ensureAPI(schemaPath), domain.EdgeContractConsumer, 0.7,
				"conventions:schema-reference", "SCHEMA_REFERENCE")
		}
	}

	// COLOCATED_WITH: conventional test file -> its source counterpart. The
	// confidence cap keeps this edge unable alone to satisfy any coverage
	// threshold (LLD line 203).
	for _, pair := range analysis.CoLocationPairings(sortedTracked) {
		addEdge(sourceNodeByPath[pair[0]], sourceNodeByPath[pair[1]], domain.EdgeColocatedWith, 0.5,
			"conventions:co-location", "CONVENTION_COLOCATION")
	}

	return newNodes, edges
}

// unitOwnerStableKey returns the owner node stable key a unit records as its
// test owner: the first CODEOWNERS owner of the first source path that has
// one. It returns "" when no source path is owned, and the unit then carries
// no owner metadata (ADR 0013).
func unitOwnerStableKey(rules []analysis.CodeownersRule, sourcePaths []string) string {
	for _, sourcePath := range sourcePaths {
		if owners := analysis.CodeownerRulesFor(rules, sourcePath); len(owners) > 0 {
			return analysis.StableOwnerKey(owners[0])
		}
	}
	return ""
}
