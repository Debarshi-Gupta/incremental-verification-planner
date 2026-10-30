package security

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRedactSecretsAndANSI(t *testing.T) {
	got := Redact("\x1b[31mAuthorization: Bearer abcdef123456\x1b[0m API_TOKEN=secret-value\n", 200)
	if strings.Contains(got, "abcdef123456") || strings.Contains(got, "secret-value") || strings.Contains(got, "\x1b") {
		t.Fatalf("sensitive data survived redaction: %q", got)
	}
}

func TestRedactDoesNotSplitUTF8AtLimit(t *testing.T) {
	got := Redact("ab€c", 4)
	if got != "ab" || !utf8.ValidString(got) {
		t.Fatalf("invalid truncation: %q", got)
	}
}

func TestNormalizeRepoPath(t *testing.T) {
	for _, bad := range []string{"../secret", "a/../b", "C:/secret", "/etc/passwd", "a\\b"} {
		if _, err := NormalizeRepoPath(bad); err == nil {
			t.Errorf("expected rejection for %q", bad)
		}
	}
	if got, err := NormalizeRepoPath("pkg/unit.go"); err != nil || got != "pkg/unit.go" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestEnvironmentAllowlistDeniesSecrets(t *testing.T) {
	got := EnvironmentAllowlist(map[string]string{"CI": "true", "API_TOKEN": "unsafe"}, []string{"CI", "API_TOKEN"})
	if len(got) != 1 || got["CI"] != "true" {
		t.Fatalf("unexpected allowlist: %#v", got)
	}
}
