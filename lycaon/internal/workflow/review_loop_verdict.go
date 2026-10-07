package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// ReviewLoopVerdictInvalidCode identifies an invalid review result.
const ReviewLoopVerdictInvalidCode = "SUBMIT_VERDICT_INVALID"

// VerdictEnum returns verdicts with the terminal value first.
func VerdictEnum(def workflowdef.ReviewLoopDef) []string {
	raw := strings.TrimSpace(def.VerdictSchema[workflowdef.VerdictDecisionKey])
	if raw == "" {
		return nil
	}
	var out []string
	for _, v := range strings.Split(raw, "|") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// requiredVerdictFields returns required fields in stable order.
func requiredVerdictFields(def workflowdef.ReviewLoopDef) []string {
	var out []string
	for k := range def.VerdictSchema {
		if k == workflowdef.VerdictDecisionKey {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// VerdictClaim retains claim identity and cited evidence across review phases.
type VerdictClaim struct {
	Question     *ReviewQuestion `json:"question,omitempty"`
	ScanGroupIDs []string        `json:"scan_group_ids,omitempty"`
	ID           string          `json:"id"`
	// Title is the claim in one line; the phase that introduces a claim sets it.
	Title     string `json:"title,omitempty"`
	Statement string `json:"statement"`
	Status    string `json:"status,omitempty"`
	// Answers rate a claim that describes a flaw, against the workflow's
	// declared rating questions.
	Answers       map[string]string                    `json:"answers,omitempty"`
	CitedEvidence []api.CitationGroundingCitedEvidence `json:"cited_evidence,omitempty"`
}

// maxClaimTitleRunes bounds a claim title to one line of a report table.
const maxClaimTitleRunes = 120

// VerdictRules binds claim validation to run history and the declared rating.
type VerdictRules struct {
	KnownClaims map[string]bool
	Brief       *workflowdef.Brief
}

// ParseVerdictClaims decodes the stored JSON arrays for claim fields.
func ParseVerdictClaims(def workflowdef.ReviewLoopDef, verdict map[string]string) (map[string][]VerdictClaim, error) {
	var out map[string][]VerdictClaim
	for field, kind := range def.VerdictSchema {
		if field == workflowdef.VerdictDecisionKey || strings.TrimSpace(kind) != workflowdef.VerdictClaimsType {
			continue
		}
		raw := strings.TrimSpace(verdict[field])
		if raw == "" {
			continue
		}
		var claims []VerdictClaim
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&claims); err != nil {
			return nil, fmt.Errorf("%s: verdict field %q must be a JSON array of {id, title, statement, status, cited_evidence}: %w",
				ReviewLoopVerdictInvalidCode, field, err)
		}
		if err := dec.Decode(&struct{}{}); err != io.EOF {
			return nil, fmt.Errorf("%s: verdict field %q must contain exactly one JSON array",
				ReviewLoopVerdictInvalidCode, field)
		}
		seen := map[string]struct{}{}
		words := def.StatusWords()
		for i, c := range claims {
			if strings.TrimSpace(c.Status) == "" && len(words) == 1 {
				// One declared status word is the only valid value.
				claims[i].Status = words[0]
			}
			if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.Statement) == "" {
				return nil, fmt.Errorf("%s: verdict field %q claim %d requires id and statement",
					ReviewLoopVerdictInvalidCode, field, i)
			}
			if _, dup := seen[c.ID]; dup {
				return nil, fmt.Errorf("%s: verdict field %q repeats claim id %q",
					ReviewLoopVerdictInvalidCode, field, c.ID)
			}
			seen[c.ID] = struct{}{}
			for j, ce := range c.CitedEvidence {
				if strings.TrimSpace(ce.Handle) == "" && strings.TrimSpace(ce.Path) == "" {
					return nil, fmt.Errorf("%s: verdict field %q claim %q cited_evidence[%d] requires handle or path",
						ReviewLoopVerdictInvalidCode, field, c.ID, j)
				}
				if ce.Line < 0 || (ce.Line > 0 && strings.TrimSpace(ce.Path) == "") {
					return nil, fmt.Errorf("%s: verdict field %q claim %q cited_evidence[%d] line requires a path",
						ReviewLoopVerdictInvalidCode, field, c.ID, j)
				}
				if strings.TrimSpace(ce.Excerpt) != "" && strings.TrimSpace(ce.Path) == "" {
					return nil, fmt.Errorf("%s: verdict field %q claim %q cited_evidence[%d] excerpt requires a path",
						ReviewLoopVerdictInvalidCode, field, c.ID, j)
				}
			}
		}
		if out == nil {
			out = map[string][]VerdictClaim{}
		}
		out[field] = claims
	}
	return out, nil
}

// ParseVerdictSetAsides decodes every set_asides-typed verdict field, in field
// order. Each entry names its groups by id or by a scanner and path selector
// and states the reason that holds for every group it selects.
func ParseVerdictSetAsides(def workflowdef.ReviewLoopDef, verdict map[string]string) ([]guidance.CoordinatorSetAside, error) {
	var fields []string
	for field, kind := range def.VerdictSchema {
		if field != workflowdef.VerdictDecisionKey && strings.TrimSpace(kind) == workflowdef.VerdictSetAsidesType {
			fields = append(fields, field)
		}
	}
	sort.Strings(fields)
	var out []guidance.CoordinatorSetAside
	for _, field := range fields {
		raw := strings.TrimSpace(verdict[field])
		if raw == "" {
			continue
		}
		var entries []guidance.CoordinatorSetAside
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&entries); err != nil {
			return nil, fmt.Errorf("%s: verdict field %q must be a JSON array of {reason, scan_group_ids} or {reason, scanner, paths}: %w",
				ReviewLoopVerdictInvalidCode, field, err)
		}
		if err := dec.Decode(&struct{}{}); err != io.EOF {
			return nil, fmt.Errorf("%s: verdict field %q must contain exactly one JSON array",
				ReviewLoopVerdictInvalidCode, field)
		}
		for i, sa := range entries {
			if strings.TrimSpace(sa.Reason) == "" {
				return nil, fmt.Errorf("%s: verdict field %q set-aside %d needs a reason", ReviewLoopVerdictInvalidCode, field, i)
			}
			if len(sa.ScanGroupIDs) == 0 && (strings.TrimSpace(sa.Scanner) == "" || len(sa.Paths) == 0) {
				return nil, fmt.Errorf("%s: verdict field %q set-aside %d names no groups: give scan_group_ids, or a scanner and paths",
					ReviewLoopVerdictInvalidCode, field, i)
			}
		}
		out = append(out, entries...)
	}
	return out, nil
}

// ValidateReviewLoopVerdict checks the declared verdict schema, then the
// claims against the phase's statuses and the run's rules.
func ValidateReviewLoopVerdict(def workflowdef.ReviewLoopDef, verdict map[string]string, rules VerdictRules) error {
	var issues []error
	enum := VerdictEnum(def)
	got := strings.TrimSpace(verdict[workflowdef.VerdictDecisionKey])
	switch {
	case len(enum) == 0:
		issues = append(issues, fmt.Errorf("%s: review_loop verdict_schema declares no verdict enum", ReviewLoopVerdictInvalidCode))
	case got == "":
		issues = append(issues, fmt.Errorf("%s: verdict is empty (want one of %s)", ReviewLoopVerdictInvalidCode, strings.Join(enum, "|")))
	case !slices.Contains(enum, got):
		issues = append(issues, fmt.Errorf("%s: verdict %q not in schema enum %s", ReviewLoopVerdictInvalidCode, got, strings.Join(enum, "|")))
	}
	var undeclared []string
	for field := range verdict {
		if _, ok := def.VerdictSchema[field]; !ok {
			undeclared = append(undeclared, field)
		}
	}
	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		issues = append(issues, fmt.Errorf("%s: undeclared verdict field(s): %s", ReviewLoopVerdictInvalidCode, strings.Join(undeclared, ", ")))
	}
	for _, field := range requiredVerdictFields(def) {
		if strings.TrimSpace(verdict[field]) == "" {
			issues = append(issues, fmt.Errorf("%s: required verdict field %q is empty", ReviewLoopVerdictInvalidCode, field))
		}
	}
	if _, err := ParseVerdictSetAsides(def, verdict); err != nil {
		issues = append(issues, err)
	}
	if _, err := ParseVerdictCoverage(def, verdict); err != nil {
		issues = append(issues, err)
	}
	byField, err := ParseVerdictClaims(def, verdict)
	if err != nil {
		issues = append(issues, err)
	} else {
		for _, field := range sortedClaimFields(byField) {
			for _, claim := range byField[field] {
				if err := validateVerdictClaim(def, field, claim, rules); err != nil {
					issues = append(issues, err)
				}
			}
		}
	}
	return errors.Join(issues...)
}

func validateVerdictClaim(def workflowdef.ReviewLoopDef, field string, c VerdictClaim, rules VerdictRules) error {
	if c.Question != nil && def.FollowupAttempts == 0 {
		return fmt.Errorf("%s: this phase does not declare question follow-up", ReviewLoopVerdictInvalidCode)
	}
	status := strings.ToLower(strings.TrimSpace(c.Status))
	if status == "" {
		return fmt.Errorf("%s: verdict field %q claim %q requires status (one of %s)",
			ReviewLoopVerdictInvalidCode, field, c.ID, strings.Join(def.StatusWords(), "|"))
	}
	if _, ok := def.ClaimStatuses[status]; !ok {
		return fmt.Errorf("%s: verdict field %q claim %q status %q is not declared for this phase (want one of %s)",
			ReviewLoopVerdictInvalidCode, field, c.ID, c.Status, strings.Join(def.StatusWords(), "|"))
	}
	title := strings.TrimSpace(c.Title)
	if title == "" && !rules.KnownClaims[strings.TrimSpace(c.ID)] {
		return fmt.Errorf("%s: verdict field %q claim %q is new in this phase and requires a one-line title",
			ReviewLoopVerdictInvalidCode, field, c.ID)
	}
	if n := len([]rune(title)); n > maxClaimTitleRunes {
		return fmt.Errorf("%s: verdict field %q claim %q title is %d characters (limit %d)",
			ReviewLoopVerdictInvalidCode, field, c.ID, n, maxClaimTitleRunes)
	}
	if len(c.Answers) > 0 {
		if err := rules.Brief.CheckAnswers(c.Answers); err != nil {
			return fmt.Errorf("%s: verdict field %q claim %q answers: %w", ReviewLoopVerdictInvalidCode, field, c.ID, err)
		}
	}
	return nil
}

// The first declared decision value is terminal.
func ReviewLoopVerdictTerminal(def workflowdef.ReviewLoopDef, verdict map[string]string) bool {
	enum := VerdictEnum(def)
	if len(enum) == 0 {
		return false
	}
	return strings.TrimSpace(verdict[workflowdef.VerdictDecisionKey]) == enum[0]
}

// ReviewLoopVerdictEvidenceVerdict maps a review_loop verdict to the evidence gate verdict:
// a terminal verdict is a passing gate record; a non-terminal one needs changes (and does
// not satisfy the gate). Only a passing record with anchors advances the phase.
func ReviewLoopVerdictEvidenceVerdict(def workflowdef.ReviewLoopDef, verdict map[string]string) evidence.GateVerdict {
	if ReviewLoopVerdictTerminal(def, verdict) {
		return evidence.GateVerdictApproved
	}
	return evidence.GateVerdictNeedsChanges
}
