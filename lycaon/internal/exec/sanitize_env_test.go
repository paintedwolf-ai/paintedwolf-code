package exec

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSanitizeEnvironStripsInjectionKeys(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"LD_PRELOAD=/evil.so",
		"DYLD_INSERT_LIBRARIES=/evil.dylib",
		"GIT_EXEC_PATH=/tmp/hook",
		"NODE_OPTIONS=--require /evil",
		"BASH_ENV=/tmp/bashrc",
		"HOME=/home/user",
	}
	got := SanitizeEnviron(in)
	want := []string{"PATH=/usr/bin", "HOME=/home/user"}
	if !slices.Equal(got, want) {
		t.Fatalf("SanitizeEnviron() = %v, want %v", got, want)
	}
}

func TestReducedEnvironDropsAmbientCredentials(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("GITHUB_TOKEN", "secret")
	t.Setenv("LANG", "en_US.UTF-8")
	got := ReducedEnviron()
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "AWS_SECRET_ACCESS_KEY=") || strings.Contains(joined, "GITHUB_TOKEN=") {
		t.Fatalf("ReducedEnviron retained ambient credential: %v", got)
	}
	if !strings.Contains(joined, "LANG=en_US.UTF-8") || !strings.Contains(joined, "PATH=") {
		t.Fatalf("ReducedEnviron dropped process plumbing: %v", got)
	}
}

func TestLocalGitEnvStripsInheritedGitKeepsExtras(t *testing.T) {
	t.Setenv("GIT_DIR", "/inherited/hook/state")
	t.Setenv("LD_PRELOAD", "/evil.so")
	got := LocalGitEnv("GIT_CONFIG_GLOBAL=/dev/null", "EXTRA=1")
	if slices.Contains(got, "GIT_DIR=/inherited/hook/state") {
		t.Fatalf("inherited GIT_DIR leaked into %v", got)
	}
	if slices.Contains(got, "LD_PRELOAD=/evil.so") {
		t.Fatalf("inherited LD_PRELOAD leaked into %v", got)
	}
	if !slices.Contains(got, "GIT_CONFIG_GLOBAL=/dev/null") {
		t.Fatalf("explicit GIT_CONFIG_GLOBAL extra dropped from %v", got)
	}
	if !slices.Contains(got, "EXTRA=1") {
		t.Fatalf("expected EXTRA=1 in %v", got)
	}
}

func TestRunUsesSanitizedInheritedEnv(t *testing.T) {
	t.Setenv("LD_PRELOAD", "/should-not-appear")
	t.Setenv("PW_TEST_SANITIZE_PROBE", "ok")
	out, code, err := Run(t.Context(), "printenv", []string{"PW_TEST_SANITIZE_PROBE"}, ExecOpts{Launch: HostLaunch("exec test"),
		Timeout:        5 * time.Second,
		MaxOutputBytes: 256,
	})
	if err != nil || code != 0 {
		t.Fatalf("printenv probe: err=%v code=%d", err, code)
	}
	if !strings.Contains(string(out), "ok") {
		t.Fatalf("probe missing from output %q", out)
	}
	out, _, err = Run(t.Context(), "printenv", []string{"LD_PRELOAD"}, ExecOpts{Launch: HostLaunch("exec test"),
		Timeout:        5 * time.Second,
		MaxOutputBytes: 256,
	})
	if err != nil {
		t.Fatalf("printenv LD_PRELOAD: err=%v", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("LD_PRELOAD leaked to child: %q", out)
	}
}

// The engine's own namespace holds this run's state — capture paths, control
// sockets, a boot's debug directory. A child that receives it keys its build
// cache on values that change every restart.
func TestChildrenDoNotReceiveTheEngineNamespace(t *testing.T) {
	t.Setenv("LYCAON_DEBUG_SESSION_DIR", "/tmp/this-boot")
	out, _, err := Run(t.Context(), "printenv", []string{"LYCAON_DEBUG_SESSION_DIR"}, ExecOpts{
		Launch:         HostLaunch("exec test"),
		Timeout:        5 * time.Second,
		MaxOutputBytes: 256,
	})
	if err != nil {
		t.Fatalf("printenv: %v", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("engine state reached a child: %q", out)
	}
}

// os/exec reads a nil Env as "inherit the parent's environment", so only a nil
// input may produce nil.
func TestSanitizeEnvironPreservesEmptiness(t *testing.T) {
	if got := SanitizeEnviron(nil); got != nil {
		t.Fatalf("SanitizeEnviron(nil) = %v, want nil", got)
	}
	got := SanitizeEnviron([]string{})
	if got == nil {
		t.Fatal("SanitizeEnviron([]) returned nil: an empty environment would inherit the parent's")
	}
	if len(got) != 0 {
		t.Fatalf("SanitizeEnviron([]) = %v, want empty", got)
	}
	// Every entry filtered out is also an empty environment, not an absent one.
	blocked := SanitizeEnviron([]string{"DYLD_INSERT_LIBRARIES=/evil.dylib"})
	if blocked == nil {
		t.Fatal("an environment whose entries were all blocked returned nil: the child would inherit the parent's")
	}
	if len(blocked) != 0 {
		t.Fatalf("blocked entry survived: %v", blocked)
	}
}

func TestReducedEnvironCanonicalizesTmpdir(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	got := ReducedEnviron()
	found := false
	for _, entry := range got {
		if strings.HasPrefix(entry, "TMPDIR=") {
			found = true
			val := strings.TrimPrefix(entry, "TMPDIR=")
			if val == "" {
				t.Fatalf("TMPDIR is empty in ReducedEnviron: %v", got)
			}
			break
		}
	}
	if !found {
		t.Fatalf("TMPDIR missing from ReducedEnviron: %v", got)
	}
}
