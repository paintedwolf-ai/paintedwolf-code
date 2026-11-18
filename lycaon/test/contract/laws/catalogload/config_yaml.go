package catalogload

import (
	"bytes"
	"errors"
	"io"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/usernotice"
	"github.com/lycaon/lycaon/test/contract/laws"
)

// strictCheck decodes the files a glob under lycaon/config selects into
// their production type, refusing unknown keys.
type strictCheck struct {
	label  string
	glob   string
	decode func(raw []byte) error
}

var strictConfigRows = []strictCheck{
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
				ID             string                            `yaml:"id"`
				Description    string                            `yaml:"description"`
				Tools          map[string]sandbox.ToolMode       `yaml:"tools"`
				DenyTools      []string                          `yaml:"deny_tools"`
				MCPDeny        []string                          `yaml:"mcp_deny"`
				ReadScope      string                            `yaml:"read_scope"`
				WriteScope     string                            `yaml:"write_scope"`
				ReadScopes     []string                          `yaml:"read_scopes"`
				WriteScopes    []string                          `yaml:"write_scopes"`
				WaitConditions []string                          `yaml:"wait_conditions"`
				CommandEffects []toolcontract.CommandEffectClass `yaml:"command_effects"`
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

var configYAML = []laws.Rule{{
	// Every bundled YAML document parses.
	Name:    "config-yaml-is-syntactically-valid",
	Anchors: []laws.Anchor{laws.Dir("lycaon/config")},
	Check: func(r *laws.Run) {
		for _, f := range r.Tree("lycaon/config", ".yaml", ".yml") {
			decoder := yaml.NewDecoder(bytes.NewReader(f.Bytes()))
			for {
				var document yaml.Node
				err := decoder.Decode(&document)
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					r.Report("lycaon/config/%s: %v", f.Rel, err)
					break
				}
			}
		}
	},
}, {
	// Bundled files carry only fields their production type declares.
	Name:    "config-yaml-parses-strictly-against-its-type",
	Anchors: []laws.Anchor{laws.Dir("lycaon/config")},
	Check: func(r *laws.Run) {
		for _, row := range strictConfigRows {
			for _, rel := range r.Glob("lycaon/config/" + row.glob) {
				if err := row.decode(r.Bytes(rel)); err != nil {
					r.Report("%s (%s): %v", rel, row.label, err)
				}
			}
		}
	},
}}
