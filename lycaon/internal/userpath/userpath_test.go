package userpath

import (
	"context"
	"errors"
	"io/fs"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func testConfig() Config {
	return Config{
		Probe: ProbeConfig{
			Timeout:        time.Second,
			MaxOutputBytes: 4096,
			MaxEntries:     16,
			Marker:         "__marker__",
			EnvAllowlist:   []string{"HOME", "PATH"},
		},
		Shells: []ShellInvocation{{
			Names:   []string{"zsh", "bash"},
			Args:    []string{"-lc"},
			Command: `printf "%s\n%s\n%s\n" "{marker}" "$PATH" "{marker}"`,
		}},
		Fallback: []string{"/usr/bin", "/bin"},
	}
}

type fakeShell struct {
	stdout string
	err    error
}

func providerWith(t *testing.T, cfg Config, shell string, env map[string]string, shellOut fakeShell) *Provider {
	t.Helper()
	p := NewProvider(cfg)
	p.accountShell = func(context.Context) (string, error) {
		return "", errors.New("account lookup unavailable")
	}
	p.lookupEnv = func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}
	p.environ = func() []string {
		out := make([]string, 0, len(env))
		for k, v := range env {
			out = append(out, k+"="+v)
		}
		return out
	}
	p.stat = func(path string) (fs.FileInfo, error) {
		if path != shell {
			return nil, os.ErrNotExist
		}
		return executableStub{name: filepath.Base(path)}, nil
	}
	p.run = func(cmd *osexec.Cmd) error {
		if cmd.Stdout != nil && shellOut.stdout != "" {
			if _, err := cmd.Stdout.Write([]byte(shellOut.stdout)); err != nil {
				return err
			}
		}
		return shellOut.err
	}
	return p
}

func TestProbeUsesAccountShellWhenGUIEnvironmentHasNoShell(t *testing.T) {
	cfg := testConfig()
	cfg.Probe.EnvAllowlist = append(cfg.Probe.EnvAllowlist, "SHELL")
	env := map[string]string{"PATH": "/usr/bin", "HOME": "/Users/x"}
	p := providerWith(t, cfg, "/opt/homebrew/bin/zsh", env, fakeShell{
		stdout: framed(cfg.Probe.Marker, "/opt/homebrew/bin:/usr/bin:/bin"),
	})
	p.accountShell = func(context.Context) (string, error) {
		return "/opt/homebrew/bin/zsh", nil
	}
	var childShell string
	inner := p.run
	p.run = func(cmd *osexec.Cmd) error {
		for _, entry := range cmd.Env {
			if strings.HasPrefix(entry, "SHELL=") {
				childShell = strings.TrimPrefix(entry, "SHELL=")
			}
		}
		return inner(cmd)
	}

	snap := p.Resolve(context.Background())
	if snap.Source() != SourceProbe || snap.Failure() != FailureNone {
		t.Fatalf("source/failure = %q/%q (%s), want account login shell", snap.Source(), snap.Failure(), snap.Reason())
	}
	if childShell != "/opt/homebrew/bin/zsh" {
		t.Fatalf("probe SHELL = %q, want account shell", childShell)
	}
}

type executableStub struct{ name string }

func (e executableStub) Name() string       { return e.name }
func (e executableStub) Size() int64        { return 0 }
func (e executableStub) Mode() fs.FileMode  { return 0o755 }
func (e executableStub) ModTime() time.Time { return time.Time{} }
func (e executableStub) IsDir() bool        { return false }
func (e executableStub) Sys() any           { return nil }

func framed(marker string, value string) string {
	return "some rc banner\n" + marker + "\n" + value + "\n" + marker + "\nversion manager notice\n"
}

func TestProbeAnswersFromLoginShell(t *testing.T) {
	cfg := testConfig()
	env := map[string]string{"SHELL": "/bin/zsh", "PATH": "/usr/bin", "HOME": "/Users/x"}
	p := providerWith(t, cfg, "/bin/zsh", env, fakeShell{
		stdout: framed(cfg.Probe.Marker, "/opt/homebrew/bin:/usr/bin:/bin"),
	})

	snap := p.Resolve(context.Background())
	if snap.Source() != SourceProbe {
		t.Fatalf("source = %q (%s), want the login shell to answer", snap.Source(), snap.Reason())
	}
	want := []string{"/opt/homebrew/bin", "/usr/bin", "/bin"}
	if got := snap.Entries(); strings.Join(got, ":") != strings.Join(want, ":") {
		t.Fatalf("entries = %v, want %v", got, want)
	}
}

func TestProbeReadsOnlyFramedValue(t *testing.T) {
	cfg := testConfig()
	env := map[string]string{"SHELL": "/bin/zsh", "PATH": "/usr/bin"}
	noisy := "/tmp/attacker\n" + cfg.Probe.Marker + "\n/opt/real\n" + cfg.Probe.Marker + "\n/tmp/after\n"
	p := providerWith(t, cfg, "/bin/zsh", env, fakeShell{stdout: noisy})

	entries := p.Resolve(context.Background()).Entries()
	if strings.Join(entries, ":") != "/opt/real" {
		t.Fatalf("entries = %v, want only the framed value", entries)
	}
}

func TestProbeCaptureIsBoundedWhileShellRuns(t *testing.T) {
	cfg := testConfig()
	cfg.Probe.MaxOutputBytes = 32
	env := map[string]string{"SHELL": "/bin/zsh", "PATH": "/usr/bin"}
	p := providerWith(t, cfg, "/bin/zsh", env, fakeShell{
		stdout: strings.Repeat("x", cfg.Probe.MaxOutputBytes+4096),
	})

	snap := p.Resolve(context.Background())
	if snap.Source() != SourceInherited || snap.Failure() != FailureProbeFailed {
		t.Fatalf("source/failure = %q/%q, want bounded probe fallback", snap.Source(), snap.Failure())
	}
	if !strings.Contains(snap.Reason(), "wrote more than 32 bytes") {
		t.Fatalf("reason = %q, want capture limit", snap.Reason())
	}
}

func TestProbeFallsBackWhenShellIsUnusable(t *testing.T) {
	cases := []struct {
		name  string
		env   map[string]string
		shell string
		out   fakeShell
	}{
		{"no SHELL", map[string]string{"PATH": "/usr/bin"}, "", fakeShell{}},
		{"relative SHELL", map[string]string{"SHELL": "zsh", "PATH": "/usr/bin"}, "", fakeShell{}},
		{"unknown shell", map[string]string{"SHELL": "/bin/exoticsh", "PATH": "/usr/bin"}, "/bin/exoticsh", fakeShell{}},
		{"shell exits nonzero", map[string]string{"SHELL": "/bin/zsh", "PATH": "/usr/bin"}, "/bin/zsh", fakeShell{err: errors.New("exit 1")}},
		{"no marker", map[string]string{"SHELL": "/bin/zsh", "PATH": "/usr/bin"}, "/bin/zsh", fakeShell{stdout: "/opt/homebrew/bin\n"}},
		{"cut off before closing marker", map[string]string{"SHELL": "/bin/zsh", "PATH": "/usr/bin"}, "/bin/zsh", fakeShell{stdout: "__marker__\n/opt/x\n"}},
		{"framed value has no absolute entry", map[string]string{"SHELL": "/bin/zsh", "PATH": "/usr/bin"}, "/bin/zsh", fakeShell{stdout: framed("__marker__", "relative:./also")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := providerWith(t, testConfig(), tc.shell, tc.env, tc.out)
			snap := p.Resolve(context.Background())
			if snap.Source() != SourceInherited {
				t.Fatalf("source = %q, want the inherited PATH", snap.Source())
			}
			if snap.Reason() == "" {
				t.Fatal("a non-probe source must say why; silence here reads as a successful probe")
			}
			if snap.Failure() == FailureNone {
				t.Fatal("a non-probe source must carry a structured failure")
			}
			if strings.Join(snap.Entries(), ":") != "/usr/bin" {
				t.Fatalf("entries = %v, want the inherited value", snap.Entries())
			}
		})
	}
}

func TestFallsBackToCatalogWhenInheritedPathIsUnusable(t *testing.T) {
	env := map[string]string{"PATH": "relative:"}
	p := providerWith(t, testConfig(), "", env, fakeShell{})

	snap := p.Resolve(context.Background())
	if snap.Source() != SourceFallback {
		t.Fatalf("source = %q, want the catalog fallback", snap.Source())
	}
	if strings.Join(snap.Entries(), ":") != "/usr/bin:/bin" {
		t.Fatalf("entries = %v, want the catalog fallback", snap.Entries())
	}
}

func TestSnapshotIsFixedForTheProcess(t *testing.T) {
	cfg := testConfig()
	env := map[string]string{"SHELL": "/bin/zsh", "PATH": "/usr/bin"}
	calls := 0
	p := providerWith(t, cfg, "/bin/zsh", env, fakeShell{})
	p.run = func(cmd *osexec.Cmd) error {
		calls++
		if calls == 1 {
			return errors.New("first probe fails")
		}
		_, err := cmd.Stdout.Write([]byte(framed(cfg.Probe.Marker, "/opt/homebrew/bin")))
		return err
	}

	first := p.Resolve(context.Background())
	second := p.Resolve(context.Background())
	if first.Source() != SourceInherited || second.Source() != first.Source() {
		t.Fatalf("first=%q second=%q, want one fixed answer", first.Source(), second.Source())
	}
	if calls != 1 {
		t.Fatalf("probe ran %d times, want exactly one", calls)
	}
}

func TestProbeChildSeesOnlyAllowlistedEnv(t *testing.T) {
	cfg := testConfig()
	env := map[string]string{
		"SHELL": "/bin/zsh", "PATH": "/usr/bin", "HOME": "/Users/x",
		"LYCAON_API_TOKEN": "bearer", "AWS_SECRET_ACCESS_KEY": "secret",
	}
	var childEnv []string
	p := providerWith(t, cfg, "/bin/zsh", env, fakeShell{stdout: framed(cfg.Probe.Marker, "/usr/bin")})
	inner := p.run
	p.run = func(cmd *osexec.Cmd) error {
		childEnv = append([]string(nil), cmd.Env...)
		return inner(cmd)
	}
	p.Resolve(context.Background())

	for _, entry := range childEnv {
		key, _, _ := strings.Cut(entry, "=")
		if key != "HOME" && key != "PATH" {
			t.Errorf("probe child received %q; only allowlisted keys may reach a shell profile", key)
		}
	}
	if len(childEnv) != 2 {
		t.Fatalf("child env = %v, want exactly the allowlisted pair", childEnv)
	}
}

func TestParseEntriesRejectsUnusableEntries(t *testing.T) {
	raw := strings.Join([]string{
		"/usr/bin", "", "relative/dir", ".", "/usr/bin", "/opt/x\x00", "/opt/y",
	}, string(filepath.ListSeparator))

	got := parseEntries(raw, 16)
	if strings.Join(got, ":") != "/usr/bin:/opt/y" {
		t.Fatalf("entries = %v; empty, relative, NUL-bearing and duplicate entries must all be dropped", got)
	}
}

func TestParseEntriesHonoursTheEntryCap(t *testing.T) {
	raw := strings.Join([]string{"/a", "/b", "/c", "/d"}, string(filepath.ListSeparator))
	if got := parseEntries(raw, 2); len(got) != 2 {
		t.Fatalf("entries = %v, want the cap applied", got)
	}
}

func TestWithAppendedNeverDuplicatesOrReorders(t *testing.T) {
	base := Snapshot{entries: []string{"/usr/bin", "/bin"}, source: SourceProbe}
	got := base.WithAppended("/usr/bin/", "/opt/vendor", "", "/opt/vendor")

	if strings.Join(got.Entries(), ":") != "/usr/bin:/bin:/opt/vendor" {
		t.Fatalf("entries = %v, want one appended directory and no reordering", got.Entries())
	}
	if strings.Join(base.Entries(), ":") != "/usr/bin:/bin" {
		t.Fatal("WithAppended mutated the receiver; the snapshot has to stay a value")
	}
}

func TestBundledCatalogLoads(t *testing.T) {
	cfg, err := LoadConfig()
	testutil.FailErr(t, "LoadConfig", err)

	if _, known := cfg.invocationFor("zsh"); !known {
		t.Error("zsh is not in the shipped shell table; it is the macOS default login shell")
	}
	if _, known := cfg.invocationFor("exoticsh"); known {
		t.Error("an unknown interpreter must not resolve to an invocation")
	}
}

func TestConfigRejectsACommandThatCannotBeFramed(t *testing.T) {
	cfg := testConfig()
	cfg.Shells[0].Command = `printf "%s\n" "$PATH"`
	if err := validateConfig(cfg); !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("err = %v, want a command without the marker to be refused", err)
	}
}
