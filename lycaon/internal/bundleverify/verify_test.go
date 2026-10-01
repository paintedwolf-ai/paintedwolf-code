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

// fakeRunner keys results by command and optional first argument.
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
	r.stdout["codesign -d"] = []byte(entitlementsBrowserOK)
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
	mustMkdirAll(t, filepath.Join(app, "Contents", "Resources", "engine-root", "config"))

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

	mustWriteFile(t, filepath.Join(app, "Contents", "Resources", "engine-root", "config", ".keep"), []byte("x"))

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
	if fake, ok := runner.(*fakeRunner); ok {
		key := filepath.Join(app, sidecarRelPath)
		if _, exists := fake.stdout[key]; !exists {
			fake.stdout[key] = []byte(filepath.Join(app, fakeOpenGrepRelPath) + "\n")
		}
	}
	report, err := Verify(context.Background(), Options{
		AppPath:       app,
		RequireSigned: requireSigned,
		Runner:        runner,
	})
	if err != nil {
		testutil.FailErr(t, "verify bundle", err)
	}
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

func TestVerifyCleanBundle(t *testing.T) {
	report := runVerify(t, buildFakeApp(t, defaultAppOptions()), true, signedOK())

	if report.Failed() {
		t.Fatalf("clean bundle reported failure: %+v", report.Findings)
	}
	errs, _ := report.Counts()
	if errs != 0 {
		t.Fatalf("clean bundle has %d error findings: %+v", errs, report.Findings)
	}
	if report.MachOCount != 6 {
		t.Fatalf("MachOCount = %d, want 6 (sidecar, logs CLI, decision engine, app binary, browser, scanner)", report.MachOCount)
	}
}

func TestVerifyMinOSAboveFloor(t *testing.T) {
	opts := defaultAppOptions()
	opts.sidecarMinOS = "15.0"

	report := runVerify(t, buildFakeApp(t, opts), false, signedOK())

	got := requireCodeCount(t, report, CodeMinOSAboveFloor, 1)
	if got[0].Detail["want"] != platformfloor.MacOSMin() || got[0].Detail["got"] != "15.0" {
		t.Fatalf("detail = %v, want {want:%s got:15.0}", got[0].Detail, platformfloor.MacOSMin())
	}
	if !report.Failed() {
		t.Fatal("MINOS_ABOVE_FLOOR must fail the report")
	}
}

func TestVerifyMinOSBelowFloorIsNumericCompare(t *testing.T) {
	opts := defaultAppOptions()
	opts.sidecarMinOS = "9.0"

	report := runVerify(t, buildFakeApp(t, opts), false, signedOK())

	requireNoCode(t, report, CodeMinOSAboveFloor)
}

func TestVerifyBrowserWrongArch(t *testing.T) {
	opts := defaultAppOptions()
	opts.browserArch = macho.CpuAmd64

	report := runVerify(t, buildFakeApp(t, opts), false, signedOK())

	got := requireCodeCount(t, report, CodeArchUnexpected, 2) // wrong slice + no shipped slice
	for _, f := range got {
		if filepath.Base(f.Path) != "chrome-headless-shell" {
			t.Fatalf("ARCH_UNEXPECTED on %q, want it scoped to the browser binary", f.Path)
		}
	}
}

func TestVerifyNonSystemDylib(t *testing.T) {
	opts := defaultAppOptions()
	opts.sidecarDylibs = []string{"/usr/lib/libSystem.B.dylib", "/opt/homebrew/lib/libfoo.dylib"}

	report := runVerify(t, buildFakeApp(t, opts), false, signedOK())

	got := requireCodeCount(t, report, CodeNonSystemDylib, 1)
	if got[0].Detail["dylib"] != "/opt/homebrew/lib/libfoo.dylib" {
		t.Fatalf("detail dylib = %q", got[0].Detail["dylib"])
	}
}

func TestVerifyRpathDylibIsFine(t *testing.T) {
	opts := defaultAppOptions()
	opts.sidecarDylibs = []string{"@rpath/libbar.dylib", "@executable_path/../Frameworks/libbaz.dylib"}

	report := runVerify(t, buildFakeApp(t, opts), false, signedOK())

	requireNoCode(t, report, CodeNonSystemDylib)
}

func TestVerifyBuildPathLeak(t *testing.T) {
	opts := defaultAppOptions()
	opts.sidecarExtra = []byte("\x00/Users/someone/git/lycaon/lycaon/internal/session/manager.go\x00")

	report := runVerify(t, buildFakeApp(t, opts), false, signedOK())

	got := requireCodeCount(t, report, CodeBuildPathLeak, 1)
	if got[0].Detail["path"] != "/Users/someone/git/lycaon/lycaon/internal/session/manager.go" {
		t.Fatalf("detail path = %q", got[0].Detail["path"])
	}
}

func TestVerifyEmbeddedUsersStringIsNotALeak(t *testing.T) {
	opts := defaultAppOptions()
	opts.sidecarExtra = []byte("\x00'''^/Users/(?i)[a-z0-9]+/[\\w .-/]+$'''\x00/Users/me/.config/paintedwolf/sandboxes/deadbeef/job-a\x00")

	report := runVerify(t, buildFakeApp(t, opts), false, signedOK())

	requireNoCode(t, report, CodeBuildPathLeak)
}

func TestVerifySidecarMissing(t *testing.T) {
	opts := defaultAppOptions()
	opts.omitSidecar = true

	report := runVerify(t, buildFakeApp(t, opts), false, signedOK())

	requireCodeCount(t, report, CodeSidecarMissing, 1)
	if !report.Failed() {
		t.Fatal("SIDECAR_MISSING must fail the report")
	}
}

func TestVerifyDecideEngineMissing(t *testing.T) {
	opts := defaultAppOptions()
	opts.omitDecide = true

	report := runVerify(t, buildFakeApp(t, opts), false, signedOK())

	// The binary and its Metal library are each reported.
	requireCodeCount(t, report, CodeDecideEngineMissing, 2)
	if !report.Failed() {
		t.Fatal("DECIDE_ENGINE_MISSING must fail the report")
	}
}

func TestVerifyDecideModelMissing(t *testing.T) {
	opts := defaultAppOptions()
	opts.omitModel = true

	report := runVerify(t, buildFakeApp(t, opts), false, signedOK())

	// Every pinned file and the completion marker are reported.
	requireCodeCount(t, report, CodeDecideModelMissing, len(bialy.ShippedModel.Files)+1)
	if !report.Failed() {
		t.Fatal("a missing checkpoint must fail the report")
	}
}

func TestVerifyDecideModelSizeDiffers(t *testing.T) {
	app := buildFakeApp(t, defaultAppOptions())
	pinned := bialy.ShippedModel.Files[0]
	rel := engineRootRelPath + "/" + bialy.ShippedModel.BundledRel() + "/" + pinned.Path
	if err := os.Truncate(filepath.Join(app, filepath.FromSlash(rel)), pinned.Size+1); err != nil {
		testutil.FailErr(t, "resize checkpoint file", err)
	}

	report := runVerify(t, app, false, signedOK())

	got := requireCodeCount(t, report, CodeDecideModelMissing, 1)
	if got[0].Path != rel {
		t.Fatalf("finding names %s, want %s", got[0].Path, rel)
	}
}

func TestVerifyDecideMetallibMissing(t *testing.T) {
	opts := defaultAppOptions()
	opts.omitMetallib = true

	report := runVerify(t, buildFakeApp(t, opts), false, signedOK())

	requireCodeCount(t, report, CodeDecideEngineMissing, 1)
	for _, f := range report.Findings {
		if f.Code == CodeDecideEngineMissing && f.Path != decideMetallibRelPath {
			t.Fatalf("finding names %s, want the Metal library", f.Path)
		}
	}
	if !report.Failed() {
		t.Fatal("a missing Metal library must fail the report")
	}
}

func TestVerifyLogsCLIMissing(t *testing.T) {
	opts := defaultAppOptions()
	opts.omitLogsCLI = true

	report := runVerify(t, buildFakeApp(t, opts), false, signedOK())

	requireCodeCount(t, report, CodeLogsCLIMissing, 1)
	if !report.Failed() {
		t.Fatal("LOGS_CLI_MISSING must fail the report")
	}
}

func TestVerifyEngineResourceMissing(t *testing.T) {
	opts := defaultAppOptions()
	opts.omitBrowser = true

	report := runVerify(t, buildFakeApp(t, opts), false, signedOK())

	got := requireCodeCount(t, report, CodeEngineResourceMissing, 1)
	if got[0].Path != engineBrowserRelPath {
		t.Fatalf("path = %q, want %q", got[0].Path, engineBrowserRelPath)
	}
}

func TestVerifyNoticesMissing(t *testing.T) {
	opts := defaultAppOptions()
	opts.omitNotices = true

	report := runVerify(t, buildFakeApp(t, opts), false, signedOK())

	got := requireCodeCount(t, report, CodeNoticesMissing, 1)
	if got[0].Path != noticesRelPath {
		t.Fatalf("path = %q, want %q", got[0].Path, noticesRelPath)
	}
	if !report.Failed() {
		t.Fatal("NOTICES_MISSING must fail the report")
	}
}

func TestVerifyNoticesTooSmall(t *testing.T) {
	opts := defaultAppOptions()
	opts.omitNotices = true
	opts.extraFiles = map[string][]byte{
		noticesRelPath: []byte("too small\n"),
	}

	report := runVerify(t, buildFakeApp(t, opts), false, signedOK())

	got := requireCodeCount(t, report, CodeNoticesMissing, 1)
	if got[0].Detail["reason"] != "too small" {
		t.Fatalf("detail = %+v, want too small", got[0].Detail)
	}
}

func TestVerifyAdhocSignatureWarnsWithoutRequireSigned(t *testing.T) {
	runner := newFakeRunner()
	runner.stderr["codesign"] = []byte(codesignAdhoc)

	report := runVerify(t, buildFakeApp(t, defaultAppOptions()), false, runner)

	got := requireCodeCount(t, report, CodeSignatureAdhoc, 7) // .app + 6 Mach-Os
	for _, f := range got {
		if f.Severity != SeverityWarn {
			t.Fatalf("SIGNATURE_ADHOC severity = %q, want warn without --require-signed", f.Severity)
		}
	}
	if report.Failed() {
		t.Fatalf("adhoc build must not fail without --require-signed: %+v", report.Findings)
	}
}

func TestVerifyAdhocSignatureFailsWithRequireSigned(t *testing.T) {
	runner := newFakeRunner()
	runner.stderr["codesign"] = []byte(codesignAdhoc)

	report := runVerify(t, buildFakeApp(t, defaultAppOptions()), true, runner)

	for _, f := range findingsWithCode(report, CodeSignatureAdhoc) {
		if f.Severity != SeverityError {
			t.Fatalf("SIGNATURE_ADHOC severity = %q, want error with --require-signed", f.Severity)
		}
	}
	if !report.Failed() {
		t.Fatal("adhoc signature must fail the report under --require-signed")
	}
}

func TestVerifyNotStapled(t *testing.T) {
	runner := signedOK()
	runner.err["xcrun"] = errors.New("stapler: does not have a ticket")

	report := runVerify(t, buildFakeApp(t, defaultAppOptions()), true, runner)

	got := requireCodeCount(t, report, CodeNotStapled, 1)
	if got[0].Severity != SeverityError {
		t.Fatalf("NOT_STAPLED severity = %q, want error under --require-signed", got[0].Severity)
	}
}

func TestVerifyToolMissingWarnsWithoutRequireSigned(t *testing.T) {
	runner := signedOK()
	runner.missing["codesign"] = true

	opts := defaultAppOptions()
	opts.sidecarMinOS = "15.0"

	report := runVerify(t, buildFakeApp(t, opts), false, runner)

	requireCodeCount(t, report, CodeMinOSAboveFloor, 1)
	requireNoCode(t, report, CodeSignatureAdhoc)

	got := requireCodeCount(t, report, CodeToolUnavailable, 1)
	if got[0].Severity != SeverityWarn {
		t.Fatalf("TOOL_UNAVAILABLE severity = %q, want warn without --require-signed", got[0].Severity)
	}
}

func TestVerifyToolMissingFailsWithRequireSigned(t *testing.T) {
	runner := signedOK()
	runner.missing["codesign"] = true

	report := runVerify(t, buildFakeApp(t, defaultAppOptions()), true, runner)

	got := requireCodeCount(t, report, CodeToolUnavailable, 1)
	if got[0].Severity != SeverityError {
		t.Fatalf("TOOL_UNAVAILABLE severity = %q, want error under --require-signed", got[0].Severity)
	}
	if !report.Failed() {
		t.Fatal("a bundle the host cannot verify must fail under --require-signed")
	}
}

func TestVerifySpctlRunsWithoutXcrun(t *testing.T) {
	runner := signedOK()
	runner.missing["xcrun"] = true
	runner.err["spctl"] = errors.New("rejected")

	report := runVerify(t, buildFakeApp(t, defaultAppOptions()), true, runner)

	requireCodeCount(t, report, CodeGatekeeperRejected, 1)
	requireCodeCount(t, report, CodeToolUnavailable, 1)
	if len(runner.callsFor("spctl")) == 0 {
		t.Fatal("spctl was never invoked despite not depending on xcrun")
	}
}

func TestVerifyDeepVerifyBrokenSeal(t *testing.T) {
	runner := signedOK()
	runner.err["codesign --verify"] = errors.New("invalid signature")

	report := runVerify(t, buildFakeApp(t, defaultAppOptions()), true, runner)

	got := requireCodeCount(t, report, CodeSignatureBroken, 1)
	if got[0].Severity != SeverityError {
		t.Fatalf("SIGNATURE_BROKEN severity = %q, want error under --require-signed", got[0].Severity)
	}
	if !report.Failed() {
		t.Fatal("a broken seal must fail the report under --require-signed")
	}
}

func TestVerifyDeepVerifyBrokenSealWarnsWithoutRequireSigned(t *testing.T) {
	runner := signedOK()
	runner.err["codesign --verify"] = errors.New("invalid signature")

	report := runVerify(t, buildFakeApp(t, defaultAppOptions()), false, runner)

	got := requireCodeCount(t, report, CodeSignatureBroken, 1)
	if got[0].Severity != SeverityWarn {
		t.Fatalf("SIGNATURE_BROKEN severity = %q, want warn without --require-signed", got[0].Severity)
	}
}

func TestVerifyDMGAssessesBothAppAndDMG(t *testing.T) {
	runner := signedOK()
	app := buildFakeApp(t, defaultAppOptions())
	runner.stdout[filepath.Join(app, sidecarRelPath)] = []byte(filepath.Join(app, fakeOpenGrepRelPath) + "\n")
	dmg := filepath.Join(filepath.Dir(app), "Painted Wolf Code.dmg")
	mustWriteFile(t, dmg, []byte("dmg"))

	report, err := Verify(context.Background(), Options{
		AppPath:       app,
		DMGPath:       dmg,
		RequireSigned: true,
		Runner:        runner,
	})
	if err != nil {
		testutil.FailErr(t, "verify bundle with dmg", err)
	}
	if report.Failed() {
		t.Fatalf("clean signed bundle with dmg failed: %+v", report.Findings)
	}

	assessed := map[string]string{}
	for _, call := range runner.callsFor("spctl") {
		target := call[len(call)-1]
		var assessType string
		for i, arg := range call {
			if arg == "--type" && i+1 < len(call) {
				assessType = call[i+1]
			}
		}
		assessed[target] = assessType
	}
	if assessed[app] != "exec" {
		t.Fatalf("spctl did not assess the .app as exec: %v", assessed)
	}
	if assessed[dmg] != "open" {
		t.Fatalf("spctl did not assess the dmg as open: %v", assessed)
	}

	staple := runner.callsFor("xcrun")
	if len(staple) != 1 || staple[0][len(staple[0])-1] != dmg {
		t.Fatalf("stapler must validate the dmg when one is provided: %v", staple)
	}
}

func TestVerifyBrowserEntitlementsMissing(t *testing.T) {
	runner := signedOK()
	runner.stdout["codesign -d"] = []byte(entitlementsEmpty)

	report := runVerify(t, buildFakeApp(t, defaultAppOptions()), true, runner)

	got := requireCodeCount(t, report, CodeBrowserEntitlementMissing, 2)
	seen := map[string]bool{}
	for _, f := range got {
		if f.Severity != SeverityError {
			t.Fatalf("BROWSER_ENTITLEMENT_MISSING severity = %q, want error under --require-signed", f.Severity)
		}
		if filepath.Base(f.Path) != "chrome-headless-shell" {
			t.Fatalf("finding on %q, want it scoped to the browser binary", f.Path)
		}
		seen[f.Detail["entitlement"]] = true
	}
	if !seen["com.apple.security.cs.allow-jit"] || !seen["com.apple.security.cs.allow-unsigned-executable-memory"] {
		t.Fatalf("findings do not name both required entitlements: %+v", got)
	}
	if !report.Failed() {
		t.Fatal("missing browser entitlements must fail the report under --require-signed")
	}
}

func TestVerifyBrowserEntitlementsMissingWarnsWithoutRequireSigned(t *testing.T) {
	runner := signedOK()
	runner.stdout["codesign -d"] = []byte(entitlementsEmpty)

	report := runVerify(t, buildFakeApp(t, defaultAppOptions()), false, runner)

	got := requireCodeCount(t, report, CodeBrowserEntitlementMissing, 2)
	for _, f := range got {
		if f.Severity != SeverityWarn {
			t.Fatalf("BROWSER_ENTITLEMENT_MISSING severity = %q, want warn without --require-signed", f.Severity)
		}
	}
}

func TestVerifyBrowserEntitlementsSkippedWithoutHardenedRuntime(t *testing.T) {
	runner := newFakeRunner()
	runner.stderr["codesign"] = []byte(codesignDeveloperIDNoRuntime)

	report := runVerify(t, buildFakeApp(t, defaultAppOptions()), false, runner)

	requireNoCode(t, report, CodeBrowserEntitlementMissing)
	if len(findingsWithCode(report, CodeHardenedRuntimeMissing)) == 0 {
		t.Fatal("expected HARDENED_RUNTIME_MISSING findings for a non-hardened bundle")
	}
}

func TestVerifyBrowserEntitlementsAbsentBrowser(t *testing.T) {
	opts := defaultAppOptions()
	opts.omitBrowser = true

	report := runVerify(t, buildFakeApp(t, opts), true, signedOK())

	requireNoCode(t, report, CodeBrowserEntitlementMissing)
}

func TestVerifyFatBinaryExtraSlice(t *testing.T) {
	opts := defaultAppOptions()
	opts.browserFat = true

	report := runVerify(t, buildFakeApp(t, opts), false, signedOK())

	requireNoCode(t, report, CodeArchUnexpected)
	got := requireCodeCount(t, report, CodeArchExtraSlice, 1)
	if got[0].Severity != SeverityWarn {
		t.Fatalf("ARCH_EXTRA_SLICE severity = %q, want warn", got[0].Severity)
	}
}

func TestVerifySkipsNonMachOFiles(t *testing.T) {
	opts := defaultAppOptions()
	opts.extraFiles = map[string][]byte{
		"Contents/Resources/icudtl.dat": []byte("\x00\x01binary payload, not a mach-o"),
		"Contents/Resources/app.js":     []byte("export const x = 1;"),
	}

	report := runVerify(t, buildFakeApp(t, opts), false, signedOK())

	if report.MachOCount != 6 {
		t.Fatalf("MachOCount = %d, want 6 — non-Mach-O files must not be counted", report.MachOCount)
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
