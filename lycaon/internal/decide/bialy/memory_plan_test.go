package bialy_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/testutil"
)

func memoryCheckpoint(t *testing.T) bialy.Config {
	t.Helper()
	dir := t.TempDir()
	testutil.FailErr(t, "create encoder directory", os.Mkdir(filepath.Join(dir, "encoder"), 0o700))
	files := map[string]string{
		"encoder/config.json":  `{"model_type":"modernbert","hidden_size":768,"num_hidden_layers":22,"num_attention_heads":12,"intermediate_size":1152,"vocab_size":256000}`,
		"rl_agent_config.json": `{"max_len":1024,"head_layers":2}`,
		"model.safetensors":    "weights",
		"head.safetensors":     "head",
	}
	for name, content := range files {
		testutil.FailErr(t, "write checkpoint fixture", os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
	}
	return bialy.Config{ModelDir: dir, Heads: map[decide.Head]string{"turn-load": filepath.Join(dir, "head.safetensors")}}
}

func TestMemoryPlanAccountsForResidentHeadsAndBatchWorkspace(t *testing.T) {
	cfg := memoryCheckpoint(t)
	for _, backend := range []string{"mlx", "vulkan", "cpu"} {
		t.Run(backend, func(t *testing.T) {
			plan, err := bialy.PlanMemory(cfg, backend, 2)
			testutil.FailErr(t, "plan checkpoint", err)
			if backend == "mlx" && (plan.WorkspaceBytes != 512<<20 || plan.DeviceBytes-plan.WorkspaceBytes != 11+(64<<20)) {
				t.Fatalf("MLX resident and workspace grants: %+v", plan)
			}
			if plan.HostBytes == 0 || plan.MaxRows != 2 {
				t.Fatalf("invalid plan: %+v", plan)
			}
			withoutHeads := cfg
			withoutHeads.Heads = nil
			base, err := bialy.PlanMemory(withoutHeads, backend, 2)
			testutil.FailErr(t, "plan backbone", err)
			if backend == "cpu" {
				if plan.DeviceBytes != 0 || plan.HostBytes-base.HostBytes != 8 {
					t.Fatalf("CPU head residency: base=%+v plan=%+v", base, plan)
				}
			} else if plan.DeviceBytes-base.DeviceBytes != 4 {
				t.Fatalf("GPU head residency: base=%+v plan=%+v", base, plan)
			}
			large, err := bialy.PlanMemory(cfg, backend, 8)
			testutil.FailErr(t, "plan larger batch", err)
			if large.HostBytes+large.DeviceBytes < plan.HostBytes+plan.DeviceBytes || (backend == "mlx" && large.DeviceBytes <= plan.DeviceBytes) {
				t.Fatalf("larger batch lost workspace: small=%+v large=%+v", plan, large)
			}
		})
	}
}

func TestMemoryPlanRejectsUnsupportedCheckpoints(t *testing.T) {
	for _, name := range []string{"architecture", "length", "negative-head-length", "large-head-length", "metadata-size", "trailing-json", "missing-weights", "empty-weights", "large-weights", "directory-weights", "head-count", "head-size", "backend", "rows"} {
		t.Run(name, func(t *testing.T) {
			cfg := memoryCheckpoint(t)
			backend, rows := "mlx", 2
			write := func(path, content string) {
				t.Helper()
				testutil.FailErr(t, "replace metadata fixture", os.WriteFile(filepath.Join(cfg.ModelDir, path), []byte(content), 0o600))
			}
			switch name {
			case "architecture":
				write("encoder/config.json", `{"model_type":"other"}`)
			case "length":
				write("rl_agent_config.json", `{"max_len":8192,"head_layers":2}`)
			case "negative-head-length":
				cfg.HeadMaxLen = -1
			case "large-head-length":
				cfg.HeadMaxLen = 1025
			case "metadata-size":
				write("rl_agent_config.json", `{"max_len":1024,"head_layers":2}`+strings.Repeat(" ", 64<<10))
			case "trailing-json":
				write("rl_agent_config.json", `{"max_len":1024,"head_layers":2}{}`)
			case "missing-weights", "directory-weights":
				path := filepath.Join(cfg.ModelDir, "model.safetensors")
				testutil.FailErr(t, "remove fixture weights", os.Remove(path))
				if name == "directory-weights" {
					testutil.FailErr(t, "replace weights with directory", os.Mkdir(path, 0o700))
				}
			case "empty-weights":
				write("model.safetensors", "")
			case "large-weights":
				testutil.FailErr(t, "grow sparse weights", os.Truncate(filepath.Join(cfg.ModelDir, "model.safetensors"), (768<<20)+1))
			case "head-count":
				for i := 0; i < 7; i++ {
					cfg.Heads[decide.Head(fmt.Sprint(i))] = "missing"
				}
			case "head-size":
				testutil.FailErr(t, "grow sparse head", os.Truncate(filepath.Join(cfg.ModelDir, "head.safetensors"), (64<<20)+1))
			case "backend":
				backend = "unknown"
			case "rows":
				rows = 32
			}
			if _, err := bialy.PlanMemory(cfg, backend, rows); err == nil {
				t.Fatal("unsupported checkpoint admitted")
			}
		})
	}
}
