package analysis

import (
	"path"
	"strings"
)

// GlobMatch implements deterministic slash-separated globs where ** matches
// zero or more path segments. It never treats backslashes as separators.
func GlobMatch(pattern, name string) bool {
	if strings.Contains(pattern, `\`) || strings.Contains(name, `\`) {
		return false
	}
	return globSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func globSegments(pattern, name []string) bool {
	if len(pattern) == 0 {
		return len(name) == 0
	}
	if pattern[0] == "**" {
		for n := 0; n <= len(name); n++ {
			if globSegments(pattern[1:], name[n:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	matched, err := path.Match(pattern[0], name[0])
	return err == nil && matched && globSegments(pattern[1:], name[1:])
}
