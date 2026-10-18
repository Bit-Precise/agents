package api

import (
	"encoding/json"
	"os"
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
	source, err := os.ReadFile("../control/handlers.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range regexp.MustCompile(`"(GET|POST|DELETE|PUT|PATCH) (/api/v1/[^" ]+)"`).FindAllStringSubmatch(string(source), -1) {
		item, ok := paths[match[2]].(map[string]any)
		if !ok || item[strings.ToLower(match[1])] == nil {
			t.Errorf("undocumented API %s %s", match[1], match[2])
		}
	}
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
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
	for path, item := range paths {
		for method, value := range item.(map[string]any) {
			op := value.(map[string]any)
			id, _ := op["operationId"].(string)
			if id == "" || seen[id] {
				t.Errorf("missing/duplicate operationId at %s %s", method, path)
			}
			seen[id] = true
		}
	}
}
