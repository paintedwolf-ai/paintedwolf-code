package laws_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/contract/laws"
	"github.com/lycaon/lycaon/test/contract/laws/forbiddencalls"
)

var goleakTree = map[string]string{
	"lycaon/internal/testutil/leakcheck.go": "package testutil\n\nfunc VerifyNoLeaks() {}\n",
	"lycaon/internal/app/app_test.go":       "package app\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n",
}

// The family fails on each kind of drift, shown on real rows.
func TestForbiddenCallsFailClosed(t *testing.T) {
	t.Parallel()
	goleak := rule(t, forbiddencalls.Family, "goleak-through-testutil")
	adapters := rule(t, forbiddencalls.Family, "agent-adapters-reach-files-through-the-door")
	adapterTree := map[string]string{
		"lycaon/internal/fseffect/door.go":            "package fseffect\n",
		"lycaon/internal/tools/projectpaths/paths.go": "package projectpaths\n",
		"lycaon/internal/tools/native/tool.go":        "package native\n",
		"lycaon/internal/httpaction/action.go":        "package httpaction\n",
		"lycaon/internal/commandprogram/program.go":   "package commandprogram\n",
	}
	for name, tc := range map[string]struct {
		rule  laws.Rule
		files map[string]string
		want  outcome
	}{
		"clean": {rule: goleak, files: goleakTree, want: passes},
		"seeded violation": {rule: goleak, files: with(goleakTree, map[string]string{
			"lycaon/internal/app/app_test.go": "package app\n\nimport leak \"go.uber.org/goleak\"\n\nfunc helper() { leak.VerifyNone(nil) }\n",
		}), want: violates},
		"renamed anchor": {rule: goleak, files: with(goleakTree, map[string]string{
			"lycaon/internal/testutil/leakcheck.go": "package testutil\n\nfunc VerifyLeaks() {}\n",
		}), want: missesAnchor},
		"stale exemption": {rule: adapters, files: adapterTree, want: staleExemption},
		"empty corpus":    {rule: goleak, files: nil, want: fails},
		"unparsable file": {rule: goleak, files: with(goleakTree, map[string]string{
			"lycaon/internal/app/broken.go": "package app\n\nfunc (\n",
		}), want: fails},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			expect(t, laws.Evaluate(fixture(t, tc.files), tc.rule), tc.want, 0)
		})
	}
}

var execStub = "package exec\n\ntype ExecOpts struct {\n\tDir    string\n\tLaunch any\n}\n\n" +
	"type Prepared struct{}\n\nfunc (Prepared) Start() error { return nil }\nfunc (Prepared) Run() error { return nil }\n" +
	"func (Prepared) Output() ([]byte, error) { return nil, nil }\nfunc (Prepared) CombinedOutput() ([]byte, error) { return nil, nil }\n" +
	"func (Prepared) Wait() error { return nil }\n\n" +
	"func PrepareCommand(any, string, []string, ExecOpts) (Prepared, func(), error) { return Prepared{}, nil, nil }\n" +
	"func StartPrepared(any, Prepared) {}\n"

// A prepared command starts through the lifecycle; its native Start, Run,
// Output and CombinedOutput are bypasses, Wait and StartPrepared are not.
func TestProcessLaunchRejectsPreparedStartBypasses(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"lycaon/internal/exec/exec.go": execStub,
		"lycaon/internal/fixture/launch.go": "package fixture\n\nimport process \"github.com/lycaon/lycaon/internal/exec\"\n\n" +
			"func example() {\n\tcmd, cleanup, err := process.PrepareCommand(nil, \"fixture\", nil, process.ExecOpts{Launch: 1})\n" +
			"\t_, _ = cleanup, err\n\tcmd.Start(); cmd.Run(); cmd.Output(); cmd.CombinedOutput(); cmd.Wait()\n" +
			"\tprocess.StartPrepared(nil, cmd)\n}\n",
	}
	expect(t, laws.Evaluate(fixture(t, files), rule(t, forbiddencalls.Family, "process-launches-use-the-typed-door")), violates, 4)
}

// A zero options value returned beside a non-nil error is not a launch
// request; a successful zero return and a populated value still need Launch.
func TestProcessLaunchOptionsErrorReturnIsNotExempt(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"lycaon/internal/exec/exec.go": execStub,
		"lycaon/internal/exec/opts.go": "package exec\n\nvar launch any\n\nfunc validate() error { return nil }\n\n" +
			"func invalid() (ExecOpts, error) {\n\tif err := validate(); err != nil {\n\t\treturn ExecOpts{}, err\n\t}\n\treturn ExecOpts{Launch: launch}, nil\n}\n\n" +
			"func success() (ExecOpts, error) { return ExecOpts{}, nil }\n\n" +
			"func unattributed() (ExecOpts, error) { return ExecOpts{Dir: \".\"}, validate() }\n",
	}
	expect(t, laws.Evaluate(fixture(t, files), rule(t, forbiddencalls.Family, "process-launches-use-the-typed-door")), violates, 2)
}

// An engine call made while depsMu is held fails; the snapshot-then-unlock
// shape passes.
func TestRuntimeLockFiresOnlyWhileHeld(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"lycaon/internal/coordinator/runtime.go": "package coordinator\n\nimport \"sync\"\n\ntype stub struct{}\n\nfunc (*stub) Call() {}\n\n" +
			"type Runtime struct {\n\tdepsMu    sync.Mutex\n\tassembler *stub\n\tkicks     *stub\n\tboard     *stub\n\tloop      *stub\n}\n\n" +
			"func (r *Runtime) Bad() {\n\tr.depsMu.Lock()\n\tdefer r.depsMu.Unlock()\n\tr.assembler.Call()\n}\n\n" +
			"func (r *Runtime) Good() {\n\tr.depsMu.Lock()\n\ta := r.assembler\n\tr.depsMu.Unlock()\n\ta.Call()\n}\n",
	}
	res := laws.Evaluate(fixture(t, files), rule(t, forbiddencalls.Family, "runtime-methods-release-deps-mu-before-engine-calls"))
	expect(t, res, violates, 1)
	if !strings.Contains(res.Violations[0], "Bad") {
		t.Fatalf("flagged the wrong method: %v", res.Violations)
	}
}

// A direct read of the guarded field fails; reads through the accessor pass.
func TestLoopAdmissionDepsReadThroughAccessor(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"lycaon/internal/coordinator/loopwake/admission.go": "package loopwake\n\nimport \"sync\"\n\n" +
			"type Admission struct {\n\tdepsMu sync.RWMutex\n\tdeps   int\n}\n\n" +
			"func (l *Admission) setDeps(d int) { l.depsMu.Lock(); l.deps = d; l.depsMu.Unlock() }\n" +
			"func (l *Admission) loopDeps() int { l.depsMu.RLock(); defer l.depsMu.RUnlock(); return l.deps }\n" +
			"func (l *Admission) Good() int     { return l.loopDeps() }\n" +
			"func (l *Admission) Bad() int      { return l.deps }\n",
	}
	expect(t, laws.Evaluate(fixture(t, files), rule(t, forbiddencalls.Family, "loop-admission-deps-read-through-accessor")), violates, 1)
}

// Comparing an epoch with nil asks whether one exists; comparing two
// epochs trusts freshness and needs watch coverage.
func TestEpochComparisonDistinguishesPresenceFromFreshness(t *testing.T) {
	t.Parallel()
	for expression, freshness := range map[string]bool{
		"epoch != nil":                         false,
		"nil == state.epoch":                   false,
		"(epoch) != (nil)":                     false,
		"epoch == observed":                    true,
		"state.epoch != captured.epoch":        true,
		"repochange.EpochCurrent(root, epoch)": true,
	} {
		t.Run(expression, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{
				"lycaon/internal/repochange/notify.go": "package repochange\n\nfunc EpochCurrent(root string, epoch any) bool { return false }\n",
				"lycaon/internal/probe/probe.go":       "package probe\n\nfunc probe() {\n\tif " + expression + " {\n\t}\n}\n",
			}
			want := passes
			if freshness {
				want = violates
			}
			expect(t, laws.Evaluate(fixture(t, files), bare(rule(t, forbiddencalls.Family, "epoch-fast-paths-consult-watch-coverage"))), want, 1)
		})
	}
}

// pongoStub stands in for the template module: the render methods of
// *Template are what the ceiling rule derives and watches.
var pongoStub = map[string]string{
	"lycaon/pongostub/go.mod": "module github.com/flosch/pongo2/v6\n\ngo 1.26\n",
	"lycaon/pongostub/pongo.go": "package pongo2\n\ntype Template struct{}\ntype TemplateSet struct{}\n\n" +
		"func (*Template) Execute(map[string]any) (string, error) { return \"\", nil }\n" +
		"func (*Template) ExecuteBlocks(map[string]any, []string) (map[string]string, error) { return nil, nil }\n" +
		"func (*TemplateSet) BanTag(string) error { return nil }\n" +
		"func (*TemplateSet) FromCache(string) (*Template, error) { return nil, nil }\n" +
		"func NewSet(string, any) *TemplateSet { return nil }\n" +
		"func FromString(string) (*Template, error) { return nil, nil }\n" +
		"func SetAutoescape(bool) {}\n" +
		"func RenderTemplateString(string, map[string]any) (string, error) { return \"\", nil }\n" +
		"func ReplaceTag(string, any) error { return nil }\n" +
		"func MustNewLocalFileSystemLoader(string) any { return nil }\n",
	"lycaon/go.mod": fixtureModule + "\nrequire github.com/flosch/pongo2/v6 v6.0.0\n\nreplace github.com/flosch/pongo2/v6 => ./pongostub\n",
	"lycaon/internal/pongoplain/plain.go": "package pongoplain\n\nimport \"github.com/flosch/pongo2/v6\"\n\n" +
		"func Compile(string) (*pongo2.Template, error) { return nil, nil }\n" +
		"func Execute(any, *pongo2.Template, map[string]any) (string, error) { return \"\", nil }\n",
}

// Each route around the template ceiling is caught once; the ceiling's own
// package, pass-through bodies, bounded execution and set methods are clean.
func TestPongoCeilingCatchesEachRoute(t *testing.T) {
	t.Parallel()
	const pongoImport = "import pongo2 \"github.com/flosch/pongo2/v6\"\n"
	const plainImport = "import (\n\t\"fmt\"\n\n\t\"github.com/lycaon/lycaon/internal/pongoplain\"\n)\n"
	const mixedImport = "import (\n\t\"github.com/flosch/pongo2/v6\"\n\n\t\"github.com/lycaon/lycaon/internal/pongoplain\"\n)\n"
	for name, tc := range map[string]struct {
		path, src string
		want      outcome
	}{
		"set built outside pongoplain": {src: pongoImport + "func f(l any) { set := pongo2.NewSet(\"x\", l); _ = set }\n", want: violates},
		"parsed on the default set":    {src: pongoImport + "func f(b string) { _, _ = pongo2.FromString(b) }\n", want: violates},
		"autoescape reset elsewhere":   {src: pongoImport + "func f() { pongo2.SetAutoescape(true) }\n", want: violates},
		"default-set render shortcut":  {src: pongoImport + "func f(b string) { _, _ = pongo2.RenderTemplateString(b, nil) }\n", want: violates},
		"template executed directly":   {src: pongoImport + "func f(t *pongo2.Template) { _, _ = t.Execute(nil) }\n", want: violates},
		"blocks executed directly":     {src: pongoImport + "func f(t *pongo2.Template) { _, _ = t.ExecuteBlocks(nil, nil) }\n", want: violates},
		"tag replaced":                 {src: pongoImport + "func f() { _ = pongo2.ReplaceTag(\"include\", nil) }\n", want: violates},
		"loader built outside":         {src: pongoImport + "func f() { _ = pongo2.MustNewLocalFileSystemLoader(\"/tmp\") }\n", want: violates},
		"ceiling re-opened":            {src: pongoImport + "func f(set *pongo2.TemplateSet) { set.BanTag(\"include\") }\n", want: violates},
		"aliased import":               {src: "import p2 \"github.com/flosch/pongo2/v6\"\n" + "func f(l any) { set := p2.NewSet(\"x\", l); _ = set }\n", want: violates},
		"dot import":                   {src: "import . \"github.com/flosch/pongo2/v6\"\n" + "func f(l any) { set := NewSet(\"x\", l); _ = set }\n", want: violates},
		"source concatenated":          {src: plainImport + "func f(when string) { _, _ = pongoplain.Compile(\"{% if \" + when + \" %}1{% endif %}\"); _ = fmt.Sprint() }\n", want: violates},
		"source formatted":             {src: plainImport + "func f(when string) { _, _ = pongoplain.Compile(fmt.Sprintf(\"{%% if %s %%}1{%% endif %%}\", when)) }\n", want: violates},
		"the ceiling builds sets": {
			path: "lycaon/internal/pongoplain/sets.go",
			src:  pongoImport + "func f(l any) { set := pongo2.NewSet(\"x\", l); _ = set }\n", want: passes,
		},
		"authored body passed through": {src: plainImport + "func f(body string) { _, _ = pongoplain.Compile(body); _ = fmt.Sprint() }\n", want: passes},
		"bounded execution":            {src: mixedImport + "func f(t *pongo2.Template) { _, _ = pongoplain.Execute(nil, t, nil) }\n", want: passes},
		"set method":                   {src: pongoImport + "func f(set *pongo2.TemplateSet, ref string) { _, _ = set.FromCache(ref) }\n", want: passes},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := tc.path
			pkg := "pongoplain"
			if path == "" {
				path, pkg = "lycaon/internal/p/p.go", "p"
			}
			files := with(pongoStub, map[string]string{path: "package " + pkg + "\n\n" + tc.src})
			expect(t, laws.Evaluate(fixture(t, files), rule(t, forbiddencalls.Family, "pongo-templates-carry-the-host-ceiling")), tc.want, 1)
		})
	}
}

// Only the broker relay's exact Send arguments and the supervisor's exact
// stream assignment hand os.Stdout to a child.
func TestChildStdoutForwardingIsExact(t *testing.T) {
	t.Parallel()
	const relay = `func runBrokerRelay() { launchbroker.Send(broker, request, os.Stdin, os.Stdout, os.Stderr, files...) }`
	const supervisor = `func (t supervisorTarget) command() { cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr }`
	for _, tc := range []struct {
		name, path, source string
		allowed, blocked   int
	}{
		{"relay", "internal/exec/relay_linux.go", relay, 1, 0},
		{"supervisor", "internal/exec/supervisor_linux.go", supervisor, 1, 0},
		{"other path", "internal/exec/other.go", relay, 0, 1},
		{"other function", "internal/exec/relay_linux.go", `func serve() { launchbroker.Send(broker, request, os.Stdin, os.Stdout, os.Stderr, files...) }`, 0, 1},
		{"other receiver", "internal/exec/supervisor_linux.go", `func (t other) command() { cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr }`, 0, 1},
		{"wrong broker call", "internal/exec/relay_linux.go", `func runBrokerRelay() { other.Send(broker, request, os.Stdin, os.Stdout, os.Stderr, files...) }`, 0, 1},
		{"wrong stream slot", "internal/exec/relay_linux.go", `func runBrokerRelay() { launchbroker.Send(broker, request, os.Stdout, os.Stdin, os.Stderr, files...) }`, 0, 1},
		{"stdout write", "internal/exec/relay_linux.go", relay + `; func serve() { os.Stdout.Write(data) }`, 1, 1},
		{"write inside relay", "internal/exec/relay_linux.go", `func runBrokerRelay() { launchbroker.Send(broker, request, os.Stdin, os.Stdout, os.Stderr, files...); os.Stdout.Write(data) }`, 1, 1},
		{"supervisor logging", "internal/exec/supervisor_linux.go", supervisor + `; func serve() { fmt.Fprintln(os.Stdout, "bad") }`, 1, 1},
		{"wrong assignment", "internal/exec/supervisor_linux.go", `func (t supervisorTarget) command() { other.Stdin, other.Stdout, other.Stderr = os.Stdin, os.Stdout, os.Stderr }`, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package fixture\n"+tc.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			allowed := forbiddencalls.ChildStdoutForwarding(tc.path, file)
			blocked := 0
			ast.Inspect(file, func(node ast.Node) bool {
				if sel, ok := node.(*ast.SelectorExpr); ok && forbiddencalls.StdoutSelector(sel, "os", "Stdout") && !allowed[sel] {
					blocked++
				}
				return true
			})
			if len(allowed) != tc.allowed || blocked != tc.blocked {
				t.Fatalf("allowed=%d blocked=%d, want %d/%d", len(allowed), blocked, tc.allowed, tc.blocked)
			}
		})
	}
}

// Evidence records carry observed handles or gate outcomes; a manifest
// record that shares the name is another domain.
func TestRuntimeEvidenceRecordClassification(t *testing.T) {
	t.Parallel()
	for source, evidence := range map[string]bool{
		"package drafts; type Record struct {SessionID, WorkflowID, ManifestYAML string}": false,
		"package evidence; type Record struct {Handle, GateType string}":                  true,
		"package shadow; type Record struct {Handle string}":                              true,
		"package shadow; type Record struct {GateType string}":                            true,
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		spec := file.Decls[0].(*ast.GenDecl).Specs[0].(*ast.TypeSpec)
		if got := forbiddencalls.RuntimeEvidenceRecord(spec); got != evidence {
			t.Fatalf("classification = %v, want %v for %s", got, evidence, source)
		}
	}
}
