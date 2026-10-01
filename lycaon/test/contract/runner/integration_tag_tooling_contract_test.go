package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// Package-graph tools include the integration build tier.

// integrationTaggedFileCount counts tests in the integration tier.
func integrationTaggedFileCount(t *testing.T, root string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(filepath.Join(root, "lycaon"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		head := make([]byte, 256)
		f, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		defer func() { _ = f.Close() }()
		n, _ := f.Read(head)
		if strings.Contains(string(head[:n]), "//go:build integration") {
			count++
		}
		return nil
	})
	contractcheck.FailErr(t, "walk for integration-tagged tests", err)
	return count
}

func TestIntegrationTierIsPopulated(t *testing.T) {
	t.Parallel()
	if got := integrationTaggedFileCount(t, contractcheck.RepoRoot(t)); got == 0 {
		t.Fatal("no integration-tagged test files found, so the integration tooling checks below are vacuous")
	}
}

// The dead-code gate analyzes integration-only callers.
func TestDeadcodeGateAnalyzesTheIntegrationBuild(t *testing.T) {
	t.Parallel()
	script := filepath.Join(contractcheck.RepoRoot(t), "scripts", "deadcode-check.sh")
	data, err := os.ReadFile(script)
	contractcheck.FailErr(t, "read deadcode-check.sh", err)
	if !strings.Contains(string(data), "-tags integration") {
		t.Fatal("scripts/deadcode-check.sh must invoke deadcode with -tags integration: " +
			"without it, production functions reached only by integration tests are reported unreachable")
	}
}

// The lint gate analyzes integration-tagged files.
func TestGolangciLintAnalyzesTheIntegrationBuild(t *testing.T) {
	t.Parallel()
	path := filepath.Join(contractcheck.RepoRoot(t), "lycaon", ".golangci.yml")
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read .golangci.yml", err)

	var cfg struct {
		Run struct {
			BuildTags []string `yaml:"build-tags"`
		} `yaml:"run"`
	}
	contractcheck.FailErr(t, "parse .golangci.yml", yaml.Unmarshal(data, &cfg))

	for _, tag := range cfg.Run.BuildTags {
		if strings.TrimSpace(tag) == "integration" {
			return
		}
	}
	t.Fatalf("lycaon/.golangci.yml run.build-tags = %v, want it to include \"integration\": "+
		"tagged files are otherwise excluded from the linted build", cfg.Run.BuildTags)
}
