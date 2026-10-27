package api

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// T-15.8 (D-15.3): the hand-written structs and the cli/testdata fixtures are checked against
// contracts/openapi.json, with the standard library only. `make cli-contract-check` runs the
// tests named TestContract*.

const contractPath = "../../../contracts/openapi.json"

// structSchemas maps every response struct to its component schema.
var structSchemas = map[reflect.Type]string{
	reflect.TypeFor[TimeWindow]():      "TimeWindow",
	reflect.TypeFor[GraphNode]():       "GraphNode",
	reflect.TypeFor[GraphEdge]():       "GraphEdge",
	reflect.TypeFor[GraphSummary]():    "GraphSummary",
	reflect.TypeFor[GraphResponse]():   "GraphResponse",
	reflect.TypeFor[NamespaceList]():   "NamespaceList",
	reflect.TypeFor[NodeDependency]():  "NodeDependency",
	reflect.TypeFor[NodeDetail]():      "NodeDetail",
	reflect.TypeFor[EdgeDetail]():      "EdgeDetail",
	reflect.TypeFor[DiffEdge]():        "DiffEdge",
	reflect.TypeFor[DiffSummary]():     "DiffSummary",
	reflect.TypeFor[DiffResponse]():    "DiffResponse",
	reflect.TypeFor[ErrorResponse]():   "ErrorResponse",
	reflect.TypeFor[Anomaly]():         "Anomaly",
	reflect.TypeFor[AnomalySummary]():  "AnomalySummary",
	reflect.TypeFor[AnomalyResponse](): "AnomalyResponse",
}

// fixtureSchemas maps every file in cli/testdata to the schema of the response it captured.
var fixtureSchemas = map[string]string{
	"namespaces.json":              "NamespaceList",
	"graph.json":                   "GraphResponse",
	"graph.empty.json":             "GraphResponse",
	"graph.query-backend.json":     "GraphResponse",
	"node.deployment-backend.json": "NodeDetail",
	"edge.json":                    "EdgeDetail",
	"edge.not-found.json":          "ErrorResponse",
	"diff.json":                    "DiffResponse",
	"anomalies.json":               "AnomalyResponse",
	"graph.export.json":            "GraphResponse",
}

type schema = map[string]any

func loadContract(t *testing.T) map[string]schema {
	t.Helper()
	b, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	return parseContract(t, b)
}

func parseContract(t *testing.T, b []byte) map[string]schema {
	t.Helper()
	var doc struct {
		Components struct {
			Schemas map[string]schema `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Components.Schemas) == 0 {
		t.Fatal("the contract has no component schemas")
	}
	return doc.Components.Schemas
}

// ── Struct conformance ─────────────────────────────────────────────────────────────────────

// checkStructs reports every JSON field the structs decode that the contract lacks, or types
// differently. A nullable contract field must be a pointer, so null stays distinguishable from
// zero (topology-contract §6).
func checkStructs(schemas map[string]schema) []string {
	var problems []string
	for typ, name := range structSchemas {
		s, ok := schemas[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: no component schema %q", typ.Name(), name))
			continue
		}
		props, _ := s["properties"].(map[string]any)
		for i := range typ.NumField() {
			f := typ.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")[0]
			if tag == "" || tag == "-" {
				continue
			}
			p, ok := props[tag].(map[string]any)
			if !ok {
				problems = append(problems, fmt.Sprintf("%s.%s: the contract's %s has no field %q", typ.Name(), f.Name, name, tag))
				continue
			}
			if msg := compatible(f.Type, p); msg != "" {
				problems = append(problems, fmt.Sprintf("%s.%s (%q): %s", typ.Name(), f.Name, tag, msg))
			}
		}
	}
	sort.Strings(problems)
	return problems
}

// compatible says why a Go type cannot decode values of schema s, or "" if it can.
func compatible(t reflect.Type, s schema) string {
	branches, nullable := nonNull(s)
	if t.Kind() == reflect.Pointer {
		if !nullable {
			return "a pointer for a field the contract never sends as null"
		}
		t = t.Elem()
	} else if nullable {
		return "the contract allows null, so the field must be a pointer"
	}
	for _, b := range branches {
		if msg := compatibleOne(t, b); msg != "" {
			return msg
		}
	}
	return ""
}

// nonNull splits `anyOf [{…}, {"type": "null"}]` into its non-null branches.
func nonNull(s schema) (branches []schema, nullable bool) {
	alts, ok := s["anyOf"].([]any)
	if !ok {
		return []schema{s}, false
	}
	for _, b := range alts {
		bs, _ := b.(map[string]any)
		if bs["type"] == "null" {
			nullable = true
			continue
		}
		branches = append(branches, bs)
	}
	return branches, nullable
}

func compatibleOne(t reflect.Type, s schema) string {
	if ref, ok := s["$ref"].(string); ok {
		want := strings.TrimPrefix(ref, "#/components/schemas/")
		if got, ok := structSchemas[t]; !ok || got != want {
			return fmt.Sprintf("Go type %s for a %s", t, want)
		}
		return ""
	}
	typ, _ := s["type"].(string)
	switch {
	case t == reflect.TypeFor[time.Time]():
		if typ != "string" || s["format"] != "date-time" {
			return fmt.Sprintf("time.Time for type %q format %v", typ, s["format"])
		}
	case t.Kind() == reflect.String:
		if typ != "string" {
			return fmt.Sprintf("string for type %q", typ)
		}
	case t.Kind() == reflect.Bool:
		if typ != "boolean" {
			return fmt.Sprintf("bool for type %q", typ)
		}
	case t.Kind() >= reflect.Int && t.Kind() <= reflect.Int64:
		if typ != "integer" {
			return fmt.Sprintf("integer for type %q", typ)
		}
	case t.Kind() == reflect.Float64:
		if typ != "number" && typ != "integer" {
			return fmt.Sprintf("float64 for type %q", typ)
		}
	case t.Kind() == reflect.Slice:
		items, _ := s["items"].(map[string]any)
		if typ != "array" || items == nil {
			return fmt.Sprintf("slice for type %q", typ)
		}
		return compatible(t.Elem(), items)
	default:
		return fmt.Sprintf("unchecked Go type %s", t)
	}
	return ""
}

func TestContractStructsMatchTheSchemas(t *testing.T) {
	if problems := checkStructs(loadContract(t)); len(problems) > 0 {
		t.Errorf("the structs drifted from contracts/openapi.json:\n  %s", strings.Join(problems, "\n  "))
	}
}

// T-15.8: renaming or retyping a field the structs read, in a copy of the contract, fails.
func TestContractStructCheckCatchesARenameAndARetype(t *testing.T) {
	b, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	renamed := parseContract(t, b)
	props := renamed["GraphEdge"]["properties"].(map[string]any)
	props["connections"] = props["connection_count"]
	delete(props, "connection_count")
	if problems := checkStructs(renamed); !containsAll(problems, `GraphEdge.ConnectionCount: the contract's GraphEdge has no field "connection_count"`) {
		t.Errorf("a renamed field was not caught: %v", problems)
	}

	retyped := parseContract(t, b)
	retyped["GraphSummary"]["properties"].(map[string]any)["truncated"] = map[string]any{"type": "string"}
	if problems := checkStructs(retyped); !containsAll(problems, `GraphSummary.Truncated ("truncated"): bool for type "string"`) {
		t.Errorf("a retyped field was not caught: %v", problems)
	}

	nullable := parseContract(t, b)
	nullable["GraphNode"]["properties"].(map[string]any)["name"] = map[string]any{
		"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}}}
	if problems := checkStructs(nullable); !containsAll(problems, `GraphNode.Name ("name"): the contract allows null`) {
		t.Errorf("a field turned nullable was not caught: %v", problems)
	}
}

func containsAll(problems []string, prefix string) bool {
	return slices.ContainsFunc(problems, func(p string) bool { return strings.HasPrefix(p, prefix) })
}

// ── Fixture conformance ────────────────────────────────────────────────────────────────────

// walker validates a decoded JSON value against a schema. It handles exactly the keywords the
// contract uses and fails on any other, so it cannot silently under-check a contract that grows.
// Objects are treated as closed: a field the schema does not list fails unless the schema says
// additionalProperties, so a fixture cannot carry a field the contract lacks.
type walker struct {
	schemas map[string]schema
	errs    []string
}

// annotations change nothing about validity.
var annotations = map[string]bool{"title": true, "description": true, "default": true, "examples": true}

func (w *walker) fail(path, format string, args ...any) {
	w.errs = append(w.errs, path+": "+fmt.Sprintf(format, args...))
}

// knownKeyword reports whether the walker handles k (or k changes nothing about validity).
func knownKeyword(k string) bool {
	switch k {
	case "$ref", "type", "properties", "required", "items", "anyOf", "const", "enum",
		"additionalProperties", "format", "minimum", "maximum", "minLength", "maxLength", "maxItems":
		return true
	}
	return annotations[k]
}

func (w *walker) walk(path string, v any, s schema) {
	for k := range s {
		if !knownKeyword(k) {
			w.fail(path, "the walker does not know the schema keyword %q", k)
			return
		}
	}
	if ref, ok := s["$ref"].(string); ok {
		name, local := strings.CutPrefix(ref, "#/components/schemas/")
		target, found := w.schemas[name]
		if !local || !found {
			w.fail(path, "unresolvable $ref %q", ref)
			return
		}
		w.walk(path, v, target)
		return
	}
	if branches, ok := s["anyOf"].([]any); ok {
		for _, b := range branches {
			sub := &walker{schemas: w.schemas}
			sub.walk(path, v, b.(map[string]any))
			if len(sub.errs) == 0 {
				return
			}
			// An unknown keyword in any branch is a walker failure, not a mismatch.
			for _, e := range sub.errs {
				if strings.Contains(e, "does not know the schema keyword") {
					w.errs = append(w.errs, e)
					return
				}
			}
		}
		w.fail(path, "%s matches no anyOf branch", short(v))
		return
	}
	if c, ok := s["const"]; ok && !reflect.DeepEqual(v, c) {
		w.fail(path, "%s is not the const %v", short(v), c)
	}
	if enum, ok := s["enum"].([]any); ok && !slices.ContainsFunc(enum, func(e any) bool { return reflect.DeepEqual(e, v) }) {
		w.fail(path, "%s is not one of %v", short(v), enum)
	}
	typ, _ := s["type"].(string)
	switch typ {
	case "object":
		w.object(path, v, s)
	case "array":
		arr, ok := v.([]any)
		if !ok {
			w.fail(path, "%s is not an array", short(v))
			return
		}
		if m, ok := s["maxItems"].(float64); ok && float64(len(arr)) > m {
			w.fail(path, "%d items, at most %v allowed", len(arr), m)
		}
		if items, ok := s["items"].(map[string]any); ok {
			for i, x := range arr {
				w.walk(fmt.Sprintf("%s[%d]", path, i), x, items)
			}
		}
	case "string":
		str, ok := v.(string)
		if !ok {
			w.fail(path, "%s is not a string", short(v))
			return
		}
		if m, ok := s["minLength"].(float64); ok && float64(len([]rune(str))) < m {
			w.fail(path, "shorter than %v", m)
		}
		if m, ok := s["maxLength"].(float64); ok && float64(len([]rune(str))) > m {
			w.fail(path, "longer than %v", m)
		}
		if s["format"] == "date-time" {
			if _, err := time.Parse(time.RFC3339Nano, str); err != nil {
				w.fail(path, "%q is not an RFC 3339 date-time", str)
			}
		}
	case "integer", "number":
		n, ok := v.(float64)
		if !ok || (typ == "integer" && n != math.Trunc(n)) {
			w.fail(path, "%s is not an %s", short(v), typ)
			return
		}
		if m, ok := s["minimum"].(float64); ok && n < m {
			w.fail(path, "%v is below the minimum %v", n, m)
		}
		if m, ok := s["maximum"].(float64); ok && n > m {
			w.fail(path, "%v is above the maximum %v", n, m)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			w.fail(path, "%s is not a boolean", short(v))
		}
	case "null":
		if v != nil {
			w.fail(path, "%s is not null", short(v))
		}
	case "":
		// Constrained only by const/enum above.
	default:
		w.fail(path, "unknown type %q", typ)
	}
}

func (w *walker) object(path string, v any, s schema) {
	obj, ok := v.(map[string]any)
	if !ok {
		w.fail(path, "%s is not an object", short(v))
		return
	}
	for _, r := range asSlice(s["required"]) {
		if _, ok := obj[r.(string)]; !ok {
			w.fail(path, "required field %q is missing", r)
		}
	}
	props, _ := s["properties"].(map[string]any)
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if p, ok := props[k].(map[string]any); ok {
			w.walk(path+"."+k, obj[k], p)
			continue
		}
		switch extra := s["additionalProperties"].(type) {
		case map[string]any:
			w.walk(path+"."+k, obj[k], extra)
		case bool:
			if !extra {
				w.fail(path, "field %q is not in the schema", k)
			}
		default:
			w.fail(path, "field %q is not in the schema", k)
		}
	}
}

func asSlice(v any) []any { s, _ := v.([]any); return s }

func short(v any) string {
	b, _ := json.Marshal(v)
	if len(b) > 60 {
		return string(b[:60]) + "…"
	}
	return string(b)
}

func validate(schemas map[string]schema, name string, data []byte) []string {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return []string{err.Error()}
	}
	w := &walker{schemas: schemas}
	w.walk("$", v, schema{"$ref": "#/components/schemas/" + name})
	return w.errs
}

func TestContractFixturesMatchTheirResponseSchemas(t *testing.T) {
	schemas := loadContract(t)
	files, err := filepath.Glob("../../testdata/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no fixtures in cli/testdata")
	}
	for _, f := range files {
		name, ok := fixtureSchemas[filepath.Base(f)]
		if !ok {
			t.Errorf("%s: no response schema mapped for this fixture", filepath.Base(f))
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if errs := validate(schemas, name, data); len(errs) > 0 {
			t.Errorf("%s against %s:\n  %s", filepath.Base(f), name, strings.Join(errs, "\n  "))
		}
	}
}

// T-15.8: a fixture with a field the schema lacks fails; so does a wrong type, a missing required
// field, and an unknown schema keyword.
func TestContractWalkerCatchesDrift(t *testing.T) {
	schemas := loadContract(t)
	ns := `{"window": {"start": "2026-08-10T11:00:00Z", "end": "2026-08-10T12:00:00Z"}, "namespaces": ["demo"]`
	for _, tc := range []struct {
		name, body, want string
	}{
		{"extra field", ns + `, "clusters": []}`, `field "clusters" is not in the schema`},
		{"wrong type", `{"window": {"start": "2026-08-10T11:00:00Z", "end": "x"}, "namespaces": ["demo"]}`, "not an RFC 3339"},
		{"missing field", `{"namespaces": ["demo"]}`, `required field "window" is missing`},
		{"null where not nullable", `{"window": null, "namespaces": ["demo"]}`, "not an object"},
	} {
		errs := validate(schemas, "NamespaceList", []byte(tc.body))
		if !slices.ContainsFunc(errs, func(e string) bool { return strings.Contains(e, tc.want) }) {
			t.Errorf("%s: errors %v, want one containing %q", tc.name, errs, tc.want)
		}
	}

	grown := map[string]schema{"X": {"type": "object", "patternProperties": map[string]any{}}}
	if errs := validate(grown, "X", []byte(`{}`)); len(errs) == 0 || !strings.Contains(errs[0], `"patternProperties"`) {
		t.Errorf("an unknown keyword was not refused: %v", errs)
	}
	inBranch := map[string]schema{"X": {"anyOf": []any{map[string]any{"type": "string", "pattern": "^a"}}}}
	if errs := validate(inBranch, "X", []byte(`"a"`)); len(errs) == 0 || !strings.Contains(errs[0], `"pattern"`) {
		t.Errorf("an unknown keyword inside anyOf was not refused: %v", errs)
	}
}

// unknownKeywords walks s statically — every property, items, additionalProperties and every
// anyOf branch, following $refs once each — and reports each keyword the walker does not handle.
// The fixture walk sees only the schema nodes a fixture's values reach, and stops at the first
// anyOf branch that matches; this covers the rest.
func unknownKeywords(schemas map[string]schema, path string, s schema, seen map[string]bool) []string {
	var out []string
	for k, v := range s {
		if !knownKeyword(k) {
			out = append(out, fmt.Sprintf("%s: the walker does not know the schema keyword %q", path, k))
			continue
		}
		switch k {
		case "$ref":
			name, _ := strings.CutPrefix(v.(string), "#/components/schemas/")
			if target, ok := schemas[name]; ok && !seen[name] {
				seen[name] = true
				out = append(out, unknownKeywords(schemas, name, target, seen)...)
			}
		case "properties":
			for field, sub := range v.(map[string]any) {
				out = append(out, unknownKeywords(schemas, path+"."+field, sub.(map[string]any), seen)...)
			}
		case "items", "additionalProperties":
			if sub, ok := v.(map[string]any); ok {
				out = append(out, unknownKeywords(schemas, path+"["+k+"]", sub, seen)...)
			}
		case "anyOf":
			for i, b := range v.([]any) {
				out = append(out, unknownKeywords(schemas, fmt.Sprintf("%s.anyOf[%d]", path, i), b.(map[string]any), seen)...)
			}
		}
	}
	return out
}

func reachableUnknownKeywords(schemas map[string]schema) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range fixtureSchemas {
		if !seen[name] {
			seen[name] = true
			out = append(out, unknownKeywords(schemas, name, schemas[name], seen)...)
		}
	}
	slices.Sort(out)
	return out
}

// T-15.8 / D-15.3: no schema the CLI reads — the response schemas and everything they reference —
// uses a keyword the walker does not handle, whether or not a fixture reaches it.
func TestContractSchemasUseOnlyKnownKeywords(t *testing.T) {
	schemas := loadContract(t)
	if problems := reachableUnknownKeywords(schemas); len(problems) > 0 {
		t.Errorf("the contract grew keywords the walker cannot check:\n  %s", strings.Join(problems, "\n  "))
	}
	// A keyword in a later anyOf branch of a property no fixture carries is still found.
	grown := map[string]schema{}
	for name, s := range schemas {
		grown[name] = s
	}
	grown["NamespaceList"] = schema{"type": "object", "properties": map[string]any{
		"unused": map[string]any{"anyOf": []any{
			map[string]any{"type": "null"},
			map[string]any{"type": "integer", "exclusiveMinimum": 0},
		}},
	}}
	if problems := reachableUnknownKeywords(grown); !slices.ContainsFunc(problems, func(p string) bool {
		return strings.Contains(p, "NamespaceList.unused.anyOf[1]") && strings.Contains(p, `"exclusiveMinimum"`)
	}) {
		t.Errorf("a keyword in an unreached anyOf branch was not found: %v", problems)
	}
}

// T-15.8 / D-15.16: the client has the reads and no ingest method. ADR-016 D-16.3 added the
// sixth read, Anomalies — still a GET over the browser's path, nothing that writes.
func TestContractClientHasNoIngestMethod(t *testing.T) {
	var methods []string
	ct := reflect.TypeFor[*Client]()
	for i := range ct.NumMethod() {
		methods = append(methods, ct.Method(i).Name)
	}
	want := []string{"Anomalies", "Diff", "Edge", "Graph", "Namespaces", "Node", "Server"}
	if !slices.Equal(methods, want) {
		t.Errorf("Client methods %v, want exactly %v", methods, want)
	}
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "/ingest") || strings.Contains(string(src), "http.MethodPost") {
		t.Error("client.go mentions an ingest path or POST")
	}
}
