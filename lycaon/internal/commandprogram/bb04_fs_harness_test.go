package commandprogram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type bb04FSPredicates struct{ root string }

func (p bb04FSPredicates) TestPath(_ context.Context, _ toolcontract.OperationInvocation, op, operand string) (bool, error) {
	target := operand
	if !filepath.IsAbs(target) {
		target = filepath.Join(p.root, operand)
	}
	info, err := os.Stat(target)
	if err != nil {
		return false, nil
	}
	switch op {
	case "-e":
		return true, nil
	case "-f":
		return info.Mode().IsRegular(), nil
	case "-d":
		return info.IsDir(), nil
	default:
		return false, nil
	}
}

type bb04FSInput struct{ cwd string }

func (i bb04FSInput) OpenInput(_ context.Context, dest InputDestination) (io.ReadCloser, error) {
	path := dest.Target.Value
	if !filepath.IsAbs(path) {
		path = filepath.Join(i.cwd, path)
	}
	return os.Open(path)
}

type bb04FSPublication struct{ cwd string }

type bb04FSStaged struct {
	path   string
	append bool
	buf    bytes.Buffer
}

func (p bb04FSPublication) Open(_ context.Context, dest OutputDestination) (StagedOutput, error) {
	path := dest.Target.Value
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.cwd, path)
	}
	return &bb04FSStaged{
		path:   path,
		append: strings.HasSuffix(dest.Redirect.Operator, ">>"),
	}, nil
}

func (s *bb04FSStaged) Write(b []byte) (int, error) { return s.buf.Write(b) }

func (s *bb04FSStaged) Publish(context.Context) (OutputPublished, error) {
	flags := os.O_CREATE | os.O_WRONLY
	if s.append {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(s.path, flags, 0o644)
	if err != nil {
		return OutputPublished{}, err
	}
	_, werr := f.Write(s.buf.Bytes())
	cerr := f.Close()
	if werr != nil {
		return OutputPublished{}, werr
	}
	if cerr != nil {
		return OutputPublished{}, cerr
	}
	return OutputPublished{PublishedRevision: "fs"}, nil
}

func (s *bb04FSStaged) Abandon(context.Context) error { return nil }

func bb04SeedTree(t testing.TB, dir string) {
	t.Helper()
	files := map[string]string{
		"seed.txt":  "alpha one\nbeta two\ngamma three\n",
		"quote.txt": "don't split spaces\nsecond line\n",
		"empty.txt": "",
	}
	for name, body := range files {
		testutil.FailErr(t, "seed "+name, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
}

func bb04SnapshotTree(t testing.TB, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	testutil.FailErr(t, "walk "+dir, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = string(raw)
		return nil
	}))
	return out
}

type bb04RunResult struct {
	status int
	stdout string
	stderr string
	tree   map[string]string
	err    error
}

func bb04RunCommandProgram(t testing.TB, dir, program string) bb04RunResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	root := fixtureInvocation()
	root.SourceUnitID = "program"
	dispatch := &bb04FSDispatch{root: dir, cwd: dir}
	mem := fixtureScratch(t)
	ports := EvaluationPorts{
		Invocation:  root,
		Dispatch:    dispatch,
		Binder:      CatalogBinder{},
		Files:       memoryFiles{},
		Publication: bb04FSPublication{cwd: dir},
		Input:       bb04FSInput{cwd: dir},
		Predicates:  PredicateRuntime{Files: bb04FSPredicates{root: dir}},
		Supervision: &fakeJobs{},
		Memory:      mem,
		Streams:     Streams{Input: NullInput(), Output: &stdout, Error: &stderr},
	}
	ctx := t.Context()
	parsed, err := commandsurface.ParseAdmitted(ctx, mem, []byte(program), commandsurface.ParseOptions{SourceName: "program"})
	if err != nil {
		return bb04RunResult{err: err}
	}
	defer parsed.Close()
	lease, err := parsed.SnapshotWithMemory(ctx, mem)
	if err != nil {
		return bb04RunResult{err: err}
	}
	defer lease.Close()
	out, evalErr := Evaluate(ctx, lease.Plan, ports)
	return bb04RunResult{
		status: out.Status,
		stdout: stdout.String(),
		stderr: stderr.String(),
		tree:   bb04SnapshotTree(t, dir),
		err:    evalErr,
	}
}

func bb04RunBash(t testing.TB, bashPath, dir, program string, pipefail bool) bb04RunResult {
	t.Helper()
	script := program
	if pipefail {
		script = "set -o pipefail\n" + program
	}
	cmd := exec.CommandContext(t.Context(), bashPath, "--noprofile", "--norc", "-c", script)
	cmd.Dir = dir
	cmd.Env = []string{"LC_ALL=C", "LANG=C", "PATH=" + os.Getenv("PATH")}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	status := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			status = exitErr.ExitCode()
		} else {
			return bb04RunResult{err: err}
		}
	}
	return bb04RunResult{
		status: status,
		stdout: stdout.String(),
		stderr: stderr.String(),
		tree:   bb04SnapshotTree(t, dir),
	}
}

func bb04ResultsEqual(a, b bb04RunResult) (bool, string) {
	if a.err != nil || b.err != nil {
		return false, fmt.Sprintf("err eval=%v bash=%v", a.err, b.err)
	}
	if a.status != b.status {
		return false, fmt.Sprintf("status eval=%d bash=%d", a.status, b.status)
	}
	if a.stdout != b.stdout {
		return false, fmt.Sprintf("stdout eval=%q bash=%q", a.stdout, b.stdout)
	}
	if a.stderr != b.stderr {
		return false, fmt.Sprintf("stderr eval=%q bash=%q", a.stderr, b.stderr)
	}
	if len(a.tree) != len(b.tree) {
		return false, fmt.Sprintf("tree keys eval=%v bash=%v", bb04SortedKeys(a.tree), bb04SortedKeys(b.tree))
	}
	for k, va := range a.tree {
		if vb, ok := b.tree[k]; !ok || va != vb {
			return false, fmt.Sprintf("file %q eval=%q bash=%q (ok=%v)", k, va, vb, ok)
		}
	}
	return true, ""
}

func bb04SortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// bb04MinimizeStatements shrinks a failing statement list to a 1-minimal
// prefix/subset that still reproduces a differential mismatch.
