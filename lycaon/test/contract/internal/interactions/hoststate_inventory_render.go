package interactions

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

func computeInventoryGaps(rep *HostStateInventoryReport, cov *hoststate.CoverageReport) []InventoryGap {
	var gaps []InventoryGap
	for _, r := range rep.DomainClasses {
		if r.ProductionImpossible {
			if strings.TrimSpace(r.ImpossibilityReason) == "" {
				gaps = append(gaps, InventoryGap{
					Category: "unexplained_impossibility", InputDomain: r.InputDomain,
					ValueOrClass: r.ValueOrClass, Recipient: r.Recipient,
					ProductionSource: r.ProductionSource,
					Detail:           "marked production_impossible without production source reason",
				})
			}
			continue
		}
		if !r.DeterministicDelivered || !r.FuzzSelectable {
			gaps = append(gaps, InventoryGap{
				Category:         "domain_coverage_gap",
				InputDomain:      r.InputDomain,
				ValueOrClass:     r.ValueOrClass,
				Recipient:        r.Recipient,
				ProductionSource: r.ProductionSource,
				Detail: fmt.Sprintf("disposition=%s deterministic_delivered=%t fuzz_selectable=%t",
					r.Disposition, r.DeterministicDelivered, r.FuzzSelectable),
			})
		}
	}

	for _, fr := range rep.ReaderFactCoverage {
		gap := InventoryGap{
			InputDomain: "reader_fact", ValueOrClass: fr.FactName, Recipient: fr.Recipient,
			ProductionSource: "config/packs/painted-wolf/platform/host/reader-facts.yaml",
		}
		switch {
		case len(fr.ObservedClasses) == 0:
			gap.Category, gap.Detail = "unobserved_reader_fact", "zero observed classes on recipient"
		case len(fr.ObservedClasses) < 2 && fr.ConstraintSource == "":
			gap.Category = "constant_reader_fact_unexplained"
			gap.Detail = fmt.Sprintf("only observed %v without a production constraint", fr.ObservedClasses)
		case len(fr.ObservedClasses) >= 2 && fr.ConstraintSource != "":
			gap.Category = "stale_reader_fact_constraint"
			gap.Detail = fmt.Sprintf("observed %v although %s holds it constant", fr.ObservedClasses, fr.ConstraintSource)
		default:
			continue
		}
		gaps = append(gaps, gap)
	}

	for _, bp := range rep.BoundaryPartitions {
		if len(bp.GeneratedClasses) < 2 {
			gaps = append(gaps, InventoryGap{
				Category: "degenerate_envelope_boundary", InputDomain: "anchor_envelope:" + bp.AnchorID,
				ValueOrClass: bp.VarName, Recipient: "both",
				ProductionSource: "config/packs/painted-wolf/platform/host/anchors/catalog.yaml",
				Detail:           fmt.Sprintf("only %d boundary class(es): %v", len(bp.GeneratedClasses), bp.GeneratedClasses),
			})
		}
	}

	for _, u := range rep.UnmatchedProducers {
		gaps = append(gaps, InventoryGap{
			Category: "unmatched_producer_or_observation", InputDomain: "instruction_producer_join",
			ValueOrClass: u, Recipient: "both",
			ProductionSource: "test/contract/internal/interactions:BuildInteractionLedger",
			Detail:           "instruction primitive input has no matched authority domain or occurrence spec",
		})
	}

	if cov != nil {
		if err := cov.Validate(); err != nil {
			gaps = append(gaps, InventoryGap{
				Category: "dead_or_vacuous_branch", InputDomain: "branch_coverage",
				ValueOrClass: "template_or_oar_branches", Recipient: "both",
				ProductionSource: "test/contract/internal/hoststate:CoverageReport",
				Detail:           err.Error(),
			})
		}
	}

	slices.SortFunc(gaps, func(a, b InventoryGap) int {
		return strings.Compare(
			a.Category+"|"+a.InputDomain+"|"+a.Recipient+"|"+a.ValueOrClass,
			b.Category+"|"+b.InputDomain+"|"+b.Recipient+"|"+b.ValueOrClass,
		)
	})
	return gaps
}

// ValidateZeroUnexplainedGaps returns an error naming every unexplained domain,
// reader-fact, envelope-boundary, or unmatched-producer gap in rep.
func (rep *HostStateInventoryReport) ValidateZeroUnexplainedGaps() error {
	if rep == nil {
		return fmt.Errorf("nil host-state inventory report")
	}
	if len(rep.UnexplainedGaps) == 0 && len(rep.UnmatchedProducers) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "host-state inventory has %d unexplained gap(s) and %d unmatched producer(s):\n",
		len(rep.UnexplainedGaps), len(rep.UnmatchedProducers))
	for _, g := range rep.UnexplainedGaps {
		fmt.Fprintf(&b, "  - [%s] domain=%s recipient=%s value=%s (%s): %s\n",
			g.Category, g.InputDomain, g.Recipient, g.ValueOrClass, g.ProductionSource, g.Detail)
	}
	return fmt.Errorf("%s", strings.TrimRight(b.String(), "\n"))
}

// MarshalJSONBytes serializes rep deterministically with 2-space indentation.
func (rep *HostStateInventoryReport) MarshalJSONBytes() ([]byte, error) {
	return json.MarshalIndent(rep, "", "  ")
}

// FormatMarkdown renders a human-auditable Markdown receipt for rep.
func (rep *HostStateInventoryReport) FormatMarkdown() string {
	var b strings.Builder
	b.WriteString("# Host-state inventory\n\n")
	fmt.Fprintf(&b, "- **Catalog revision**: `%s`\n", rep.CatalogRevision)
	fmt.Fprintf(&b, "- **Coordinator states**: `%d` | **Worker states**: `%d`\n", rep.TotalCoordinatorStates, rep.TotalWorkerStates)
	fmt.Fprintf(&b, "- **Instruction sites**: `%d` | **Anchor specs**: `%d`\n", rep.TotalInstructionSites, rep.TotalAnchorSpecs)
	fmt.Fprintf(&b, "- **Matched producer inputs**: `%d` | **Unmatched producers**: `%d`\n", len(rep.MatchedProducerInputs), len(rep.UnmatchedProducers))
	fmt.Fprintf(&b, "- **Unexplained gaps**: `%d`\n\n", len(rep.UnexplainedGaps))

	if len(rep.UnexplainedGaps) > 0 {
		b.WriteString("## Unexplained gaps\n\n")
		b.WriteString("| Category | Domain | Recipient | Value or class | Source | Detail |\n")
		b.WriteString("|---|---|---|---|---|---|\n")
		for _, g := range rep.UnexplainedGaps {
			fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | `%s` | `%s` | %s |\n",
				g.Category, g.InputDomain, g.Recipient, g.ValueOrClass, g.ProductionSource, g.Detail)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Authority domains by recipient\n\n")
	b.WriteString("| Domain | Recipient | Value or class | Disposition | Delivered | Fuzz selectable | Resolved input | Delivered result or reason |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|\n")
	for _, r := range rep.DomainClasses {
		res := r.GuidanceRelevantResult
		if r.ProductionImpossible {
			res = "impossible: " + r.ImpossibilityReason
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | `%s` | `%t` | `%t` | `%s` | %s |\n",
			r.InputDomain, r.Recipient, r.ValueOrClass, r.Disposition,
			r.DeterministicDelivered, r.FuzzSelectable, r.ResolvedInputValue, res)
	}
	return b.String()
}
