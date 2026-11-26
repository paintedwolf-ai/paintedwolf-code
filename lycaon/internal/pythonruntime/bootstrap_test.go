package pythonruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestTrustedBootstrapSourceModes(t *testing.T) {
	cases := []struct {
		name string
		cfg  BootstrapConfig
		want []string
	}{
		{
			name: "code mode",
			cfg: BootstrapConfig{
				Mode: ModeCode,
				Code: "print('ok')",
				CWD:  "/workspace/sub",
				Args: []string{"--flag"},
			},
			want: []string{
				`os.chdir(cwd)`,
				`sys.dont_write_bytecode = True`,
				`PYTHONPATH`,
				`atexit._run_exitfuncs()`,
				`sys.stdout.flush()`,
				`sys.stderr.flush()`,
			},
		},
		{
			name: "script mode",
			cfg: BootstrapConfig{
				Mode:   ModeScript,
				Script: "tools/run.py",
				CWD:    "/workspace",
			},
			want: []string{
				`os.path.dirname(os.path.abspath(script_path))`,
				`tools/run.py`,
			},
		},
		{
			name: "module mode",
			cfg: BootstrapConfig{
				Mode:   ModeModule,
				Module: "unittest",
				Args:   []string{"-v"},
			},
			want: []string{
				`runpy.run_module`,
				`unittest`,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv, err := BuildBootstrapArgv(tc.cfg)
			testutil.FailErr(t, "build bootstrap argv", err)
			if len(argv) != 5 || argv[0] != "python3" || argv[1] != "-I" || argv[2] != "-B" || argv[3] != "-c" {
				t.Fatalf("unexpected bootstrap argv prefix: %v", argv[:min(len(argv), 4)])
			}
			src := argv[4]
			for _, sub := range tc.want {
				if !strings.Contains(src, sub) {
					t.Fatalf("bootstrap source missing %q", sub)
				}
			}
			// Must contain zero guest-visible completion marker or nonce.
			if strings.Contains(src, ".pw_internal") || strings.Contains(src, "nonce") {
				t.Fatalf("bootstrap source must not expose guest-visible completion nonce/path")
			}
		})
	}
}

func TestHostLifecycleRejectsAbruptZeroExit(t *testing.T) {
	ctx := context.Background()
	// wasmFreshCounterModule calls proc_exit(0) directly (simulating os._exit(0)).
	rt, err := NewWazeroRuntime(ctx, RuntimeConfig{
		WasmBytes:           wasmFreshCounterModule(),
		UseTrustedBootstrap: true,
	})
	testutil.FailErr(t, "new wazero runtime with trusted bootstrap", err)
	defer rt.Close(ctx)

	ws := &stubWorkspaceAdapter{}
	res, err := rt.Execute(ctx, Invocation{
		ExecutionID: "abrupt-exit-0",
		Mode:        ModeCode,
		Code:        "import os; os._exit(0)",
		Workspace:   ws,
	})
	testutil.FailErr(t, "execute abrupt exit 0", err)
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if res.CleanCompletion {
		t.Fatal("proc_exit(0) must NOT set CleanCompletion=true under trusted bootstrap")
	}
	if !ws.settled || ws.settledClean {
		t.Fatalf("expected WorkspaceAdapter.Settle(false), got settled=%v clean=%v", ws.settled, ws.settledClean)
	}
}

func TestExecuteHandlesSettlementConflictsAndPartialFailure(t *testing.T) {
	ctx := context.Background()
	rt, err := NewWazeroRuntime(ctx, RuntimeConfig{
		WasmBytes: wasmFreshCounterModule(),
	})
	testutil.FailErr(t, "new wazero runtime", err)
	defer rt.Close(ctx)

	t.Run("revision conflict", func(t *testing.T) {
		ws := &customSettleWorkspace{
			report: &SettlementReport{
				CleanCompletion: true,
				Conflicts: []RevisionConflict{
					{
						Target:           CanonicalTarget{Namespace: NamespaceWorkspace, RelPath: "app.py"},
						ExpectedRevision: "rev-1",
						ActualRevision:   "rev-2",
					},
				},
			},
		}
		res, err := rt.Execute(ctx, Invocation{
			ExecutionID: "settle-conflict",
			Mode:        ModeCode,
			Code:        "pass",
			Workspace:   ws,
		})
		testutil.FailErr(t, "execute with conflict", err)
		if res.CleanCompletion {
			t.Fatal("expected CleanCompletion=false when SettlementReport has conflicts")
		}
		if res.Stop == nil || res.Stop.Code != StopRevisionConflict {
			t.Fatalf("expected StopRevisionConflict, got %+v", res.Stop)
		}
	})

	t.Run("partial failure", func(t *testing.T) {
		ws := &customSettleWorkspace{
			report: &SettlementReport{
				CleanCompletion: true,
				PartialFailure:  errors.New("disk full"),
			},
		}
		res, err := rt.Execute(ctx, Invocation{
			ExecutionID: "settle-partial",
			Mode:        ModeCode,
			Code:        "pass",
			Workspace:   ws,
		})
		testutil.FailErr(t, "execute with partial failure", err)
		if res.CleanCompletion {
			t.Fatal("expected CleanCompletion=false when SettlementReport has PartialFailure")
		}
		if len(res.Diagnostics) == 0 {
			t.Fatal("expected diagnostic recorded for PartialFailure")
		}
	})
}

type customSettleWorkspace struct {
	stubWorkspaceAdapter
	report *SettlementReport
	err    error
}

func (c *customSettleWorkspace) Settle(_ context.Context, _ bool) (*SettlementReport, error) {
	return c.report, c.err
}

func TestDefaultResourceLimitsAndNormalizeCWD(t *testing.T) {
	lim := DefaultResourceLimits()
	if lim.MaxMemoryPages != 4096 || lim.MaxGuestWork != 50_000_000 || lim.MaxHostWork != 100_000 ||
		lim.MaxOutputBytes != 65536 || lim.MaxStagedBytes != 16*1024*1024 || lim.Timeout.Seconds() != 120 {
		t.Fatalf("unexpected DefaultResourceLimits: %+v", lim)
	}

	cases := []struct {
		in   string
		want string
	}{
		{"", "/workspace"},
		{".", "/workspace"},
		{"./", "/workspace"},
		{"bench", "/workspace/bench"},
		{"./tools/helper", "/workspace/tools/helper"},
		{"tools/helper", "/workspace/tools/helper"},
		{"/workspace/tools/helper", "/workspace/tools/helper"},
		{"/workspace/./tools/helper", "/workspace/tools/helper"},
		{"../../etc", "/workspace"},
	}
	for _, tc := range cases {
		if got := normalizeGuestCWD("/workspace", tc.in); got != tc.want {
			t.Fatalf("normalizeGuestCWD(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
