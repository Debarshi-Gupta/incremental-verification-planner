package security

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ansiPattern       = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\))`)
	secretPattern     = regexp.MustCompile(`(?i)(\b(?:authorization\s*[:=]\s*bearer|bearer|api[_-]?key|access[_-]?token|refresh[_-]?token|password|passwd|secret|client[_-]?secret|private[_-]?key)\b\s*[:=]?\s*)(["']?)[A-Za-z0-9_./+=:-]{6,}(["']?)`)
	assignmentPattern = regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|API_KEY|PRIVATE_KEY|CREDENTIAL)[A-Z0-9_]*)\s*=\s*([^\s,;]+)`)
)

// Redact removes common terminal control sequences and credential-like values,
// strips remaining control characters, and bounds output before it is stored.
func Redact(input string, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = 16 * 1024
	}
	s := ansiPattern.ReplaceAllString(input, "")
	s = secretPattern.ReplaceAllString(s, "$1[REDACTED]")
	s = assignmentPattern.ReplaceAllString(s, "$1=[REDACTED]")
	var out strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' || !unicode.IsControl(r) {
			if size := utf8.RuneLen(r); size > 0 && out.Len()+size > maxBytes {
				break
			}
			out.WriteRune(r)
		}
	}
	return out.String()
}

// EnvironmentAllowlist returns only explicitly allowed, non-secret variables.
// Caller-provided names are still checked against the denylist.
func EnvironmentAllowlist(input map[string]string, allowed []string) map[string]string {
	allow := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allow[key] = struct{}{}
	}
	out := make(map[string]string)
	for key, value := range input {
		if _, ok := allow[key]; ok && !isSecretName(key) {
			out[key] = value
		}
	}
	return out
}

func isSecretName(key string) bool {
	k := strings.ToUpper(key)
	for _, part := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "PRIVATE_KEY", "API_KEY", "ACCESS_KEY"} {
		if strings.Contains(k, part) {
			return true
		}
	}
	return false
}
