package preflight

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/platformfloor"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/userpath"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	os.Exit(m.Run())
}

func run(t *testing.T, p Probe, env Env) Result {
	t.Helper()
	res := p.Run(context.Background(), env)
	res.ID = p.ID()
	return res
}

func TestCommandPathProbe(t *testing.T) {
	if got := run(t, commandPathProbe{}, Env{UserPathSource: userpath.SourceConfigured}); got.Status != StatusOK {
		t.Fatalf("configured path status = %q, want ok", got.Status)
	}
	if got := run(t, commandPathProbe{}, Env{UserPathSource: userpath.SourceProbe}); got.Status != StatusOK {
		t.Fatalf("login-shell path status = %q, want ok", got.Status)
	}
	got := run(t, commandPathProbe{}, Env{
		UserPathSource:  userpath.SourceInherited,
		UserPathFailure: userpath.FailureShellUnavailable,
	})
	if got.Status != StatusDegraded || got.Code != CodeCommandPathLimited {
		t.Fatalf("got %q/%q, want degraded/%s", got.Status, got.Code, CodeCommandPathLimited)
	}
	if got.Detail["source"] != "inherited" || got.Detail["reason"] != "shell_unavailable" {
		t.Fatalf("detail = %v", got.Detail)
	}
}

func TestOSVersionProbe(t *testing.T) {
	cases := []struct {
		name     string
		version  string
		err      error
		want     Status
		wantCode string
	}{
		{name: "at the floor", version: platformfloor.MacOSMin(), want: StatusOK},
		{name: "above the floor", version: "15.5", want: StatusOK},
		{name: "far above the floor", version: "26.1", want: StatusOK},
		{name: "below the floor", version: "13.9", want: StatusBlocked, wantCode: CodeOSBelowFloor},
		{name: "single digit major", version: "9.0", want: StatusBlocked, wantCode: CodeOSBelowFloor},
		{name: "sysctl unreadable", err: errors.New("sysctl failed"), want: StatusDegraded, wantCode: CodeOSBelowFloor},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := run(t, osVersionProbe{}, Env{
				OSProductVer: func() (string, error) { return tc.version, tc.err },
			})
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q (version %q)", got.Status, tc.want, tc.version)
			}
			if got.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", got.Code, tc.wantCode)
			}
		})
	}
}

func TestConfigDirProbe(t *testing.T) {
	t.Run("writable", func(t *testing.T) {
		got := run(t, configDirProbe{}, Env{ConfigDir: filepath.Join(t.TempDir(), "lycaon")})
		if got.Status != StatusOK {
			t.Fatalf("status = %q, want ok (%v)", got.Status, got.Detail)
		}
	})

	t.Run("unresolved", func(t *testing.T) {
		got := run(t, configDirProbe{}, Env{ConfigDir: ""})
		if got.Status != StatusBlocked || got.Code != CodeConfigDirUnwritable {
			t.Fatalf("got %q/%q, want blocked/%s", got.Status, got.Code, CodeConfigDirUnwritable)
		}
	})

	t.Run("read only", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root defeats the mode bits this case relies on")
		}
		parent := t.TempDir()
		if err := os.Chmod(parent, 0o500); err != nil {
			testutil.FailErr(t, "chmod parent read-only", err)
		}
		t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

		got := run(t, configDirProbe{}, Env{ConfigDir: filepath.Join(parent, "lycaon")})
		if got.Status != StatusBlocked || got.Code != CodeConfigDirUnwritable {
			t.Fatalf("got %q/%q, want blocked/%s", got.Status, got.Code, CodeConfigDirUnwritable)
		}
	})
}

// File-opening probes share the descriptor-exhaustion condition.
func TestHostFileLimitClassification(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantCode   string
		wantReason string
	}{
		{
			name:       "process limit",
			err:        &fs.PathError{Op: "open", Path: "config", Err: syscall.EMFILE},
			wantCode:   CodeHostFileLimitReached,
			wantReason: ReasonFileLimitProcess,
		},
		{
			name:       "system limit",
			err:        fmt.Errorf("load manifest: %w", &fs.PathError{Op: "open", Path: "m", Err: syscall.ENFILE}),
			wantCode:   CodeHostFileLimitReached,
			wantReason: ReasonFileLimitSystem,
		},
		// A real permissions failure must keep reaching its own condition.
		{name: "permission denied", err: &fs.PathError{Op: "open", Path: "config", Err: syscall.EACCES}},
		{name: "plain error", err: errors.New("no such manifest")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := hostFileLimitResult(tc.err)
			if tc.wantCode == "" {
				if ok {
					t.Fatalf("classified %v as %q; only EMFILE/ENFILE are the file limit", tc.err, got.Code)
				}
				return
			}
			if !ok {
				t.Fatalf("did not classify %v as the file limit", tc.err)
			}
			if got.Code != tc.wantCode || got.Status != StatusBlocked {
				t.Fatalf("got %q/%q, want blocked/%s", got.Status, got.Code, tc.wantCode)
			}
			if got.Detail["reason"] != tc.wantReason {
				t.Fatalf("reason = %q, want %q", got.Detail["reason"], tc.wantReason)
			}
		})
	}
}

// The scanner resolver classifies every failure as a missing engine, whose copy
// tells the user to reinstall the app. Out of descriptors, nothing is missing.
func TestScannerEngineProbeReportsFileLimitNotMissingEngine(t *testing.T) {
	got := run(t, scannerEngineProbe{}, Env{
		ResolveScanner: func() (string, string, error) {
			return "", "not_found", &fs.PathError{Op: "open", Path: "manifest.yaml", Err: syscall.EMFILE}
		},
	})
	if got.Code != CodeHostFileLimitReached {
		t.Fatalf("code = %q, want %s — a descriptor limit is not a missing engine", got.Code, CodeHostFileLimitReached)
	}
}

func TestDiskSpaceProbe(t *testing.T) {
	cases := []struct {
		name string
		free uint64
		err  error
		want Status
	}{
		{name: "plenty", free: 50 << 30, want: StatusOK},
		{name: "low", free: 1 << 30, want: StatusDegraded},
		{name: "exactly at the floor", free: diskSpaceFloorBytes, want: StatusOK},
		// An unreadable statfs is not evidence the disk is full.
		{name: "statfs error", err: errors.New("statfs failed"), want: StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := run(t, diskSpaceProbe{}, Env{
				ConfigDir: t.TempDir(),
				FreeBytes: func(string) (uint64, error) { return tc.free, tc.err },
			})
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q", got.Status, tc.want)
			}
		})
	}
}

func TestGitEngineProbe(t *testing.T) {
	gittestsetup.Enable()
	got := run(t, gitEngineProbe{}, Env{})
	if got.Status != StatusOK {
		t.Fatalf("status = %q code=%q detail=%v, want ok with bundled engine", got.Status, got.Code, got.Detail)
	}
}

func TestGitEngineProbeWithoutHostGit(t *testing.T) {
	gittestsetup.Enable()
	t.Setenv("PATH", "")
	got := run(t, gitEngineProbe{}, Env{})
	if got.Status != StatusOK {
		t.Fatalf("probe must be green with empty PATH; status=%q code=%q", got.Status, got.Code)
	}
}

func TestScannerEngineProbe(t *testing.T) {
	cases := []struct {
		name       string
		reason     string
		err        error
		want       Status
		wantReason string
	}{
		{name: "resolves", want: StatusOK},
		{name: "not found", reason: "not_found", err: errors.New("missing"), want: StatusDegraded, wantReason: "not_found"},
		{name: "checksum mismatch", reason: "checksum", err: errors.New("bad checksum"), want: StatusDegraded, wantReason: "checksum"},
		{name: "unclassified error", err: errors.New("boom"), want: StatusDegraded, wantReason: "not_found"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := run(t, scannerEngineProbe{}, Env{
				ResolveScanner: func() (string, string, error) { return "", tc.reason, tc.err },
			})
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q", got.Status, tc.want)
			}
			if tc.wantReason != "" && got.Detail["reason"] != tc.wantReason {
				t.Fatalf("reason = %q, want %q", got.Detail["reason"], tc.wantReason)
			}
		})
	}
}

var errBrowserProbe = errors.New("browser check failed")

func TestBrowserEngineProbe(t *testing.T) {
	t.Run("no checker wired", func(t *testing.T) {
		if got := run(t, browserEngineProbe{}, Env{}); got.Status != StatusOK {
			t.Fatalf("status = %q, want ok", got.Status)
		}
	})

	t.Run("usable", func(t *testing.T) {
		got := run(t, browserEngineProbe{}, Env{
			CheckBrowser: func(context.Context) (BrowserReason, error) { return "", nil },
		})
		if got.Status != StatusOK {
			t.Fatalf("status = %q, want ok", got.Status)
		}
	})

	t.Run("unusable", func(t *testing.T) {
		got := run(t, browserEngineProbe{}, Env{
			CheckBrowser: func(context.Context) (BrowserReason, error) { return ReasonBrowserUnusable, errBrowserProbe },
		})
		if got.Status != StatusDegraded || got.Code != CodeBrowserEngineUnavailable || got.Detail["reason"] != string(ReasonBrowserUnusable) {
			t.Fatalf("got %q/%q/%v, want degraded/%s/%s", got.Status, got.Code, got.Detail, CodeBrowserEngineUnavailable, ReasonBrowserUnusable)
		}
	})

	for _, reason := range []BrowserReason{ReasonBrowserManagedCacheMissing, ReasonBrowserBundleMissing} {
		t.Run(string(reason), func(t *testing.T) {
			got := run(t, browserEngineProbe{}, Env{
				CheckBrowser: func(context.Context) (BrowserReason, error) { return reason, errBrowserProbe },
			})
			if got.Status != StatusDegraded || got.Code != CodeBrowserEngineUnavailable || got.Detail["reason"] != string(reason) {
				t.Fatalf("got %q/%q/%v, want degraded/%s/%s", got.Status, got.Code, got.Detail, CodeBrowserEngineUnavailable, reason)
			}
		})
	}

	t.Run("failure without reason defaults", func(t *testing.T) {
		got := run(t, browserEngineProbe{}, Env{
			CheckBrowser: func(context.Context) (BrowserReason, error) { return "", errBrowserProbe },
		})
		if got.Status != StatusDegraded || got.Detail["reason"] != string(ReasonBrowserUnusable) {
			t.Fatalf("got %q/%v, want degraded/%s", got.Status, got.Detail, ReasonBrowserUnusable)
		}
	})
}

func TestProviderConfiguredProbe(t *testing.T) {
	noneMissing := func() map[string]string { return nil }

	degraded := Env{ProviderCount: func() int { return 0 }, MissingRoleProviders: noneMissing}
	if got := run(t, providerConfiguredProbe{}, degraded); got.Status != StatusDegraded || got.Code != CodeNoProviderConfigured {
		t.Fatalf("got %q/%q, want degraded/%s", got.Status, got.Code, CodeNoProviderConfigured)
	}

	healthy := Env{ProviderCount: func() int { return 1 }, MissingRoleProviders: noneMissing}
	if got := run(t, providerConfiguredProbe{}, healthy); got.Status != StatusOK {
		t.Fatalf("status = %q, want ok", got.Status)
	}
}

// An assigned provider can be missing even when other providers are configured.
func TestProviderConfiguredProbeChecksAssignedRoles(t *testing.T) {
	env := Env{
		ProviderCount:        func() int { return 1 },
		MissingRoleProviders: func() map[string]string { return map[string]string{"coordinator": "fireworks-1"} },
	}
	got := run(t, providerConfiguredProbe{}, env)
	if got.Status != StatusDegraded || got.Code != CodeNoProviderConfigured {
		t.Fatalf("got %q/%q, want degraded/%s", got.Status, got.Code, CodeNoProviderConfigured)
	}
	if got.Detail["coordinator"] != "fireworks-1" {
		t.Fatalf("detail = %v, want the unconfigured role named", got.Detail)
	}
}

// TestProviderConfiguredProbeUnwiredDegrades pins the fail-closed direction: a
// probe missing its callbacks has established nothing and must not report ok.
func TestProviderConfiguredProbeUnwiredDegrades(t *testing.T) {
	for name, env := range map[string]Env{
		"no callbacks":    {},
		"count only":      {ProviderCount: func() int { return 1 }},
		"role check only": {MissingRoleProviders: func() map[string]string { return nil }},
	} {
		if got := run(t, providerConfiguredProbe{}, env); got.Status != StatusDegraded {
			t.Fatalf("%s: status = %q, want degraded", name, got.Status)
		}
	}
}

func TestRegistryOverallIsWorstStatus(t *testing.T) {
	reg := Default()

	// Everything healthy except a missing provider (degraded).
	env := Env{
		ConfigDir:            filepath.Join(t.TempDir(), "lycaon"),
		FreeBytes:            func(string) (uint64, error) { return 50 << 30, nil },
		OSProductVer:         func() (string, error) { return "15.5", nil },
		ProviderCount:        func() int { return 0 },
		MissingRoleProviders: func() map[string]string { return nil },
		ResolveScanner:       func() (string, string, error) { return "/opt/opengrep", "", nil },
	}

	results, overall := reg.Run(context.Background(), env)
	if len(results) != len(reg.IDs()) {
		t.Fatalf("got %d results for %d probes", len(results), len(reg.IDs()))
	}
	if overall != StatusDegraded {
		t.Fatalf("overall = %q, want degraded", overall)
	}

	// A blocked probe must dominate a degraded one.
	env.OSProductVer = func() (string, error) { return "12.0", nil }
	if _, overall = reg.Run(context.Background(), env); overall != StatusBlocked {
		t.Fatalf("overall = %q, want blocked", overall)
	}
}

func TestRegistryPreservesWireOrder(t *testing.T) {
	want := []string{
		"os_version", "config_dir", "disk_space", "command_path", "git_engine",
		"scanner_engine", "browser_engine", "decision_engine", "provider_configured", "lite_slot",
	}
	got := Default().IDs()
	if len(got) != len(want) {
		t.Fatalf("probe ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("probe ids = %v, want %v (order is wire-visible)", got, want)
		}
	}
}

func TestOKResultsCarryNoCode(t *testing.T) {
	results, _ := Default().Run(context.Background(), Env{
		ConfigDir:            filepath.Join(t.TempDir(), "lycaon"),
		FreeBytes:            func(string) (uint64, error) { return 50 << 30, nil },
		OSProductVer:         func() (string, error) { return "15.5", nil },
		ProviderCount:        func() int { return 2 },
		MissingRoleProviders: func() map[string]string { return nil },
		ResolveScanner:       func() (string, string, error) { return "/opt/opengrep", "", nil },
	})

	for _, r := range results {
		if r.Status == StatusOK && r.Code != "" {
			t.Fatalf("probe %s is ok but carries code %q", r.ID, r.Code)
		}
	}
}

func TestLiteSlotProbe(t *testing.T) {
	if got := run(t, liteSlotProbe{}, Env{}); got.Status != StatusOK {
		t.Fatalf("unwired status = %q want ok", got.Status)
	}
	if got := run(t, liteSlotProbe{}, Env{LiteSlotUnavailable: func() (bool, map[string]string) { return false, nil }}); got.Status != StatusOK {
		t.Fatalf("ready status = %q want ok", got.Status)
	}
	got := run(t, liteSlotProbe{}, Env{
		LiteSlotUnavailable: func() (bool, map[string]string) {
			return true, map[string]string{"reason": "silent", "provider_id": "ollama-1"}
		},
	})
	if got.Status != StatusDegraded || got.Code != CodeLiteUnavailable {
		t.Fatalf("got %q/%q want degraded/%s", got.Status, got.Code, CodeLiteUnavailable)
	}
	if got.Detail["reason"] != "silent" || got.Detail["provider_id"] != "ollama-1" {
		t.Fatalf("detail = %v", got.Detail)
	}
}

func TestDecisionEngineProbe(t *testing.T) {
	t.Run("no checker wired", func(t *testing.T) {
		if got := run(t, decisionEngineProbe{}, Env{}); got.Status != StatusOK {
			t.Fatalf("status = %q, want ok", got.Status)
		}
	})

	t.Run("answering", func(t *testing.T) {
		got := run(t, decisionEngineProbe{}, Env{
			CheckDecisionEngine: func(context.Context) DecisionReason { return "" },
		})
		if got.Status != StatusOK || got.Code != "" {
			t.Fatalf("got %q/%q, want ok", got.Status, got.Code)
		}
	})

	for _, reason := range []DecisionReason{ReasonDecisionDisabled, ReasonDecisionBinaryMissing, ReasonDecisionModelMissing, ReasonDecisionUnusable} {
		t.Run(string(reason), func(t *testing.T) {
			got := run(t, decisionEngineProbe{}, Env{
				CheckDecisionEngine: func(context.Context) DecisionReason { return reason },
			})
			if got.Status != StatusDegraded || got.Code != CodeDecisionEngineUnavailable || got.Detail["reason"] != string(reason) {
				t.Fatalf("got %q/%q/%v, want degraded/%s/%s", got.Status, got.Code, got.Detail, CodeDecisionEngineUnavailable, reason)
			}
		})
	}
}
