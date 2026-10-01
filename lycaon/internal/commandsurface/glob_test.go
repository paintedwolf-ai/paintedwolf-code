package commandsurface_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
)

func globTree(t *testing.T, files ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file))
		testutil.FailErr(t, "mkdir "+file, os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write "+file, os.WriteFile(path, nil, 0o644))
	}
	return root
}

// expandLine parses line and returns the expanded argv of its single stage.
func expandLine(t *testing.T, line string, scope commandsurface.GlobScope) ([]string, bool, error) {
	t.Helper()
	plan, err := commandsurface.ParsePlan(map[string]any{"command": line})
	testutil.FailErr(t, "parse "+line, err)
	stages, changed, err := commandsurface.ExpandGlobs(context.Background(), plan.Stages, scope)
	if err != nil {
		return nil, false, err
	}
	return stages[0].Args, changed, nil
}

func TestExpandGlobsFollowsShellRules(t *testing.T) {
	root := globTree(t,
		"a.go", "b.go", ".hidden.go", ".env", "-rf", "notes.txt",
		"sub/c.go", "sub/deep/d.go", "sub/.cache/e.go", ".git/f.go", "space dir/g.go",
	)
	cases := []struct {
		line string
		want []string
	}{
		{"ls *.go", []string{"a.go", "b.go"}},
		{"ls .*", []string{".env", ".git", ".hidden.go"}},
		{"ls **/*.go", []string{"a.go", "b.go", "space dir/g.go", "sub/c.go", "sub/deep/d.go"}},
		{"ls sub/**", []string{"sub/c.go", "sub/deep", "sub/deep/d.go"}},
		{"ls */", []string{"space dir/", "sub/"}},
		{"ls [!a].go", []string{"b.go"}},
		{"ls ?.go", []string{"a.go", "b.go"}},
		{"ls ./*.txt", []string{"./notes.txt"}},
		{"ls 'space dir'/*.go", []string{"space dir/g.go"}},
		{"find . -name '*.go'", []string{".", "-name", "*.go"}},
		{`ls \*.go`, []string{"*.go"}},
		{"ls *.none", []string{"*.none"}},
		{"rm -* x", []string{"-*", "x"}},
	}
	for _, c := range cases {
		got, _, err := expandLine(t, c.line, commandsurface.GlobScope{Dir: root})
		testutil.FailErr(t, "expand "+c.line, err)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s expanded to %q, want %q", c.line, got, c.want)
		}
	}
}

func TestExpandGlobsResolvesUnderTheStageCwdAndAbsolutePaths(t *testing.T) {
	root := globTree(t, "top.go", "sub/in.go")
	got, _, err := expandLine(t, "ls *.go", commandsurface.GlobScope{Dir: filepath.Join(root, "sub")})
	testutil.FailErr(t, "expand under cwd", err)
	if !reflect.DeepEqual(got, []string{"in.go"}) {
		t.Fatalf("cwd expansion = %q", got)
	}
	pattern := filepath.ToSlash(root) + "/sub/*.go"
	got, _, err = expandLine(t, "ls "+pattern, commandsurface.GlobScope{Dir: t.TempDir()})
	testutil.FailErr(t, "expand absolute", err)
	if !reflect.DeepEqual(got, []string{filepath.ToSlash(root) + "/sub/in.go"}) {
		t.Fatalf("absolute expansion = %q", got)
	}
}

func TestExpandGlobsExcludesPathsTheProcessCannotRead(t *testing.T) {
	root := globTree(t, "open/a.txt", "secret/b.txt", "secret.txt")
	denied := filepath.Join(root, "secret")
	readable := func(abs string) bool {
		return abs != denied && !strings.HasPrefix(abs, denied+string(filepath.Separator)) && abs != denied+".txt"
	}
	got, _, err := expandLine(t, "cat */*.txt *.txt **/b.txt", commandsurface.GlobScope{Dir: root, Readable: readable})
	testutil.FailErr(t, "expand with read floor", err)
	want := []string{"open/a.txt", "*.txt", "**/b.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expansion listed unreadable paths: %q, want %q", got, want)
	}
}

func TestExpandGlobsRefusesPastItsBudget(t *testing.T) {
	root := globTree(t, "a.go", "b.go", "c.go", "sub/d.go", "sub/e.go")
	_, _, err := expandLine(t, "ls *.go", commandsurface.GlobScope{Dir: root, MaxMatches: 2})
	var budget *commandsurface.GlobBudgetError
	if !errors.As(err, &budget) || budget.Pattern != "*.go" || budget.Limit != commandsurface.GlobLimitMatches {
		t.Fatalf("err = %v, want a matches budget error naming *.go", err)
	}
	_, _, err = expandLine(t, "ls **/*.go", commandsurface.GlobScope{Dir: root, MaxVisits: 3})
	if !errors.As(err, &budget) || budget.Pattern != "**/*.go" || budget.Limit != commandsurface.GlobLimitEntries {
		t.Fatalf("err = %v, want an entries budget error naming **/*.go", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plan, err := commandsurface.ParsePlan(map[string]any{"command": "ls **/*.go"})
	testutil.FailErr(t, "parse", err)
	if _, _, err := commandsurface.ExpandGlobs(ctx, plan.Stages, commandsurface.GlobScope{Dir: root}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled expansion err = %v", err)
	}
}

func TestExpandedPlanRendersToTheArgvThatRuns(t *testing.T) {
	root := globTree(t, "a b.go", "c[1].go", "d*.go")
	plan, err := commandsurface.ParsePlan(map[string]any{"command": "cat *.go > out.txt && echo done"})
	testutil.FailErr(t, "parse", err)
	stages, changed, err := commandsurface.ExpandGlobs(context.Background(), plan.Stages, commandsurface.GlobScope{Dir: root})
	testutil.FailErr(t, "expand", err)
	if !changed {
		t.Fatal("expansion reported no change")
	}
	rendered := commandsurface.RenderStages(stages)
	again, err := commandsurface.ParsePlan(map[string]any{"command": rendered})
	testutil.FailErr(t, "reparse "+rendered, err)
	if !reflect.DeepEqual(again.Stages[0].Args, []string{"a b.go", "c[1].go", "d*.go"}) || len(again.Stages[0].Globs) != 0 {
		t.Fatalf("rendered %q reparsed to %q globs %q", rendered, again.Stages[0].Args, again.Stages[0].Globs)
	}
	if again.Stages[0].Streams != plan.Stages[0].Streams || again.Stages[1].Connector != plan.Stages[1].Connector {
		t.Fatalf("rendered %q lost its redirection or connector", rendered)
	}
}
