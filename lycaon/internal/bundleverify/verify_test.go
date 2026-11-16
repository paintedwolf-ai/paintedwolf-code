package bundleverify

import (
	"context"
	"debug/macho"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/platformfloor"
	"github.com/lycaon/lycaon/internal/testutil"
)

// fakeRunner keys results by command, optional first argument, and optional target basename.
type fakeRunner struct {
	stdout  map[string][]byte
	stderr  map[string][]byte
	err     map[string]error
	missing map[string]bool
	calls   [][]string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		stdout:  map[string][]byte{},
		stderr:  map[string][]byte{},
		err:     map[string]error{},
		missing: map[string]bool{},
	}
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.missing[name] {
		return nil, nil, exec.ErrNotFound
	}
	key := name
	if len(args) > 0 {
		composite := name + " " + args[0]
		// "<tool> <first arg> @<target basename>" answers for one target.
		if targeted := composite + " @" + filepath.Base(args[len(args)-1]); f.has(targeted) {
			return f.stdout[targeted], f.stderr[targeted], f.err[targeted]
		}
		if _, ok := f.stdout[composite]; ok {
			key = composite
		} else if _, ok := f.stderr[composite]; ok {
			key = composite
		} else if _, ok := f.err[composite]; ok {
			key = composite
		}
	}
	return f.stdout[key], f.stderr[key], f.err[key]
}

func (f *fakeRunner) has(key string) bool {
	_, out := f.stdout[key]
	_, errOut := f.stderr[key]
	_, err := f.err[key]
	return out || errOut || err
}

func (f *fakeRunner) callsFor(name string, prefix ...string) [][]string {
	var out [][]string
	for _, c := range f.calls {
		if c[0] == name && len(c) >= 1+len(prefix) && slices.Equal(c[1:1+len(prefix)], prefix) {
			out = append(out, c)
		}
	}
	return out
}

func signedOK() *fakeRunner {
	r := newFakeRunner()
	r.stderr["codesign"] = []byte(codesignDeveloperIDWithRuntime)
	r.stdout["codesign -d"] = []byte(entitlementsEmpty)
	r.stdout["codesign -d @"+browserBinaryName] = []byte(entitlementsBrowserOK)
	r.stdout["codesign -d @pw"] = []byte(entitlementsEngineOK)
	return r
}

type appOptions struct {
	sidecarArch   macho.Cpu
	sidecarMinOS  string
	sidecarDylibs []string
	sidecarExtra  []byte
	logsExtra     []byte
	browserArch   macho.Cpu
	browserFat    bool
	omitSidecar   bool
	omitLogsCLI   bool
	omitCore      bool
	omitDecide    bool
	omitMetallib  bool
	omitModel     bool
	omitBrowser   bool
	omitNotices   bool
	extraFiles    map[string][]byte
}

func defaultAppOptions() appOptions {
	return appOptions{
		sidecarArch:   macho.CpuArm64,
		sidecarMinOS:  "13.0",
		sidecarDylibs: []string{"/usr/lib/libSystem.B.dylib", "/System/Library/Frameworks/Security.framework/Security"},
		browserArch:   macho.CpuArm64,
	}
}

func buildFakeApp(t *testing.T, opts appOptions) string {
	t.Helper()

	app := filepath.Join(t.TempDir(), "Painted Wolf Code.app")
	mustMkdirAll(t, filepath.Join(app, "Contents", "MacOS"))
	mustWriteFile(t, filepath.Join(app, filepath.Dir(filepath.Dir(sidecarRelPath)), "embedded.provisionprofile"), []byte("synthetic profile"))
	mustMkdirAll(t, filepath.Join(app, "Contents", "Resources", "engine-root", "schemas"))

	if !opts.omitSidecar {
		writeThinMachOWithExtra(t, filepath.Join(app, sidecarRelPath),
			opts.sidecarArch, opts.sidecarMinOS, opts.sidecarDylibs, opts.sidecarExtra)
	}
	if !opts.omitLogsCLI {
		// The log viewer has an independent binary fixture.
		writeThinMachOWithExtra(t, filepath.Join(app, logsCLIRelPath),
			macho.CpuArm64, "13.0",
			[]string{"/usr/lib/libSystem.B.dylib", "/System/Library/Frameworks/Security.framework/Security"},
			opts.logsExtra)
	}
	if !opts.omitCore {
		writeThinMachO(t, filepath.Join(app, documentCoreRelPath),
			macho.CpuArm64, "13.0", []string{"/usr/lib/libSystem.B.dylib"})
	}
	if !opts.omitDecide {
		writeThinMachO(t, filepath.Join(app, decideEngineRelPath),
			macho.CpuArm64, "13.0",
			[]string{"/usr/lib/libSystem.B.dylib", "/System/Library/Frameworks/Metal.framework/Metal"})
	}
	if !opts.omitDecide && !opts.omitMetallib {
		mustWriteFile(t, filepath.Join(app, filepath.FromSlash(decideMetallibRelPath)), []byte("metallib fixture"))
	}
	if !opts.omitModel {
		writeFakeCheckpoint(t, app)
	}

	writeThinMachO(t, filepath.Join(app, "Contents", "MacOS", "Painted Wolf Code"),
		macho.CpuArm64, "13.0", []string{"/usr/lib/libSystem.B.dylib"})

	mustWriteFile(t, filepath.Join(app, "Contents", "Resources", "engine-root", "schemas", ".keep"), []byte("x"))

	if !opts.omitNotices {
		stub := strings.Repeat("third-party notices fixture line\n", 200)
		mustWriteFile(t, filepath.Join(app, noticesRelPath), []byte(stub))
	}

	if !opts.omitBrowser {
		browser := filepath.Join(app, engineBrowserRelPath, "chrome-headless-shell")
		if opts.browserFat {
			writeFatMachO(t, browser, []fatSlice{
				{cpu: macho.CpuArm64, minOS: "13.0"},
				{cpu: macho.CpuAmd64, minOS: "13.0"},
			})
		} else {
			writeThinMachO(t, browser, opts.browserArch, "13.0", []string{"/usr/lib/libSystem.B.dylib"})
		}
	}

	writeFakeOpenGrep(t, app)

	for rel, content := range opts.extraFiles {
		mustWriteFile(t, filepath.Join(app, rel), content)
	}

	return app
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir "+dir, err)
	}
}

// writeFakeCheckpoint lays out the shipped checkpoint as sparse files at their pinned sizes.
func writeFakeCheckpoint(t *testing.T, app string) {
	t.Helper()
	dir := filepath.Join(app, filepath.FromSlash(engineRootRelPath+"/"+bialy.ShippedModel.BundledRel()))
	for _, f := range bialy.ShippedModel.Files {
		path := filepath.Join(dir, filepath.FromSlash(f.Path))
		mustWriteFile(t, path, nil)
		if err := os.Truncate(path, f.Size); err != nil {
			testutil.FailErr(t, "size "+path, err)
		}
	}
	mustWriteFile(t, filepath.Join(dir, bialy.CompleteMarker), []byte("fixture\n"))
}

func mustWriteFile(t *testing.T, path string, content []byte) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, content, 0o644); err != nil {
		testutil.FailErr(t, "write "+path, err)
	}
}

func runVerify(t *testing.T, app string, requireSigned bool, runner Runner) Report {
	t.Helper()
	return runVerifyOptions(t, Options{AppPath: app, RequireSigned: requireSigned, Runner: runner})
}

// runVerifyOptions verifies a fixture bundle, answering the scanner probe for
// a fake runner that has no answer of its own.
func runVerifyOptions(t *testing.T, options Options) Report {
	t.Helper()
	if fake, ok := options.Runner.(*fakeRunner); ok {
		key := filepath.Join(options.AppPath, sidecarRelPath)
		if _, exists := fake.stdout[key]; !exists {
			fake.stdout[key] = []byte(filepath.Join(options.AppPath, fakeOpenGrepRelPath) + "\n")
		}
	}
	report, err := Verify(context.Background(), options)
	testutil.FailErr(t, "verify bundle", err)
	return report
}

func findingsWithCode(r Report, code string) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Code == code {
			out = append(out, f)
		}
	}
	return out
}

func requireNoCode(t *testing.T, r Report, code string) {
	t.Helper()
	if got := findingsWithCode(r, code); len(got) != 0 {
		t.Fatalf("unexpected %s findings: %+v", code, got)
	}
}

func requireCodeCount(t *testing.T, r Report, code string, want int) []Finding {
	t.Helper()
	got := findingsWithCode(r, code)
	if len(got) != want {
		t.Fatalf("%s findings = %d, want %d (all findings: %+v)", code, len(got), want, r.Findings)
	}
	return got
}

// verifyRun is one Verify call over a fixture bundle.
type verifyRun struct {
	app, dmg string
	runner   *fakeRunner
	report   Report
}

// wantCode is the exact number of findings with a code and, when set, the
// severity each of them has.
type wantCode struct {
	code     string
	count    int
	severity Severity
}

type verdict int

const (
	anyVerdict verdict = iota
	reportPasses
	reportFails
)

type verifyCase struct {
	name string
	// bundle varies the fixture bundle before it is built.
	bundle func(*appOptions)
	// runner replaces the fully signed tool answers.
	runner func() *fakeRunner
	// arrange varies the built bundle and the tool answers.
	arrange       func(t *testing.T, app string, runner *fakeRunner)
	requireSigned bool
	withDMG       bool
	want          []wantCode
	verdict       verdict
	inspect       func(t *testing.T, run verifyRun)
}

func codesignReporting(description string) func() *fakeRunner {
	return func() *fakeRunner {
		runner := newFakeRunner()
		runner.stderr["codesign"] = []byte(description)
		return runner
	}
}

// entitlementsOf answers the entitlement query for one binary.
func entitlementsOf(relPath, plist string) func(*testing.T, string, *fakeRunner) {
	return func(_ *testing.T, _ string, runner *fakeRunner) {
		runner.stdout["codesign -d @"+filepath.Base(relPath)] = []byte(plist)
	}
}

func brokenSeal(_ *testing.T, _ string, runner *fakeRunner) {
	runner.err["codesign --verify"] = errors.New("invalid signature")
}

func missingCodesign(_ *testing.T, _ string, runner *fakeRunner) {
	runner.missing["codesign"] = true
}

// firstFinding checks the first finding with code.
func firstFinding(code string, check func(*testing.T, Finding)) func(*testing.T, verifyRun) {
	return func(t *testing.T, run verifyRun) {
		check(t, findingsWithCode(run.report, code)[0])
	}
}

func findingAt(code, path string) func(*testing.T, verifyRun) {
	return firstFinding(code, func(t *testing.T, f Finding) {
		if f.Path != path {
			t.Fatalf("%s names %q, want %q", code, f.Path, path)
		}
	})
}

func findingDetail(code, key, want string) func(*testing.T, verifyRun) {
	return firstFinding(code, func(t *testing.T, f Finding) {
		if f.Detail[key] != want {
			t.Fatalf("%s detail = %v, want %s=%q", code, f.Detail, key, want)
		}
	})
}

// everyFindingOn checks every finding with code names the binary called base.
func everyFindingOn(code, base string) func(*testing.T, verifyRun) {
	return func(t *testing.T, run verifyRun) {
		for _, f := range findingsWithCode(run.report, code) {
			if filepath.Base(f.Path) != base {
				t.Fatalf("%s on %q, want it scoped to %s", code, f.Path, base)
			}
		}
	}
}

func machOCount(want int) func(*testing.T, verifyRun) {
	return func(t *testing.T, run verifyRun) {
		if run.report.MachOCount != want {
			t.Fatalf("MachOCount = %d, want %d", run.report.MachOCount, want)
		}
	}
}

// refusesRuntimeCodeGeneration grants the browser's entitlements to another binary.
func refusesRuntimeCodeGeneration(relPath string) verifyCase {
	return verifyCase{
		name:          "runtime code generation is refused on " + filepath.Base(relPath),
		arrange:       entitlementsOf(relPath, entitlementsBrowserOK),
		requireSigned: true,
		want:          []wantCode{{CodeEntitlementUnexpected, 2, SeverityError}},
		inspect:       everyFindingOn(CodeEntitlementUnexpected, filepath.Base(relPath)),
	}
}

func TestVerify(t *testing.T) {
	modelFile := bialy.ShippedModel.Files[0]
	modelFileRel := engineRootRelPath + "/" + bialy.ShippedModel.BundledRel() + "/" + modelFile.Path
	const leakedSource = "/Users/someone/git/lycaon/lycaon/internal/session/manager.go"

	cases := []verifyCase{
		{
			name:          "clean bundle",
			requireSigned: true,
			verdict:       reportPasses,
			inspect: func(t *testing.T, run verifyRun) {
				if errs, _ := run.report.Counts(); errs != 0 {
					t.Fatalf("clean bundle has %d error findings: %+v", errs, run.report.Findings)
				}
				// Sidecar, logs CLI, document core, decision engine, app binary, browser, scanner.
				machOCount(7)(t, run)
			},
		},
		{
			name:    "minimum OS above the floor",
			bundle:  func(o *appOptions) { o.sidecarMinOS = "15.0" },
			want:    []wantCode{{code: CodeMinOSAboveFloor, count: 1}},
			verdict: reportFails,
			inspect: firstFinding(CodeMinOSAboveFloor, func(t *testing.T, f Finding) {
				if f.Detail["want"] != platformfloor.MacOSMin() || f.Detail["got"] != "15.0" {
					t.Fatalf("detail = %v, want {want:%s got:15.0}", f.Detail, platformfloor.MacOSMin())
				}
			}),
		},
		{
			name:   "minimum OS below the floor compares numerically",
			bundle: func(o *appOptions) { o.sidecarMinOS = "9.0" },
			want:   []wantCode{{code: CodeMinOSAboveFloor}},
		},
		{
			name:   "browser built for the wrong architecture",
			bundle: func(o *appOptions) { o.browserArch = macho.CpuAmd64 },
			// The wrong slice and the absent shipped slice are each reported.
			want:    []wantCode{{code: CodeArchUnexpected, count: 2}},
			inspect: everyFindingOn(CodeArchUnexpected, browserBinaryName),
		},
		{
			name: "non-system dylib",
			bundle: func(o *appOptions) {
				o.sidecarDylibs = []string{"/usr/lib/libSystem.B.dylib", "/opt/homebrew/lib/libfoo.dylib"}
			},
			want:    []wantCode{{code: CodeNonSystemDylib, count: 1}},
			inspect: findingDetail(CodeNonSystemDylib, "dylib", "/opt/homebrew/lib/libfoo.dylib"),
		},
		{
			name: "rpath dylib is accepted",
			bundle: func(o *appOptions) {
				o.sidecarDylibs = []string{"@rpath/libbar.dylib", "@executable_path/../Frameworks/libbaz.dylib"}
			},
			want: []wantCode{{code: CodeNonSystemDylib}},
		},
		{
			name:    "build path leak",
			bundle:  func(o *appOptions) { o.sidecarExtra = []byte("\x00" + leakedSource + "\x00") },
			want:    []wantCode{{code: CodeBuildPathLeak, count: 1}},
			inspect: findingDetail(CodeBuildPathLeak, "path", leakedSource),
		},
		{
			name: "embedded user path that is not a source file is not a leak",
			bundle: func(o *appOptions) {
				o.sidecarExtra = []byte("\x00'''^/Users/(?i)[a-z0-9]+/[\\w .-/]+$'''\x00/Users/me/.config/paintedwolf/sandboxes/deadbeef/job-a\x00")
			},
			want: []wantCode{{code: CodeBuildPathLeak}},
		},
		{
			name:    "sidecar missing",
			bundle:  func(o *appOptions) { o.omitSidecar = true },
			want:    []wantCode{{code: CodeSidecarMissing, count: 1}},
			verdict: reportFails,
		},
		{
			name:   "decision engine missing",
			bundle: func(o *appOptions) { o.omitDecide = true },
			// The binary and its Metal library are each reported.
			want:    []wantCode{{code: CodeDecideEngineMissing, count: 2}},
			verdict: reportFails,
		},
		{
			name:   "decision model missing",
			bundle: func(o *appOptions) { o.omitModel = true },
			// Every pinned file and the completion marker are reported.
			want:    []wantCode{{code: CodeDecideModelMissing, count: len(bialy.ShippedModel.Files) + 1}},
			verdict: reportFails,
		},
		{
			name: "decision model file size differs",
			arrange: func(t *testing.T, app string, _ *fakeRunner) {
				testutil.FailErr(t, "resize checkpoint file", os.Truncate(filepath.Join(app, filepath.FromSlash(modelFileRel)), modelFile.Size+1))
			},
			want:    []wantCode{{code: CodeDecideModelMissing, count: 1}},
			inspect: findingAt(CodeDecideModelMissing, modelFileRel),
		},
		{
			name:    "decision engine Metal library missing",
			bundle:  func(o *appOptions) { o.omitMetallib = true },
			want:    []wantCode{{code: CodeDecideEngineMissing, count: 1}},
			verdict: reportFails,
			inspect: findingAt(CodeDecideEngineMissing, decideMetallibRelPath),
		},
		{
			name:    "logs CLI missing",
			bundle:  func(o *appOptions) { o.omitLogsCLI = true },
			want:    []wantCode{{code: CodeLogsCLIMissing, count: 1}},
			verdict: reportFails,
		},
		{
			name:    "engine browser missing",
			bundle:  func(o *appOptions) { o.omitBrowser = true },
			want:    []wantCode{{code: CodeEngineResourceMissing, count: 1}},
			inspect: findingAt(CodeEngineResourceMissing, engineBrowserRelPath),
		},
		{
			name:    "notices missing",
			bundle:  func(o *appOptions) { o.omitNotices = true },
			want:    []wantCode{{code: CodeNoticesMissing, count: 1}},
			verdict: reportFails,
			inspect: findingAt(CodeNoticesMissing, noticesRelPath),
		},
		{
			name: "notices too small",
			bundle: func(o *appOptions) {
				o.omitNotices = true
				o.extraFiles = map[string][]byte{noticesRelPath: []byte("too small\n")}
			},
			want:    []wantCode{{code: CodeNoticesMissing, count: 1}},
			inspect: findingDetail(CodeNoticesMissing, "reason", "too small"),
		},
		{
			name:   "ad hoc signature warns when signing is not required",
			runner: codesignReporting(codesignAdhoc),
			// The app and its seven Mach-O files.
			want:    []wantCode{{CodeSignatureAdhoc, 8, SeverityWarn}},
			verdict: reportPasses,
		},
		{
			name:          "ad hoc signature fails when signing is required",
			runner:        codesignReporting(codesignAdhoc),
			requireSigned: true,
			verdict:       reportFails,
			inspect: func(t *testing.T, run verifyRun) {
				for _, f := range findingsWithCode(run.report, CodeSignatureAdhoc) {
					if f.Severity != SeverityError {
						t.Fatalf("%s severity = %q, want error when signing is required", f.Code, f.Severity)
					}
				}
			},
		},
		{
			name: "not stapled",
			arrange: func(_ *testing.T, _ string, runner *fakeRunner) {
				runner.err["xcrun"] = errors.New("stapler: does not have a ticket")
			},
			requireSigned: true,
			want:          []wantCode{{CodeNotStapled, 1, SeverityError}},
		},
		{
			name:    "missing tool warns when signing is not required",
			bundle:  func(o *appOptions) { o.sidecarMinOS = "15.0" },
			arrange: missingCodesign,
			want: []wantCode{
				{code: CodeMinOSAboveFloor, count: 1},
				{code: CodeSignatureAdhoc},
				{CodeToolUnavailable, 1, SeverityWarn},
			},
		},
		{
			name:          "missing tool fails when signing is required",
			arrange:       missingCodesign,
			requireSigned: true,
			want:          []wantCode{{CodeToolUnavailable, 1, SeverityError}},
			verdict:       reportFails,
		},
		{
			name: "Gatekeeper assessment runs without xcrun",
			arrange: func(_ *testing.T, _ string, runner *fakeRunner) {
				runner.missing["xcrun"] = true
				runner.err["spctl"] = errors.New("rejected")
			},
			requireSigned: true,
			want:          []wantCode{{code: CodeGatekeeperRejected, count: 1}, {code: CodeToolUnavailable, count: 1}},
			inspect: func(t *testing.T, run verifyRun) {
				if len(run.runner.callsFor("spctl")) == 0 {
					t.Fatal("spctl was never invoked despite not depending on xcrun")
				}
			},
		},
		{
			name:          "broken seal fails when signing is required",
			arrange:       brokenSeal,
			requireSigned: true,
			want:          []wantCode{{CodeSignatureBroken, 1, SeverityError}},
			verdict:       reportFails,
		},
		{
			name:    "broken seal warns when signing is not required",
			arrange: brokenSeal,
			want:    []wantCode{{CodeSignatureBroken, 1, SeverityWarn}},
		},
		{
			name:          "disk image and app are both assessed",
			requireSigned: true,
			withDMG:       true,
			verdict:       reportPasses,
			inspect: func(t *testing.T, run verifyRun) {
				assessed := map[string]string{}
				for _, call := range run.runner.callsFor("spctl") {
					target := call[len(call)-1]
					for i, arg := range call {
						if arg == "--type" && i+1 < len(call) {
							assessed[target] = call[i+1]
						}
					}
				}
				if assessed[run.app] != "exec" || assessed[run.dmg] != "open" {
					t.Fatalf("spctl assessments = %v, want the app as exec and the disk image as open", assessed)
				}
				staple := run.runner.callsFor("xcrun")
				if len(staple) != 1 || staple[0][len(staple[0])-1] != run.dmg {
					t.Fatalf("stapler calls = %v, want one validating the disk image", staple)
				}
			},
		},
		{
			name:          "browser without its JIT entitlements fails",
			arrange:       entitlementsOf(browserBinaryName, entitlementsEmpty),
			requireSigned: true,
			want:          []wantCode{{CodeEntitlementMissing, 2, SeverityError}},
			inspect: func(t *testing.T, run verifyRun) {
				everyFindingOn(CodeEntitlementMissing, browserBinaryName)(t, run)
				seen := map[string]bool{}
				for _, f := range findingsWithCode(run.report, CodeEntitlementMissing) {
					seen[f.Detail["entitlement"]] = true
				}
				if !seen["com.apple.security.cs.allow-jit"] || !seen["com.apple.security.cs.allow-unsigned-executable-memory"] {
					t.Fatalf("findings do not name both required entitlements: %v", seen)
				}
			},
		},
		{
			name:    "missing entitlements warn when signing is not required",
			arrange: entitlementsOf(browserBinaryName, entitlementsEmpty),
			want:    []wantCode{{CodeEntitlementMissing, 2, SeverityWarn}},
		},
		refusesRuntimeCodeGeneration(sidecarRelPath),
		refusesRuntimeCodeGeneration(documentCoreRelPath),
		refusesRuntimeCodeGeneration(decideEngineRelPath),
		{
			name: "debuggable executable is refused",
			arrange: entitlementsOf(documentCoreRelPath,
				`<plist version="1.0"><dict><key>com.apple.security.get-task-allow</key><true/></dict></plist>`),
			requireSigned: true,
			want:          []wantCode{{code: CodeEntitlementUnexpected, count: 1}},
			inspect: func(t *testing.T, run verifyRun) {
				findingAt(CodeEntitlementUnexpected, documentCoreRelPath)(t, run)
				findingDetail(CodeEntitlementUnexpected, "entitlement", "com.apple.security.get-task-allow")(t, run)
			},
		},
		{
			name:          "engine profile entitlements are accepted on the engine",
			requireSigned: true,
			want:          []wantCode{{code: CodeEntitlementUnexpected}, {code: CodeEntitlementMissing}},
		},
		{
			name:          "engine profile entitlements are refused on the log viewer",
			arrange:       entitlementsOf(logsCLIRelPath, entitlementsEngineOK),
			requireSigned: true,
			want:          []wantCode{{code: CodeEntitlementUnexpected, count: 3}},
			inspect:       findingAt(CodeEntitlementUnexpected, logsCLIRelPath),
		},
		{
			name:          "unreadable entitlements fail",
			arrange:       entitlementsOf(decideEngineRelPath, "<plist><dict><key>truncated"),
			requireSigned: true,
			want:          []wantCode{{code: CodeEntitlementUnreadable, count: 1}},
			inspect:       findingAt(CodeEntitlementUnreadable, decideEngineRelPath),
		},
		{
			name:   "entitlements are not read without the hardened runtime",
			runner: codesignReporting(codesignDeveloperIDNoRuntime),
			want:   []wantCode{{code: CodeEntitlementMissing}},
			inspect: func(t *testing.T, run verifyRun) {
				if len(findingsWithCode(run.report, CodeHardenedRuntimeMissing)) == 0 {
					t.Fatal("a bundle without the hardened runtime reported no HARDENED_RUNTIME_MISSING finding")
				}
			},
		},
		{
			name:          "no browser needs no browser entitlements",
			bundle:        func(o *appOptions) { o.omitBrowser = true },
			requireSigned: true,
			want:          []wantCode{{code: CodeEntitlementMissing}},
		},
		{
			name:    "document core missing",
			bundle:  func(o *appOptions) { o.omitCore = true },
			want:    []wantCode{{code: CodeDocumentCoreMissing, count: 1}},
			inspect: findingAt(CodeDocumentCoreMissing, documentCoreRelPath),
		},
		{
			name:          "document core is probed through the packaged engine",
			requireSigned: true,
			want:          []wantCode{{code: CodeDocumentCoreFailed}},
			inspect: func(t *testing.T, run verifyRun) {
				if calls := run.runner.callsFor(filepath.Join(run.app, sidecarRelPath), "diagnostics", "document-core"); len(calls) != 1 {
					t.Fatalf("document core probe calls = %v, want one", calls)
				}
			},
		},
		{
			name: "document core probe failure carries the probe output",
			arrange: func(_ *testing.T, app string, runner *fakeRunner) {
				probe := filepath.Join(app, sidecarRelPath) + " diagnostics"
				runner.err[probe] = errors.New("signal: killed")
				runner.stderr[probe] = []byte("document core exited (signal: killed)")
			},
			requireSigned: true,
			want:          []wantCode{{CodeDocumentCoreFailed, 1, SeverityError}},
			inspect: firstFinding(CodeDocumentCoreFailed, func(t *testing.T, f Finding) {
				if !strings.Contains(f.Detail["output"], "signal: killed") {
					t.Fatalf("finding = %+v, want the probe's output", f)
				}
			}),
		},
		{
			name:   "fat binary with an extra slice warns",
			bundle: func(o *appOptions) { o.browserFat = true },
			want:   []wantCode{{code: CodeArchUnexpected}, {CodeArchExtraSlice, 1, SeverityWarn}},
		},
		{
			name: "files that are not Mach-O are not counted",
			bundle: func(o *appOptions) {
				o.extraFiles = map[string][]byte{
					"Contents/Resources/icudtl.dat": []byte("\x00\x01binary payload, not a mach-o"),
					"Contents/Resources/app.js":     []byte("export const x = 1;"),
				}
			},
			inspect: machOCount(7),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts := defaultAppOptions()
			if c.bundle != nil {
				c.bundle(&opts)
			}
			run := verifyRun{app: buildFakeApp(t, opts), runner: signedOK()}
			if c.runner != nil {
				run.runner = c.runner()
			}
			if c.arrange != nil {
				c.arrange(t, run.app, run.runner)
			}
			if c.withDMG {
				run.dmg = filepath.Join(filepath.Dir(run.app), "Painted Wolf Code.dmg")
				mustWriteFile(t, run.dmg, []byte("dmg"))
			}
			run.report = runVerifyOptions(t, Options{AppPath: run.app, DMGPath: run.dmg, RequireSigned: c.requireSigned, Runner: run.runner})

			for _, want := range c.want {
				for _, f := range requireCodeCount(t, run.report, want.code, want.count) {
					if want.severity != "" && f.Severity != want.severity {
						t.Fatalf("%s severity = %q, want %q", want.code, f.Severity, want.severity)
					}
				}
			}
			if c.verdict != anyVerdict && run.report.Failed() != (c.verdict == reportFails) {
				t.Fatalf("report failed = %v: %+v", run.report.Failed(), run.report.Findings)
			}
			if c.inspect != nil {
				c.inspect(t, run)
			}
		})
	}
}

func TestFindingsAreSortedStably(t *testing.T) {
	opts := defaultAppOptions()
	opts.sidecarMinOS = "15.0"
	opts.sidecarDylibs = []string{"/opt/homebrew/lib/libfoo.dylib"}

	report := runVerify(t, buildFakeApp(t, opts), false, signedOK())

	for i := 1; i < len(report.Findings); i++ {
		prev, cur := report.Findings[i-1], report.Findings[i]
		if prev.Severity == SeverityWarn && cur.Severity == SeverityError {
			t.Fatalf("findings not ordered by severity: %+v", report.Findings)
		}
	}
}

func TestGitengineBloatedRejectsMaterializedAlias(t *testing.T) {
	opts := defaultAppOptions()
	app := buildFakeApp(t, opts)
	core := filepath.Join(app, engineGitRelPath, "libexec", "git-core")
	mustMkdirAll(t, core)
	mustWriteFile(t, filepath.Join(core, "git-status"), make([]byte, 2*1024*1024))

	report := runVerify(t, app, false, signedOK())
	hits := findingsWithCode(report, CodeGitengineBloated)
	if len(hits) == 0 {
		t.Fatal("expected GITENGINE_BLOATED for materialized git-status")
	}
}

func TestGitengineSlimTreeIsClean(t *testing.T) {
	opts := defaultAppOptions()
	app := buildFakeApp(t, opts)
	core := filepath.Join(app, engineGitRelPath, "libexec", "git-core")
	mustMkdirAll(t, core)
	mustWriteFile(t, filepath.Join(core, "git"), []byte("fake-git"))
	mustWriteFile(t, filepath.Join(app, engineGitRelPath, "bin", "git-lfs"), []byte("fake-lfs"))

	report := runVerify(t, app, false, signedOK())
	if hits := findingsWithCode(report, CodeGitengineBloated); len(hits) != 0 {
		t.Fatalf("slim gitengine must not trip GITENGINE_BLOATED: %+v", hits)
	}
}
