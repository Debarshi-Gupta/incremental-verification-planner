package config

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Wave 1 schema<->defaults parity gate (docs/adr/0011). The published JSON
// Schema must describe exactly the keys the Go config structs carry, every
// shipped configuration document (product defaults, both templates, and the
// LLD 5.2 example) must validate against it, and schema value constraints must
// agree with config.Validate on boundary values.

func mustLoadSchema(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "schemas", "ivp-config.schema.json"))
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	return doc
}

func mustLoadYAML(t *testing.T, filename string) any {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	return doc
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func scoped(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func derefSchema(root, node map[string]any) map[string]any {
	for i := 0; i < 8; i++ {
		ref, ok := node["$ref"].(string)
		if !ok {
			return node
		}
		name := strings.TrimPrefix(ref, "#/$defs/")
		defs, _ := root["$defs"].(map[string]any)
		next, ok := defs[name].(map[string]any)
		if !ok {
			return node
		}
		node = next
	}
	return node
}

func yamlFields(t *testing.T, typ reflect.Type) map[string]reflect.Type {
	t.Helper()
	fields := make(map[string]reflect.Type, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := strings.Split(field.Tag.Get("yaml"), ",")[0]
		if tag == "" || tag == "-" {
			t.Fatalf("config type %s field %s is missing a yaml tag", typ.Name(), field.Name)
		}
		fields[tag] = field.Type
	}
	return fields
}

// structLeafPaths flattens the Go config tree; arrays are leaves.
func structLeafPaths(t *testing.T, typ reflect.Type, prefix string, out map[string]bool) {
	t.Helper()
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		t.Fatalf("expected struct at %q, got %s", prefix, typ)
	}
	for tag, fieldType := range yamlFields(t, typ) {
		if fieldType.Kind() == reflect.Pointer {
			fieldType = fieldType.Elem()
		}
		path := scoped(prefix, tag)
		if fieldType.Kind() == reflect.Struct {
			structLeafPaths(t, fieldType, path, out)
			continue
		}
		out[path] = true
	}
}

// schemaLeafPaths flattens the schema's property tree; arrays are leaves.
func schemaLeafPaths(root, node map[string]any, prefix string, out map[string]bool) {
	node = derefSchema(root, node)
	props, ok := node["properties"].(map[string]any)
	if !ok {
		out[prefix] = true
		return
	}
	for _, key := range sortedKeys(props) {
		child, _ := props[key].(map[string]any)
		schemaLeafPaths(root, child, scoped(prefix, key), out)
	}
}

func yamlLeafPaths(doc any, prefix string, out map[string]bool) {
	if mapping, ok := doc.(map[string]any); ok {
		for _, key := range sortedKeys(mapping) {
			yamlLeafPaths(mapping[key], scoped(prefix, key), out)
		}
		return
	}
	out[prefix] = true
}

// deepPair walks schema object nodes alongside the matching Go type, recording
// every property path on both sides and enforcing additionalProperties:false.
func deepPair(t *testing.T, root, node map[string]any, typ reflect.Type, prefix string, schemaProps, structProps map[string]bool) {
	t.Helper()
	node = derefSchema(root, node)
	if additional, ok := node["additionalProperties"]; ok {
		if allowed, isBool := additional.(bool); !isBool || allowed {
			t.Errorf("schema node %q must set additionalProperties:false (unknown keys are errors, LLD line 272)", prefix)
		}
	}
	if props, ok := node["properties"].(map[string]any); ok {
		if typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct {
			t.Errorf("schema object at %q has no matching Go struct (type %s)", prefix, typ)
			return
		}
		fields := yamlFields(t, typ)
		for _, key := range sortedKeys(props) {
			schemaProps[scoped(prefix, key)] = true
		}
		for key := range fields {
			structProps[scoped(prefix, key)] = true
		}
		for _, key := range sortedKeys(props) {
			fieldType, ok := fields[key]
			if !ok {
				continue
			}
			child, _ := props[key].(map[string]any)
			deepPair(t, root, child, fieldType, scoped(prefix, key), schemaProps, structProps)
		}
		return
	}
	if items, ok := node["items"].(map[string]any); ok && len(items) > 0 {
		if typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Slice && typ.Kind() != reflect.Array {
			t.Errorf("schema array at %q has no matching Go slice (type %s)", prefix, typ)
			return
		}
		deepPair(t, root, items, typ.Elem(), prefix+"[]", schemaProps, structProps)
	}
}

func reportSetDiff(t *testing.T, label string, want, got map[string]bool) {
	t.Helper()
	var missing, extra []string
	for key := range want {
		if !got[key] {
			missing = append(missing, key)
		}
	}
	for key := range got {
		if !want[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("%s: missing %d key(s): %s", label, len(missing), strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		t.Errorf("%s: unexpected %d key(s): %s", label, len(extra), strings.Join(extra, ", "))
	}
}

// --- minimal JSON Schema (2020-12 subset) evaluator ---

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

func sameScalar(a, b any) bool {
	af, aok := toFloat(a)
	bf, bok := toFloat(b)
	if aok && bok {
		return af == bf
	}
	return reflect.DeepEqual(a, b)
}

func jsonTypeName(v any) string {
	switch value := v.(type) {
	case map[string]any:
		_ = value
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case int, int64:
		return "integer"
	case float64:
		if value == math.Trunc(value) {
			return "integer"
		}
		return "number"
	}
	return "unknown"
}

func typeMatches(want string, v any) bool {
	got := jsonTypeName(v)
	switch want {
	case "number":
		return got == "number" || got == "integer"
	case "integer":
		return got == "integer"
	default:
		return got == want
	}
}

func schemaValidateNode(root, node map[string]any, path string, v any, errs *[]string) {
	node = derefSchema(root, node)
	if want, ok := node["const"]; ok && !sameScalar(want, v) {
		*errs = append(*errs, fmt.Sprintf("%s: %v is not the only allowed value", path, v))
	}
	if enum, ok := node["enum"].([]any); ok {
		matched := false
		for _, allowed := range enum {
			if sameScalar(allowed, v) {
				matched = true
				break
			}
		}
		if !matched {
			*errs = append(*errs, fmt.Sprintf("%s: %v is not one of %v", path, v, enum))
		}
	}
	if want, ok := node["type"].(string); ok && !typeMatches(want, v) {
		*errs = append(*errs, fmt.Sprintf("%s: %v is not of type %s", path, v, want))
		return
	}
	switch value := v.(type) {
	case string:
		if min, ok := node["minLength"].(float64); ok && float64(len(value)) < min {
			*errs = append(*errs, fmt.Sprintf("%s: shorter than minLength %d", path, int(min)))
		}
		if pattern, ok := node["pattern"].(string); ok {
			re, err := regexp.Compile(pattern)
			if err != nil {
				*errs = append(*errs, fmt.Sprintf("%s: schema pattern does not compile: %v", path, err))
			} else if !re.MatchString(value) {
				*errs = append(*errs, fmt.Sprintf("%s: %q does not match pattern %q", path, value, pattern))
			}
		}
	case []any:
		if min, ok := node["minItems"].(float64); ok && float64(len(value)) < min {
			*errs = append(*errs, fmt.Sprintf("%s: fewer than minItems %d", path, int(min)))
		}
		if items, ok := node["items"].(map[string]any); ok {
			for i, item := range value {
				schemaValidateNode(root, items, fmt.Sprintf("%s[%d]", path, i), item, errs)
			}
		}
	case map[string]any:
		props, _ := node["properties"].(map[string]any)
		if required, ok := node["required"].([]any); ok {
			for _, name := range required {
				key, isString := name.(string)
				if !isString {
					continue
				}
				if _, present := value[key]; !present {
					*errs = append(*errs, fmt.Sprintf("%s: missing required key %q", path, key))
				}
			}
		}
		for _, key := range sortedKeys(value) {
			child, ok := props[key]
			if !ok {
				if additional, present := node["additionalProperties"]; present {
					if allowed, isBool := additional.(bool); !isBool || allowed {
						_ = allowed
						continue
					}
					*errs = append(*errs, fmt.Sprintf("%s: unknown key %q", path, key))
					continue
				}
				continue
			}
			childNode, _ := child.(map[string]any)
			schemaValidateNode(root, childNode, scoped(path, key), value[key], errs)
		}
	}
	if number, isNumber := toFloat(v); isNumber {
		if min, ok := node["minimum"].(float64); ok && number < min {
			*errs = append(*errs, fmt.Sprintf("%s: %v below minimum %v", path, number, min))
		}
		if max, ok := node["maximum"].(float64); ok && number > max {
			*errs = append(*errs, fmt.Sprintf("%s: %v above maximum %v", path, number, max))
		}
		if min, ok := node["exclusiveMinimum"].(float64); ok && number <= min {
			*errs = append(*errs, fmt.Sprintf("%s: %v not above exclusiveMinimum %v", path, number, min))
		}
	}
}

func schemaErrors(root map[string]any, doc any) []string {
	var errs []string
	schemaValidateNode(root, root, "", doc, &errs)
	return errs
}

// --- tests ---

func TestSchemaMatchesConfigStructTree(t *testing.T) {
	root := mustLoadSchema(t)
	structLeaves := map[string]bool{}
	structLeafPaths(t, reflect.TypeOf(Config{}), "", structLeaves)
	schemaLeaves := map[string]bool{}
	schemaLeafPaths(root, root, "", schemaLeaves)
	reportSetDiff(t, "leaf keys (schema vs config.Defaults type tree)", schemaLeaves, structLeaves)

	schemaProps := map[string]bool{}
	structProps := map[string]bool{}
	deepPair(t, root, root, reflect.TypeOf(Config{}), "", schemaProps, structProps)
	reportSetDiff(t, "nested property keys (schema vs config structs)", schemaProps, structProps)
}

func TestShippedAndDocumentedConfigsValidateAgainstSchema(t *testing.T) {
	root := mustLoadSchema(t)
	defaultJSON, err := json.Marshal(Defaults())
	if err != nil {
		t.Fatalf("marshal defaults: %v", err)
	}
	var defaultsDoc any
	if err := json.Unmarshal(defaultJSON, &defaultsDoc); err != nil {
		t.Fatalf("decode defaults: %v", err)
	}
	if errs := schemaErrors(root, defaultsDoc); len(errs) > 0 {
		t.Errorf("config.Defaults() violates the published schema: %v", errs)
	}
}

func TestLLDExampleAndTemplatesValidate(t *testing.T) {
	root := mustLoadSchema(t)
	documents := map[string]any{
		"config/default-config.yaml":       mustLoadYAML(t, filepath.Join("..", "..", "config", "default-config.yaml")),
		"internal/cli/default-config.yaml": mustLoadYAML(t, filepath.Join("..", "..", "internal", "cli", "default-config.yaml")),
		"LLD 5.2 example":                  lldExampleConfig(t),
	}
	for name, doc := range documents {
		if errs := schemaErrors(root, doc); len(errs) > 0 {
			t.Errorf("%s violates the published schema: %v", name, errs)
		}
	}
}

// lldExampleConfig extracts the unfenced YAML example under LLD section 5.2.
func lldExampleConfig(t *testing.T) any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "Incremental_Verification_Planner_LLD.md"))
	if err != nil {
		t.Fatalf("read LLD: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "5.2 Example complete configuration" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatal("LLD section 5.2 heading not found")
	}
	var body []string
	for _, line := range lines[start:] {
		if strings.HasPrefix(strings.TrimSpace(line), "This example is schema-complete") {
			break
		}
		body = append(body, line)
	}
	if len(body) < 50 {
		t.Fatalf("LLD 5.2 example extraction looks wrong: only %d lines", len(body))
	}
	var doc any
	if err := yaml.Unmarshal([]byte(strings.Join(body, "\n")), &doc); err != nil {
		t.Fatalf("LLD 5.2 example is not valid YAML: %v", err)
	}
	return doc
}

func TestShippedTemplatesAreKeyCompleteAndIdentical(t *testing.T) {
	structLeaves := map[string]bool{}
	structLeafPaths(t, reflect.TypeOf(Config{}), "", structLeaves)
	templatePaths := map[string]map[string]bool{}
	for _, name := range []string{filepath.Join("..", "..", "config", "default-config.yaml"), filepath.Join("..", "..", "internal", "cli", "default-config.yaml")} {
		keys := map[string]bool{}
		yamlLeafPaths(mustLoadYAML(t, name), "", keys)
		templatePaths[name] = keys
		reportSetDiff(t, "template keys vs config struct keys ("+name+")", structLeaves, keys)
		if _, err := LoadFile(name); err != nil {
			t.Errorf("shipped template %s does not load: %v", name, err)
		}
	}
	var first string
	for name := range templatePaths {
		if first == "" {
			first = name
			continue
		}
		reportSetDiff(t, "template key drift between shipped copies", templatePaths[first], templatePaths[name])
	}
}

// TestSchemaAndRuntimeValidationAgreeOnBoundaries feeds small YAML documents to
// both the published schema and config.Validate; both must reach the same
// verdict on every boundary value (schema<->defaults parity, ADR 0011).
// stringSet normalizes a YAML or Go string list into a set for value-parity checks.
func stringSet(t *testing.T, label string, value any) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	switch list := value.(type) {
	case []any:
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				t.Fatalf("%s: non-string entry %v", label, item)
			}
			out[s] = true
		}
	case []string:
		for _, s := range list {
			out[s] = true
		}
	default:
		t.Fatalf("%s: unexpected list type %T", label, value)
	}
	return out
}

// TestSafetyDefaultsMatchLLDExampleAndShippedTemplates pins the three LLD 5.2
// safety value lists (hard full-suite paths, hard risk labels, always-run check
// IDs): the example extracted from the LLD, config.Defaults(),
// config/default-config.yaml, and internal/cli/default-config.yaml must carry
// identical sets. The published schema deliberately carries no literal default
// values (ADR 0011 rule 6), and the key-only template comparison above cannot
// see value drift, so this test owns value parity (LLD lines 321-355).
func TestSafetyDefaultsMatchLLDExampleAndShippedTemplates(t *testing.T) {
	lldRoot, ok := lldExampleConfig(t).(map[string]any)
	if !ok {
		t.Fatal("LLD 5.2 example root is not a mapping")
	}
	lldSafety, ok := lldRoot["safety"].(map[string]any)
	if !ok {
		t.Fatal("LLD 5.2 example has no safety mapping")
	}
	cfgTemplate, ok := mustLoadYAML(t, filepath.Join("..", "..", "config", "default-config.yaml")).(map[string]any)
	if !ok {
		t.Fatal("config/default-config.yaml root is not a mapping")
	}
	cliTemplate, ok := mustLoadYAML(t, filepath.Join("..", "..", "internal", "cli", "default-config.yaml")).(map[string]any)
	if !ok {
		t.Fatal("internal/cli/default-config.yaml root is not a mapping")
	}
	cfgSafety, ok := cfgTemplate["safety"].(map[string]any)
	if !ok {
		t.Fatal("config/default-config.yaml has no safety mapping")
	}
	cliSafety, ok := cliTemplate["safety"].(map[string]any)
	if !ok {
		t.Fatal("internal/cli/default-config.yaml has no safety mapping")
	}
	defaults := Defaults().Safety
	for _, field := range []string{"hard_full_suite_paths", "hard_full_suite_risk_labels", "always_run_check_ids"} {
		label := "safety." + field
		want := stringSet(t, "LLD 5.2 example "+label, lldSafety[field])
		if len(want) == 0 {
			t.Errorf("LLD 5.2 example %s is empty", label)
		}
		var defaultsValue any
		switch field {
		case "hard_full_suite_paths":
			defaultsValue = defaults.HardFullSuitePaths
		case "hard_full_suite_risk_labels":
			defaultsValue = defaults.HardFullSuiteRiskLabels
		case "always_run_check_ids":
			defaultsValue = defaults.AlwaysRunCheckIDs
		}
		sources := map[string]any{
			"config.Defaults()":                defaultsValue,
			"config/default-config.yaml":       cfgSafety[field],
			"internal/cli/default-config.yaml": cliSafety[field],
		}
		for name, value := range sources {
			got := stringSet(t, name+" "+label, value)
			reportSetDiff(t, name+" "+label+" vs LLD 5.2 example", want, got)
		}
	}
}

func TestSchemaAndRuntimeValidationAgreeOnBoundaries(t *testing.T) {
	root := mustLoadSchema(t)
	cases := []struct {
		name      string
		document  string
		wantValid bool
	}{
		{"defaults of excerpt cap accepted", "telemetry:\n  max_excerpt_bytes: 8192\n", true},
		{"runtime excerpt ceiling accepted", "telemetry:\n  max_excerpt_bytes: 1048576\n", true},
		{"excerpt ceiling+1 rejected", "telemetry:\n  max_excerpt_bytes: 1048577\n", false},
		{"low risk band edge accepted", "selection:\n  low_risk_max_score: 24\n", true},
		{"low risk band overflow rejected", "selection:\n  low_risk_max_score: 25\n", false},
		{"medium risk band edge accepted", "selection:\n  medium_risk_max_score: 59\n", true},
		{"medium risk band below range rejected", "selection:\n  medium_risk_max_score: 24\n", false},
		{"confidence floor accepted", "selection:\n  minimum_selection_confidence: 0.98\n", true},
		{"confidence below floor rejected", "selection:\n  minimum_selection_confidence: 0.97\n", false},
		{"runtime fraction edge accepted", "selection:\n  max_selective_estimated_runtime_fraction: 0.70\n", true},
		{"runtime fraction over ceiling rejected", "selection:\n  max_selective_estimated_runtime_fraction: 0.71\n", false},
		{"runtime fraction zero rejected", "selection:\n  max_selective_estimated_runtime_fraction: 0\n", false},
		{"coverage floor accepted", "selection:\n  low_risk_min_weighted_coverage: 0.95\n", true},
		{"coverage below floor rejected", "selection:\n  low_risk_min_weighted_coverage: 0.90\n", false},
		{"one calibration run accepted", "modes:\n  risk_based_ci:\n    minimum_calibration_runs: 1\n", true},
		{"zero calibration runs rejected", "modes:\n  risk_based_ci:\n    minimum_calibration_runs: 0\n", false},
		{"zero audits before gating rejected", "safety:\n  audit:\n    minimum_audits_before_selective_ci: 0\n", false},
		{"retry ceiling accepted", "execution:\n  failure_retry_count: 3\n", true},
		{"retry over ceiling rejected", "execution:\n  failure_retry_count: 4\n", false},
		{"history window ceiling accepted", "analysis:\n  history_window_days: 3650\n", true},
		{"history window over ceiling rejected", "analysis:\n  history_window_days: 3651\n", false},
		{"flake minimum accepted", "analysis:\n  flake_minimum_comparable_observations: 1\n", true},
		{"flake minimum default accepted", "analysis:\n  flake_minimum_comparable_observations: 5\n", true},
		{"flake minimum zero rejected", "analysis:\n  flake_minimum_comparable_observations: 0\n", false},
		{"flake minimum negative rejected", "analysis:\n  flake_minimum_comparable_observations: -1\n", false},
		{"raw stdout capture rejected", "telemetry:\n  capture_raw_stdout: true\n", false},
		{"observe without full suite rejected", "modes:\n  observe:\n    execute_full_suite: false\n", false},
		{"fast feedback without final CI gate rejected", "modes:\n  fast_feedback:\n    final_ci_full_suite_required: false\n", false},
		{"quarantine disabled rejected", "safety:\n  audit:\n    quarantine_on_confirmed_miss: false\n", false},
		{"quarantine hours zero rejected", "safety:\n  audit:\n    quarantine_hours: 0\n", false},
		{"offline policy override rejected", "server:\n  offline_policy: IGNORE\n", false},
		{"require clean dependency resolution false rejected", "analysis:\n  require_clean_dependency_resolution: false\n", false},
		{"whitespace slug rejected", "repository:\n  slug: ' '\n", false},
		{"empty endpoint accepted", "server:\n  endpoint: ''\n", true},
		{"credentials endpoint rejected", "server:\n  endpoint: https://user@example.com\n", false},
		{"query endpoint rejected", "server:\n  endpoint: 'https://example.com?x=1'\n", false},
		{"output bytes ceiling accepted", "execution:\n  max_output_bytes: 67108864\n", true},
		{"output bytes over ceiling rejected", "execution:\n  max_output_bytes: 67108865\n", false},
		{"retention zero rejected", "telemetry:\n  retention_days: 0\n", false},
		{"unknown key rejected", "unknown_setting: true\n", false},
		{"reviewed exclusion accepted", "safety:\n  reviewed_exclusions:\n    - unit_key: unit:legacy-suite\n      reviewer: release-owner\n      reviewed_at: '2020-01-01'\n      rationale: reviewed flaky legacy suite\n", true},
		{"empty exclusion list accepted", "safety:\n  reviewed_exclusions: []\n", true},
		{"whitespace exclusion field rejected", "safety:\n  reviewed_exclusions:\n    - unit_key: ' '\n      reviewer: release-owner\n      reviewed_at: '2020-01-01'\n      rationale: reviewed\n", false},
		{"exclusion date shape rejected", "safety:\n  reviewed_exclusions:\n    - unit_key: unit:legacy-suite\n      reviewer: release-owner\n      reviewed_at: '2020/01/01'\n      rationale: reviewed\n", false},
		{"exclusion missing rationale rejected", "safety:\n  reviewed_exclusions:\n    - unit_key: unit:legacy-suite\n      reviewer: release-owner\n      reviewed_at: '2020-01-01'\n", false},
		{"exclusion unknown key rejected", "safety:\n  reviewed_exclusions:\n    - unit_key: unit:legacy-suite\n      reviewer: release-owner\n      reviewed_at: '2020-01-01'\n      rationale: reviewed\n      approved_by: lead\n", false},
		{"hard-path exception accepted", "safety:\n  hard_path_exceptions:\n    - path: package-lock.json\n      rule: LOCKFILE_CHANGE\n      reviewer: build-owner\n      reviewed_at: '2020-01-01'\n      rationale: reviewed dependency refresh\n", true},
		{"empty exception list accepted", "safety:\n  hard_path_exceptions: []\n", true},
		{"whitespace exception field rejected", "safety:\n  hard_path_exceptions:\n    - path: package-lock.json\n      rule: LOCKFILE_CHANGE\n      reviewer: ' '\n      reviewed_at: '2020-01-01'\n      rationale: reviewed\n", false},
		{"exception date shape rejected", "safety:\n  hard_path_exceptions:\n    - path: package-lock.json\n      rule: LOCKFILE_CHANGE\n      reviewer: build-owner\n      reviewed_at: '2020/01/01'\n      rationale: reviewed\n", false},
		{"exception missing rationale rejected", "safety:\n  hard_path_exceptions:\n    - path: package-lock.json\n      rule: LOCKFILE_CHANGE\n      reviewer: build-owner\n      reviewed_at: '2020-01-01'\n", false},
		{"exception unknown rule rejected", "safety:\n  hard_path_exceptions:\n    - path: package-lock.json\n      rule: SECURITY_CRITICAL_CHANGE\n      reviewer: build-owner\n      reviewed_at: '2020-01-01'\n      rationale: reviewed\n", false},
		{"exception unknown key rejected", "safety:\n  hard_path_exceptions:\n    - path: package-lock.json\n      rule: LOCKFILE_CHANGE\n      reviewer: build-owner\n      reviewed_at: '2020-01-01'\n      rationale: reviewed\n      approved_by: lead\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var doc any
			if err := yaml.Unmarshal([]byte(tc.document), &doc); err != nil {
				t.Fatalf("case document does not parse: %v", err)
			}
			schemaErrs := schemaErrors(root, doc)
			schemaValid := len(schemaErrs) == 0

			cfg, decodeErr := Decode(strings.NewReader(tc.document), Defaults())
			runtimeErr := decodeErr
			if runtimeErr == nil {
				runtimeErr = Validate(cfg)
			}
			runtimeValid := runtimeErr == nil

			if schemaValid != tc.wantValid {
				t.Errorf("schema verdict = %v, want %v (errors: %v)", schemaValid, tc.wantValid, schemaErrs)
			}
			if runtimeValid != tc.wantValid {
				t.Errorf("config.Validate verdict = %v, want %v (error: %v)", runtimeValid, tc.wantValid, runtimeErr)
			}
			if schemaValid != runtimeValid {
				t.Errorf("schema and config.Validate disagree: schema=%v runtime=%v (schema errors: %v, runtime error: %v)", schemaValid, runtimeValid, schemaErrs, runtimeErr)
			}
		})
	}
}
