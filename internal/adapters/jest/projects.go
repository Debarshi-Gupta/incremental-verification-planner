package jest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ivp/ivp/internal/adapters"
)

// jestProject is one project of a Jest configuration. Scope is the
// repository relative directory that owns the project's test files; "." is the
// repository root, which is the scope of a configuration without a projects
// array.
type jestProject struct {
	// Name is the display name the project declares, empty when it declares
	// none.
	Name string
	// Scope is the repository relative directory that owns the project.
	Scope string
}

// jestProjectEntry is one unresolved entry of a projects array.
type jestProjectEntry struct {
	// Path is the directory or configuration file the project points at.
	Path string
	// DisplayName is the optional display name of the project.
	DisplayName string
}

// jestProjects is the resolved project layout of a repository's Jest
// configuration.
type jestProjects struct {
	// projects are the resolved projects, ordered by scope.
	projects []jestProject
	// declared reports that the configuration contains a projects array.
	declared bool
	// failed reports that the projects array could not be resolved. A failed
	// resolution never attributes a path to a project.
	failed bool
}

// jestConfigFiles lists the configuration files the adapter reads. Jest
// resolves exactly one root configuration and prefers a jest.config file over
// the package.json jest section, so the first file that exists wins. The
// order follows the extensions jest itself resolves.
var jestConfigFiles = []string{"jest.config.js", "jest.config.mjs", "jest.config.cjs", "jest.config.ts", "package.json"}

// readJestProjects reads the projects array of the repository's root Jest
// configuration. The configuration is read statically: no JavaScript is
// executed and no configuration loader is emulated. The second result reports
// whether the configuration declares a projects array at all.
func readJestProjects(root string) (jestProjects, error) {
	absRoot, err := adapters.ResolveWithin(root, ".")
	if err != nil {
		return jestProjects{failed: true}, err
	}
	for _, candidate := range jestConfigFiles {
		abs, err := adapters.ResolveWithin(absRoot, candidate)
		if err != nil {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			continue
		}
		source, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		entries, declared, err := parseJestProjects(string(source), candidate == "package.json")
		if err != nil {
			return jestProjects{declared: true, failed: true}, err
		}
		if !declared {
			// The configuration exists but declares no projects: the
			// repository is a single Jest project.
			return jestProjects{}, nil
		}
		resolved, err := resolveJestProjects(absRoot, filepath.Dir(abs), entries)
		if err != nil {
			return jestProjects{declared: true, failed: true}, err
		}
		return jestProjects{projects: resolved, declared: true}, nil
	}
	return jestProjects{}, nil
}

// parseJestProjects extracts the projects array of a Jest configuration. A
// package.json configuration is read as JSON, a JavaScript or TypeScript
// configuration is scanned for its literal projects array.
func parseJestProjects(source string, packageJSON bool) ([]jestProjectEntry, bool, error) {
	if packageJSON {
		return parsePackageJestProjects(source)
	}
	return jsProjectsDeclaration(source)
}

// parsePackageJestProjects reads the projects array of the "jest" section of a
// package.json.
func parsePackageJestProjects(source string) ([]jestProjectEntry, bool, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(source), &document); err != nil {
		return nil, false, errors.New("package.json is not valid JSON")
	}
	raw, ok := document["jest"]
	if !ok {
		return nil, false, nil
	}
	var section map[string]json.RawMessage
	if err := json.Unmarshal(raw, &section); err != nil {
		return nil, true, errors.New("the Jest section of package.json is not an object")
	}
	rawProjects, ok := section["projects"]
	if !ok {
		return nil, false, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(rawProjects, &items); err != nil {
		return nil, true, errors.New("the Jest projects array is not a list")
	}
	entries := make([]jestProjectEntry, 0, len(items))
	for _, item := range items {
		entry, err := jsonProjectEntry(item)
		if err != nil {
			return nil, true, err
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return nil, true, errors.New("the Jest projects array is empty")
	}
	return entries, true, nil
}

// jsonProjectEntry reads one entry of a JSON projects array. An entry is
// either a path or an object that declares a rootDir.
func jsonProjectEntry(raw json.RawMessage) (jestProjectEntry, error) {
	var path string
	if err := json.Unmarshal(raw, &path); err == nil {
		if strings.TrimSpace(path) == "" {
			return jestProjectEntry{}, errors.New("a Jest project entry declares an empty path")
		}
		return jestProjectEntry{Path: path}, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return jestProjectEntry{}, errors.New("a Jest project entry is neither a path nor an object")
	}
	entry := jestProjectEntry{}
	if value, ok := object["rootDir"]; ok {
		if err := json.Unmarshal(value, &entry.Path); err != nil {
			return jestProjectEntry{}, errors.New("a Jest project rootDir is not a string")
		}
	}
	if value, ok := object["displayName"]; ok {
		var name string
		if err := json.Unmarshal(value, &name); err == nil {
			entry.DisplayName = name
		} else {
			var structured struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(value, &structured); err == nil {
				entry.DisplayName = structured.Name
			}
		}
	}
	if strings.TrimSpace(entry.Path) == "" {
		return jestProjectEntry{}, errors.New("a Jest project entry declares no rootDir")
	}
	return entry, nil
}

// jsProjectsDeclaration extracts the entries of a literal `projects: [...]`
// array from a JavaScript or TypeScript configuration without executing it.
func jsProjectsDeclaration(source string) ([]jestProjectEntry, bool, error) {
	index := 0
	for index < len(source) {
		char := source[index]
		switch {
		case char == '/' && index+1 < len(source) && source[index+1] == '/':
			index = skipLineComment(source, index)
			continue
		case char == '/' && index+1 < len(source) && source[index+1] == '*':
			index = skipBlockComment(source, index)
			continue
		case char == '"' || char == '\'' || char == '`':
			_, next, ok := readJSString(source, index)
			if !ok {
				return nil, false, errors.New("the Jest configuration contains an unterminated string")
			}
			index = next
			continue
		case isJSIdentifierStart(char):
			start := index
			for index < len(source) && isJSIdentifierPart(source[index]) {
				index++
			}
			if source[start:index] != "projects" {
				continue
			}
			cursor := skipJSSpace(source, index)
			if cursor >= len(source) || source[cursor] != ':' {
				continue
			}
			cursor = skipJSSpace(source, cursor+1)
			if cursor >= len(source) || source[cursor] != '[' {
				continue
			}
			body, _, ok := readJSArray(source, cursor)
			if !ok {
				return nil, true, errors.New("the Jest projects array could not be read")
			}
			entries, err := parseJSProjectEntries(body)
			return entries, true, err
		default:
			index++
		}
	}
	return nil, false, nil
}

// parseJSProjectEntries splits the body of a projects array into entries.
func parseJSProjectEntries(body string) ([]jestProjectEntry, error) {
	var entries []jestProjectEntry
	for _, raw := range splitJSArrayEntries(body) {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		switch {
		case entry[0] == '"' || entry[0] == '\'' || entry[0] == '`':
			value, _, ok := readJSString(entry, 0)
			if !ok {
				return nil, errors.New("a Jest project entry is not a readable string")
			}
			entries = append(entries, jestProjectEntry{Path: value})
		case entry[0] == '{':
			parsed, err := parseJSProjectObject(entry)
			if err != nil {
				return nil, err
			}
			entries = append(entries, parsed)
		default:
			return nil, errors.New("a Jest project entry is not a literal path")
		}
	}
	if len(entries) == 0 {
		return nil, errors.New("the Jest projects array is empty")
	}
	return entries, nil
}

// parseJSProjectObject reads the rootDir and the display name of a project
// object entry.
func parseJSProjectObject(entry string) (jestProjectEntry, error) {
	root, ok := jsObjectStringProperty(entry, "rootDir")
	if !ok {
		return jestProjectEntry{}, errors.New("a Jest project object declares no rootDir")
	}
	parsed := jestProjectEntry{Path: root}
	if name, ok := jsObjectStringProperty(entry, "displayName"); ok {
		parsed.DisplayName = name
	}
	return parsed, nil
}

// jsObjectStringProperty reads a string property of an object literal. A
// displayName may be an object with a name property, which is resolved too.
func jsObjectStringProperty(object, name string) (string, bool) {
	index := 0
	for index < len(object) {
		char := object[index]
		switch {
		case char == '"' || char == '\'' || char == '`':
			_, next, ok := readJSString(object, index)
			if !ok {
				return "", false
			}
			index = next
			continue
		case isJSIdentifierStart(char):
			start := index
			for index < len(object) && isJSIdentifierPart(object[index]) {
				index++
			}
			if object[start:index] != name {
				continue
			}
			cursor := skipJSSpace(object, index)
			if cursor >= len(object) || object[cursor] != ':' {
				continue
			}
			cursor = skipJSSpace(object, cursor+1)
			if cursor >= len(object) {
				return "", false
			}
			if object[cursor] == '"' || object[cursor] == '\'' || object[cursor] == '`' {
				value, _, ok := readJSString(object, cursor)
				if !ok {
					return "", false
				}
				return value, true
			}
			if object[cursor] == '{' {
				if nested, ok := jsObjectStringProperty(object[cursor:], "name"); ok {
					return nested, true
				}
			}
			return "", false
		default:
			index++
		}
	}
	return "", false
}

// splitJSArrayEntries splits an array body at its top level commas.
func splitJSArrayEntries(body string) []string {
	var entries []string
	depth := 0
	start := 0
	index := 0
	for index < len(body) {
		char := body[index]
		switch {
		case char == '/' && index+1 < len(body) && body[index+1] == '/':
			index = skipLineComment(body, index)
			continue
		case char == '/' && index+1 < len(body) && body[index+1] == '*':
			index = skipBlockComment(body, index)
			continue
		case char == '"' || char == '\'' || char == '`':
			_, next, ok := readJSString(body, index)
			if !ok {
				return []string{body}
			}
			index = next
			continue
		case char == '[' || char == '{' || char == '(':
			depth++
		case char == ']' || char == '}' || char == ')':
			depth--
		case char == ',' && depth == 0:
			entries = append(entries, body[start:index])
			start = index + 1
		}
		index++
	}
	if start < len(body) {
		entries = append(entries, body[start:])
	}
	return entries
}

// readJSArray reads the array literal that starts at index and returns its
// body.
func readJSArray(source string, index int) (string, int, bool) {
	depth := 0
	start := index + 1
	for index < len(source) {
		char := source[index]
		switch {
		case char == '/' && index+1 < len(source) && source[index+1] == '/':
			index = skipLineComment(source, index)
			continue
		case char == '/' && index+1 < len(source) && source[index+1] == '*':
			index = skipBlockComment(source, index)
			continue
		case char == '"' || char == '\'' || char == '`':
			_, next, ok := readJSString(source, index)
			if !ok {
				return "", index, false
			}
			index = next
			continue
		case char == '[':
			depth++
		case char == ']':
			depth--
			if depth == 0 {
				return source[start:index], index + 1, true
			}
		}
		index++
	}
	return "", index, false
}

// readJSString reads the quoted string that starts at index and returns its
// value and the index after the closing quote.
func readJSString(source string, index int) (string, int, bool) {
	quote := source[index]
	index++
	var builder strings.Builder
	for index < len(source) {
		char := source[index]
		if char == '\\' && index+1 < len(source) {
			switch source[index+1] {
			case 'n':
				builder.WriteByte('\n')
			case 't':
				builder.WriteByte('\t')
			case 'r':
				builder.WriteByte('\r')
			default:
				builder.WriteByte(source[index+1])
			}
			index += 2
			continue
		}
		if char == quote {
			return builder.String(), index + 1, true
		}
		builder.WriteByte(char)
		index++
	}
	return "", index, false
}

// skipJSSpace skips whitespace and comments.
func skipJSSpace(source string, index int) int {
	for index < len(source) {
		switch source[index] {
		case ' ', '\t', '\r', '\n':
			index++
		case '/':
			if index+1 < len(source) && source[index+1] == '/' {
				index = skipLineComment(source, index)
				continue
			}
			if index+1 < len(source) && source[index+1] == '*' {
				index = skipBlockComment(source, index)
				continue
			}
			return index
		default:
			return index
		}
	}
	return index
}

// skipLineComment skips a line comment.
func skipLineComment(source string, index int) int {
	for index < len(source) && source[index] != '\n' {
		index++
	}
	return index
}

// skipBlockComment skips a block comment.
func skipBlockComment(source string, index int) int {
	end := strings.Index(source[index+2:], "*/")
	if end < 0 {
		return len(source)
	}
	return index + 2 + end + 2
}

// isJSIdentifierStart reports whether the byte starts a JavaScript identifier.
func isJSIdentifierStart(char byte) bool {
	return char == '_' || char == '$' || (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}

// isJSIdentifierPart reports whether the byte continues a JavaScript
// identifier.
func isJSIdentifierPart(char byte) bool {
	return isJSIdentifierStart(char) || (char >= '0' && char <= '9')
}

// resolveJestProjects resolves the project entries to repository relative
// scopes. An entry that points at a configuration file owns the directory
// that contains it.
func resolveJestProjects(absRoot, configDir string, entries []jestProjectEntry) ([]jestProject, error) {
	var projects []jestProject
	seen := map[string]bool{}
	for _, entry := range entries {
		raw := strings.TrimSpace(entry.Path)
		if raw == "" {
			return nil, errors.New("a Jest project entry declares no path")
		}
		expanded := strings.ReplaceAll(raw, "<rootDir>", configDir)
		absolute := expanded
		if !filepath.IsAbs(absolute) {
			absolute = filepath.Join(configDir, filepath.FromSlash(expanded))
		}
		if info, err := os.Stat(absolute); err == nil && !info.IsDir() {
			absolute = filepath.Dir(absolute)
		}
		resolved, err := adapters.ResolveWithin(absRoot, absolute)
		if err != nil {
			return nil, fmt.Errorf("the Jest project %q escapes the repository", raw)
		}
		relative, err := filepath.Rel(absRoot, resolved)
		if err != nil {
			return nil, err
		}
		scope := filepath.ToSlash(relative)
		if scope == "" {
			scope = "."
		}
		if seen[scope] {
			continue
		}
		seen[scope] = true
		projects = append(projects, jestProject{Name: entry.DisplayName, Scope: scope})
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Scope < projects[j].Scope })
	return projects, nil
}

// owners returns the projects whose scope contains the given repository
// relative path. A path can be owned by more than one project, and every
// matching project is a legitimate owner of the path.
func (p jestProjects) owners(path string) []jestProject {
	var owners []jestProject
	for _, project := range p.projects {
		if project.Scope == "." || path == project.Scope || strings.HasPrefix(path, project.Scope+"/") {
			owners = append(owners, project)
		}
	}
	return owners
}

// scope returns the scope of the first project that owns the path.
func (p jestProjects) scope(path string) string {
	if owners := p.owners(path); len(owners) > 0 {
		return owners[0].Scope
	}
	return ""
}
