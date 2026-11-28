package pythonruntime

import (
	"context"
	"strings"
	"testing"

	experimentalsys "github.com/tetratelabs/wazero/experimental/sys"

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
				Mode:            ModeCode,
				Code:            "print('ok')",
				CWD:             "/workspace/sub",
				Args:            []string{"--flag"},
				CompletionNonce: "abc123",
			},
			want: []string{
				`os.chdir(cwd)`,
				`sys.dont_write_bytecode = True`,
				`PYTHONPATH`,
				`atexit._run_exitfuncs()`,
				`sys.stdout.flush()`,
				`sys.stderr.flush()`,
				`/.pw_internal/complete`,
				`abc123`,
			},
		},
		{
			name: "script mode",
			cfg: BootstrapConfig{
				Mode:            ModeScript,
				Script:          "tools/run.py",
				CWD:             "/workspace",
				CompletionNonce: "deadbeef",
			},
			want: []string{
				`os.path.dirname(os.path.abspath(script_path))`,
				`tools/run.py`,
				`deadbeef`,
			},
		},
		{
			name: "module mode",
			cfg: BootstrapConfig{
				Mode:            ModeModule,
				Module:          "unittest",
				Args:            []string{"-v"},
				CompletionNonce: "cafebabe",
			},
			want: []string{
				`runpy.run_module`,
				`unittest`,
				`cafebabe`,
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
		})
	}
}

func TestCompletionMarkerFSContract(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	latch := NewStopLatch(cancel)
	nonce := GenerateCompletionNonce()
	mfs := NewCompletionMarkerFS(nonce, latch)

	if mfs.Completed() {
		t.Fatal("marker should not be completed initially")
	}

	// Wrong nonce must not mark completion.
	f, errno := mfs.OpenFile(InternalCompletionFile, experimentalsys.O_WRONLY|experimentalsys.O_CREAT, 0o200)
	if errno != 0 {
		t.Fatalf("OpenFile(complete) errno = %v", errno)
	}
	_, errno = f.Write([]byte("wrong-nonce"))
	if errno != 0 {
		t.Fatalf("Write(wrong) errno = %v", errno)
	}
	_ = f.Close()
	if mfs.Completed() {
		t.Fatal("wrong nonce must not mark completion")
	}

	// Exact nonce marks completion.
	f, errno = mfs.OpenFile(InternalCompletionFile, experimentalsys.O_WRONLY|experimentalsys.O_CREAT, 0o200)
	if errno != 0 {
		t.Fatalf("OpenFile(complete) errno = %v", errno)
	}
	_, errno = f.Write([]byte(nonce))
	if errno != 0 {
		t.Fatalf("Write(nonce) errno = %v", errno)
	}
	_ = f.Close()
	if !mfs.Completed() {
		t.Fatal("exact nonce did not mark completion")
	}

	// Once StopLatch trips, CompletionMarkerFS rejects opens and writes.
	mfsStopped := NewCompletionMarkerFS(nonce, latch)
	latch.Trigger(TerminalStop{Code: StopCancelled, Reason: "test stop"})
	if _, errno := mfsStopped.OpenFile(InternalCompletionFile, experimentalsys.O_WRONLY, 0o200); errno != experimentalsys.EACCES {
		t.Fatalf("OpenFile after stop errno = %v, want EACCES", errno)
	}
	if mfsStopped.Completed() {
		t.Fatal("stopped marker must never mark completion")
	}
}

func TestRequireCompletionMarkerRejectsAbruptZeroExit(t *testing.T) {
	ctx := context.Background()
	// wasmFreshCounterModule calls proc_exit(0) directly (simulating os._exit(0)
	// without writing the trusted bootstrap completion marker).
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
		t.Fatal("os._exit(0) without completion marker must NOT set CleanCompletion=true")
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
				PartialFailure:  context.DeadlineExceeded,
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
