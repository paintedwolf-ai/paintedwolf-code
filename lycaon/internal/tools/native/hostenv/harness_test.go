//go:build darwin || linux

package hostenv

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostos"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

// fixtureHost is the injected host-facts port; it is the owner's seam for
// identity and system queries, not a replacement for any OS call.
type fixtureHost struct {
	system   hostos.System
	identity hostos.Identity
	err      error
}

func (h fixtureHost) System(context.Context) (hostos.System, error)     { return h.system, h.err }
func (h fixtureHost) Identity(context.Context) (hostos.Identity, error) { return h.identity, h.err }
func (h fixtureHost) Processors(_ context.Context, all bool) (int, error) {
	if all {
		return 8, h.err
	}
	return 6, h.err
}

func defaultHost() fixtureHost {
	return fixtureHost{
		system:   hostos.System{Sysname: "FixtureOS", Nodename: "fixture-host", Release: "1.2.3", Version: "#1 fixture", Machine: "fx64"},
		identity: hostos.Identity{User: "fixture-user", UID: 501, GID: 20, Groups: []int{20, 12}, GroupNames: map[int]string{20: "staff", 12: "everyone"}},
	}
}

// fixtureClock counts reads so tests can assert one capture per operation.
type fixtureClock struct {
	at    time.Time
	reads int
}

func (c *fixtureClock) now() time.Time { c.reads++; return c.at }

// nestedRecord is the evaluator's nested-command port as seen by env.
type nestedRecord struct {
	argv        []string
	environment map[string]string
	result      toolcontract.ProducerResult
}

func (n *nestedRecord) RunNested(_ context.Context, _ commandprogram.OperationRequest, argv commandprogram.Fields, environment map[string]string, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
	n.argv, n.environment = argv.Values(), environment
	return n.result, nil
}

type harness struct {
	t      *testing.T
	root   string
	path   string
	tc     tools.ToolContext
	owners *Owners
	clock  *fixtureClock
	nested commandprogram.NestedCommand
	env    []string
	cwd    string
}

type run struct {
	stdout, stderr string
	result         toolcontract.ProducerResult
	err            error
	argv           commandprogram.Fields
}

// newHarness installs a resolved PATH holding only the fixture programs,
// through the executor's PATH source seam, and restores it afterwards.
func newHarness(t *testing.T, host HostFacts) *harness {
	t.Helper()
	root, bin := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "mkdir pkg", os.MkdirAll(filepath.Join(root, "pkg"), 0o755))
	for _, name := range []string{"cat", "git", "pwtool"} {
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	}
	testutil.FailErr(t, "write plain file", os.WriteFile(filepath.Join(bin, "plain"), nil, 0o644))
	exec.SetResolvedPathSource(func() string { return bin })
	t.Cleanup(func() { exec.SetResolvedPathSource(nil) })
	clock := &fixtureClock{at: time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("fixture", 5*3600))}
	owners, err := NewOwners(host, clock.now, nativefixture.Boundary(t))
	testutil.FailErr(t, "owners", err)
	tc := tools.ToolContext{
		Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "ws", Path: root, IsPrimary: true}}, ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID},
	}
	return &harness{t: t, root: root, path: bin, tc: tc, owners: owners, clock: clock}
}

// run parses one real program, expands its words through the word expander
// under an invocation frame and dispatches the first command's builtin.
func (h *harness) run(source string) run {
	h.t.Helper()
	parsed, err := commandsurface.ParseProgram([]byte(source), commandsurface.ParseOptions{SourceName: "inline"})
	testutil.FailErr(h.t, "parse "+source, err)
	statement := parsed.Snapshot().Statements[0]
	command := statement.Command.(commandsurface.SimpleCommand)
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(h.t, "memory limits", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(h.t, "memory", err)
	frame, err := commandprogram.NewVariables(h.t.Context(), memory, "inline", h.env, nil)
	testutil.FailErr(h.t, "frame", err)
	defer frame.Close()
	expander, err := commandprogram.NewWordExpander(commandprogram.ExpanderPorts{Substitutions: noSubstitution{}, Files: commandsurface.OSGlobFiles{Dir: h.root}, Memory: memory})
	testutil.FailErr(h.t, "expander", err)
	var argv commandprogram.Fields
	for _, word := range command.Words {
		fields, err := expander.ExpandFields(h.t.Context(), frame, word)
		testutil.FailErr(h.t, "expand", err)
		argv = append(argv, fields...)
	}
	entries, release, err := frame.Environment(h.t.Context(), nil)
	testutil.FailErr(h.t, "environment", err)
	defer release()
	environment := entries
	request := commandprogram.OperationRequest{
		Invocation: toolcontract.OperationInvocation{
			Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: string(statement.ID), ExecutionID: "child"},
			ActorID: "actor", SessionID: "session", Scope: toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: "project", Directory: h.cwd},
			SourceUnitID: "inline", Span: statement.Span, CatalogDigest: toolcontract.LanguageCatalogDigest(),
		},
		OperationID: argv[0].Value, Statement: statement, Argv: argv, Environment: environment,
	}
	builtins := h.owners.Builtins(h.tc, h.nested, catalogBindings{})
	var stdout, stderr bytes.Buffer
	result, err := builtins[argv[0].Value].Invoke(h.t.Context(), request, commandprogram.Streams{Output: &stdout, Error: &stderr})
	if validation := result.Validate(); validation != nil {
		h.t.Fatalf("%s settled an invalid producer result %+v: %v", source, result, validation)
	}
	return run{stdout: stdout.String(), stderr: stderr.String(), result: result, err: err, argv: argv}
}

type noSubstitution struct{}

func (noSubstitution) Substitute(context.Context, commandprogram.ExpansionScope, commandprogram.SubstitutionRequest, io.Writer) (toolcontract.ProducerResult, error) {
	return toolcontract.ProducerResult{}, errors.New("fixtures run no substitutions")
}

// expectOutput asserts exact bytes and a completed status.
func (r run) expectOutput(t *testing.T, source, stdout, stderr string, status int) {
	t.Helper()
	if r.err != nil || r.stdout != stdout || r.stderr != stderr || r.result.Status != status || r.result.Terminal != toolcontract.ProducerCompleted || r.result.PublicationEligible != (status == 0) {
		t.Fatalf("%s: stdout=%q stderr=%q result=%+v err=%v; want stdout=%q stderr=%q status=%d", source, r.stdout, r.stderr, r.result, r.err, stdout, stderr, status)
	}
}

// expectRefusal asserts a typed refusal with no stdout.
func (r run) expectRefusal(t *testing.T, source, code string) *toolrejection.ToolReject {
	t.Helper()
	reject := toolrejection.AsToolReject(r.err)
	if reject == nil || reject.Code != code || r.result.Terminal != toolcontract.ProducerRefused || r.result.StopCode != code || r.stdout != "" {
		t.Fatalf("%s: result=%+v err=%v stdout=%q; want refusal %s", source, r.result, r.err, r.stdout, code)
	}
	return reject
}

// catalogBindings binds every catalog native, as a fully wired program does.
type catalogBindings struct{}

func (catalogBindings) NativeBound(string) bool   { return true }
func (catalogBindings) BaseEnvironment() []string { return nil }
