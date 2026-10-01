package extpacks

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/detectionpack"
)

// ValidateReport summarizes a scoped extension catalog.
type ValidateReport struct {
	Packs          []PackSummary   `json:"packs"`
	Units          []UnitEffective `json:"units"`
	Diagnostics    []Diagnostic    `json:"diagnostics"`
	Conflicts      int             `json:"conflicts"`
	SuiteConflicts int             `json:"suite_conflicts"`
	// RehearsalFailures counts detection cases that did not behave as declared.
	RehearsalFailures int               `json:"rehearsal_failures"`
	MetaPacks         []MetaPackSummary `json:"meta_packs,omitempty"`
	OK                bool              `json:"ok"`
}

// Validate builds the scoped effective-catalog report.
func Validate(
	ctx context.Context,
	projectDir string,
	eff *EffectiveCatalog,
) (*ValidateReport, error) {
	if eff == nil {
		return nil, fmt.Errorf("extpacks: validate requires a resolved catalog")
	}
	content, err := DiscoverAllContent([]string{projectDir})
	if err != nil {
		return nil, err
	}
	rep := &ValidateReport{
		Packs:       append([]PackSummary(nil), eff.Packs...),
		Diagnostics: append([]Diagnostic(nil), eff.Diagnostics...),
	}
	for _, u := range eff.Units {
		rep.Units = append(rep.Units, u)
		if u.Status == UnitStatusConflict {
			rep.Conflicts++
		}
	}
	// Rehearsal checks declared detection coverage during authoring validation.
	semantics, semanticsErr := detectionpack.LoadActionSemantics("")
	if semanticsErr != nil {
		return nil, fmt.Errorf("detection action semantics: %w", semanticsErr)
	}
	if rehearsal := RehearseDetectionPacks(eff, semantics); len(rehearsal) > 0 {
		rep.Diagnostics = append(rep.Diagnostics, rehearsal...)
		rep.RehearsalFailures = len(rehearsal)
	}
	if err := ApplyMetaValidate(ctx, rep, eff, content); err != nil {
		return nil, err
	}
	sort.Slice(rep.Units, func(i, j int) bool { return rep.Units[i].ID < rep.Units[j].ID })
	// Severity determines which diagnostics fail validation.
	rep.Diagnostics = StampSeverities(rep.Diagnostics)
	rep.OK = !HasErrorDiagnostic(rep.Diagnostics)
	return rep, nil
}

// FormatValidateText renders a validation report.
func FormatValidateText(rep *ValidateReport) string {
	if rep == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "extension packs: %d packs, %d units tracked, %d conflicts, suite_conflicts=%d, rehearsal_failures=%d\n",
		len(rep.Packs), len(rep.Units), rep.Conflicts, rep.SuiteConflicts, rep.RehearsalFailures)
	for _, p := range rep.Packs {
		status := "ok"
		if !p.Contributing {
			status = string(p.BlockedReason)
			if status == "" {
				status = "blocked"
			}
		}
		fmt.Fprintf(&b, "  pack %-40s enabled=%v contributing=%v [%s]\n",
			p.ID, p.Enabled, p.Contributing, status)
	}
	for _, m := range rep.MetaPacks {
		fmt.Fprintf(&b, "  meta %-40s status=%s removable=%v\n", m.ID, m.Status, m.Removable)
	}
	for _, d := range rep.Diagnostics {
		fmt.Fprintf(&b, "  %-7s %-24s pack=%s unit=%s — %s\n",
			SeverityForCode(d.Code), d.Code, d.PackID, d.UnitID, d.Message)
	}
	if rep.OK {
		b.WriteString("validate: OK\n")
	} else {
		fmt.Fprintf(&b, "validate: FAIL — %d diagnostic(s) mean content the desired state asks for is not in effect\n",
			ErrorDiagnosticCount(rep.Diagnostics))
	}
	return b.String()
}
