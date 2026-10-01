package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/eval/toolusage"
	"github.com/lycaon/lycaon/internal/fseffect"
	"gopkg.in/yaml.v3"
)

// generatePolicy is the unattended policy file: fixed answers for questions,
// and rules for any approval the sidecar's own settings still raise.
type generatePolicy struct {
	toolusage.UnattendedPolicy `yaml:",inline"`
	Approvals                  []toolusage.ApprovalRule `yaml:"approvals,omitempty"`
}

func runDecideGenerate(args []string) error {
	fs := flag.NewFlagSet("decide generate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	addr := fs.String("addr", toolusage.DefaultLiveBaseURL(), "sidecar URL")
	token := fs.String("token", os.Getenv("LYCAON_API_TOKEN"), "sidecar API token")
	tasksPath := fs.String("tasks", "", "JSON-lines task file (required)")
	project := fs.String("project", "", "project folder the sessions work in (required)")
	projectName := fs.String("project-name", "", "project name recorded in the store")
	manifestPath := fs.String("manifest", "", "manifest output, one JSON line per task (required)")
	policyPath := fs.String("unattended", "", "unattended policy YAML (required)")
	timeout := fs.Duration("timeout", 15*time.Minute, "per-prompt timeout")
	if err := fs.Parse(args); err != nil {
		return exitCodeError{code: 2, err: err}
	}
	if *tasksPath == "" || *project == "" || *manifestPath == "" || *policyPath == "" || *addr == "" || *token == "" {
		return exitCodeError{code: 2, err: fmt.Errorf("--addr, --token, --tasks, --project, --manifest and --unattended are required\n%s", decideUsage)}
	}
	tasks, err := toolusage.LoadGenerateTasks(*tasksPath)
	if err != nil {
		return exitCodeError{code: 2, err: err}
	}
	policy, err := loadGeneratePolicy(*policyPath)
	if err != nil {
		return exitCodeError{code: 2, err: err}
	}
	manifest, err := openDecideOutput(*manifestPath, true)
	if err != nil {
		return err
	}
	defer func() { _ = manifest.Close() }()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return toolusage.RunGenerate(ctx, toolusage.GenerateOptions{
		BaseURL: *addr, Token: *token, ProjectDir: *project, ProjectName: *projectName, Tasks: tasks,
		Timeout: *timeout, Unattended: policy.UnattendedPolicy, Approvals: policy.Approvals, Manifest: manifest,
	})
}

func loadGeneratePolicy(path string) (generatePolicy, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- operator-selected policy
	if err != nil {
		return generatePolicy{}, err
	}
	var policy generatePolicy
	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)
	if err := dec.Decode(&policy); err != nil {
		return generatePolicy{}, fmt.Errorf("unattended policy %s: %w", path, err)
	}
	return policy, nil
}

// openDecideOutput opens an operator-named output file, appending or replacing it.
func openDecideOutput(path string, appendMode bool) (*os.File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return fseffect.OpenWrite(fseffect.PathLocation(abs), appendMode, 0o600)
}
