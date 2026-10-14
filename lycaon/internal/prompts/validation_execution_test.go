package prompts_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func TestValidationExecutionMatchesOfferedTools(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	for _, template := range []string{"partials/coordinator-verify-before-close.md", "partials/worker-write-leg-verify.md"} {
		for _, selected := range []bool{false, true} {
			for _, verify := range []bool{false, true} {
				for _, command := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/selected=%v/verify=%v/command=%v", template, selected, verify, command), func(t *testing.T) {
						vars := map[string]any{"profile_has_verify": verify, "profile_has_command": command}
						if selected {
							vars["verify_command"] = "project-check"
						}
						got, err := engine.Render(t.Context(), template, vars)
						testutil.FailErr(t, "render validation guidance", err)
						if !verify && strings.Contains(got, "verify(") {
							t.Fatalf("unavailable verify recipe: %s", got)
						}
						if !command && strings.Contains(got, "command(") {
							t.Fatalf("unavailable command recipe: %s", got)
						}
						if selected && verify && !strings.Contains(got, "Bare `verify()` runs it") {
							t.Fatal("selected check not runnable")
						}
						if selected && !verify && command && !strings.Contains(got, "command(command: \"project-check\"") {
							t.Fatal("command-only selected check not runnable")
						}
						if !selected && verify && !strings.Contains(got, "verify(command:") {
							t.Fatal("unselected verify has no command recipe")
						}
						if !selected && command && !strings.Contains(got, "command(verification: true)") {
							t.Fatal("command-only check has no evidence recipe")
						}
						if !verify && !command && !strings.Contains(got, "No execution tool is offered") {
							t.Fatal("missing inspection fallback")
						}
					})
				}
			}
		}
	}
}

// Render the inclusion sites with production capability projections, not flags
// injected into the validation partial. Deferred tools are reachable only when
// the current offer contains the loading tool.
func TestValidationGuidanceFollowsCompiledAvailability(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	for _, surface := range []string{"agents/coordinator-mode-implement-investigate.md", "agents/coordinator-mode-implement-dispatch.md", "agents/coordinator-mode-implement-overlay-promote.md", "partials/agent-tool-surface.md"} {
		for verify := toolsurface.Unavailable; verify <= toolsurface.Deferred; verify++ {
			for command := toolsurface.Unavailable; command <= toolsurface.Deferred; command++ {
				for _, loader := range []bool{false, true} {
					for _, selected := range []bool{false, true} {
						t.Run(fmt.Sprintf("%s/verify=%d/command=%d/loader=%v/selected=%v", surface, verify, command, loader, selected), func(t *testing.T) {
							offered := []string{"read", "edit"}
							var deferred []string
							if loader {
								offered = append(offered, "request_tools")
							}
							for name, mode := range map[string]toolsurface.Availability{"verify": verify, "command": command} {
								if mode == toolsurface.Immediate {
									offered = append(offered, name)
								}
								if mode == toolsurface.Deferred {
									deferred = append(deferred, name)
								}
							}
							plan := toolsurface.Compile(offered, deferred)
							vars := prompts.CoordinatorPolicyTemplateVars(plan.ImmediateNames(), plan.DeferredNames())
							vars["has_file_tools"] = true // The fixture offers read/edit in an attached workspace.
							if surface == "partials/agent-tool-surface.md" {
								data := prompts.AgentToolSurfaceData{ToolProfile: "implement"}
								for _, name := range plan.ImmediateNames() {
									data.Tools = append(data.Tools, prompts.AgentToolArgView{Name: name})
								}
								for _, name := range plan.DeferredNames() {
									data.Requestable = append(data.Requestable, prompts.RequestableToolView{Name: name})
								}
								vars = prompts.AgentToolSurfaceTemplateVars(data)
							}
							if selected {
								vars["verify_command"] = "project-check"
							}
							got, err := engine.Render(t.Context(), surface, vars)
							testutil.FailErr(t, "render complete validation surface", err)
							for name, mode := range map[string]toolsurface.Availability{"verify": verify, "command": command} {
								if mode != toolsurface.Immediate && strings.Contains(got, "`"+name+"(") {
									t.Fatalf("unoffered %s recipe in %s", name, got)
								}
							}
							reachable := verify == toolsurface.Immediate || command == toolsurface.Immediate || (loader && (verify == toolsurface.Deferred || command == toolsurface.Deferred))
							if reachable && !strings.Contains(got, "verification.method") {
								t.Fatalf("reachable validation guidance omitted: %s", got)
							}
							needsRequest := loader && command != toolsurface.Immediate && ((verify != toolsurface.Immediate && (command == toolsurface.Deferred || verify == toolsurface.Deferred)) || (selected && command == toolsurface.Deferred))
							if strings.Contains(got, `request_tools(need: "run a verification command")`) != needsRequest {
								t.Fatalf("loading guidance disagrees with reachable tools: %s", got)
							}
							if reachable && strings.Contains(got, "No execution tool is offered") {
								t.Fatal("loadable execution described as unavailable")
							}
						})
					}
				}
			}
		}
	}
}
