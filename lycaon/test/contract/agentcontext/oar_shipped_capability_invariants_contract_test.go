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
	if sum.PolicyFiles != 626 || sum.PolicyRules != 599 || sum.PolicyOARHints != 1 || sum.PolicyLegacyBanners != 26 {
		t.Fatalf("unexpected stock policy inventory: files=%d rules=%d hints=%d banners=%d",
			sum.PolicyFiles, sum.PolicyRules, sum.PolicyOARHints, sum.PolicyLegacyBanners)
	}
	if sum.NextStepsFiles != 143 || sum.AnchorFiles != 2 {
		t.Fatalf("unexpected stock host OAR inventory: nextSteps=%d anchors=%d",
			sum.NextStepsFiles, sum.AnchorFiles)
	}
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
			t.Fatalf("expected set-assignment violation naming source/expression/alias, got:\n%s", formatOARViolations(sum.Violations))
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
		if !hasViolationMatching(sum.Violations, "SEEDED_INLINE_COPY.yaml#fix", "paintedwolf.ask_worker_writes", "command") {
			t.Fatalf("expected inline real-vocabulary copy violation, got:\n%s", formatOARViolations(sum.Violations))
		}
		if !hasViolationMatching(sum.Violations, "SEEDED_NEXT_STEP_MODE.yaml#steps[0]", "paintedwolf.session_mode != 'ask'", "ask_user") {
			t.Fatalf("expected next-step session_mode reconstruction violation, got:\n%s", formatOARViolations(sum.Violations))
		}
		if !hasViolationMatching(sum.Violations, "SEEDED_REDUNDANT_REACH.yaml#steps[0]", "admitted_command_forms", "command") {
			t.Fatalf("expected redundant admitted_command_forms predicate violation, got:\n%s", formatOARViolations(sum.Violations))
		}
	})
}

func TestSeededOARDiscoveryOtherwiseAndControls(t *testing.T) {
	_, schemaDir, _, _ := oarAuditEnv(t)
	vocab, _ := promptNameCatalog(t)

	t.Run("seeded_regression_unguarded_recovery_and_otherwise_promises", func(t *testing.T) {
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
		sum := auditStockOARRoot(t, vocab, tmp, schemaDir, false)
		if !hasViolationMatching(sum.Violations, "SEEDED_OTHERWISE_LOADER.yaml#steps[0].otherwise", "Request it with request_tools", "request_tools") {
			t.Fatalf("expected otherwise loader-promise violation, got:\n%s", formatOARViolations(sum.Violations))
		}
		if !hasViolationMatching(sum.Violations, "SEEDED_OTHERWISE_LOADER.yaml#fix", "<unguarded>", "command") {
			t.Fatalf("expected unguarded command promise violation, got:\n%s", formatOARViolations(sum.Violations))
		}
	})

	t.Run("seeded_discovery_of_newly_added_and_orphan_oar_assets", func(t *testing.T) {
		tmp := t.TempDir()
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
		if !hasViolationMatching(sum.Violations, "NEWLY_ADDED_STOCK_RULE.yaml#fix", "<unguarded>", "fetch_url") {
			t.Fatalf("expected newly added rule discovery violation, got:\n%s", formatOARViolations(sum.Violations))
		}
		if !hasViolationMatching(sum.Violations, "ORPHAN_NEXT_STEPS.yaml", "", "ORPHAN_NEXT_STEPS") {
			t.Fatalf("expected orphan next-steps violation, got:\n%s", formatOARViolations(sum.Violations))
		}
		if !hasViolationMatching(sum.Violations, "UNEXPECTED_NOTE.txt", "", "discovery") {
			t.Fatalf("expected unexpected-extension discovery violation, got:\n%s", formatOARViolations(sum.Violations))
		}
	})

	t.Run("passing_controls_valid_guards_occurrence_when_and_presentation", func(t *testing.T) {
		tmp := t.TempDir()
		writeSeededFile(t, tmp, "painted-wolf/platform/policy/CONTROL_OCCURRENCE_AND_GUARD.yaml", `spec_version: "1.0"
id: CONTROL_OCCURRENCE_AND_GUARD
kind: policy
on: session.pre_invoke
selector: {}
requires_facts: [paintedwolf.admitted_command_forms, paintedwolf.ask_worker_writes, paintedwolf.can_command, paintedwolf.command_operation_reachable, paintedwolf.next_steps, paintedwolf.session_mode, paintedwolf.user_guidance]
when: paintedwolf.session_mode == "ask" && paintedwolf.ask_worker_writes
effect: block
message: Ask mode occurrence block
what: 'Write leg blocked{% if paintedwolf.user_guidance %} ({{ paintedwolf.user_guidance }}){% endif %}.'
cause: Ask mode blocks mutating delegation.
why: Session authority is read-only.
fix: '{% if paintedwolf.can_command %}Inspect read-only repository settings with `+"`git config --list`"+` inside command, or switch{% else %}Switch{% endif %} to Agent mode before mutating. {{ paintedwolf.next_steps }}'
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
			t.Fatalf("passing control triggered unexpected violation(s):\n%s", formatOARViolations(sum.Violations))
		}
	})
}
