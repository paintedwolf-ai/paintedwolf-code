//go:build linux

package launchguard

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/packagegeneration"
	"github.com/lycaon/lycaon/internal/testutil"
)

const diagGlycinModeEnv = "PW_DIAG_GLYCIN_MODE"

// TestDiagGlycinLoaderExec reproduces the glycin-svg loader spawn with visible
// stderr to diagnose the amd64 platform launchguard failure. Temporary
// diagnostic; remove before the fix lands.
func TestDiagGlycinLoaderExec(t *testing.T) {
	mode := os.Getenv(diagGlycinModeEnv)
	if mode == "" {
		diagGlycinParent(t)
		return
	}
	diagGlycinChild(t, mode)
}

func diagGlycinLoaderPath(t *testing.T) string {
	loader := "/usr/libexec/glycin-loaders/2+/glycin-svg"
	if _, err := os.Stat(loader); err != nil {
		t.Skipf("glycin-svg loader absent on this host: %v", err)
	}
	return loader
}

func diagGlycinBwrapCommand(t *testing.T, loader string) *exec.Cmd {
	cache := t.TempDir()
	command := exec.Command("bwrap",
		"--unshare-all", "--die-with-parent", "--chdir", "/",
		"--ro-bind", "/usr", "/usr",
		"--dev", "/dev",
		"--ro-bind-try", "/etc/ld.so.cache", "/etc/ld.so.cache",
		"--bind-try", cache, cache,
		"--tmpfs", "/tmp-home",
		"--tmpfs", "/tmp-run",
		"--clearenv",
		"--setenv", "HOME", "/tmp-home",
		"--setenv", "XDG_RUNTIME_DIR", "/tmp-run",
		"--setenv", "XDG_CACHE_HOME", cache,
		loader,
	)
	command.Env = nil
	return command
}

func diagGlycinRun(t *testing.T, label string, command *exec.Cmd) int {
	output, err := command.CombinedOutput()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			t.Logf("%s: spawn failure: %v", label, err)
			return -1
		}
	}
	t.Logf("%s: exit %d output:\n%s", label, code, output)
	return code
}

func diagGlycinParent(t *testing.T) {
	loader := diagGlycinLoaderPath(t)
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skipf("bwrap absent: %v", err)
	}

	// Report which catalog executables resolve on this host: the fixture
	// staging silently skips paths that resolve to os.ErrNotExist.
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "packaging", "linux", "protected-launch.json"))
	testutil.FailErr(t, "read protected launch catalog", err)
	var catalog struct {
		Architectures map[string]struct {
			Multiarch string `json:"multiarch"`
		} `json:"architectures"`
		Families map[string]struct {
			Executables []string `json:"system_executables"`
		} `json:"families"`
	}
	testutil.FailErr(t, "parse protected launch catalog", json.Unmarshal(raw, &catalog))
	for _, template := range catalog.Families["deb"].Executables {
		path := strings.ReplaceAll(template, "{multiarch}", catalog.Architectures[runtime.GOARCH].Multiarch)
		resolved, err := packagegeneration.ResolveSystemExecutable(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			t.Logf("catalog %s: SKIPPED (ErrNotExist)", path)
		case err != nil:
			t.Logf("catalog %s: resolve error: %v", path, err)
		default:
			t.Logf("catalog %s: resolved %s", path, resolved)
		}
	}

	// Plain: no Landlock, no seccomp. The loader exits 2 on its own when the
	// exec succeeds (missing --dbus-fd); bwrap exits 1 with a die message when
	// sandbox setup or the exec fails.
	diagGlycinRun(t, "plain", diagGlycinBwrapCommand(t, loader))

	// Plain with an allow-all seccomp filter to exercise bwrap's --seccomp
	// application path.
	diagGlycinRun(t, "plain-seccomp", diagGlycinSeccompAllowAll(t, diagGlycinBwrapCommand(t, loader)))

	// Restricted: the protected execution policy grants execute only on the
	// named paths. With the loader granted the exec must succeed; without the
	// grant bwrap must die with EACCES (exit 1), reproducing the failure.
	for _, mode := range []string{"restricted-grant", "restricted-nogrant"} {
		command := exec.Command(os.Args[0], "-test.run", "^TestDiagGlycinLoaderExec$", "-test.v")
		command.Env = append(os.Environ(), diagGlycinModeEnv+"="+mode)
		diagGlycinRun(t, mode, command)
	}
}

func diagGlycinChild(t *testing.T, mode string) {
	loader := diagGlycinLoaderPath(t)
	exe, err := os.Open("/proc/self/exe")
	testutil.FailErr(t, "open self as image", err)
	defer func() { _ = exe.Close() }()
	policy := GenerationPolicy{LogicalInstallRoot: "/usr/bin"}
	if mode == "restricted-grant" {
		policy.SystemExecutables = []string{loader}
	}
	if err := restrictExecution(policy, exe); err != nil {
		t.Fatalf("restrict: %v", err)
	}
	diagGlycinRun(t, mode, diagGlycinBwrapCommand(t, loader))
}

// diagGlycinSeccompAllowAll writes a one-instruction allow-all BPF filter and
// wires it as bwrap's --seccomp fd, exercising bwrap's seccomp application
// path without glycin's real filter.
func diagGlycinSeccompAllowAll(t *testing.T, command *exec.Cmd) *exec.Cmd {
	filterFile, err := os.CreateTemp(t.TempDir(), "diag-seccomp")
	testutil.FailErr(t, "create seccomp filter file", err)
	// BPF_RET|BPF_K with SECCOMP_RET_ALLOW: code 0x06, k 0x7fff0000.
	insn := []byte{0x06, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff, 0x7f}
	if _, err := filterFile.Write(insn); err != nil {
		t.Fatalf("write seccomp filter: %v", err)
	}
	if _, err := filterFile.Seek(0, 0); err != nil {
		t.Fatalf("seek seccomp filter: %v", err)
	}
	command.ExtraFiles = append(command.ExtraFiles, filterFile)
	seccompFD := strconv.Itoa(3 + len(command.ExtraFiles) - 1)
	loader := command.Args[len(command.Args)-1]
	command.Args = append(command.Args[:len(command.Args)-1], "--seccomp", seccompFD, loader)
	return command
}
