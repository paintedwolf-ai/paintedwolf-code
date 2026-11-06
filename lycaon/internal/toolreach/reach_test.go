package toolreach

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/toolsurface"
)

func TestStatusFollowsTheFrozenRequest(t *testing.T) {
	plan := toolsurface.Compile([]string{"command", LoaderTool}, []string{"task"})
	withLoader := Freeze([]string{"command", LoaderTool}, plan)
	withoutLoader := Freeze([]string{"command"}, plan)
	cases := []struct {
		request Request
		tool    string
		want    Status
	}{
		{withLoader, "command", Callable},
		{withLoader, "task", Loadable},
		{withLoader, "absent", Absent},
		{withoutLoader, "task", Absent},
		{Request{}, "command", Absent},
	}
	for _, tc := range cases {
		if got := tc.request.Status(tc.tool); got != tc.want {
			t.Errorf("%s = %d, want %d", tc.tool, got, tc.want)
		}
	}
}

func TestSequenceLoadsADeferredTargetFirst(t *testing.T) {
	request := Freeze([]string{LoaderTool}, toolsurface.Compile([]string{LoaderTool}, []string{"task"}))
	call := Call{Tool: "task", Partial: true}
	got, ok := request.Sequence(call)
	if !ok || !reflect.DeepEqual(got, []Call{Load("task"), call}) {
		t.Fatalf("sequence = %#v, %v", got, ok)
	}
	if _, ok := request.Sequence(Call{Tool: "absent"}); ok {
		t.Fatal("an unreachable call produced a sequence")
	}
}

func TestCallTextShowsNestedArgumentsAndPartialCalls(t *testing.T) {
	call := Call{Tool: "command", Partial: true, Args: map[string]any{
		"capability_request": map[string]any{"read_path": "/supplied"},
		"background":         true,
	}}
	want := "`command(background=true, capability_request.read_path=\"/supplied\", …)`"
	if got := call.Text(); got != want {
		t.Fatalf("text = %s", got)
	}
}
