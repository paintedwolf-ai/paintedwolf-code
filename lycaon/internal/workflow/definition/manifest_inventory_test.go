package definition

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// independentManifestPaths recurses the manifest root type a second time,
// written on its own rather than calling the walker's helpers. If the two
// disagree, a manifest field is either missing from the docs inventory or
// invented by it.
func independentManifestPaths(t reflect.Type, prefix string, seen map[reflect.Type]bool, out map[string]string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || t == reflect.TypeOf(yaml.Node{}) {
		return
	}
	if seen[t] {
		return
	}
	seen[t] = true
	defer delete(seen, t)

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		raw, ok := f.Tag.Lookup("yaml")
		if !ok {
			continue
		}
		name := strings.Split(raw, ",")[0]
		if name == "" || name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		out[path] = f.Name

		ft := f.Type
		for {
			switch ft.Kind() {
			case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
				ft = ft.Elem()
				continue
			default:
			}
			break
		}
		if ft.Kind() == reflect.Struct {
			independentManifestPaths(ft, path, seen, out)
		}
	}
}

func TestManifestFieldInventoryCoversEveryYAMLField(t *testing.T) {
	want := map[string]string{}
	independentManifestPaths(reflect.TypeOf(workflowFile{}), "", map[reflect.Type]bool{}, want)

	got := map[string]bool{}
	for _, f := range ManifestFieldInventory() {
		if got[f.Path] {
			t.Fatalf("inventory lists %q twice", f.Path)
		}
		got[f.Path] = true
	}
	for path := range want {
		if !got[path] {
			t.Fatalf("manifest field %q is parsed by the loader but missing from ManifestFieldInventory()", path)
		}
	}
	for path := range got {
		if _, ok := want[path]; !ok {
			t.Fatalf("ManifestFieldInventory() lists %q, which the loader does not parse", path)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("inventory has %d paths, independent walk found %d", len(got), len(want))
	}
}

func TestManifestFieldInventoryShape(t *testing.T) {
	rows := ManifestFieldInventory()
	if len(rows) == 0 {
		t.Fatal("empty inventory")
	}
	if rows[0].Path != "id" || rows[0].Section != manifestRootSection {
		t.Fatalf("first row = %+v, want the root id field", rows[0])
	}
	byPath := map[string]ManifestField{}
	for _, r := range rows {
		if r.Section == "" || r.Key == "" || r.Path == "" || r.Type == "" {
			t.Fatalf("incomplete inventory row: %+v", r)
		}
		byPath[r.Path] = r
	}
	for path, wantType := range map[string]string{
		"phases.human_approval.readiness":  "string",
		"phases.review_loop.iteration_cap": "int",
		"phases.transitions.when":          "string",
		"parameters.type":                  "string",
		"controls.on_pause.hold_pending":   "bool",
		"phases":                           "[]block",
	} {
		row, ok := byPath[path]
		if !ok {
			t.Fatalf("inventory is missing %q", path)
		}
		if row.Type != wantType {
			t.Fatalf("%s type = %q want %q", path, row.Type, wantType)
		}
	}
	// Two blocks of the same Go type in different places stay distinct
	// sections, so the docs table never merges them.
	if byPath["controls.content_review.tools"].Section == byPath["phases.controls.content_review.tools"].Section {
		t.Fatal("workflow-level and phase-level content_review collapsed into one section")
	}
}

// TestManifestFieldInventoryDeclarationOrder pins that documentation order is
// the order the manifest struct declares, not an alphabetical rearrangement.
func TestManifestFieldInventoryDeclarationOrder(t *testing.T) {
	var order []string
	for _, r := range ManifestFieldInventory() {
		if r.Section == manifestRootSection {
			order = append(order, r.Key)
		}
	}
	want := []string{"id", "version", "attach", "request", "extends", "name"}
	if len(order) < len(want) {
		t.Fatalf("root fields = %v", order)
	}
	for i, key := range want {
		if order[i] != key {
			t.Fatalf("root field[%d] = %q want %q", i, order[i], key)
		}
	}
}
