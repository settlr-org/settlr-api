package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var routeRe = regexp.MustCompile(`mux\.Handle(?:Func)?\("(GET|POST|PUT|PATCH|DELETE) ([^"]+)"`)

// Every route registered on the mux must be documented in the embedded OpenAPI spec, and the
// repo-root copy must match the embedded one, so clients never code against a stale contract.
func TestOpenAPICoversAllRoutes(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		t.Fatalf("embedded openapi.json: %v", err)
	}
	var files []string
	for _, root := range []string{"../../internal", "."} {
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err == nil && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				files = append(files, p)
			}
			return nil
		})
	}
	seen := 0
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range routeRe.FindAllStringSubmatch(string(src), -1) {
			seen++
			method, path := strings.ToLower(m[1]), m[2]
			if _, ok := spec.Paths[path][method]; !ok {
				t.Errorf("%s %s (in %s) is not documented in cmd/settlr/openapi.json", m[1], path, f)
			}
		}
	}
	if seen < 50 {
		t.Fatalf("only found %d routes; route scan is broken", seen)
	}
	root, err := os.ReadFile("../../openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(root) != string(openapiJSON) {
		t.Error("openapi.json at the repo root differs from cmd/settlr/openapi.json; copy it over")
	}
}
