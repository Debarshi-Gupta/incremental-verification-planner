package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// CanonicalDigest hashes JSON-canonicalized input. Callers should pass only
// normalized metadata; Go's JSON encoder sorts map keys deterministically.
func CanonicalDigest(value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("canonical fingerprint: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func SnapshotDigest(s RepositorySnapshot) (string, error) {
	return CanonicalDigest(struct {
		RepositoryID        UUID
		CommitSHA           string
		BaseSHA             string
		TreeHash            string
		WorktreeHash        string
		SubmoduleDigest     string
		ToolchainDigest     string
		BuildConfigDigest   string
		AdapterSetDigest    string
		GraphVersion        int64
		EvidenceGeneratedAt string
		IsComplete          bool
		CompletenessReasons []string
	}{s.RepositoryID, s.CommitSHA, s.BaseSHA, s.TreeHash, s.WorktreeHash, s.SubmoduleDigest, s.ToolchainDigest, s.BuildConfigDigest, s.AdapterSetDigest, s.GraphVersion, s.EvidenceGeneratedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), s.IsComplete, sortedCopy(s.CompletenessReasons)})
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
