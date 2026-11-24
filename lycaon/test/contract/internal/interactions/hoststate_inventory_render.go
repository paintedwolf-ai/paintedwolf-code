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
		if len(fr.ObservedClasses) == 0 {
			gaps = append(gaps, InventoryGap{
				Category: "unobserved_reader_fact", InputDomain: "reader_fact",
				ValueOrClass: fr.FactName, Recipient: fr.Recipient,
				ProductionSource: "config/packs/painted-wolf/platform/host/reader-facts.yaml",
				Detail:           "zero observed classes on recipient",
			})
			continue
		}
		if len(fr.ObservedClasses) < 2 && strings.TrimSpace(fr.ConstraintReason) == "" {
			gaps = append(gaps, InventoryGap{
				Category: "constant_reader_fact_unexplained", InputDomain: "reader_fact",
				ValueOrClass: fr.FactName, Recipient: fr.Recipient,
				ProductionSource: "config/packs/painted-wolf/platform/host/reader-facts.yaml",
				Detail:           fmt.Sprintf("only observed %v without production constraint reason", fr.ObservedClasses),
			})
		}
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
	fmt.Fprintf(&b, "host-state inventory has %d unexplained gap(s) and %d unmatched producer(s) [tree=%s constructors=%s]:\n",
		len(rep.UnexplainedGaps), len(rep.UnmatchedProducers), rep.SourceTree, rep.ConstructorSetLabel)
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
	fmt.Fprintf(&b, "# Host-State Authority & Producer Coverage Inventory (%s)\n\n", rep.ConstructorSetLabel)
	fmt.Fprintf(&b, "- **Source Commit**: `%s`\n", rep.SourceCommit)
	fmt.Fprintf(&b, "- **Source Tree**: `%s`\n", rep.SourceTree)
	fmt.Fprintf(&b, "- **Catalog Revision**: `%s`\n", rep.CatalogRevision)
	fmt.Fprintf(&b, "- **Coordinator States**: `%d` | **Worker States**: `%d`\n", rep.TotalCoordinatorStates, rep.TotalWorkerStates)
	fmt.Fprintf(&b, "- **Joined Instruction Sites**: `%d` | **Joined Anchor Specs**: `%d`\n", rep.TotalInstructionSites, rep.TotalAnchorSpecs)
	fmt.Fprintf(&b, "- **Matched Producer Inputs**: `%d` | **Unmatched Producers**: `%d`\n", len(rep.MatchedProducerInputs), len(rep.UnmatchedProducers))
	fmt.Fprintf(&b, "- **Unexplained Gaps**: `%d`\n\n", len(rep.UnexplainedGaps))

	if len(rep.UnexplainedGaps) > 0 {
		b.WriteString("## Unexplained Current Gaps\n\n")
		b.WriteString("| Category | Domain | Recipient | Value / Class | Source | Detail |\n")
		b.WriteString("|---|---|---|---|---|---|\n")
		for _, g := range rep.UnexplainedGaps {
			fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | `%s` | `%s` | %s |\n",
				g.Category, g.InputDomain, g.Recipient, g.ValueOrClass, g.ProductionSource, g.Detail)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Authority Domain & Recipient Matrix\n\n")
	b.WriteString("| Domain | Recipient | Value / Class | Disposition | Det. Delivered | Fuzz Selectable | Resolved Input | Guidance-Relevant Result / Reason |\n")
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
