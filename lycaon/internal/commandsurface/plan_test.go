package commandsurface_test

import (
	"errors"
	"maps"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestParsePlanNeverMutatesArguments(t *testing.T) {
	args := map[string]any{"command": "go test ./... > out.log 2> err.log && sort < in.txt", "cwd": "pkg"}
	before := maps.Clone(args)
	for range 2 {
		plan, err := commandsurface.ParsePlan(args)
		testutil.FailErr(t, "parse", err)
		if !reflect.DeepEqual(plan.WritePaths(), []string{"pkg/out.log", "pkg/err.log"}) {
			t.Fatalf("write paths = %q", plan.WritePaths())
		}
		if !reflect.DeepEqual(plan.ReadPaths(), []string{"pkg/in.txt"}) {
			t.Fatalf("read paths = %q", plan.ReadPaths())
		}
	}
	if !reflect.DeepEqual(args, before) {
		t.Fatalf("parsing changed the arguments: %#v", args)
	}
}

func TestParsePlanRefusesAStreamNamedTwice(t *testing.T) {
	cases := []struct {
		args  map[string]any
		issue argv.RedirectionIssue
	}{
		{map[string]any{"command": "go test > out.log", "stdout_to": "other.log"}, argv.IssueStdoutConflict},
		{map[string]any{"command": "go test &> out.log", "stdout_to": "other.log"}, argv.IssueStdoutConflict},
		{map[string]any{"command": "go test 2> err.log", "stderr_to": "other.log"}, argv.IssueStderrConflict},
		{map[string]any{"command": "sort < in.txt", "stdin": "data"}, argv.IssueStdinConflict},
		{map[string]any{"command": "sort < in.txt", "stdin_from": "other.txt"}, argv.IssueStdinConflict},
	}
	for _, c := range cases {
		_, err := commandsurface.ParsePlan(c.args)
		var redirection *argv.RedirectionError
		if !errors.As(err, &redirection) || redirection.Issue != c.issue {
			t.Fatalf("%v: err = %v, want %s", c.args, err, c.issue)
		}
	}
	for _, args := range []map[string]any{
		{"command": "go test 2> err.log", "stdout_to": "out.log"},
		{"command": "go test > /dev/null", "stdout_to": "out.log"},
		{"command": "go test > out.log", "stderr_to": "err.log"},
	} {
		_, err := commandsurface.ParsePlan(args)
		testutil.FailErr(t, "parse independent streams", err)
	}
	if _, err := commandsurface.ParsePlan(map[string]any{"command": "cat", "stdin": "x", "stdin_from": "y"}); !errors.Is(err, commandsurface.ErrStdinSources) {
		t.Fatalf("stdin sources err = %v", err)
	}
	if _, err := commandsurface.ParsePlan(map[string]any{"command": "cat", "append": true}); !errors.Is(err, commandsurface.ErrAppendWithoutTarget) {
		t.Fatalf("append without target err = %v", err)
	}
}
