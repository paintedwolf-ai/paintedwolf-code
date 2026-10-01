package guidance_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/configlayout"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
	"gopkg.in/yaml.v3"
)

func loadValidatedHints(t *testing.T) *guidance.HintConfig {
	t.Helper()
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfigStock", err)
	if err := guidance.ValidateHintConfig(cfg); err != nil {
		testutil.FailErr(t, "ValidateHintConfig", err)
	}
	return cfg
}

func setupGuidanceRenderer(t *testing.T) {
	t.Helper()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(engine))
}

func TestEveryRejectCodeHasScenario(t *testing.T) {
	cfg := loadValidatedHints(t)
	setupGuidanceRenderer(t)
	f := guidance.NewStaticRejectFormatter(cfg)
	for code, entry := range cfg.HintCodes {
		if guidance.CopyOnlyEmit(entry.Emit) {
			continue
		}
		// Warn-severity hints render through the same formatter, so their
		// scenarios are rehearsed too: copy that moves between members can
		// otherwise drop a phrase the rendered banner never shows.
		scenarios := entry.Scenarios
		if len(scenarios) == 0 {
			t.Fatalf("hint %q missing scenarios", code)
		}
		for _, sc := range scenarios {
			t.Run(code+"/"+sc.ID, func(t *testing.T) {
				vars := guidance.ScenarioVars(entry, sc)
				raw, err := f.Format(code, vars)
				testutil.FailErr(t, "Format "+code, err)
				for _, want := range sc.ExpectContains {
					if !strings.Contains(raw, want) {
						t.Fatalf("hint %q scenario %q missing %q in:\n%s", code, sc.ID, want, raw)
					}
				}
				// Rendered guidance must not leak template syntax or escaped HTML.
				for _, leak := range []string{"{%", "{{", "&quot;", "&amp;", "&#39;", "&gt;", "&lt;"} {
					if strings.Contains(raw, leak) {
						t.Fatalf("hint %q scenario %q leaks %q in:\n%s", code, sc.ID, leak, raw)
					}
				}
			})
		}
	}
}

// TestEveryToolHasDocumentedFeedback builds the tool→reject-code matrix from
// catalog hints and registered tool names.
func TestEveryToolHasDocumentedFeedback(t *testing.T) {
	cfg := loadValidatedHints(t)
	toolNames, err := registeredToolNames(t)
	testutil.FailErr(t, "registeredToolNames", err)
	if len(toolNames) == 0 {
		t.Fatal("expected registered tools")
	}

	byTool := map[string]map[string]bool{}
	for code, entry := range cfg.HintCodes {
		if guidance.CopyOnlyEmit(entry.Emit) {
			continue
		}
		for _, tool := range entry.Tools {
			if byTool[tool] == nil {
				byTool[tool] = map[string]bool{}
			}
			byTool[tool][code] = true
		}
	}

	universal := []string{"TOOL_ARGS_INVALID", "DOOM_LOOP_REPEAT"}
	for _, tool := range toolNames {
		set := map[string]bool{}
		for _, u := range universal {
			if _, ok := cfg.HintCodes[u]; ok {
				set[u] = true
			}
		}
		for code := range byTool[tool] {
			set[code] = true
		}
		if len(set) == 0 {
			t.Fatalf("tool %q has no reject coverage from hints", tool)
		}
	}
}

func registeredToolNames(t *testing.T) ([]string, error) {
	t.Helper()
	root := configlayout.FindModuleRoot()
	names := map[string]bool{
		"read": true, "write": true, "edit": true, "command": true,
		"grep": true, "find": true, "list_dir": true,
		"stat": true, "wc": true, "chmod": true, "delete": true,
		"task": true,
	}
	if data, err := os.ReadFile(filepath.Join(root, "config", "packs", "painted-wolf", "platform", "tools", "lycaon-tools.yaml")); err == nil {
		var doc struct {
			Tools []string `yaml:"tools"`
		}
		if err := yaml.Unmarshal(data, &doc); err == nil {
			for _, name := range doc.Tools {
				names[strings.TrimSpace(name)] = true
			}
		}
	}
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	if err == nil {
		for name := range schemas.Tools {
			names[name] = true
		}
	}
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func TestRenderUnifiedRejectBlock(t *testing.T) {
	setupGuidanceRenderer(t)
	cfg := loadValidatedHints(t)
	entry := cfg.HintCodes["TOOL_ARGS_INVALID"]
	block, err := guidance.RenderUnifiedRejectBlock(context.Background(), "TOOL_ARGS_INVALID", entry, map[string]any{
		"reason": "missing path",
	})
	testutil.FailErr(t, "RenderUnifiedRejectBlock", err)
	for _, want := range []string{"Rejected:", "Fix:", "Code: TOOL_ARGS_INVALID"} {
		if !strings.Contains(block, want) {
			t.Fatalf("missing %q in:\n%s", want, block)
		}
	}
}

// TestRejectBlockCarriesTheAuthoredCause pins the card as the only place the
// agent can read the cause a blocking unit is required to carry.
func TestRejectBlockCarriesTheAuthoredCause(t *testing.T) {
	setupGuidanceRenderer(t)
	cfg := loadValidatedHints(t)
	entry, ok := cfg.HintCodes["TOOL_OWNER_FAILED"]
	if !ok {
		t.Fatal("TOOL_OWNER_FAILED missing from stock policy")
	}
	const reason = "pongoplain: render context limit exceeded: collection has 1026 items"
	block, err := guidance.RenderUnifiedRejectBlock(context.Background(), "TOOL_OWNER_FAILED", entry, map[string]any{
		"reason": reason,
	})
	testutil.FailErr(t, "RenderUnifiedRejectBlock", err)
	if !strings.Contains(block, "Cause: "+reason) {
		t.Fatalf("the subsystem owner's reason must reach the agent:\n%s", block)
	}
	parsed, err := guidance.NewStaticRejectFormatter(cfg).Parse(block)
	testutil.FailErr(t, "Parse", err)
	if parsed.Cause != reason {
		t.Fatalf("Parse dropped the cause: %q", parsed.Cause)
	}
}

// A unit with no authored cause falls back to its message; never print twice.
func TestRejectBlockOmitsACauseThatOnlyRestatesWhat(t *testing.T) {
	setupGuidanceRenderer(t)
	cfg := loadValidatedHints(t)
	for code, entry := range cfg.HintCodes {
		block, err := guidance.RenderUnifiedRejectBlock(context.Background(), code, entry, nil)
		if err != nil {
			continue
		}
		parsed, perr := guidance.NewStaticRejectFormatter(cfg).Parse(block)
		if perr != nil {
			continue
		}
		if parsed.Cause != "" && parsed.Cause == parsed.What {
			t.Fatalf("%s repeats What as Cause:\n%s", code, block)
		}
	}
}

func TestCapabilityRequestFieldsReachRejectCopy(t *testing.T) {
	setupGuidanceRenderer(t)
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfigStock", err)
	entry, ok := cfg.HintCodes["SANDBOX_CAPABILITY_REQUEST_INVALID"]
	if !ok {
		t.Fatal("SANDBOX_CAPABILITY_REQUEST_INVALID missing from stock policy")
	}
	contract := toolcontract.Contract{Capabilities: toolcontract.CapabilityLoopbackConnect}
	block, err := guidance.RenderUnifiedRejectBlock(context.Background(), "SANDBOX_CAPABILITY_REQUEST_INVALID", entry, map[string]any{
		"tool": "http_request", "capability_request_fields": contract.CapabilityRequestFields(),
		"supports_loopback_connect": true,
	})
	testutil.FailErr(t, "RenderUnifiedRejectBlock", err)
	for _, field := range contract.CapabilityRequestFields() {
		if !strings.Contains(block, field) {
			t.Fatalf("missing catalog field %q in:\n%s", field, block)
		}
	}
	for _, unsupported := range []string{"local_listen", "direct_ip", "socket_paths", "host_resources"} {
		if strings.Contains(block, unsupported) {
			t.Fatalf("unsupported HTTP capability %q offered in:\n%s", unsupported, block)
		}
	}
}
