package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestOpenAPICoversPublicRoutesAndResolvesSchemas(t *testing.T) {
	data, err := files.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document["openapi"] != "3.1.0" {
		t.Fatal("expected OpenAPI 3.1")
	}
	paths := document["paths"].(map[string]any)
	sources, err := filepath.Glob("../control/*.go")
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	pattern := regexp.MustCompile(`"(GET|POST|DELETE|PUT|PATCH) ((?:/api)?/v1/[^" ]+)"`)
	for _, filename := range sources {
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}
		source, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(source), -1) {
			registered[match[1]+" "+match[2]] = true
			item, ok := paths[match[2]].(map[string]any)
			if !ok || item[strings.ToLower(match[1])] == nil {
				t.Errorf("undocumented API %s %s in %s", match[1], match[2], filename)
			}
		}
	}
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	if schemas["State"] != nil {
		t.Error("aggregate State schema remains in the public API")
	}
	var walk func(any)
	walk = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			if target, ok := node["$ref"].(string); ok {
				prefix := "#/components/schemas/"
				if !strings.HasPrefix(target, prefix) || schemas[strings.TrimPrefix(target, prefix)] == nil {
					t.Errorf("unresolved reference %s", target)
				}
			}
			for _, v := range node {
				walk(v)
			}
		case []any:
			for _, v := range node {
				walk(v)
			}
		}
	}
	walk(document)
	seen := map[string]bool{}
	deviceRoutes := map[string]bool{
		"POST /api/v1/enroll":           true,
		"GET /api/v1/agent/credentials": true,
		"POST /api/v1/agent/config":     true,
	}
	for path, item := range paths {
		for method, value := range item.(map[string]any) {
			if !strings.Contains(" get post delete put patch options head trace ", " "+method+" ") {
				continue
			}
			op := value.(map[string]any)
			id, _ := op["operationId"].(string)
			if id == "" || seen[id] {
				t.Errorf("missing/duplicate operationId at %s %s", method, path)
			}
			seen[id] = true
			route := strings.ToUpper(method) + " " + path
			if strings.HasPrefix(path, "/api/v1/") && !deviceRoutes[route] {
				t.Errorf("obsolete management API remains documented: %s", route)
			}
			if (strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/api/v1/")) && !registered[route] {
				t.Errorf("documented API has no registered handler: %s", route)
			}
			if id == "getState" {
				t.Error("aggregate getState operation remains in the public API")
			}
		}
	}
}
