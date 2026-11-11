package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/nativemanifest"
)

func TestRunGeneratesAndChecksNativeToolContracts(t *testing.T) {
	dir := t.TempDir()
	outGo := filepath.Join(dir, "approval_tiers.generated.go")
	outContractGo := filepath.Join(dir, "contracts.generated.go")
	args := []string{"--out-go", outGo, "--out-contract-go", outContractGo}
	var output bytes.Buffer
	if err := run(args, &output); err != nil {
		t.Fatalf("run codegen: %v", err)
	}

	generated, err := os.ReadFile(outGo)
	if err != nil {
		t.Fatalf("read generated approval tiers: %v", err)
	}
	for _, want := range []string{"reversibleOperations", "recoverableOperations", "irreversibleOperations", `"read"`} {
		if !strings.Contains(string(generated), want) {
			t.Errorf("generated approval tiers do not contain %q:\n%s", want, generated)
		}
	}
	if err := run(append(args, "--check"), &output); err != nil {
		t.Fatalf("check generated contracts: %v", err)
	}

	if err := os.WriteFile(outGo, []byte("stale"), 0o600); err != nil {
		t.Fatalf("make generated output stale: %v", err)
	}
	if err := run(append(args, "--check"), &output); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale output error = %v", err)
	}
}

func TestRunRequiresBothOutputPaths(t *testing.T) {
	err := run(nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--out-go and --out-contract-go are required") {
		t.Fatalf("missing output paths error = %v", err)
	}
}

func TestRunReportsFlagAndOutputErrors(t *testing.T) {
	if err := run([]string{"--unknown"}, &bytes.Buffer{}); err == nil {
		t.Fatal("unknown flag succeeded")
	}

	err := run([]string{
		"--out-go", filepath.Join(t.TempDir(), "approval_tiers.generated.go"),
		"--out-contract-go", filepath.Join(t.TempDir(), "contracts.generated.go"),
	}, failingWriter{})
	if !errors.Is(err, errWriteFailed) {
		t.Fatalf("output error = %v", err)
	}
}

func TestRunReturnsManifestValidationErrors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*nativemanifest.Config)
		want   string
	}{
		{
			name: "tool contracts",
			mutate: func(cfg *nativemanifest.Config) {
				cfg.BatchPolicy["missing_tool"] = "serial"
			},
			want: "batch_policy without approval_reversibility",
		},
		{
			name: "grant target scopes",
			mutate: func(cfg *nativemanifest.Config) {
				cfg.GrantTargetScope["missing_tool"] = "invalid"
			},
			want: "grant_target_scope",
		},
		{
			name: "spawns process",
			mutate: func(cfg *nativemanifest.Config) {
				cfg.SpawnsProcess = append(cfg.SpawnsProcess, "missing_tool")
			},
			want: "spawns_process",
		},
		{
			name: "survey neutral",
			mutate: func(cfg *nativemanifest.Config) {
				cfg.SurveyNeutral = append(cfg.SurveyNeutral, "missing_tool")
			},
			want: "survey_neutral",
		},
		{
			name: "bounded in process",
			mutate: func(cfg *nativemanifest.Config) {
				cfg.BoundedInProcess = append(cfg.BoundedInProcess, "missing_tool")
			},
			want: "bounded_in_process",
		},
		{
			name: "chunkable payload",
			mutate: func(cfg *nativemanifest.Config) {
				cfg.ChunkablePayload = append(cfg.ChunkablePayload, "missing_tool")
			},
			want: "chunkable_payload",
		},
		{
			name: "detach after budget",
			mutate: func(cfg *nativemanifest.Config) {
				cfg.DetachAfterBudget = append(cfg.DetachAfterBudget, "missing_tool")
			},
			want: "detach_after_budget",
		},
		{
			name: "session scope",
			mutate: func(cfg *nativemanifest.Config) {
				cfg.SessionScope["missing_tool"] = "worker_child"
			},
			want: "session_scope",
		},
		{
			name: "socket argument",
			mutate: func(cfg *nativemanifest.Config) {
				cfg.SocketArg["missing_tool"] = "socket"
			},
			want: "socket_arg",
		},
		{
			name: "secret reference surface",
			mutate: func(cfg *nativemanifest.Config) {
				cfg.SecretReferenceSurface["missing_tool"] = nativemanifest.SecretSurfaceFile
			},
			want: "secret_reference_surface",
		},
		{
			name: "secret reference arguments",
			mutate: func(cfg *nativemanifest.Config) {
				cfg.SecretReferenceArgs["missing_tool"] = []string{"path"}
			},
			want: "secret_reference_args",
		},
		{
			name: "grant identity neutral arguments",
			mutate: func(cfg *nativemanifest.Config) {
				cfg.GrantIdentityNeutralArgs["missing_tool"] = []string{"path"}
			},
			want: "grant_identity_neutral_args",
		},
		{
			name: "path mutations",
			mutate: func(cfg *nativemanifest.Config) {
				cfg.MutatesPath = append(cfg.MutatesPath, "missing_tool")
			},
			want: "mutates_path",
		},
		{
			name: "content mutations",
			mutate: func(cfg *nativemanifest.Config) {
				cfg.MutatesContent = append(cfg.MutatesContent, "missing_tool")
			},
			want: "mutates_content",
		},
		{
			name: "multi-root",
			mutate: func(cfg *nativemanifest.Config) {
				cfg.MultiRoot["invalid_capability"] = nil
			},
			want: "invalid multi_root entries",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := nativemanifest.Load()
			if err != nil {
				t.Fatalf("load native tool manifest: %v", err)
			}
			tt.mutate(&cfg)
			args := []string{
				"--out-go", filepath.Join(t.TempDir(), "approval_tiers.generated.go"),
				"--out-contract-go", filepath.Join(t.TempDir(), "contracts.generated.go"),
			}
			err = runWithManifest(args, &bytes.Buffer{}, func() (nativemanifest.Config, error) {
				return cfg, nil
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validation error = %v, want %q", err, tt.want)
			}
		})
	}
}

var errWriteFailed = errors.New("write failed")

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errWriteFailed }

var _ io.Writer = failingWriter{}
