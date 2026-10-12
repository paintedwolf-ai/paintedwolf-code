package structure

import (
	"os"
	"reflect"
	"sort"
	"testing"
)

func fakeTree(files map[string]string) fakeTreeType {
	return fakeTreeType{files: files}
}

type fakeTreeType struct {
	files map[string]string
}

func (f fakeTreeType) Files() ([]string, error) {
	out := make([]string, 0, len(f.files))
	for path := range f.files {
		out = append(out, path)
	}
	sort.Strings(out)
	return out, nil
}

func (f fakeTreeType) Read(relative string) ([]byte, error) {
	body, ok := f.files[relative]
	if !ok {
		return nil, os.ErrNotExist
	}
	return []byte(body), nil
}

func TestBuildGraph(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"lycaon/internal/session/manager.go":                     "package session\n\nimport (\n\t\"github.com/lycaon/lycaon/internal/db\"\n\t\"github.com/lycaon/lycaon/internal/workflow\"\n)\n",
		"lycaon/internal/session/store/store.go":                 "package store\n\nimport \"github.com/lycaon/lycaon/internal/db\"\n",
		"lycaon/internal/workflow/manager.go":                    "package workflow\n\nimport \"github.com/lycaon/lycaon/internal/db\"\n",
		"lycaon/internal/db/db.go":                               "package db\n",
		"lycaon/pkg/api/types.go":                                "package api\n\nimport \"github.com/lycaon/lycaon/internal/db\"\n",
		"lycaon/internal/db/db_test.go":                          "package db\n\nimport \"github.com/lycaon/lycaon/internal/session\"\n",
		"lycaon/internal/db/migrations/1.sql":                    "",
		"lycaon/config/runtime/scanners/rules/vendor/fixture.go": "not a Go package",
		"lycaon/internal/db/testdata/fixture.go":                 "not a Go package",
	}
	graph, err := buildGraph(fakeTree(files))
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	if !graph.Imports["internal/session"]["internal/db"] || !graph.Imports["internal/session"]["internal/workflow"] {
		t.Fatalf("session imports = %#v", graph.Imports["internal/session"])
	}
	if !graph.Imports["pkg/api"]["internal/db"] {
		t.Fatalf("expected api to import db")
	}
	if len(graph.Imports["internal/db"]) != 0 {
		t.Fatalf("test-only import leaked into graph: %#v", graph.Imports["internal/db"])
	}
	if len(graph.Consumers["internal/db"]) != 4 {
		t.Fatalf("db consumers = %#v", graph.Consumers["internal/db"])
	}
	if !graph.Packages["internal/db"] || graph.Packages["internal/db/migrations"] {
		t.Fatalf("package set = %#v", graph.Packages)
	}
}

func TestOutsideConsumers(t *testing.T) {
	t.Parallel()
	graph := &Graph{
		Imports: map[string]map[string]bool{
			"internal/session":       {"db": true},
			"internal/session/store": {"db": true},
		},
		Consumers: map[string]map[string]bool{
			"db":                     {"internal/session": true, "internal/session/store": true, "pkg/api": true},
			"internal/session/store": {"pkg/api": true},
		},
		Packages: map[string]bool{
			"internal/session":       true,
			"internal/session/store": true,
		},
	}
	got := graph.outsideConsumers("internal/session", "internal/session/store")
	want := []string{"pkg/api"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outsideConsumers = %#v", got)
	}
}

func TestMembersDirectChildrenOnly(t *testing.T) {
	t.Parallel()
	graph := &Graph{
		Packages: map[string]bool{
			"internal/a":          true,
			"internal/a/sub":      true,
			"internal/a/sub/deep": true,
			"internal/other":      true,
		},
	}
	got := graph.members("internal/a")
	want := []string{"internal/a", "internal/a/sub"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("members = %#v", got)
	}
}
