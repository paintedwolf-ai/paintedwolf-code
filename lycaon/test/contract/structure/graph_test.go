package structure

import (
	"reflect"
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
	sortPaths(out)
	return out, nil
}

func (f fakeTreeType) Read(relative string) ([]byte, error) {
	body, ok := f.files[relative]
	if !ok {
		return nil, os.ErrNotExist
	}
	return []byte(body), nil
}

func sortPaths(paths []string) {
	sort.Strings(paths)
}

func TestBuildGraph(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"lycaon/internal/session/manager.go":     "package session\n\nimport (\n\t\"github.com/lycaon/lycaon/internal/db\"\n\t\"github.com/lycaon/lycaon/internal/workflow\"\n)\n",
		"lycaon/internal/session/store/store.go": "package store\n\nimport \"github.com/lycaon/lycaon/internal/db\"\n",
		"lycaon/internal/workflow/manager.go":    "package workflow\n\nimport \"github.com/lycaon/lycaon/internal/db\"\n",
		"lycaon/internal/db/db.go":              "package db\n",
		"lycaon/pkg/api/types.go":              "package api\n\nimport \"github.com/lycaon/lycaon/internal/db\"\n",
		"lycaon/internal/db/db_test.go":         "package db\n\nimport \"github.com/lycaon/lycaon/internal/session\"\n",
		"lycaon/internal/db/migrations/1.sql":   "",
	}
	graph, err := buildGraph(fakeTree(files))
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	if !graph.Imports["internal/session"]["db"] || !graph.Imports["internal/session"]["workflow"] {
		t.Fatalf("session imports = %#v", graph.Imports["internal/session"])
	}
	if !graph.Imports["pkg/api"]["db"] {
		t.Fatalf("expected api to import db")
	}
	if len(graph.Imports["internal/db"]) != 0 {
		t.Fatalf("test-only import leaked into graph: %#v", graph.Imports["internal/db"])
	}
	if len(graph.Consumers["db"]) != 3 {
		t.Fatalf("db consumers = %#v", graph.Consumers["db"])
	}
}

func TestOutsideConsumers(t *testing.T) {
	t.Parallel()
	graph := &Graph{
		Imports: map[string]map[string]bool{
			"internal/session":           {"db": true},
			"internal/session/store":     {"db": true},
			"internal/hostprocess":       {"internal/session/store": true},
			"internal/tools/native/toolkit": {"internal/session/store": true},
		},
		Consumers: map[string]map[string]bool{
			"db":                        {"internal/session": true, "internal/session/store": true, "pkg/api": true},
			"internal/session/store":    {"internal/hostprocess": true, "internal/tools/native/toolkit": true},
		},
	}
	got := graph.outsideConsumers("internal/session", "internal/session/store")
	want := []string{"internal/hostprocess", "internal/tools/native/toolkit"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outsideConsumers = %#v", got)
	}
}

func TestMembersDirectChildrenOnly(t *testing.T) {
	t.Parallel()
	graph := &Graph{
		Imports: map[string]map[string]bool{
			"internal/a":           {"b": true},
			"internal/a/sub":      {"b": true},
			"internal/a/sub/deep": {},
		},
		Consumers: map[string]map[string]bool{},
	}
	got := graph.members("internal/a")
	want := []string{"internal/a", "internal/a/sub"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("members = %#v", got)
	}
}