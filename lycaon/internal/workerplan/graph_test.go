package workerplan_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerplan"
	"github.com/lycaon/lycaon/pkg/api"
)

func tasksDoc(tasksJSON string) *api.Blueprint {
	return &api.Blueprint{Content: "<!-- lycaon:tasks " + tasksJSON + " -->"}
}

func TestBlueprintWithoutTasksPlansOneWriteLeg(t *testing.T) {
	for name, doc := range map[string]*api.Blueprint{
		"no task block":    {Title: "Add caching", Content: "# Plan\nNo task block."},
		"empty task block": tasksDoc("  "),
	} {
		legs, err := workerplan.ParseBlueprintLegs(doc)
		testutil.FailErr(t, name, err)
		if len(legs) != 1 || legs[0].WorkID != "plan-1" || !slices.Equal(legs[0].Files, []string{"**/*"}) {
			t.Fatalf("%s: legs = %+v, want one leg over the whole tree", name, legs)
		}
	}
}

func TestMalformedTaskGraphIsRefusedByReason(t *testing.T) {
	for _, tc := range []struct {
		doc    *api.Blueprint
		reason string
		task   string
		other  string
	}{
		{doc: &api.Blueprint{Content: `<!-- lycaon:tasks [{"id":"a"}]`}, reason: "tasks_block_unclosed"},
		{doc: tasksDoc(`{bad`), reason: "tasks_json_invalid"},
		{doc: tasksDoc(`[{"id":" bad ","files":["a.go"]}]`), reason: "task_id_invalid", task: " bad "},
		{doc: tasksDoc(`[{"id":"a","files":["a.go"]},{"id":"a","files":["b.go"]}]`), reason: "task_id_duplicate", task: "a"},
		{doc: tasksDoc(`[{"id":"a","files":["a.go"],"depends_on":[" b"]},{"id":"b","files":["b.go"]}]`), reason: "dependency_invalid", task: "a", other: " b"},
		{doc: tasksDoc(`[{"id":"a","files":["a.go"],"depends_on":["a"]}]`), reason: "dependency_self", task: "a"},
		{doc: tasksDoc(`[{"id":"a","files":["a.go"],"depends_on":["b","b"]},{"id":"b","files":["b.go"]}]`), reason: "dependency_repeated", task: "a", other: "b"},
		{doc: tasksDoc(`[{"id":"a","files":["a.go"],"depends_on":["missing"]}]`), reason: "dependency_unknown", task: "a", other: "missing"},
		{doc: tasksDoc(`[{"id":"a","files":["a.go"],"depends_on":["b"]},{"id":"b","files":["b.go"],"depends_on":["a"]}]`), reason: "dependency_cycle", task: "a"},
		{doc: tasksDoc(`[{"id":"a","files":["pkg/x.go"]},{"id":"b","files":["pkg/*"]}]`), reason: "write_paths_overlap", task: "a", other: "b"},
	} {
		_, err := workerplan.ParseBlueprintLegs(tc.doc)
		var graph *workerplan.GraphError
		if !errors.As(err, &graph) {
			t.Fatalf("%s: error = %v, want a GraphError", tc.reason, err)
		}
		if got := (workerplan.GraphError{Reason: tc.reason, Task: tc.task, Other: tc.other}); *graph != got {
			t.Fatalf("graph error = %+v, want %+v", *graph, got)
		}
	}
}

func TestOrderedTasksMayWriteTheSamePaths(t *testing.T) {
	legs, err := workerplan.ParseBlueprintLegs(tasksDoc(`[
		{"id":"leg-a","title":"Write core","files":["internal/core/a.go"],"verify":["go test ./internal/core/..."]},
		{"id":"leg-b","title":"Extend core","files":["internal/core/*"],"depends_on":["leg-a"]}
	]`))
	testutil.FailErr(t, "ordered overlap", err)
	if len(legs) != 2 || !slices.Equal(legs[1].DependsOn, []string{"leg-a"}) {
		t.Fatalf("legs = %+v", legs)
	}
	want := []string{"job:complete", "summary:present", "file:modified:internal/core/a.go", "test:pass:go test ./internal/core/..."}
	if !slices.Equal(legs[0].Criteria, want) {
		t.Fatalf("criteria = %v, want %v", legs[0].Criteria, want)
	}
}

func TestPathsOverlap(t *testing.T) {
	for _, tc := range []struct {
		a, b []string
		want bool
	}{
		{[]string{"internal/a/**"}, []string{"internal/a/b/c.go"}, true},
		{[]string{"internal/a"}, []string{"internal/a/b.go"}, true},
		{[]string{"pkg/*.go"}, []string{"pkg/x.go"}, true},
		{[]string{"./pkg/x.go"}, []string{"pkg/x.go"}, true},
		{[]string{"internal/a/x.go"}, []string{"internal/b/y.go"}, false},
		{[]string{"internal/ab/x.go"}, []string{"internal/a"}, false},
		{nil, []string{"pkg/x.go"}, true},
		{[]string{"**/*"}, []string{"pkg/x.go"}, true},
	} {
		if got := workerplan.PathsOverlap(tc.a, tc.b); got != tc.want {
			t.Fatalf("PathsOverlap(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
		if got := workerplan.PathsOverlap(tc.b, tc.a); got != tc.want {
			t.Fatalf("PathsOverlap(%v, %v) = %v, want %v", tc.b, tc.a, got, tc.want)
		}
	}
}
