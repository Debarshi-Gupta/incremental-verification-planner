package api

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
)

// Wave 1 OpenAPI drift gate (docs/adr/0010). The hand-maintained
// api/openapi.yaml must document exactly the routes the chi router serves:
// same method+path set in both directions, with the spec's server mounting
// (global /api/v1, per-path override for the root-mounted health endpoints)
// applied when resolving spec paths to effective URLs.

var specMethods = []string{"get", "post", "put", "patch", "delete", "head", "options"}

type specDocument struct {
	Servers []struct {
		URL string `yaml:"url"`
	} `yaml:"servers"`
	Paths map[string]map[string]any `yaml:"paths"`
}

func loadSpec(t *testing.T) *specDocument {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	var doc specDocument
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}
	if len(doc.Servers) == 0 || doc.Servers[0].URL == "" {
		t.Fatal("openapi.yaml must declare a default server URL")
	}
	if len(doc.Paths) == 0 {
		t.Fatal("openapi.yaml declares no paths")
	}
	return &doc
}

// specRoutes resolves every documented operation to its effective
// "METHOD /full/path" key using the default server unless the path item
// overrides it (OpenAPI Path Item Object `servers` field).
func specRoutes(doc *specDocument) map[string]bool {
	routes := make(map[string]bool)
	for path, item := range doc.Paths {
		base := doc.Servers[0].URL
		if list, ok := item["servers"].([]any); ok && len(list) > 0 {
			if entry, ok := list[0].(map[string]any); ok {
				if url, ok := entry["url"].(string); ok {
					base = url
				}
			}
		}
		effective := strings.TrimSuffix(base, "/") + path
		if base == "/" {
			effective = path
		}
		for _, method := range specMethods {
			if _, ok := item[method]; ok {
				routes[strings.ToUpper(method)+" "+effective] = true
			}
		}
	}
	return routes
}

func routerRoutes(t *testing.T) map[string]bool {
	t.Helper()
	h := NewRouter(Options{})
	mux, ok := h.(chi.Routes)
	if !ok {
		t.Fatalf("NewRouter returned %T, not a chi router", h)
	}
	routes := make(map[string]bool)
	err := chi.Walk(mux, func(method string, route string, handler http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		if route == "/metrics" {
			// Prometheus exposition is an operational endpoint, not part of
			// the public API contract (docs/adr/0010).
			return nil
		}
		routes[method+" "+route] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk router: %v", err)
	}
	if len(routes) == 0 {
		t.Fatal("router exposes no routes")
	}
	return routes
}

func sortedUnion(sets ...map[string]bool) []string {
	seen := map[string]bool{}
	for _, set := range sets {
		for key := range set {
			seen[key] = true
		}
	}
	all := make([]string, 0, len(seen))
	for key := range seen {
		all = append(all, key)
	}
	sort.Strings(all)
	return all
}

func TestOpenAPISpecMatchesRouterRoutes(t *testing.T) {
	served := routerRoutes(t)
	documented := specRoutes(loadSpec(t))

	var undocumented, unserved []string
	for _, key := range sortedUnion(served, documented) {
		switch {
		case !documented[key]:
			undocumented = append(undocumented, key)
		case !served[key]:
			unserved = append(unserved, key)
		}
	}
	if len(undocumented) > 0 {
		t.Errorf("%d router route(s) missing from api/openapi.yaml: %s", len(undocumented), strings.Join(undocumented, ", "))
	}
	if len(unserved) > 0 {
		t.Errorf("%d documented operation(s) not served by the router: %s", len(unserved), strings.Join(unserved, ", "))
	}
}

func TestSpecDeclaresNoUnknownMethodsOrPaths(t *testing.T) {
	doc := loadSpec(t)
	for path, item := range doc.Paths {
		for key, value := range item {
			if key == "servers" || key == "parameters" || key == "summary" || key == "description" || key == "$ref" {
				continue
			}
			known := false
			for _, method := range specMethods {
				if key == method {
					known = true
					break
				}
			}
			if !known {
				t.Errorf("path %s declares unknown operation key %q (%T)", path, key, value)
			}
		}
	}
}
