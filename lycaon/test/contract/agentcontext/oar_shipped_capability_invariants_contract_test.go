package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/oar"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func writeSeededFile(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	contractcheck.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(full), 0o755))
	contractcheck.FailErr(t, "write "+rel, os.WriteFile(full, []byte(body), 0o644))
}

func hasViolationMatching(vs []oarViolation, srcSub, exprSub, action string) bool {
	for _, v := range vs {
		if strings.Contains(v.Source, srcSub) &&
			strings.Contains(v.Expression, exprSub) &&
			(action == "" || v.Action == action) {
			return true
		}
	}
	return false
}

func formatOARViolations(vs []oarViolation) string {
	lines := make([]string, 0, len(vs))
	for _, v := range vs {
		lines = append(lines, v.String())
	}
	return strings.Join(lines, "\n")
}

func oarAuditEnv(t *testing.T) (repoRoot, schemaDir, moduleRoot, packsRoot string) {
	t.Helper()
	repoRoot = contractcheck.RepoRoot(t)
	schemaDir = filepath.Join(repoRoot, "schemas")
	moduleRoot = filepath.Join(repoRoot, "lycaon")
	packsRoot = filepath.Join(moduleRoot, "config", "packs")
	contractcheck.FailErr(t, "install anchor catalog", anchorcatalog.InstallBundled())
	contractcheck.FailErr(t, "install OAR engine declaration", oar.InstallEngineDeclarationBundled(schemaDir))
	return repoRoot, schemaDir, moduleRoot, packsRoot
}

func TestShippedOARCapabilityInvariants(t *testing.T) {
	_, schemaDir, moduleRoot, packsRoot := oarAuditEnv(t)
	vocab, _ := promptNameCatalog(t)

	sum := auditStockOARRoot(t, vocab, packsRoot, schemaDir, true)
	if len(sum.Violations) > 0 {
		t.Fatalf("shipped OAR capability audit found %d violation(s):\n%s",
			len(sum.Violations), formatOARViolations(sum.Violations))
	}
	if sum.PolicyFiles == 0 || sum.PolicyRules == 0 || sum.NextStepsFiles == 0 || sum.AnchorFiles == 0 {
		t.Fatalf("empty stock OAR inventory: files=%d rules=%d hints=%d banners=%d nextSteps=%d anchors=%d",
			sum.PolicyFiles, sum.PolicyRules, sum.PolicyOARHints, sum.PolicyLegacyBanners, sum.NextStepsFiles, sum.AnchorFiles)
	}
	if sum.PolicyFiles != sum.PolicyRules+sum.PolicyOARHints+sum.PolicyLegacyBanners {
		t.Fatalf("policy file partition mismatch: files=%d != rules=%d + hints=%d + banners=%d",
			sum.PolicyFiles, sum.PolicyRules, sum.PolicyOARHints, sum.PolicyLegacyBanners)
	}
	t.Logf("shipped stock OAR inventory: policy_files=%d (rules=%d, hints=%d, banners=%d), next_steps=%d, anchors=%d",
		sum.PolicyFiles, sum.PolicyRules, sum.PolicyOARHints, sum.PolicyLegacyBanners, sum.NextStepsFiles, sum.AnchorFiles)

	portableRules, portableFixtures, portableViolations := auditShippedPortablePack(t, vocab, moduleRoot, schemaDir)
	if len(portableViolations) > 0 {
		t.Fatalf("shipped OAR portable-pack audit found %d violation(s):\n%s",
			len(portableViolations), formatOARViolations(portableViolations))
	}
	if portableRules == 0 || portableFixtures == 0 {
		t.Fatalf("empty shipped OAR portable pack: rules=%d fixtures=%d", portableRules, portableFixtures)
	}
	t.Logf("shipped OAR portable pack qualified: rules=%d fixtures_passed=%d", portableRules, portableFixtures)

	unref, err := oar.UnreferencedDeclaredFacts(moduleRoot)
	contractcheck.FailErr(t, "UnreferencedDeclaredFacts", err)
	if len(unref) > 0 {
		t.Fatalf("unreferenced declared OAR host facts: %v", unref)
	}
}

func TestSeededOARCapabilityRegressionsAliasAndVocabulary(t *testing.T) {
	_, schemaDir, _, _ := oarAuditEnv(t)
	vocab, _ := promptNameCatalog(t)

	t.Run("seeded_regression_local_set_alias", func(t *testing.T) {
		tmp := t.TempDir()
		writeSeededFile(t, tmp, "painted-wolf/platform/policy/SEEDED_SET_ALIAS.yaml", `id: SEEDED_SET_ALIAS
emit: banner
category: recoverable
severity: warning
message: seeded banner
fix: '{% set allow_cmd = paintedwolf.session_mode == "agent" %}{% if allow_cmd %}Use command with `+"`edit --path`"+` to update it.{% endif %}'
`)
		sum := auditStockOARRoot(t, vocab, tmp, schemaDir, false)
		if !hasViolationMatching(sum.Violations, "SEEDED_SET_ALIAS.yaml#fix", `set allow_cmd = paintedwolf.session_mode == "agent"`, "allow_cmd") {
			t.Fatalf("expected set-assignment capability-derivation violation, got:\n%s", formatOARViolations(sum.Violations))
		}
		if !hasViolationMatching(sum.Violations, "SEEDED_SET_ALIAS.yaml#fix", `allow_cmd => paintedwolf.session_mode == "agent"`, "edit") {
			t.Fatalf("expected downstream alias-guarded edit violation, got:\n%s", formatOARViolations(sum.Violations))
		}
		if !hasViolationMatching(sum.Violations, "SEEDED_SET_ALIAS.yaml#fix", `allow_cmd => paintedwolf.session_mode == "agent"`, "command") {
			t.Fatalf("expected downstream alias-guarded command violation, got:\n%s", formatOARViolations(sum.Violations))
		}
	})

	t.Run("seeded_regression_inline_conditions_from_real_vocabulary", func(t *testing.T) {
		tmp := t.TempDir()
		writeSeededFile(t, tmp, "painted-wolf/platform/policy/SEEDED_INLINE_COPY.yaml", `spec_version: "1.0"
id: SEEDED_INLINE_COPY
kind: policy
on: tool.rejected
selector: {}
requires_facts: [paintedwolf.ask_worker_writes, paintedwolf.rejection_code]
when: paintedwolf.rejection_code == "SEEDED_INLINE_COPY"
effect: block
message: seeded inline copy
what: seeded what
cause: seeded cause
why: seeded why
fix: '{% if paintedwolf.ask_worker_writes %}Use command with `+"`sed -n`"+` to inspect lines.{% else %}Ask the user.{% endif %}'
instead: stop and adjust
`)
		writeSeededFile(t, tmp, "painted-wolf/security/policy/SEEDED_NEXT_STEP_MODE.yaml", `spec_version: "1.0"
id: SEEDED_NEXT_STEP_MODE
kind: policy
on: credential.assignment
selector: {}
requires_facts: [paintedwolf.next_steps, paintedwolf.session_mode]
when: paintedwolf.session_mode != ""
effect: warn
message: seeded credential
what: seeded what
cause: seeded cause
why: seeded why
fix: '{{ paintedwolf.next_steps }}'
instead: continue
`)
		writeSeededFile(t, tmp, "painted-wolf/security/host/next-steps/SEEDED_NEXT_STEP_MODE.yaml", `steps:
  - when: "paintedwolf.session_mode != 'ask'"
    tool: "ask_user"
    say: "Collect it via {{ call }}."
`)
		writeSeededFile(t, tmp, "painted-wolf/platform/policy/SEEDED_REDUNDANT_REACH.yaml", `spec_version: "1.0"
id: SEEDED_REDUNDANT_REACH
kind: schema
on: tool.rejected
selector: {}
requires_facts: [paintedwolf.admitted_command_forms, paintedwolf.command_operation_reachable, paintedwolf.next_steps, paintedwolf.rejection_code]
when: paintedwolf.rejection_code == "SEEDED_REDUNDANT_REACH"
effect: block
message: seeded reach
what: seeded what
cause: seeded cause
why: seeded why
fix: '{{ paintedwolf.next_steps }}'
instead: report missing operation
`)
		writeSeededFile(t, tmp, "painted-wolf/platform/host/next-steps/SEEDED_REDUNDANT_REACH.yaml", `steps:
  - when: "paintedwolf.command_operation_reachable && size(paintedwolf.admitted_command_forms) > 0"
    tool: "command"
    pick:
      command: "paintedwolf.admitted_command_forms"
    say: "Run {{ call }}."
`)
		sum := auditStockOARRoot(t, vocab, tmp, schemaDir, false)
		if !hasViolationMatching(sum.Violations, "SEEDED_INLINE_COPY.yaml#fix", "paintedwolf.ask_worker_writes", "sed") {
			t.Fatalf("expected inline real-vocabulary sed violation, got:\n%s", formatOARViolations(sum.Violations))
		}
		if !hasViolationMatching(sum.Violations, "SEEDED_NEXT_STEP_MODE.yaml#steps[0]", "paintedwolf.session_mode != 'ask'", "ask_user") {
			t.Fatalf("expected next-step session_mode reconstruction violation, got:\n%s", formatOARViolations(sum.Violations))
		}
		if !hasViolationMatching(sum.Violations, "SEEDED_REDUNDANT_REACH.yaml#steps[0]", "admitted_command_forms", "command") {
			t.Fatalf("expected redundant admitted_command_forms predicate violation, got:\n%s", formatOARViolations(sum.Violations))
		}
	})
}

func TestSeededOARFalsePositivesAndNonFixFieldClaims(t *testing.T) {
	_, schemaDir, _, _ := oarAuditEnv(t)
	vocab, _ := promptNameCatalog(t)
	tmp := t.TempDir()

	writeSeededFile(t, tmp, "painted-wolf/platform/policy/SEEDED_SELECTOR_EXCUSE.yaml", `spec_version: "1.0"
id: SEEDED_SELECTOR_EXCUSE
kind: schema
on: tool.rejected
selector:
  tools: [render_view, view_image]
requires_facts: [paintedwolf.rejection_code]
when: paintedwolf.rejection_code == "SEEDED_SELECTOR_EXCUSE"
effect: block
message: handle missing
what: handle missing
cause: handle missing
why: handle missing
fix: Call render_view to create a fresh handle first.
instead: stop
`)
	writeSeededFile(t, tmp, "painted-wolf/platform/policy/SEEDED_PROFILE_MUTATION.yaml", `spec_version: "1.0"
id: SEEDED_PROFILE_MUTATION
kind: policy
on: tool.rejected
selector: {}
requires_facts: [paintedwolf.profile_mutation_capable, paintedwolf.rejection_code]
when: paintedwolf.rejection_code == "SEEDED_PROFILE_MUTATION"
effect: block
message: mutation blocked
what: mutation blocked
cause: mutation blocked
why: mutation blocked
fix: '{% if paintedwolf.profile_mutation_capable %}Apply the change with `+"`edit --path`"+` in command.{% else %}Report the block.{% endif %}'
instead: stop
`)
	writeSeededFile(t, tmp, "painted-wolf/platform/policy/SEEDED_TEXT_CALLABLE_LIST.yaml", `spec_version: "1.0"
id: SEEDED_TEXT_CALLABLE_LIST
kind: policy
on: coordinator.post_turn
selector: {}
requires_facts: [paintedwolf.text_action_callable_tools]
when: size(paintedwolf.text_action_callable_tools) > 0
effect: block
message: tool in text
what: tool in text
cause: tool in text
why: tool in text
fix: '{% if paintedwolf.text_action_callable_tools %}Call fetch_url on the URL directly.{% endif %}'
instead: stop
`)
	writeSeededFile(t, tmp, "painted-wolf/platform/policy/SEEDED_BARE_CAN_COMMAND_PROGRAMS.yaml", `spec_version: "1.0"
id: SEEDED_BARE_CAN_COMMAND_PROGRAMS
kind: schema
on: tool.rejected
selector: {}
requires_facts: [paintedwolf.can_command, paintedwolf.rejection_code]
when: paintedwolf.rejection_code == "SEEDED_BARE_CAN_COMMAND_PROGRAMS"
effect: block
message: parse failed
what: parse failed
cause: parse failed
why: 'Run `+"`rg -n`"+` in command to locate the symbol first.'
fix: '{% if paintedwolf.can_command %}For source reads, use `+"`sed -n`"+` or `+"`cat -n`"+` in command.{% endif %}'
instead: stop
`)
	writeSeededFile(t, tmp, "painted-wolf/platform/policy/SEEDED_UNPRUNED_CMD_SCHEMA.yaml", `spec_version: "1.0"
id: SEEDED_UNPRUNED_CMD_SCHEMA
kind: policy
on: tool.rejected
selector: {}
requires_facts: [paintedwolf.next_steps, paintedwolf.rejection_code]
when: paintedwolf.rejection_code == "SEEDED_UNPRUNED_CMD_SCHEMA"
effect: block
message: process unsupported
what: Call `+"`fetch_url(url=\"https://example.com\")`"+` to inspect the endpoint.
cause: unsupported
why: unsupported
fix: '{{ paintedwolf.next_steps }}'
instead: stop
`)
	writeSeededFile(t, tmp, "painted-wolf/platform/host/next-steps/SEEDED_UNPRUNED_CMD_SCHEMA.yaml", `steps:
  - tool: "command"
    say: "Run {{ call }} with `+"`capability_request.host_execution`"+`."
`)
	sum := auditStockOARRoot(t, vocab, tmp, schemaDir, false)
	if !hasViolationMatching(sum.Violations, "SEEDED_SELECTOR_EXCUSE.yaml#fix", "<unguarded>", "render_view") {
		t.Fatalf("expected occurrence-selector false-positive rejection on render_view, got:\n%s", formatOARViolations(sum.Violations))
	}
	if !hasViolationMatching(sum.Violations, "SEEDED_PROFILE_MUTATION.yaml#fix", "paintedwolf.profile_mutation_capable", "edit") ||
		!hasViolationMatching(sum.Violations, "SEEDED_PROFILE_MUTATION.yaml#fix", "paintedwolf.profile_mutation_capable", "command") {
		t.Fatalf("expected profile_mutation_capable rejection for edit/command, got:\n%s", formatOARViolations(sum.Violations))
	}
	if !hasViolationMatching(sum.Violations, "SEEDED_TEXT_CALLABLE_LIST.yaml#fix", "paintedwolf.text_action_callable_tools", "fetch_url") {
		t.Fatalf("expected text_action_callable_tools list rejection for named fetch_url, got:\n%s", formatOARViolations(sum.Violations))
	}
	if !hasViolationMatching(sum.Violations, "SEEDED_BARE_CAN_COMMAND_PROGRAMS.yaml#fix", "paintedwolf.can_command", "sed") ||
		!hasViolationMatching(sum.Violations, "SEEDED_BARE_CAN_COMMAND_PROGRAMS.yaml#fix", "paintedwolf.can_command", "cat") {
		t.Fatalf("expected bare can_command rejection for specific sed/cat programs, got:\n%s", formatOARViolations(sum.Violations))
	}
	if !hasViolationMatching(sum.Violations, "SEEDED_BARE_CAN_COMMAND_PROGRAMS.yaml#why", "<unguarded>", "rg") ||
		!hasViolationMatching(sum.Violations, "SEEDED_UNPRUNED_CMD_SCHEMA.yaml#what", "<unguarded>", "fetch_url") {
		t.Fatalf("expected non-fix/instead capability claim rejections in #why/#what, got:\n%s", formatOARViolations(sum.Violations))
	}
	if !hasViolationMatching(sum.Violations, "SEEDED_UNPRUNED_CMD_SCHEMA.yaml#steps[0].say", "<unguarded>", "capability_request") {
		t.Fatalf("expected unguarded command capability_request schema violation, got:\n%s", formatOARViolations(sum.Violations))
	}
}
