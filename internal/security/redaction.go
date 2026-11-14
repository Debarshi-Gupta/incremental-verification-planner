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
	// credentialShapePattern matches the value shapes of well-known
	// credentials. The keyword patterns above only catch a secret that is
	// introduced by a keyword ("token=..."); a bare credential value that was
	// concatenated into a message carries no keyword, so the shape itself is
	// the only signal (LLD 17.5).
	credentialShapePattern = regexp.MustCompile(`(?i)\b(?:gh[pousr]_[A-Za-z0-9]{16,}|akia[0-9a-z]{16}|asia[0-9a-z]{16}|xox[baprs]-[A-Za-z0-9-]{10,}|aiza[0-9A-Za-z_-]{20,}|sk-[A-Za-z0-9]{20,}|eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,})\b`)
	// Source paths must not reach an API payload, a log line, telemetry or an
	// artifact (LLD 17.5). The patterns are the ones the artifact sanitizer
	// already used, moved here so every surface that redacts scrubs paths.
	windowsPathPattern = regexp.MustCompile(`(?i)[a-z]:[\\/][^\s"'<>]+`)
	unixPathPattern    = regexp.MustCompile(`(^|[\s"'=])/(?:[A-Za-z0-9._-]+/)+[A-Za-z0-9._-]+`)
)

// Redact removes common terminal control sequences and credential-like values,
// scrubs source paths and well-known credential shapes, strips remaining
// control characters, and bounds output before it is stored.
func Redact(input string, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = 16 * 1024
	}
	s := ansiPattern.ReplaceAllString(input, "")
	s = secretPattern.ReplaceAllString(s, "$1[REDACTED]")
	s = assignmentPattern.ReplaceAllString(s, "$1=[REDACTED]")
	s = credentialShapePattern.ReplaceAllString(s, "[REDACTED]")
	s = windowsPathPattern.ReplaceAllString(s, "[PATH]")
	s = unixPathPattern.ReplaceAllString(s, "$1[PATH]")
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
