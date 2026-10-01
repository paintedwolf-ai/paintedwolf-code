package bundleverify

import (
	"context"
	"debug/macho"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

const fakeOpenGrepRelPath = engineRootRelPath + "/bundled/opengrep-1.29.0+paintedwolf.26/opengrep"

func writeFakeOpenGrep(t *testing.T, app string) {
	t.Helper()
	binary := filepath.Join(app, fakeOpenGrepRelPath)
	writeThinMachO(t, binary, macho.CpuArm64, "13.0", []string{"/usr/lib/libSystem.B.dylib"})
	for _, name := range []string{"opengrep-source.tar.gz", "source-lock.json", "provenance.json", "LICENSE"} {
		mustWriteFile(t, filepath.Join(filepath.Dir(binary), name), []byte("verified payload fixture\n"))
	}
}

func TestOpenGrepUsesPackagedSidecarAuthority(t *testing.T) {
	app := buildFakeApp(t, defaultAppOptions())
	runner := signedOK()
	report := runVerify(t, app, true, runner)
	requireNoCode(t, report, CodeOpenGrepInvalid)
	calls := runner.callsFor(filepath.Join(app, sidecarRelPath), "scan")
	if len(calls) != 1 || len(calls[0]) != 6 || strings.Join(calls[0][1:5], " ") != "scan engines verify-bundled --root" || calls[0][5] != filepath.Join(app, engineRootRelPath) {
		t.Fatalf("scanner verification did not use packaged authority: %v", calls)
	}
}

func TestOpenGrepRequiresBundledSourcePayload(t *testing.T) {
	for _, name := range []string{"opengrep", "opengrep-source.tar.gz", "source-lock.json", "provenance.json", "LICENSE"} {
		t.Run(name, func(t *testing.T) {
			app := buildFakeApp(t, defaultAppOptions())
			path := filepath.Join(filepath.Dir(filepath.Join(app, fakeOpenGrepRelPath)), name)
			testutil.FailErr(t, "remove scanner payload", os.Remove(path))
			report := runVerify(t, app, true, signedOK())
			requireCodeCount(t, report, CodeOpenGrepInvalid, 1)
		})
	}
}

func TestOpenGrepRejectsFailedAuthorityAndInvalidPaths(t *testing.T) {
	for _, name := range []string{"hash mismatch", "empty response", "outside root", "symlink payload"} {
		t.Run(name, func(t *testing.T) {
			app := buildFakeApp(t, defaultAppOptions())
			runner := signedOK()
			key := filepath.Join(app, sidecarRelPath)
			switch name {
			case "hash mismatch":
				runner.err[key] = errors.New("embedded digest does not match scanner bytes")
			case "empty response":
				runner.stdout[key] = nil
			case "outside root":
				runner.stdout[key] = []byte(filepath.Join(t.TempDir(), "opengrep"))
			case "symlink payload":
				path := filepath.Join(filepath.Dir(filepath.Join(app, fakeOpenGrepRelPath)), "LICENSE")
				testutil.FailErr(t, "remove source license", os.Remove(path))
				testutil.FailErr(t, "symlink source license", os.Symlink(filepath.Join(app, noticesRelPath), path))
			}
			report := runVerify(t, app, true, runner)
			requireCodeCount(t, report, CodeOpenGrepInvalid, 1)
		})
	}
}

func TestOpenGrepUnavailableAuthorityIsExplicit(t *testing.T) {
	app := buildFakeApp(t, defaultAppOptions())
	runner := signedOK()
	runner.err[filepath.Join(app, sidecarRelPath)] = &os.PathError{Op: "exec", Path: "pw", Err: syscall.ENOEXEC}
	for _, requireSigned := range []bool{false, true} {
		findings := checkOpenGrep(context.Background(), runner, Options{AppPath: app, RequireSigned: requireSigned})
		if len(findings) != 1 || findings[0].Code != CodeOpenGrepVerificationUnavailable || (findings[0].Severity == SeverityError) != requireSigned {
			t.Fatalf("unavailable authority must remain explicit: %+v", findings)
		}
	}
}

func TestOpenGrepRejectsEscapingResourceDirectory(t *testing.T) {
	app := buildFakeApp(t, defaultAppOptions())
	executable := filepath.Join(app, fakeOpenGrepRelPath)
	directory := filepath.Dir(executable)
	testutil.FailErr(t, "remove fixture scanner directory", os.RemoveAll(directory))
	testutil.FailErr(t, "link scanner outside resources", os.Symlink(t.TempDir(), directory))
	runner := signedOK()
	runner.stdout[filepath.Join(app, sidecarRelPath)] = []byte(executable + "\n")
	findings := checkOpenGrep(context.Background(), runner, Options{AppPath: app, RequireSigned: true})
	if len(findings) != 1 || findings[0].Code != CodeOpenGrepInvalid {
		t.Fatalf("escaping scanner directory must fail verification: %+v", findings)
	}
}

func TestOpenGrepExecutionRequiresAcceptedSignatures(t *testing.T) {
	for _, failure := range []string{"display", "broken seal", "adhoc", "runtime", "notarization", "gatekeeper", "missing tool"} {
		t.Run(failure, func(t *testing.T) {
			app := buildFakeApp(t, defaultAppOptions())
			runner := signedOK()
			switch failure {
			case "display":
				runner.err["codesign --display"] = errors.New("invalid signature")
			case "broken seal":
				runner.err["codesign --verify"] = errors.New("invalid seal")
			case "adhoc":
				runner.stderr["codesign"] = []byte("Signature=adhoc\nflags=0x10002(adhoc,runtime)\n")
			case "runtime":
				runner.stderr["codesign"] = []byte("Authority=Developer ID Application: Fixture\nflags=0x0(none)\n")
			case "notarization":
				runner.err["xcrun"] = errors.New("ticket missing")
			case "gatekeeper":
				runner.err["spctl"] = errors.New("assessment rejected")
			case "missing tool":
				runner.missing["codesign"] = true
			}
			report := runVerify(t, app, true, runner)
			requireCodeCount(t, report, CodeOpenGrepVerificationUnavailable, 1)
			if calls := runner.callsFor(filepath.Join(app, sidecarRelPath)); len(calls) != 0 {
				t.Fatalf("unverified sidecar was executed: %v", calls)
			}
		})
	}
}

func TestOpenGrepRunsAfterSigningChecks(t *testing.T) {
	for _, requireSigned := range []bool{false, true} {
		app := buildFakeApp(t, defaultAppOptions())
		runner := signedOK()
		if !requireSigned {
			runner.err["codesign --verify"] = errors.New("unsigned development bundle")
		}
		report := runVerify(t, app, requireSigned, runner)
		requireNoCode(t, report, CodeOpenGrepVerificationUnavailable)
		sidecar := filepath.Join(app, sidecarRelPath)
		if calls := runner.callsFor(sidecar, "scan"); len(calls) != 1 {
			t.Fatalf("scanner verification calls = %v, want one", calls)
		}
		probeStarted := false
		for _, call := range runner.calls {
			if call[0] == sidecar {
				probeStarted = true
			} else if probeStarted {
				t.Fatalf("sidecar must run after signature checks: %v", runner.calls)
			}
		}
	}
}
