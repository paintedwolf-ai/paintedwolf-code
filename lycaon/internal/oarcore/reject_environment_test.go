package oarcore

// Table tests for the environment declaration: profile maps, host facts, and
// fact-value coercion.

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func profileMapDoc(mut func(map[string]any)) map[string]any {
	core := map[string]any{}
	for _, anchor := range coreAnchors {
		core[anchor] = "local_" + strings.ReplaceAll(anchor, ".", "_")
	}
	doc := map[string]any{
		"oar_profile_version": "1.0",
		"core":                core,
		"unsupported":         []any{},
	}
	if mut != nil {
		mut(doc)
	}
	return doc
}

func TestLoadProfileMapRejectsFaults(t *testing.T) {
	cases := []struct {
		name string
		mut  func(map[string]any)
		want string
	}{
		{"not an object", nil, ""},
		{"undefined field", func(d map[string]any) { d["bogus"] = 1 }, `carries undefined field "bogus"`},
		{"missing version", func(d map[string]any) { delete(d, "oar_profile_version") }, `field "oar_profile_version" must be <major>.<minor>`},
		{"leading zero version", func(d map[string]any) { d["oar_profile_version"] = "01.0" }, `field "oar_profile_version" must be <major>.<minor>`},
		{"wrong major", func(d map[string]any) { d["oar_profile_version"] = "2.0" }, "targets OAR 2.0, not 1.0"},
		{"core not object", func(d map[string]any) { d["core"] = 5 }, `field "core" must be an object`},
		{"core unknown anchor", func(d map[string]any) { d["core"] = map[string]any{"bogus.anchor": "local"} }, "is not a core anchor"},
		{"core local not string", func(d map[string]any) { d["core"] = map[string]any{"tool.pre_invoke": 5} }, "malformed local anchor"},
		{"core local malformed", func(d map[string]any) { d["core"] = map[string]any{"tool.pre_invoke": "Bad_Anchor"} }, "malformed local anchor"},
		{"unsupported not list", func(d map[string]any) { d["unsupported"] = "x" }, `field "unsupported" must be a list`},
		{"unsupported not anchor", func(d map[string]any) { d["unsupported"] = []any{5} }, "as unsupported, which is not a core anchor"},
		{"unsupported twice", func(d map[string]any) {
			d["unsupported"] = []any{"tool.pre_invoke", "tool.pre_invoke"}
			delete(d["core"].(map[string]any), "tool.pre_invoke")
		}, "as unsupported twice"},
		{"declared twice", func(d map[string]any) { d["unsupported"] = []any{"tool.pre_invoke"} }, "declares core anchor tool.pre_invoke twice"},
		{"omitted anchor", func(d map[string]any) {
			delete(d["core"].(map[string]any), "tool.pre_invoke")
			d["unsupported"] = []any{"tool.handler"}
			delete(d["core"].(map[string]any), "tool.handler")
		}, "omits core anchor tool.pre_invoke"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := any(profileMapDoc(tc.mut))
			if tc.name == "not an object" {
				doc = 42
			}
			_, err := LoadProfileMap(doc)
			if err == nil {
				t.Fatalf("LoadProfileMap accepted fault %q", tc.name)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("fault %q: got %q, want substring %q", tc.name, err.Error(), tc.want)
			}
		})
	}
	profile, err := LoadProfileMap(profileMapDoc(nil))
	if err != nil {
		t.Fatalf("valid profile map: %v", err)
	}
	if len(profile.Core) != len(coreAnchors) {
		t.Fatalf("core anchors: %d", len(profile.Core))
	}
}

func TestNewEnvironmentRejectsFaults(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*EnvironmentSpec)
		want string
	}{
		{"bad host", func(s *EnvironmentSpec) { s.Host = "BAD" }, "is not a namespace"},
		{"host anchor collides", func(s *EnvironmentSpec) { s.HostAnchors = []string{"tool.pre_invoke"} }, "collides with a core anchor"},
		{"host anchor malformed", func(s *EnvironmentSpec) { s.HostAnchors = []string{"Bad_Anchor"} }, "malformed or collides"},
		{"unknown profile", func(s *EnvironmentSpec) { s.Profiles = []string{"nosuch"} }, "the specification does not define"},
		{"fact outside host namespace", func(s *EnvironmentSpec) {
			s.HostFacts = map[string]string{"other.example.x": "string"}
		}, "is not published under the host namespace"},
		{"type outside vocabulary", func(s *EnvironmentSpec) {
			s.HostFacts = map[string]string{"test.example.bad": "list<bogus>"}
		}, "outside the closed type vocabulary"},
		{"malformed detector", func(s *EnvironmentSpec) { s.Detectors = []string{"nope"} }, "malformed detector reference"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := EnvironmentSpec{
				Host:      "test.example",
				HostFacts: map[string]string{"test.example.str": "string"},
			}
			tc.mut(&spec)
			_, err := NewEnvironment(spec)
			if err == nil {
				t.Fatalf("NewEnvironment accepted fault %q", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("fault %q: got %q, want substring %q", tc.name, err.Error(), tc.want)
			}
		})
	}
}

func TestNewEnvironmentDeclaresTiers(t *testing.T) {
	env, err := NewEnvironment(EnvironmentSpec{
		Host:        "test.example",
		HostAnchors: []string{"custom.anchor"},
		HostFacts: map[string]string{
			"test.example.str":      "string",
			"test.example.fact_len": "(string) -> int",
		},
		Detectors: []string{"detector://example"},
	})
	if err != nil {
		t.Fatalf("NewEnvironment: %v", err)
	}
	if decl := env.Facts["tool"]; decl.Type != TypeString || decl.Tier != TierCore {
		t.Fatalf("core fact: %+v", decl)
	}
	if decl := env.Facts["test.example.str"]; decl.Type != TypeString || decl.Tier != TierHost {
		t.Fatalf("host fact: %+v", decl)
	}
	if fn := env.Functions["test.example.fact_len"]; fn.Sig.Arg != TypeString || fn.Sig.Ret != TypeInt || fn.Tier != TierHost {
		t.Fatalf("host function: %+v", fn)
	}
	if !env.Detectors["detector://example"] || !env.HostAnchors["custom.anchor"] {
		t.Fatalf("detectors/anchors: %+v", env)
	}
	if !IsCoreAnchor("tool.pre_invoke") || IsCoreAnchor("custom.anchor") {
		t.Fatal("IsCoreAnchor")
	}
}

func TestCoerceRejectsWrongValues(t *testing.T) {
	cases := []struct {
		name string
		raw  any
		typ  FactType
		want string
	}{
		{"bool", 1, TypeBool, "is not a bool"},
		{"int string", "x", TypeInt, "is not an int"},
		{"int fractional", 1.5, TypeInt, "is not an int"},
		{"int NaN", math.NaN(), TypeInt, "is not an int"},
		{"int infinite", math.Inf(1), TypeInt, "is not an int"},
		{"int beyond range", 9.3e18, TypeInt, "is not an int"},
		{"int bad number", json.Number("abc"), TypeInt, "is not an int"},
		{"int other", struct{}{}, TypeInt, "is not an int"},
		{"double", "x", TypeDouble, "is not a double"},
		{"double infinite", math.Inf(1), TypeDouble, "is not a finite double"},
		{"double NaN", math.NaN(), TypeDouble, "is not a finite double"},
		{"string", 1, TypeString, "is not a string"},
		{"list member", []any{1}, TypeListString, "is not a list<string>"},
		{"list scalar", 1, TypeListString, "is not a list<string>"},
		{"listmap member", []any{1}, TypeListMap, "is not a list<map<string, dyn>>"},
		{"listmap scalar", 1, TypeListMap, "is not a list<map<string, dyn>>"},
		{"map", 1, TypeMap, "is not a map<string, dyn>"},
		{"undeclarable", 1, FactType("dyn"), "has an undeclarable type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := coerce(tc.raw, tc.typ, "f"); err == nil {
				t.Fatalf("coerce(%v, %s) accepted", tc.raw, tc.typ)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("coerce(%v, %s) = %q, want substring %q", tc.raw, tc.typ, err.Error(), tc.want)
			}
		})
	}
}

func TestCoerceAcceptsValues(t *testing.T) {
	cases := []struct {
		raw  any
		typ  FactType
		want any
	}{
		{true, TypeBool, true},
		{int64(3), TypeInt, int64(3)},
		{3, TypeInt, int64(3)},
		{3.0, TypeInt, int64(3)},
		{json.Number("3"), TypeInt, int64(3)},
		{3.5, TypeDouble, 3.5},
		{"s", TypeString, "s"},
		{[]string{"a"}, TypeListString, []string{"a"}},
		{[]any{"a", "b"}, TypeListString, []string{"a", "b"}},
		{[]map[string]any{{"k": "v"}}, TypeListMap, []map[string]any{{"k": "v"}}},
		{[]any{map[string]any{"k": "v"}}, TypeListMap, []map[string]any{{"k": "v"}}},
		{map[string]any{"k": "v"}, TypeMap, map[string]any{"k": "v"}},
	}
	for _, tc := range cases {
		got, err := coerce(tc.raw, tc.typ, "f")
		if err != nil {
			t.Fatalf("coerce(%v, %s) = %v", tc.raw, tc.typ, err)
		}
		switch want := tc.want.(type) {
		case []string:
			if len(got.([]string)) != len(want) {
				t.Fatalf("coerce(%v, %s) = %v", tc.raw, tc.typ, got)
			}
		case []map[string]any:
			if len(got.([]map[string]any)) != len(want) {
				t.Fatalf("coerce(%v, %s) = %v", tc.raw, tc.typ, got)
			}
		default:
			if reflect.TypeOf(got) != reflect.TypeOf(tc.want) || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("coerce(%v, %s) = %v, want %v", tc.raw, tc.typ, got, tc.want)
			}
		}
	}
}

func TestNormalizeMemberKinds(t *testing.T) {
	cases := []struct {
		in   any
		want any
	}{
		{3, int64(3)},
		{int32(3), int64(3)},
		{uint32(3), int64(3)},
		{float32(1.5), 1.5},
		{[]any{"a", "b"}, []string{"a", "b"}},
		{[]any{"a", 1}, []any{"a", 1}},
		{"s", "s"},
		{true, true},
	}
	for _, tc := range cases {
		got := normalizeMember(tc.in)
		if strings, ok := tc.want.([]string); ok {
			gotList, ok := got.([]string)
			if !ok || len(gotList) != len(strings) {
				t.Fatalf("normalizeMember(%#v) = %#v, want %#v", tc.in, got, tc.want)
			}
			continue
		}
		if items, ok := tc.want.([]any); ok {
			gotAny, ok := got.([]any)
			if !ok || len(gotAny) != len(items) {
				t.Fatalf("normalizeMember(%#v) = %#v, want %#v", tc.in, got, tc.want)
			}
			continue
		}
		if got != tc.want {
			t.Fatalf("normalizeMember(%#v) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}
