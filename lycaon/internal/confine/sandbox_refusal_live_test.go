package confine_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	confinefixture "github.com/lycaon/lycaon/internal/testsetup/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// refusalsOf collects kernel reports for one confined action.
func refusalsOf(t *testing.T, self string, roots []string, name string, args ...string) (confine.SandboxRefusals, string) {
	t.Helper()
	c := &confine.Confinement{Roots: roots, Network: confine.NetworkDeny}
	lease, err := confine.BindAction(c, confine.EgressCommand{SessionID: "refusal-live", ToolName: "command", CommandLine: name})
	testutil.FailErr(t, "bind action", err)
	cmd, cleanup, err := confine.Command(context.Background(), self, name, args, *c)
	testutil.FailErr(t, "build confined command", err)
	out, _ := cmd.CombinedOutput()
	cleanup()
	refusals := lease.SettledRefusals(t.Context())
	lease.Close(t.Context())
	return refusals, string(out)
}

func findRefusal(refusals confine.SandboxRefusals, operation, target string) (confine.SandboxRefusal, bool) {
	for _, r := range refusals.Refusals {
		if r.Operation == operation && r.Target == target {
			return r, true
		}
	}
	return confine.SandboxRefusal{}, false
}

func TestKernelRefusalOfAWriteReachesItsAction(t *testing.T) {
	self := requireLiveBoundary(t)
	confinefixture.RequireRefusalReports(t)
	outside, err := filepath.EvalSymlinks(outsideTemporaryWriteRoots(t))
	testutil.FailErr(t, "resolve outside dir", err)
	target := filepath.Join(outside, "escape")

	refusals, out := refusalsOf(t, self, []string{t.TempDir()}, "/usr/bin/touch", target)
	if refusals.Witness != liveRefusalWitness {
		t.Fatalf("witness = %s, want %s", refusals.Witness, liveRefusalWitness)
	}
	if liveRefusalWitness != confine.WitnessKernel {
		t.Skip("Landlock has no stream of its own refusals; the floor's refusal below is what reaches the action")
	}
	refused, ok := findRefusal(refusals, "file-write-create", target)
	if !ok {
		t.Fatalf("the kernel's refusal of %s did not reach the action: %+v\noutput: %s", target, refusals.Refusals, out)
	}
	if refused.Recovery != confine.RecoverWriteRoot || refused.Grant != outside || refused.Process != "touch" {
		t.Fatalf("refusal = %+v, want write_root %s from touch", refused, outside)
	}
}

// A write a floor refuses reaches the action on every platform.
func TestFloorRefusalOfAWriteReachesItsAction(t *testing.T) {
	self := requireLiveBoundary(t)
	confinefixture.RequireRefusalReports(t)
	project, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve project", err)
	target := filepath.Join(project, "AGENTS.md")

	refusals, out := refusalsOf(t, self, liveBoundaryRoots(project), "/usr/bin/touch", target)
	if _, ok := findRefusal(refusals, "file-write-create", target); !ok {
		t.Fatalf("the floor's refusal of %s did not reach the action: %+v\noutput: %s", target, refusals.Refusals, out)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("the refused write landed: %v", err)
	}
}

func TestKernelRefusalOfAUnixListenerNeedsHostExecution(t *testing.T) {
	self := requireLiveBoundary(t)
	confinefixture.RequireRefusalReports(t)
	nc, err := exec.LookPath("nc")
	if err != nil {
		t.Skip("nc is required to open a unix-socket listener")
	}
	project, err := filepath.EvalSymlinks(shortTempDir(t))
	testutil.FailErr(t, "resolve project", err)
	socket := filepath.Join(project, "l.sock")

	refusals, out := refusalsOf(t, self, liveBoundaryRoots(project), nc, "-lU", socket)
	refused, ok := findRefusal(refusals, "network-bind", socket)
	if !ok {
		t.Fatalf("the refused listener did not reach the action: %+v\noutput: %s", refusals.Refusals, out)
	}
	if refused.Recovery != confine.RecoverHostExecution {
		t.Fatalf("refusal = %+v, want host_execution", refused)
	}
}

func TestDiskSpaceQueriesAreNotRefused(t *testing.T) {
	self := requireLiveBoundary(t)
	confinefixture.RequireRefusalReports(t)
	project, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve project", err)
	refusals, out := refusalsOf(t, self, []string{project}, "/bin/df", "-k", "/")
	if refusals.Witness != liveRefusalWitness || len(refusals.Refusals) > 0 {
		t.Fatalf("df witness = %s, refusals = %+v\noutput: %s", refusals.Witness, refusals.Refusals, out)
	}
}

func TestOrdinaryToolchainsStartWithoutRefusals(t *testing.T) {
	self := requireLiveBoundary(t)
	confinefixture.RequireRefusalReports(t)
	project, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve project", err)
	config := outsideTemporaryWriteRoots(t)
	t.Setenv("HOME", config)
	t.Setenv("XDG_CONFIG_HOME", config)
	userCfg, err := os.UserConfigDir()
	testutil.FailErr(t, "resolve user config dir", err)
	testutil.FailErr(t, "initialize toolchain state", os.MkdirAll(filepath.Join(userCfg, "go", "telemetry"), 0700))
	probes := [][]string{
		{"/bin/sh", "-c", "true"},
		{"/usr/bin/env", "true"},
		{"git", "--version"},
		{"python3", "-c", "pass"},
		{"node", "-e", "0"},
		{"go", "version"},
	}
	for _, probe := range probes {
		name, err := exec.LookPath(probe[0])
		if err != nil {
			continue
		}
		// Warm system shims (such as macOS xcrun wrappers) outside the sandbox first.
		_, _ = exec.Command(name, probe[1:]...).CombinedOutput()
		refusals, out := refusalsOf(t, self, []string{project}, name, probe[1:]...)
		if len(refusals.Refusals) > 0 {
			var lines []string
			for _, r := range refusals.Refusals {
				lines = append(lines, r.Display())
			}
			t.Errorf("%s started with refusals:\n%s\noutput: %s", strings.Join(probe, " "), strings.Join(lines, "\n"), out)
		}
	}
}

func TestSharedTempWriteRefusalNamesItsLocation(t *testing.T) {
	self := requireLiveBoundary(t)
	confinefixture.RequireRefusalReports(t)
	project, custom := t.TempDir(), outsideTemporaryWriteRoots(t)
	t.Setenv("TMPDIR", custom)
	for _, location := range []string{"/tmp", "/var/tmp", custom} {
		t.Run(filepath.Base(location), func(t *testing.T) {
			file, err := os.CreateTemp(location, "pw-temp-location-")
			testutil.FailErr(t, "seed temp sentinel", err)
			testutil.FailErr(t, "close temp sentinel", file.Close())
			t.Cleanup(func() { _ = os.Remove(file.Name()) })
			target := fspath.CanonicalPath(file.Name())
			refusals, out := refusalsOf(t, self, liveBoundaryRoots(project), "/bin/sh", "-c", `printf x > "$1"`, "sh", target)
			for _, refused := range refusals.Refusals {
				if refused.Target == target && refused.Layer == confine.FloorSharedTemp && refused.Recovery == confine.RecoverWriteRoot && refused.Grant == fspath.CanonicalPath(location) {
					info, err := os.Stat(target)
					testutil.FailErr(t, "inspect sentinel", err)
					if info.Size() != 0 {
						t.Fatal("refused write changed the file")
					}
					return
				}
			}
			t.Fatalf("shared temp write: refusals=%+v, output=%q", refusals, out)
		})
	}
}

func TestSharedTempReadAndWriteOnlyRefusalsReachAction(t *testing.T) {
	self := requireLiveBoundary(t)
	confinefixture.RequireRefusalReports(t)
	project := fspath.CanonicalPath(outsideTemporaryWriteRoots(t))
	custom := fspath.CanonicalPath(outsideTemporaryWriteRoots(t))
	t.Setenv("TMPDIR", custom)

	runConfined := func(c confine.Confinement, name string, args ...string) (int, string, confine.SandboxRefusals) {
		t.Helper()
		c.Roots = liveBoundaryRoots(c.Roots...)
		c.Network = confine.NetworkDeny
		lease, err := confine.BindAction(&c, confine.EgressCommand{SessionID: "shared-temp-live", ToolName: "command", CommandLine: name})
		testutil.FailErr(t, "bind action", err)
		cmd, cleanup, err := confine.Command(context.Background(), self, name, args, c)
		testutil.FailErr(t, "build confined command", err)
		out, _ := cmd.CombinedOutput()
		cleanup()
		refusals := lease.SettledRefusals(t.Context())
		lease.Close(t.Context())
		return cmd.ProcessState.ExitCode(), string(out), refusals
	}

	incoming := filepath.Join(custom, "incoming.csv")
	sibling := filepath.Join(custom, "sibling.csv")
	exportDir := filepath.Join(custom, "export")
	exportExisting := filepath.Join(exportDir, "existing.csv")
	testutil.FailErr(t, "mkdir export", os.MkdirAll(exportDir, 0o700))
	for path, body := range map[string]string{incoming: "alpha", sibling: "beta", exportExisting: "secret-export"} {
		testutil.FailErr(t, "write "+path, os.WriteFile(path, []byte(body), 0o600))
	}

	// Ungranted read and directory listing are denied and observed with RecoverReadPath.
	code, out, refusals := runConfined(confine.Confinement{Roots: []string{project}}, "/bin/cat", incoming)
	if code == 0 || strings.Contains(out, "alpha") {
		t.Fatalf("ungranted shared temp read succeeded: exit=%d out=%q", code, out)
	}
	readRefusal, ok := findRefusal(refusals, "file-read-data", incoming)
	if !ok || readRefusal.Layer != confine.FloorSharedTemp || readRefusal.Recovery != confine.RecoverReadPath || readRefusal.Grant != incoming {
		t.Fatalf("ungranted read refusal = %+v (all=%+v)", readRefusal, refusals.Refusals)
	}

	code, out, refusals = runConfined(confine.Confinement{Roots: []string{project}}, "/bin/ls", custom)
	if code == 0 || strings.Contains(out, "incoming.csv") {
		t.Fatalf("ungranted shared temp listing succeeded: exit=%d out=%q", code, out)
	}
	listRefusal, ok := findRefusal(refusals, "file-read-data", custom)
	if !ok || listRefusal.Layer != confine.FloorSharedTemp || listRefusal.Recovery != confine.RecoverReadPath || listRefusal.Grant != custom {
		t.Fatalf("ungranted list refusal = %+v (all=%+v)", listRefusal, refusals.Refusals)
	}

	// Exact file read grant admits only incoming.csv; sibling and directory listing stay denied.
	withExactRead := confine.Confinement{
		Roots:               []string{project},
		ProtectedReadGrants: []confine.ProtectedPathGrant{confine.NewProtectedPathGrant(incoming)},
	}
	code, out, refusals = runConfined(withExactRead, "/bin/cat", incoming)
	if code != 0 || out != "alpha" || len(refusals.Refusals) != 0 {
		t.Fatalf("exact input read failed: exit=%d out=%q refusals=%+v", code, out, refusals.Refusals)
	}
	if code, out, _ = runConfined(withExactRead, "/bin/cat", sibling); code == 0 || strings.Contains(out, "beta") {
		t.Fatalf("exact input grant leaked sibling: exit=%d out=%q", code, out)
	}
	if code, out, _ = runConfined(withExactRead, "/bin/ls", custom); code == 0 || strings.Contains(out, "sibling.csv") {
		t.Fatalf("exact input grant leaked parent listing: exit=%d out=%q", code, out)
	}

	// Write-only directory grant permits creating/replacing files while keeping reads and listing denied.
	withWriteOnly := confine.Confinement{
		Roots:             []string{project},
		GrantedWriteRoots: []string{exportDir},
	}
	created := filepath.Join(exportDir, "new.csv")
	code, out, refusals = runConfined(withWriteOnly, "/bin/sh", "-c", `printf created > "$1"`, "sh", created)
	if code != 0 || len(refusals.Refusals) != 0 {
		t.Fatalf("write-only create failed: exit=%d out=%q refusals=%+v", code, out, refusals.Refusals)
	}
	gotBytes, err := os.ReadFile(created)
	testutil.FailErr(t, "read host created output", err)
	if string(gotBytes) != "created" {
		t.Fatalf("created bytes = %q, want %q", gotBytes, "created")
	}
	code, out, refusals = runConfined(withWriteOnly, "/bin/cat", exportExisting)
	if code == 0 || strings.Contains(out, "secret-export") {
		t.Fatalf("write-only grant leaked file read: exit=%d out=%q", code, out)
	}
	if r, ok := findRefusal(refusals, "file-read-data", exportExisting); !ok || r.Recovery != confine.RecoverReadPath {
		t.Fatalf("write-only read refusal missing: %+v", refusals.Refusals)
	}
	code, out, refusals = runConfined(withWriteOnly, "/bin/ls", exportDir)
	if code == 0 || strings.Contains(out, "existing.csv") {
		t.Fatalf("write-only grant leaked directory listing: exit=%d out=%q", code, out)
	}
	if r, ok := findRefusal(refusals, "file-read-data", exportDir); !ok || r.Recovery != confine.RecoverReadPath {
		t.Fatalf("write-only list refusal missing: %+v", refusals.Refusals)
	}
}
