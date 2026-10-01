package contract

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/usernotice"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// TestConfigYAMLParsesStrictlyAgainstSchema rejects unknown bundled fields.
func TestConfigYAMLParsesStrictlyAgainstSchema(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	configDir := filepath.Join(root, "lycaon", "config")

	type strictCheck struct {
		label  string
		glob   string
		decode func(raw []byte) error
	}

	rows := []strictCheck{
		{
			label: "path-scopes",
			glob:  "packs/painted-wolf/platform/host/path-scopes.yaml",
			decode: func(raw []byte) error {
				var v struct {
					Scopes map[string]struct {
						Description string   `yaml:"description"`
						Read        []string `yaml:"read"`
						Write       []string `yaml:"write"`
						Deny        []string `yaml:"deny"`
						Allow       []string `yaml:"allow"`
					} `yaml:"scopes"`
				}
				return config.DecodeYAML(raw, &v)
			},
		},
		{
			label: "tool-profiles",
			glob:  "packs/painted-wolf/*/tools/profiles/*.yaml",
			decode: func(raw []byte) error {
				var v struct {
					ID             string                      `yaml:"id"`
					Description    string                      `yaml:"description"`
					Tools          map[string]sandbox.ToolMode `yaml:"tools"`
					DenyTools      []string                    `yaml:"deny_tools"`
					MCPDeny        []string                    `yaml:"mcp_deny"`
					ReadScope      string                      `yaml:"read_scope"`
					WriteScope     string                      `yaml:"write_scope"`
					ReadScopes     []string                    `yaml:"read_scopes"`
					WriteScopes    []string                    `yaml:"write_scopes"`
					WaitConditions []string                    `yaml:"wait_conditions"`
				}
				return config.DecodeYAML(raw, &v)
			},
		},
		{
			label: "lycaon-tools",
			glob:  "packs/painted-wolf/platform/tools/lycaon-tools.yaml",
			decode: func(raw []byte) error {
				var v tools.ToolsConfig
				return config.DecodeYAML(raw, &v)
			},
		},
		{
			label: "native-tools",
			glob:  "packs/painted-wolf/platform/tools/native-tools.yaml",
			decode: func(raw []byte) error {
				var v nativemanifest.Config
				return config.DecodeYAML(raw, &v)
			},
		},
		{
			label: "persona-contract",
			glob:  "packs/painted-wolf/*/agents/prompts/_persona-contract.yaml",
			decode: func(raw []byte) error {
				var v prompts.PersonaContract
				return config.DecodeYAML(raw, &v)
			},
		},
		{
			label: "policy-units",
			glob:  "packs/painted-wolf/*/policy/*.yaml",
			decode: func(raw []byte) error {
				var v guidance.HintEntry
				return config.DecodeYAML(raw, &v)
			},
		},
		{
			label: "user-notice-units",
			glob:  "packs/painted-wolf/platform/host/user-notices/*.yaml",
			decode: func(raw []byte) error {
				var v usernotice.Entry
				return config.DecodeYAML(raw, &v)
			},
		},
		{
			label: "compose-policy",
			glob:  "packs/painted-wolf/platform/host/compose-policy.yaml",
			decode: func(raw []byte) error {
				// This mirrors the private workflow schema.
				var v struct {
					MaxPhases                   int                 `yaml:"max_phases"`
					RequireExtends              bool                `yaml:"require_extends"`
					CoordinatorOnly             bool                `yaml:"coordinator_only"`
					AllowedExtends              []string            `yaml:"allowed_extends"`
					PostureRequiredGates        map[string][]string `yaml:"posture_required_gates"`
					RequiredPhaseIDsWhenExtends map[string][]string `yaml:"required_phase_ids_when_extends"`
				}
				return config.DecodeYAML(raw, &v)
			},
		},
		{
			label: "prompt-budgets",
			glob:  "packs/painted-wolf/platform/host/prompt-budgets.yaml",
			decode: func(raw []byte) error {
				var v prompts.PromptBudgets
				return config.DecodeYAML(raw, &v)
			},
		},
	}

	for _, row := range rows {
		t.Run(row.label, func(t *testing.T) {
			t.Parallel()
			matches, err := filepath.Glob(filepath.Join(configDir, row.glob))
			contractcheck.FailErr(t, "filepath.Glob failed", err)
			if len(matches) == 0 {
				t.Fatalf("no files matched glob %q under %s", row.glob, configDir)
			}
			sort.Strings(matches)
			for _, path := range matches {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read %s: %v", path, err)
				}
				if err := row.decode(data); err != nil {
					rel, _ := filepath.Rel(configDir, path)
					t.Errorf("%s strict parse: %v\nFix: remove the unknown key from the YAML file or add the field to the production struct.", rel, err)
				}
			}
		})
	}
}

// TestConfigYAMLIsSyntacticallyValid parses every bundled YAML document.
func TestConfigYAMLIsSyntacticallyValid(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	configDir := filepath.Join(root, "lycaon", "config")

	var failures []string
	err := filepath.Walk(configDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		for {
			var document yaml.Node
			if err := decoder.Decode(&document); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				rel, _ := filepath.Rel(configDir, path)
				failures = append(failures, fmt.Sprintf("%s: %v", rel, err))
				break
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "operation failed", err)
	if len(failures) > 0 {
		sort.Strings(failures)
		t.Fatalf("YAML files failed strict syntactic parse:\n  %s", strings.Join(failures, "\n  "))
	}
}
