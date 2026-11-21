package guidance_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/testutil/exposure"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oarcopy"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/guidancetest"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
	"gopkg.in/yaml.v3"
)

func loadValidatedHints(t *testing.T) *guidance.HintConfig {
	t.Helper()
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfigStock", err)
	if err := guidancetest.ValidateHints(cfg); err != nil {
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
		// Copy-only units and OAR hints are never evaluated, so they carry no
		// scenarios to rehearse.
		if guidancetest.CopyOnlyEmit(entry.Emit) || entry.Kind == "hint" {
			continue
		}
		// Warn-severity hints render through the same formatter, so their
		// scenarios are rehearsed too.
		scenarios := entry.Scenarios
		if len(scenarios) == 0 {
			t.Fatalf("hint %q missing scenarios", code)
		}
		for _, sc := range scenarios {
			t.Run(code+"/"+sc.ID, func(t *testing.T) {
				vars := guidance.ScenarioVars(entry, sc)
				raw, err := f.Format(code, vars)
				testutil.FailErr(t, "Format "+code, err)
				for line := range strings.Lines(raw) {
					for _, label := range []string{"Rejected:", "Cause:", "Why:", "Fix:", "Next:", "Code:"} {
						if strings.TrimSpace(line) == label {
							t.Fatalf("empty rejection field %q", label)
						}
					}
				}

				// Bound endpoint references are literal command syntax.
				facts := oarcopy.FactsFromData(vars)
				for _, key := range []string{"paintedwolf.socket_path_references", "paintedwolf.socket_url_references"} {
					if refs, ok := facts[key].(string); ok && refs != "" {
						raw = strings.ReplaceAll(raw, refs, "socket-reference")
					}
				}
				// Rendered guidance must not leak template syntax or escaped HTML.
				for _, leak := range []string{"{%", "{{", "&quot;", "&amp;", "&#39;", "&gt;", "&lt;"} {
					exposure.Absent(t, raw, leak)
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
		if guidancetest.CopyOnlyEmit(entry.Emit) {
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
	for _, want := range []string{"Rejected:", "missing path", "Code: TOOL_ARGS_INVALID"} {
		if !strings.Contains(block, want) {
			t.Fatalf("missing %q in:\n%s", want, block)
		}
	}
}

// Authored causes reach the refusal frame.
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
	contract, known := toolcontract.Lookup("http_request")
	if !known {
		t.Fatal("missing HTTP tool contract")
	}
	block, err := guidance.RenderUnifiedRejectBlock(context.Background(), "SANDBOX_CAPABILITY_REQUEST_INVALID", entry, map[string]any{
		"tool": "http_request", "capability_request_fields": []string{"host_execution"},
		"supports_loopback_connect": true,
	})
	testutil.FailErr(t, "RenderUnifiedRejectBlock", err)
	for _, field := range contract.CapabilityRequestFields() {
		if !strings.Contains(block, field) {
			t.Fatalf("missing catalog field %q in:\n%s", field, block)
		}
	}
	for _, unsupported := range []string{"local_listen", "socket_paths", "host_resources", "host_execution"} {
		if strings.Contains(block, unsupported) {
			t.Fatalf("unsupported HTTP capability %q offered in:\n%s", unsupported, block)
		}
	}
}

func TestInapplicableArgumentRecoveryUsesHostFacts(t *testing.T) {
	setupGuidanceRenderer(t)
	cfg := loadValidatedHints(t)
	entry := cfg.HintCodes["TOOL_ARGS_INVALID"]
	for _, fields := range [][]string{nil, {"workflow_work_id"}, {"first", "second"}} {
		block, err := guidance.RenderUnifiedRejectBlock(t.Context(), "TOOL_ARGS_INVALID", entry, map[string]any{
			"tool": "task", "field": "workflow_work_id", "reason": "not_a_planned_fanout_phase",
			"inapplicable_fields": fields, "argument_schema_tool": "task",
		})
		testutil.FailErr(t, "render argument recovery", err)
		if !strings.Contains(block, "at `workflow_work_id`") || strings.Contains(block, "Omit `") != (len(fields) > 0) {
			t.Fatalf("recovery for %v: %s", fields, block)
		}
		if len(fields) > 0 && !strings.Contains(block, "Omit `"+strings.Join(fields, ", ")+"`") {
			t.Fatalf("recovery lost the inapplicable fields: %s", block)
		}
	}
}

func TestAskModeRefusalProducersRenderSpecificCausesAndConditionedRecovery(t *testing.T) {
	setupGuidanceRenderer(t)
	cfg := loadValidatedHints(t)
	fmtReject := guidance.NewStaticRejectFormatter(cfg)

	renderToolReject := func(t *testing.T, rej *toolrejection.ToolReject, extra map[string]any) (string, *guidance.RejectBlock) {
		t.Helper()
		if rej == nil {
			t.Fatal("expected non-nil ToolReject")
		}
		data := map[string]any{}
		for k, v := range rej.Data {
			data[k] = v
		}
		for k, v := range extra {
			data[k] = v
		}
		raw, err := fmtReject.Format(rej.Code, data)
		testutil.FailErr(t, "Format "+rej.Code, err)
		parsed, err := fmtReject.Parse(raw)
		testutil.FailErr(t, "Parse "+rej.Code, err)
		if parsed.What == "" || parsed.Cause == "" || parsed.Fix == "" {
			t.Fatalf("rendered %s missing What/Cause/Fix:\n%s", rej.Code, raw)
		}
		return raw, parsed
	}

	// 1. Blocked native tool or external launch inside command.
	t.Run("blocked_native_tool_and_external_launch", func(t *testing.T) {
		rawVerify, _ := renderToolReject(t, tools.AskModeReject("verify"), map[string]any{"can_command": true})
		if !strings.Contains(rawVerify, "`verify` is unavailable in Ask mode") || !strings.Contains(rawVerify, "switch to Agent mode") {
			t.Fatalf("unexpected verify Ask refusal:\n%s", rawVerify)
		}

		extRej := tools.AskModeReject("command")
		extRej.Data["command"] = "pytest"
		extRej.Data["reason"] = "external_exec"
		rawExt, _ := renderToolReject(t, extRej, map[string]any{"can_command": true})
		for _, want := range []string{
			"External launch `pytest` inside command is unavailable in Ask mode.",
			"earlier completed statements remain authoritative",
			"switch to Agent mode",
		} {
			if !strings.Contains(rawExt, want) {
				t.Fatalf("external launch Ask refusal missing %q:\n%s", want, rawExt)
			}
		}
	})

	// 2. Hidden command field (never echoes secret/value, and keeps recovery conditional on whether the command is a read-only inspection).
	t.Run("hidden_command_field", func(t *testing.T) {
		const secretSentinel = "SUPER_SECRET_TOKEN_VALUE_12345"
		rej := tools.AskModeCommandFieldReject("command", map[string]any{
			"command":    "rg -n foo",
			"secret_use": map[string]any{"token": secretSentinel},
		})
		raw, _ := renderToolReject(t, rej, map[string]any{"can_command": true})
		for _, want := range []string{
			"`secret_use` on command is unavailable in Ask mode.",
			"read-only command schema",
			"If the command only inspects files or state, omit `secret_use` and retry as a read-only native command",
			"if the operation itself requires `secret_use`, issue a new Agent-mode command call once Agent mode is active, or explain that the user can switch to Agent mode if still in Ask mode.",
		} {
			if !strings.Contains(raw, want) {
				t.Fatalf("hidden field Ask refusal missing %q:\n%s", want, raw)
			}
		}
		if strings.Contains(raw, secretSentinel) {
			t.Fatalf("hidden field Ask refusal leaked field value:\n%s", raw)
		}
	})

	// 3. Worker creation or continuation: mutating profile vs write scope.
	t.Run("worker_mutating_profile_vs_write_scope", func(t *testing.T) {
		mutRej := toolrejection.AsToolReject(workeradmission.RequireAskReadScope("task", true, true, api.TaskScope{Mode: api.TaskScopeModeRead}))
		mutRej.Data["agent_type"] = "implementer"
		rawMut, _ := renderToolReject(t, mutRej, map[string]any{"can_task": true})
		for _, want := range []string{
			"Worker profile `implementer` is mutation-capable",
			"Dispatch a read-only worker profile in read scope",
			"existing read-only workers keep their read-only scope after upgrade",
		} {
			if !strings.Contains(rawMut, want) {
				t.Fatalf("mutating profile Ask refusal missing %q:\n%s", want, rawMut)
			}
		}

		scopeRej := toolrejection.AsToolReject(workeradmission.RequireAskReadScope("task", false, true, api.TaskScope{Mode: api.TaskScopeModeWrite}))
		rawScope, _ := renderToolReject(t, scopeRej, map[string]any{"can_task": true})
		for _, want := range []string{
			"requested scope `write`",
			"existing read-only workers keep their read-only scope after upgrade",
		} {
			if !strings.Contains(rawScope, want) {
				t.Fatalf("write-scope Ask refusal missing %q:\n%s", want, rawScope)
			}
		}
	})

	// 4. Connected MCP tool lacking readOnlyHint: true.
	t.Run("connected_mcp_tool_without_readonly_hint", func(t *testing.T) {
		mcpRej := &toolrejection.ToolReject{
			Code: tools.AskModeActiveCode,
			Data: map[string]any{
				"tool":     "mcp_github_create_issue",
				"provider": "github",
				"reason":   "mcp_read_only_required",
				"missing":  true,
			},
		}
		rawMCP, _ := renderToolReject(t, mcpRej, map[string]any{"can_command": true})
		for _, want := range []string{
			"Connected tool `mcp_github_create_issue` is unavailable in Ask mode.",
			"Connected provider `github` does not declare `readOnlyHint: true`",
			"switch to Agent mode",
		} {
			if !strings.Contains(rawMCP, want) {
				t.Fatalf("MCP Ask refusal missing %q:\n%s", want, rawMCP)
			}
		}
	})

	// 5. Workflow start or advance requiring Agent mode.
	t.Run("workflow_requires_agent", func(t *testing.T) {
		wfRej := &toolrejection.ToolReject{
			Code: "SESSION_ASK_MODE_WORKFLOW_REQUIRES_AGENT",
			Data: map[string]any{
				"tool":        "workflow_advance",
				"workflow_id": "feature-build",
				"phase":       "plan",
				"requested":   "implement",
			},
		}
		rawWF, _ := renderToolReject(t, wfRej, map[string]any{"can_command": true})
		for _, want := range []string{
			"Workflow `feature-build` phase `implement` requires Agent mode.",
			"the workflow remains at its current read phase `plan`",
			"Continue read-only inspection in phase `plan`",
			"switch to Agent mode to enter `implement`",
		} {
			if !strings.Contains(rawWF, want) {
				t.Fatalf("workflow Ask refusal missing %q:\n%s", want, rawWF)
			}
		}
	})

	// 6. Secret input request via ask_user.
	t.Run("secret_ask_user", func(t *testing.T) {
		secRej := &toolrejection.ToolReject{
			Code: tools.AskModeActiveCode,
			Data: map[string]any{
				"tool":          "ask_user",
				"reason":        "secret_input",
				"response_type": "secret",
			},
		}
		rawSec, _ := renderToolReject(t, secRej, nil)
		for _, want := range []string{
			"Secret collection via ask_user is unavailable in Ask mode.",
			"never ask the user to paste a secret into chat",
		} {
			if !strings.Contains(rawSec, want) {
				t.Fatalf("secret ask_user refusal missing %q:\n%s", want, rawSec)
			}
		}
	})

	// 7. Unavailable alternative: when command and task are not offered, recovery does not recommend calling them.
	t.Run("unavailable_alternative_omitted", func(t *testing.T) {
		rawNoTools, _ := renderToolReject(t, tools.AskModeReject("verify"), map[string]any{
			"can_command": false,
			"can_task":    false,
		})
		for _, forbidden := range []string{"read-only native commands", "Delegate read-scoped research"} {
			if strings.Contains(rawNoTools, forbidden) {
				t.Fatalf("refusal recommended unoffered capability %q:\n%s", forbidden, rawNoTools)
			}
		}
	})

	// 8. Independent restriction: COMMAND_EFFECT_NOT_ALLOWED on worker_readonly in Agent mode is not misattributed to Ask mode.
	t.Run("independent_worker_profile_restriction", func(t *testing.T) {
		effRej := &toolrejection.ToolReject{
			Code: "COMMAND_EFFECT_NOT_ALLOWED",
			Data: map[string]any{
				"tool":          "command",
				"profile":       "worker_readonly",
				"expected_form": "read_only",
				"source_line":   1,
				"source_column": 1,
			},
		}
		rawEff, _ := renderToolReject(t, effRej, nil)
		if !strings.Contains(rawEff, "profile **worker_readonly**") || strings.Contains(rawEff, "Ask mode") {
			t.Fatalf("independent profile restriction misattributed to Ask mode:\n%s", rawEff)
		}
	})

	// 9. Delayed Ask refusal rendered after upgrade to Agent mode uses past-tense launch-time authority and does not direct the model to ask the user to upgrade again.
	t.Run("delayed_refusal_after_upgrade_past_tense", func(t *testing.T) {
		delayedRej := tools.AskModeReject("command")
		delayedRej.Data["command"] = "pytest"
		delayedRej.Data["reason"] = "external_exec"
		delayedRej.Data["session_mode"] = "agent"
		delayedRej.Data["mode_denied"] = true
		rawDelayed, parsedDelayed := renderToolReject(t, delayedRej, map[string]any{"can_command": true})
		for _, want := range []string{
			"refused under launch-time Ask authority",
			"This program started while Ask mode was active and kept its frozen read-only authority across the session upgrade",
			"Run `pytest` in a new Agent-mode command call outside the Ask-launched program",
		} {
			if !strings.Contains(rawDelayed, want) {
				t.Fatalf("delayed Ask refusal after upgrade missing expected guidance %q:\n%s", want, rawDelayed)
			}
		}
		for _, forbidden := range []string{
			"switch to Agent mode",
			"unavailable in Ask mode",
		} {
			if strings.Contains(rawDelayed, forbidden) || strings.Contains(parsedDelayed.Fix, forbidden) {
				t.Fatalf("delayed Ask refusal after upgrade still directed switching to Agent mode (%q):\n%s", forbidden, rawDelayed)
			}
		}
	})
}
