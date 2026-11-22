package contract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/oar"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPassingOARPresentationHistoricalAndPortableControls(t *testing.T) {
	_, schemaDir, _, _ := oarAuditEnv(t)
	vocab, _ := promptNameCatalog(t)
	tmp := t.TempDir()

	writeSeededFile(t, tmp, "painted-wolf/platform/policy/CONTROL_PRESENTATION_BANNER.yaml", `id: CONTROL_PRESENTATION_BANNER
emit: banner
category: informational
severity: info
message: '`+"`task()`"+` is blocked while user input is pending'
what: '{% set badge = "Notice" %}{{ badge }} ({{ paintedwolf.phase | upper }}): Another render_view call committed a newer revision after this call read it.'
cause: 'Cannot run git here; `+"`task()`"+` scope.base_overlay_id names no live overlay.'
why: Presentation formatting and historical call observations do not promise recovery tools.
fix: '{% if paintedwolf.can_fetch_url %}Use fetch_url for a returned URL.{% else %}Synthesize from existing results.{% endif %}'
instead: Continue within current authority.
`)
	writeSeededFile(t, tmp, "painted-wolf/platform/policy/CONTROL_PORTABLE_AND_HOST.yaml", `spec_version: "1.0"
id: CONTROL_PORTABLE_RULE
namespace: example.portable
kind: policy
on: tool.pre_invoke
selector:
  tools: [read]
requires_facts: [path_outside_scope]
when: path_outside_scope && same_call_run >= 0
effect: block
profile: portable
what: 'Tool {{ tool }} requested a path outside scope.'
fix: '{% if not path_outside_scope %}Continue within scope.{% else %}Read inside the declared scope, or ask to widen scope.{% endif %}'
`)
	writeSeededFile(t, tmp, "painted-wolf/platform/policy/CONTROL_OCCURRENCE_AND_GUARD.yaml", `spec_version: "1.0"
id: CONTROL_OCCURRENCE_AND_GUARD
kind: policy
on: session.pre_invoke
selector: {}
requires_facts: [paintedwolf.admitted_command_forms, paintedwolf.ask_worker_writes, paintedwolf.command_git_config_reachable, paintedwolf.command_operation_reachable, paintedwolf.next_steps, paintedwolf.session_mode, paintedwolf.user_guidance]
when: paintedwolf.session_mode == "ask" && paintedwolf.ask_worker_writes
effect: block
message: Ask mode occurrence block
what: 'Write leg blocked{% if paintedwolf.user_guidance %} ({{ paintedwolf.user_guidance }}){% endif %}.'
cause: Ask mode blocks mutating delegation.
why: Session authority is read-only.
fix: '{% if paintedwolf.command_git_config_reachable %}Inspect read-only repository settings with `+"`git config --list`"+` inside command, or switch{% else %}Switch{% endif %} to Agent mode before mutating. {{ paintedwolf.next_steps }}'
instead: Answer from read-only inspection or ask to switch modes.
`)
	writeSeededFile(t, tmp, "painted-wolf/platform/host/next-steps/CONTROL_OCCURRENCE_AND_GUARD.yaml", `steps:
  - when: "paintedwolf.command_operation_reachable"
    tool: "command"
    pick:
      command: "paintedwolf.admitted_command_forms"
    say: "Run {{ call }}."
    otherwise: "No replacement command form is available on this turn."
`)

	sum := auditStockOARRoot(t, vocab, tmp, schemaDir, false)
	if len(sum.Violations) > 0 {
		t.Fatalf("passing controls triggered unexpected violation(s):\n%s", formatOARViolations(sum.Violations))
	}

	loader, err := oar.NewLoader(schemaDir)
	contractcheck.FailErr(t, "oar.NewLoader", err)
	portablePath := filepath.Join(tmp, "painted-wolf/platform/policy/CONTROL_PORTABLE_AND_HOST.yaml")
	rawPortable, err := os.ReadFile(portablePath)
	contractcheck.FailErr(t, "read portable control", err)
	rs, err := loader.LoadEffectivePolicyEntries([]hintregistry.Entry{{
		Code: "CONTROL_PORTABLE_RULE", Path: extpacks.OnDisk(portablePath), Body: rawPortable,
	}})
	contractcheck.FailErr(t, "LoadEffectivePolicyEntries portable control", err)
	pipe := oar.NewGuardPipeline(rs, nil, nil)
	pipe.EnableAnchor(oar.AnchorToolPreInvoke)
	gc := oar.NewGuardContext()
	gc.Invocation.Tool = "read"
	gc.Access.PathOutsideScope = true
	res, err := pipe.EvaluateBlock(context.Background(), oar.AnchorToolPreInvoke, gc)
	contractcheck.FailErr(t, "EvaluateBlock portable control", err)
	if res == nil || res.Decision == nil || res.Decision.Code != "CONTROL_PORTABLE_RULE" ||
		!strings.Contains(res.Decision.Copy["what"], "Tool read") ||
		!strings.Contains(res.Decision.Copy["fix"], "Read inside the declared scope") {
		t.Fatalf("unexpected portable OAR evaluation outcome: %+v", res)
	}
}

func TestSeededOARDiscoveryAndOtherwisePromises(t *testing.T) {
	_, schemaDir, _, _ := oarAuditEnv(t)
	vocab, _ := promptNameCatalog(t)
	tmp := t.TempDir()

	writeSeededFile(t, tmp, "painted-wolf/platform/policy/SEEDED_OTHERWISE_LOADER.yaml", `spec_version: "1.0"
id: SEEDED_OTHERWISE_LOADER
kind: policy
on: tool.rejected
selector: {}
requires_facts: [paintedwolf.next_steps, paintedwolf.rejection_code]
when: paintedwolf.rejection_code == "SEEDED_OTHERWISE_LOADER"
effect: block
message: seeded otherwise
what: seeded what
cause: seeded cause
why: seeded why
fix: '{{ paintedwolf.next_steps }} Inspect with `+"`git config --list`"+` inside command.'
instead: adjust arguments
`)
	writeSeededFile(t, tmp, "painted-wolf/platform/host/next-steps/SEEDED_OTHERWISE_LOADER.yaml", `steps:
  - tool: "command"
    say: "Run {{ call }}."
    otherwise: "Request it with request_tools, then run it."
`)
	writeSeededFile(t, tmp, "painted-wolf/custom/policy/NEWLY_ADDED_STOCK_RULE.yaml", `spec_version: "1.0"
id: NEWLY_ADDED_STOCK_RULE
kind: policy
on: tool.rejected
selector: {}
requires_facts: [paintedwolf.rejection_code]
when: paintedwolf.rejection_code == "NEWLY_ADDED_STOCK_RULE"
effect: block
message: newly added rule
what: newly added what
cause: newly added cause
why: newly added why
fix: Call fetch_url to download the remote file.
instead: stop
`)
	writeSeededFile(t, tmp, "painted-wolf/custom/host/next-steps/ORPHAN_NEXT_STEPS.yaml", `steps:
  - tool: "command"
    say: "Run {{ call }}."
`)
	writeSeededFile(t, tmp, "painted-wolf/custom/policy/UNEXPECTED_NOTE.txt", "stray file")

	sum := auditStockOARRoot(t, vocab, tmp, schemaDir, false)
	if !hasViolationMatching(sum.Violations, "SEEDED_OTHERWISE_LOADER.yaml#steps[0].otherwise", "Request it with request_tools", "request_tools") {
		t.Fatalf("expected otherwise loader-promise violation, got:\n%s", formatOARViolations(sum.Violations))
	}
	if !hasViolationMatching(sum.Violations, "SEEDED_OTHERWISE_LOADER.yaml#fix", "<unguarded>", "git config") {
		t.Fatalf("expected unguarded git config program violation, got:\n%s", formatOARViolations(sum.Violations))
	}
	if !hasViolationMatching(sum.Violations, "NEWLY_ADDED_STOCK_RULE.yaml#fix", "<unguarded>", "fetch_url") {
		t.Fatalf("expected newly added rule discovery violation, got:\n%s", formatOARViolations(sum.Violations))
	}
	if !hasViolationMatching(sum.Violations, "ORPHAN_NEXT_STEPS.yaml", "", "ORPHAN_NEXT_STEPS") {
		t.Fatalf("expected orphan next-steps violation, got:\n%s", formatOARViolations(sum.Violations))
	}
	if !hasViolationMatching(sum.Violations, "UNEXPECTED_NOTE.txt", "", "discovery") {
		t.Fatalf("expected unexpected-extension discovery violation, got:\n%s", formatOARViolations(sum.Violations))
	}
}
