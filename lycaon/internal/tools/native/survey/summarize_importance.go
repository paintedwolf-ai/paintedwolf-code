package survey

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/tools/docrefs"
)

var docSeedSurfaces = []string{
	"README.md",
	"AGENTS.md",
	"docs/README.md",
}

// computeSubtreeImportance ranks children from one-hop references.

func (g *summaryRelations) computeSubtreeImportance(
	ctx context.Context,
	target string,
	subtree *summarize.SubtreeNode,
	structure []summarize.StructureCandidate,
) (summarize.SubtreeImportance, error) {
	if subtree == nil || len(subtree.Children) == 0 {
		return nil, nil
	}
	docMax := g.caps.Pack.SubtreeDoclinkMax
	fanMax := g.caps.Pack.SubtreeFaninMax
	if docMax <= 0 && fanMax <= 0 {
		return nil, nil
	}
	imp := summarize.SubtreeImportance{}
	if docMax > 0 {
		links := g.seedDocLinkTargets(ctx, target, docMax)
		imp = summarize.AccrueDocLinks(subtree, links, docMax, imp)
	}
	if fanMax > 0 {
		targets, err := g.seedFanInTargets(ctx, target, subtree, structure, fanMax)
		if err != nil {
			return nil, err
		}
		imp = summarize.AccrueFanIn(subtree, targets, fanMax, imp)
	}
	if len(imp) == 0 {
		return nil, nil
	}
	return imp, nil
}

func (g *summaryRelations) seedDocLinkTargets(ctx context.Context, target string, max int) []string {
	if max <= 0 {
		return nil
	}
	base := strings.TrimSuffix(filepath.ToSlash(strings.TrimSpace(target)), "/")
	if base == "." {
		base = ""
	}
	type seedFile struct {
		rel  string
		body string
	}
	files := make([]seedFile, len(docSeedSurfaces))
	for i, surface := range docSeedSurfaces {
		rel := surface
		if base != "" {
			rel = path.Join(base, surface)
		}
		resolved, err := g.access.reads.Resolve(ctx, rel)
		if err != nil {
			continue
		}
		info, err := os.Stat(resolved.Abs)
		if err != nil || info.IsDir() {
			continue
		}
		content, err := g.sources.readFileCached(ctx, resolved.Abs)
		if err != nil {
			continue
		}
		files[i] = seedFile{rel: rel, body: string(content)}
	}

	var out []string
	seen := map[string]bool{}
	for _, f := range files {
		if f.rel == "" || f.body == "" {
			continue
		}
		if len(out) >= max {
			break
		}
		docDir := path.Dir(f.rel)
		for _, tok := range docrefs.Extract(f.body) {
			if len(out) >= max {
				break
			}
			display, ok := g.resolveDocRefAny(ctx, docDir, tok)
			if !ok || display == "" || seen[display] {
				continue
			}
			seen[display] = true
			out = append(out, display)
		}
	}
	return out
}

// resolveDocRefAny resolves file or directory references.

func (g *summaryRelations) resolveDocRefAny(ctx context.Context, docDir, tok string) (string, bool) {
	tok = strings.TrimPrefix(filepath.ToSlash(tok), "./")
	candidates := []string{tok}
	if docDir != "" && docDir != "." {
		candidates = append(candidates, path.Join(docDir, tok))
	}
	for _, cand := range candidates {
		cand = path.Clean(cand)
		if cand == "." || strings.HasPrefix(cand, "../") {
			continue
		}
		resolved, err := g.access.reads.Resolve(ctx, cand)
		if err != nil {
			continue
		}
		if g.access.catalog != nil {
			current := g.access.catalog.Current(ctx, g.access.projectID, []sourcecatalog.Root{{ID: resolved.Root.ID, Path: resolved.Root.Path}})
			if current.State == sourcecatalog.StateReady {
				rel := projectroot.ScopeRel(resolved.Root, resolved.Abs)
				if _, ok := current.Entry(resolved.Root.ID, rel); ok {
					return resolved.DisplayPath, true
				}
			}
		}
		if _, err := os.Stat(resolved.Abs); err != nil {
			continue
		}
		return resolved.DisplayPath, true
	}
	return "", false
}

// seedFanInTargets maps inbound imports to child representatives.

func (g *summaryRelations) seedFanInTargets(
	ctx context.Context,
	target string,
	subtree *summarize.SubtreeNode,
	structure []summarize.StructureCandidate,
	limit int,
) ([]string, error) {
	if limit <= 0 || subtree == nil {
		return nil, nil
	}
	grepPasses := g.caps.Pack.SubtreeFaninGrepMax
	if grepPasses <= 0 {
		return nil, nil
	}
	contention := summarize.ContentionBandChildren(subtree)
	if len(contention) == 0 {
		return nil, nil
	}

	type pkgHit struct {
		childPath string
		filePath  string
	}
	packages := map[string]pkgHit{}
	for _, child := range contention {
		tok, filePath, ok := g.resolveFanInToken(ctx, child, structure)
		if !ok {
			continue
		}
		if _, exists := packages[tok]; !exists {
			packages[tok] = pkgHit{childPath: child.Path, filePath: filePath}
		}
	}
	if len(packages) == 0 {
		return nil, nil
	}

	tokens := make([]string, 0, len(packages))
	for tok := range packages {
		tokens = append(tokens, tok)
	}
	sort.Strings(tokens)

	if g.faninGrepPasses != nil {
		*g.faninGrepPasses++
	}
	grepRoot := g.referenceRoot(ctx, target)
	counts := make(map[string]int, len(packages))
	err := g.access.literals(ctx, grepRoot, tokens, g.nested.surveyPruneOpts(sandbox.SurveyOptions{}), func(m grepMatch) {
		if m.Path == "" || m.Match == "" {
			return
		}
		hit, ok := packages[m.Match]
		if !ok || m.Path == hit.filePath {
			return
		}
		prefix := strings.TrimSuffix(hit.childPath, "/") + "/"
		if m.Path == hit.childPath || strings.HasPrefix(m.Path, prefix) {
			return
		}
		counts[m.Match] += max(1, m.Count)
	})
	if err != nil {
		return nil, err
	}

	var out []string
	for _, tok := range tokens {
		hit := packages[tok]
		for range min(counts[tok], limit-len(out)) {
			out = append(out, hit.filePath)
		}
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (g *summaryRelations) resolveFanInToken(
	ctx context.Context,
	child *summarize.SubtreeNode,
	structure []summarize.StructureCandidate,
) (tok, filePath string, ok bool) {
	rel := summarize.RepresentativeFilePath(child)
	if rel == "" && child != nil && child.Kind == summarize.SubtreeKindDir {
		rel = g.shallowRepresentativeFile(ctx, child.Path)
	}
	if rel == "" {
		return "", "", false
	}
	for _, sc := range structure {
		if sc.RelPath != rel {
			continue
		}
		tok = strings.TrimSpace(sc.ImportPath)
		if tok != "" {
			return tok, sc.RelPath, true
		}
	}
	if sc, outlined := g.sources.Outline(ctx, rel); outlined {
		tok = strings.TrimSpace(sc.ImportPath)
		if tok != "" {
			return tok, sc.RelPath, true
		}
	}
	if tok = g.goPackageImportPath(ctx, rel); tok != "" {
		return tok, rel, true
	}
	return "", "", false
}

// shallowRepresentativeFile finds a direct child file.

func (g *summaryRelations) shallowRepresentativeFile(ctx context.Context, display string) string {
	if g == nil || strings.TrimSpace(display) == "" {
		return ""
	}
	resolved, err := g.access.reads.Resolve(ctx, display)
	if err != nil {
		return ""
	}
	inventory, err := sourceInventoryForScope(ctx, g.access.catalog, g.access.projectID, resolved.Root, resolved.Abs)
	if err != nil {
		return ""
	}
	readFilter, err := g.access.boundary.CompileReadFilter(ctx, resolved.Root.Path, g.access.profileID)
	if err != nil {
		return ""
	}
	child := ""
	_ = inventory.walk(ctx, func(entry sourcecatalog.Entry) sourcecatalog.WalkStep {
		if entry.IsDir {
			return sourcecatalog.WalkSkip
		}
		if entry.IsSymlink || (readFilter != nil && !readFilter(entry.Path, false)) {
			return sourcecatalog.WalkContinue
		}
		child = strings.TrimPrefix(path.Join(display, catalogRelativePath(entry.Path, inventory.base)), "./")
		return sourcecatalog.WalkStop
	})
	return child
}
