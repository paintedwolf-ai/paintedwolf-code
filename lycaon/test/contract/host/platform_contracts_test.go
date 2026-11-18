package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/nativemanifest"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/sandbox"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestTopologyConfigLoads(t *testing.T) {
	t.Parallel()
	dir := extpacks.Bundled(config.PlatformFlows.Join("_topologies"))
	for _, name := range []string{"default-pipeline.yaml", "supervisor-parallel.yaml", "fan-out-recon.yaml", "pack-probe.yaml"} {
		spec, err := orchestration.LoadTopologyFromFile(dir.Join(name))
		if err != nil {
			t.Fatalf("LoadTopologyFromFile(%s): %v", name, err)
		}
		if spec.ID == "" || spec.Pattern == "" {
			t.Fatalf("invalid spec from %s: %+v", name, spec)
		}
	}
}

func TestToolProfileYAMLValid(t *testing.T) {
	t.Parallel()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	if len(profiles) < 5 {
		t.Fatalf("expected bundled tool profiles, got %d", len(profiles))
	}
	for _, p := range profiles {
		if p.ID == "" {
			t.Fatal("empty profile id")
		}
	}
}

func TestNativeToolsManifest(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "native-tools.yaml"))
	contractcheck.FailErr(t, "read file", err)
	var manifest struct {
		Native struct {
			Filesystem []string `yaml:"filesystem"`
			Git        []string `yaml:"git"`
		} `yaml:"native"`
	}
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		contractcheck.FailErr(t, "unmarshal YAML document", err)
	}
	all := append(append([]string(nil), manifest.Native.Filesystem...), manifest.Native.Git...)
	for _, tool := range all {
		if tool == "command" {
			t.Fatal("native-tools.yaml must not list command")
		}
	}
	operations, err := nativemanifest.Load()
	contractcheck.FailErr(t, "load native manifests", err)
	hasGit := false
	for _, tool := range operations.OperationGroup("git") {
		if strings.HasPrefix(tool, "git_") {
			hasGit = true
		}
	}
	if !hasGit {
		t.Fatal("expected git_* operations in the native manifests")
	}
}
