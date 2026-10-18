package wallet_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

// The conformance suite pins the wallet API's observable behaviour in JSON files shared with the Android app's
// on-device engine (conformance/README.md). Scenario files hold request scripts; golden files hold the
// normalised responses this API produced. Run with -update-conformance to regenerate the golden files.
var updateConformance = flag.Bool("update-conformance", false, "rewrite conformance/golden/*.json from the current API")

type confStep struct {
	ID     string          `json:"id,omitempty"`
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
	// Quiet steps only pin the status code (responses that depend on the identity layer).
	Quiet bool `json:"quiet,omitempty"`
}

type confScenario struct {
	Name  string     `json:"name"`
	Steps []confStep `json:"steps"`
}

type confResult struct {
	Step   string `json:"step"`
	Status int    `json:"status"`
	Body   any    `json:"body"`
}

var (
	uuidRe     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	refRe      = regexp.MustCompile(`\{\{([^}]+)\}\}`)
	volatileTS = map[string]bool{"created_at": true, "updated_at": true, "archived_at": true, "next_cursor": true}
)

// lookup resolves "step.a.b.0.c" in the saved responses.
func lookup(saved map[string]any, ref string) (any, bool) {
	parts := strings.Split(ref, ".")
	cur, ok := saved[parts[0]]
	if !ok {
		return nil, false
	}
	for _, p := range parts[1:] {
		switch v := cur.(type) {
		case map[string]any:
			cur, ok = v[p]
			if !ok {
				return nil, false
			}
		case []any:
			i, err := strconv.Atoi(p)
			if err != nil || i < 0 || i >= len(v) {
				return nil, false
			}
			cur = v[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

func interpolate(t *testing.T, saved map[string]any, s string) string {
	t.Helper()
	return refRe.ReplaceAllStringFunc(s, func(m string) string {
		v, ok := lookup(saved, strings.Trim(m, "{}"))
		if !ok {
			t.Fatalf("unresolved reference %s", m)
		}
		if str, ok := v.(string); ok {
			return str
		}
		b, _ := json.Marshal(v)
		return string(b)
	})
}

// normalizer replaces generated ids by <id:N> (numbered by first appearance in the scenario) and masks
// volatile timestamps so two engines can be compared.
type normalizer struct{ ids map[string]int }

func (n *normalizer) walk(v any, key string, parent map[string]any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			out[k] = n.walk(val, k, x)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = n.walk(val, key, parent)
		}
		return out
	case string:
		if uuidRe.MatchString(x) {
			if _, ok := n.ids[x]; !ok {
				n.ids[x] = len(n.ids) + 1
			}
			return "<id:" + strconv.Itoa(n.ids[x]) + ">"
		}
		if volatileTS[key] {
			return "<ts>"
		}
		// Adjustments are booked at "now".
		if (key == "occurred_at" || key == "sort_at") && parent != nil && parent["source"] == "ADJUSTMENT" {
			return "<ts>"
		}
	}
	return v
}

func TestConformance(t *testing.T) {
	files, _ := filepath.Glob("../../conformance/scenarios/*.json")
	if len(files) == 0 {
		t.Fatal("no conformance scenarios found")
	}
	for _, file := range files {
		file := file
		t.Run(strings.TrimSuffix(filepath.Base(file), ".json"), func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var sc confScenario
			if err := json.Unmarshal(raw, &sc); err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			pool := testutil.CleanDB(t)
			e := newEnv(t, pool)
			u := e.register("Conformance", "conformance-"+sc.Name+"@test.local")

			saved := map[string]any{}
			norm := &normalizer{ids: map[string]int{}}
			var results []confResult
			for i, st := range sc.Steps {
				path := interpolate(t, saved, st.Path)
				var body any
				if len(st.Body) > 0 {
					var parsed any
					if err := json.Unmarshal([]byte(interpolate(t, saved, string(st.Body))), &parsed); err != nil {
						t.Fatalf("step %d body: %v", i, err)
					}
					body = parsed
				}
				status, respRaw := e.raw(st.Method, path, u.token, body, nil)
				var resp any
				if len(respRaw) > 0 {
					if err := json.Unmarshal(respRaw, &resp); err != nil {
						// Non-JSON bodies (CSV exports) are compared as text.
						resp = string(respRaw)
					}
				}
				label := st.ID
				if label == "" {
					label = "#" + strconv.Itoa(i)
				}
				if st.ID != "" {
					saved[st.ID] = resp
				}
				var outBody any
				if !st.Quiet {
					outBody = norm.walk(resp, "", nil)
				}
				results = append(results, confResult{Step: label + " " + st.Method + " " + st.Path, Status: status, Body: outBody})
			}
			out, _ := json.MarshalIndent(results, "", "  ")
			out = append(out, '\n')
			golden := filepath.Join("../../conformance/golden", sc.Name+".json")
			if *updateConformance {
				if err := os.WriteFile(golden, out, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden %s (run with -update-conformance): %v", golden, err)
			}
			var a, b any
			_ = json.Unmarshal(want, &a)
			_ = json.Unmarshal(out, &b)
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("API output differs from %s; if the change is intended run: go test ./internal/wallet -run TestConformance -update-conformance", golden)
			}
		})
	}
}
