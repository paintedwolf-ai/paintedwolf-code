package repoinfo

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

// BriefTier controls how much layout detail a root brief carries.
type BriefTier int

const (
	BriefFull BriefTier = iota
	BriefSummary
)

// BriefBudget caps the composed multi-root orientation brief.
type BriefBudget struct {
	TotalBytes   int
	PrimaryShare float64
}

// DefaultBriefBudget is the combined byte ceiling for multi-root orientation.
func DefaultBriefBudget() BriefBudget {
	return BriefBudget{TotalBytes: 4096, PrimaryShare: 0.6}
}

// RootBrief is one root's analyzed brief within a multi-root composition.
type RootBrief struct {
	Label     string
	Path      string
	IsPrimary bool
	Brief     *Brief
	Tier      BriefTier
	Truncated bool
}

// MultiRootBrief composes per-root briefs primary-first.
type MultiRootBrief struct {
	Roots []RootBrief
}

// AnalyzeRoots composes cached or freshly analyzed roots within one budget.
func AnalyzeRoots(ctx context.Context, roots []projectroot.RootRef, budget BriefBudget, repo Provider) (*MultiRootBrief, error) {
	if len(roots) == 0 {
		return &MultiRootBrief{}, nil
	}
	if repo == nil {
		return nil, fmt.Errorf("repo provider required")
	}
	if budget.TotalBytes <= 0 {
		budget = DefaultBriefBudget()
	}
	if budget.PrimaryShare <= 0 || budget.PrimaryShare > 1 {
		budget.PrimaryShare = DefaultBriefBudget().PrimaryShare
	}
	out := &MultiRootBrief{Roots: make([]RootBrief, 0, len(roots))}
	for _, r := range roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := strings.TrimSpace(r.Path)
		if path == "" {
			continue
		}
		brief, err := rootBrief(ctx, repo, path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		tier := BriefSummary
		if r.IsPrimary {
			tier = BriefFull
		}
		if tier == BriefSummary {
			brief = summarizeBrief(brief)
		}
		out.Roots = append(out.Roots, RootBrief{
			Label:     r.Label,
			Path:      path,
			IsPrimary: r.IsPrimary,
			Brief:     brief,
			Tier:      tier,
		})
	}
	applyCombinedBudget(out, budget)
	return out, nil
}

func rootBrief(ctx context.Context, repo Provider, path string) (*Brief, error) {
	brief, err := repo.Brief(ctx, path)
	if err != nil {
		return nil, err
	}
	return cloneBrief(brief), nil
}

func summarizeBrief(full *Brief) *Brief {
	if full == nil {
		return emptyBrief()
	}
	out := cloneBrief(full)
	if len(out.Layout.Files) > 0 {
		out.Layout = api.RepoLayout{TopLevel: layoutTopLevelNames(out.Layout)}
	}
	if len(out.Languages) > 3 {
		out.Languages = out.Languages[:3]
	}
	return out
}

func layoutTopLevelNames(layout api.RepoLayout) []string {
	if len(layout.TopLevel) > 0 {
		return append([]string(nil), layout.TopLevel...)
	}
	seen := map[string]struct{}{}
	for _, f := range layout.Files {
		parts := strings.SplitN(strings.ReplaceAll(f, "\\", "/"), "/", 2)
		if len(parts) == 1 {
			seen[parts[0]] = struct{}{}
		} else {
			seen[parts[0]+"/"] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

func applyCombinedBudget(mrb *MultiRootBrief, budget BriefBudget) {
	if mrb == nil || len(mrb.Roots) <= 1 {
		return
	}
	primaryCap := int(float64(budget.TotalBytes) * budget.PrimaryShare)
	secondaryCap := budget.TotalBytes - primaryCap
	if secondaryCap < 0 {
		secondaryCap = 0
	}
	secondaryCount := 0
	for _, r := range mrb.Roots {
		if !r.IsPrimary {
			secondaryCount++
		}
	}
	perSecondary := secondaryCap
	if secondaryCount > 0 {
		perSecondary = secondaryCap / secondaryCount
	}
	for i := range mrb.Roots {
		cap := perSecondary
		if mrb.Roots[i].IsPrimary {
			cap = primaryCap
		}
		if cap <= 0 {
			continue
		}
		mrb.Roots[i].Brief, mrb.Roots[i].Truncated = truncateBriefToBytes(mrb.Roots[i].Brief, cap)
	}
}

func truncateBriefToBytes(b *Brief, max int) (*Brief, bool) {
	if b == nil || max <= 0 {
		return b, false
	}
	repo := WireBrief(b)
	lines := packboard.RepoOrientationLines(repo)
	body := strings.Join(lines, "\n")
	if len(body) <= max {
		return b, false
	}
	out := cloneBrief(b)
	for len(lines) > 1 && len(strings.Join(lines, "\n")) > max {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return out, true
	}
	out.FileCount = b.FileCount
	if len(out.Languages) > 1 {
		out.Languages = out.Languages[:1]
	}
	out.Layout = api.RepoLayout{}
	if len(lines) > 1 {
		out.Layout = b.Layout
		if len(out.Layout.Files) > 3 {
			out.Layout.Files = out.Layout.Files[:3]
		}
		if len(out.Layout.TopLevel) > 5 {
			out.Layout.TopLevel = out.Layout.TopLevel[:5]
		}
	}
	return out, true
}

// WireBrief maps internal brief facts to board wire shape.
func WireBrief(b *Brief) api.RepoBrief {
	if b == nil {
		return api.RepoBrief{Languages: []string{}}
	}
	return api.RepoBrief{
		Languages:   append([]string{}, b.Languages...),
		FileCount:   b.FileCount,
		Layout:      b.Layout,
		GeneratedAt: b.GeneratedAt,
		Refreshing:  b.Refreshing,
		Incomplete:  b.Partial,
	}
}

// RefreshingBrief stands in for a brief that is not materialized yet.
func RefreshingBrief() api.RepoBrief {
	return api.RepoBrief{Languages: []string{}, Refreshing: true}
}

// PrimaryRepoBrief returns the materialized primary brief.
func (m *MultiRootBrief) PrimaryRepoBrief() api.RepoBrief {
	if m == nil || len(m.Roots) == 0 || m.Roots[0].Brief == nil || !m.Roots[0].Brief.Materialized {
		return RefreshingBrief()
	}
	return WireBrief(m.Roots[0].Brief)
}

// PrimaryMaterialized reports whether a measurement exists, including partial coverage.
func (m *MultiRootBrief) PrimaryMaterialized() bool {
	if m == nil || len(m.Roots) == 0 || m.Roots[0].Brief == nil {
		return false
	}
	return m.Roots[0].Brief.Materialized
}

// OrientationRoots returns materialized board sections.
func (m *MultiRootBrief) OrientationRoots() []api.BoardOrientationRoot {
	if m == nil {
		return nil
	}
	out := make([]api.BoardOrientationRoot, 0, len(m.Roots))
	for _, r := range m.Roots {
		if r.Brief == nil || !r.Brief.Materialized {
			continue
		}
		out = append(out, api.BoardOrientationRoot{
			Label:     r.Label,
			Path:      r.Path,
			IsPrimary: r.IsPrimary,
			Brief:     WireBrief(r.Brief),
			Truncated: r.Truncated,
		})
	}
	return out
}

// RenderedBytes estimates composed repo orientation size for budget tests.
func (m *MultiRootBrief) RenderedBytes() int {
	if m == nil {
		return 0
	}
	if len(m.Roots) <= 1 {
		if len(m.Roots) == 0 {
			return 0
		}
		return len(strings.Join(packboard.RepoOrientationLines(WireBrief(m.Roots[0].Brief)), "\n"))
	}
	total := 0
	for _, r := range m.Roots {
		header := sectionHeader(r.Label, r.IsPrimary)
		lines := packboard.RepoOrientationLines(WireBrief(r.Brief))
		total += len(header) + len(strings.Join(lines, "\n")) + 2
		if r.Truncated {
			total += len("(brief trimmed to fit budget)")
		}
	}
	return total
}

func sectionHeader(label string, primary bool) string {
	if primary {
		if label == "" {
			return "## (primary)"
		}
		return "## " + label + " (primary)"
	}
	if label == "" {
		return "## @root"
	}
	return "## @" + label
}
