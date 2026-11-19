package contract

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var coordinatorPongoBooleanGateRE = regexp.MustCompile(`\{%\s*(?:if|elif)\s+(?:not\s+)?([a-z][a-z0-9_]*)\s*(?:%\}|(?:\s+(?:and|or)\s+))`)

// The nl branch is token-anchored so it matches the `nl_` prefix and not the
// letters inside ordinary words (inline, only, channel).
var bannedCoordinatorGateNameRE = regexp.MustCompile(`(?i)(greenfield|dispatch[_-]?hint|user[_-]?intent|(^|[_-])nl([_-]|$)|build[_-]?intent|chat[_-]?approv)`)

// coordinatorOccurrenceGates are values the coordinator render sites supply
// beside the reader facts.
var coordinatorOccurrenceGates = map[string]bool{
	// Round notice measurements come from the host's usage ledger for the round.
	"tokens":              true,
	"spend_under_cent":    true,
	"estimated_spend_usd": true,
	"latest_message_seq":  true,
	"round_ordinal":       true,
	"workflow_work_id":    true,
	"tools":               true,
	"tools_minutes":       true,
	// Prerequisite-blocked notice names the blocking producer worker when resolved.
	"producer": true,
	// Bounded grounding repair samples carry host-populated sample strings, truthy when non-empty.
	"observed_handles_sample": true,
	"observed_paths_sample":   true,
	"observed_urls_sample":    true,
	// Bounded grounding repair samples carry host-observed omitted counts.
	"observed_handles_omitted": true,
	"observed_paths_omitted":   true,
	"observed_urls_omitted":    true,
	"document_issues":          true,
	"agent_skills":             true,
	"rating_questions":         true,
	"agent_host_resources":     true,
	"has_file_tools":           true,
	"can_orient":               true,
	"can_spawn_web_research":   true,
	"can_spawn_workers":        true,
	// Kick counts of results awaiting landing and of partial workers.
	"pending_overlays": true,
	"partial_workers":  true,
	// The worker name a notice reports, empty for a round with no worker.
	"worker": true,
	// The settled round's notice, built from worker records and the job's report.
	"worker_result":          true,
	"leg_id":                 true,
	"progress_closure_armed": true,
	"card_label":             true,
	"card_rule":              true,
	"promoted_paths":         true,
	"drafted_synthesis":      true,
	// JSON projections of typed citation evaluation in guidance.CloseoutRepairHintData.
	"repair_observations":          true,
	"repair_targets":               true, // citation repair: rejected occurrences by repair ID
	"document_citation_targets":    true, // citation repair: rejected finding where entries
	"resume_work_allowed":          true,
	"can_recall":                   true, // recall in the closed turn's offered tools (promptloop rejectCloseoutGrounding)
	"offenders_sample":             true, // report-document repair: guidance.OffenderHintData
	"offenders_omitted":            true, // report-document repair: guidance.OffenderHintData
	"offending_keys":               true, // report-document repair: unread key names
	"allowed_top_level_keys":       true, // report-document repair: allowed top-level report fence keys
	"pinned_body_chars":            true, // report-document repair: char count of pinned body
	"retained_document":            true, // report-document repair: guidance.ReportDocumentFence
	"run_report":                   true, // report-document repair: the draft delivers its run's report
	"llm_timeout":                  true,
	"reason_text":                  true,
	"guidance":                     true,
	"options_criterion":            true,
	"topology_output":              true,
	"fanout_plan":                  true,
	"gate_obligations":             true,
	"completed_ago":                true,
	"worker_digest":                true,
	"primary_changed":              true,
	"changes":                      true, // projectroot.DiffRoots rows in RootsChangedKickData
	"active_root":                  true, // projectroot.ActiveRoot in RootsChangedKickData
	"session_folder_changed":       true, // session folder change in EnqueueRootsChangedKick
	"label":                        true, // detached root label in enqueueRootDetachCanceledKick
	"evidence_digest":              true,
	"command_completion":           true,
	"command_refusal":              true,
	"held_call_handle":             true, // host-supplied handle of a settled held call
	"scan_categories":              true,
	"scan_findings_count":          true,
	"scan_id":                      true,
	"scanner_id":                   true,
	"obligations":                  true,
	"last_worker_decision_request": true,
	"review_verdict":               true,
	"spawnable_reviewers":          true,
	"review_followup_attempts":     true,
	"command_external_changed":     true,
	"command_external_hidden":      true,
}

type coordinatorGateSite struct {
	rel  string
	gate string
}

type coordinatorGateScan struct {
	files int
	sites []coordinatorGateSite
}

func scanCoordinatorPongoBooleanGates(catalogRoot string) (coordinatorGateScan, error) {
	var out coordinatorGateScan
	err := filepath.WalkDir(catalogRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		base := filepath.Base(path)
		if !strings.HasPrefix(base, "coordinator") && !strings.Contains(path, string(filepath.Separator)+"partials"+string(filepath.Separator)+"coordinator") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(catalogRoot, path)
		if err != nil {
			return err
		}
		out.files++
		for _, m := range coordinatorPongoBooleanGateRE.FindAllStringSubmatch(string(raw), -1) {
			gate := m[1]
			if gate == "not" || gate == "forloop" {
				continue
			}
			out.sites = append(out.sites, coordinatorGateSite{rel: filepath.ToSlash(rel), gate: gate})
		}
		return nil
	})
	return out, err
}

var allowedKickTriggerClasses = map[string]bool{
	"manifest_on_reenter": true,
	"phase_on_enter":      true,
	"tool_rejection":      true,
	"host_api":            true,
	"wait_trigger":        true,
	"observed_history":    true,
	"workflow_event":      true,
	"overlay_state":       true,
}

var forbiddenKickFiresInSubstrings = []string{
	"greenfield", "user message contains", "strings.Contains", "phrase list",
	"chat yes", "lgtm", "nl intent",
}
