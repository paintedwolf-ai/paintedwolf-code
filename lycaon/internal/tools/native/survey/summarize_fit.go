package survey

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools/docrefs"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/odvcencio/gotreesitter"
	"golang.org/x/mod/modfile"
)

const callSiteExcerptMaxRunes = 120

// fitDocExts identifies documentation targets by extension.

var fitDocExts = map[string]bool{
	".md": true, ".markdown": true, ".txt": true, ".rst": true, ".adoc": true,
}

// gatherFitEdges builds one-hop context for single-file summaries.

func (g *summaryRelations) gatherFitEdges(
	ctx context.Context,
	req summarize.Request,
	structure []summarize.StructureCandidate,
	stats summarize.GatherStats,
) (summarize.FitEdges, error) {
	if !stats.PathIsFile || stats.HasPattern || stats.PathIsDir {
		return summarize.FitEdges{}, nil
	}
	if g.caps.Gather.NeighborMax == 0 && g.caps.Gather.CallSiteMax == 0 && g.caps.Gather.ImportEdgeMax == 0 {
		return summarize.FitEdges{}, nil
	}
	var target *summarize.StructureCandidate
	for i := range structure {
		sc := &structure[i]
		if sc.Kind != summarize.StructureKindFile || sc.RelPath == "" || sc.RelPath == "inline" {
			continue
		}
		if target != nil {
			// Multi-file structure under a "file" flag does not expand fit.
			return summarize.FitEdges{}, nil
		}
		target = sc
	}
	if target == nil {
		return summarize.FitEdges{}, nil
	}
	if isFitDocTarget(target.RelPath, target.Language) {
		return summarize.FitEdges{Neighbors: g.fitDocNeighbors(ctx, *target)}, nil
	}
	return g.fitCodeEdges(ctx, *target)
}

func isFitDocTarget(rel, language string) bool {
	if strings.EqualFold(strings.TrimSpace(language), "markdown") {
		return true
	}
	return fitDocExts[strings.ToLower(filepath.Ext(rel))]
}

func (g *summaryRelations) fitDocNeighbors(ctx context.Context, target summarize.StructureCandidate) []summarize.PackNeighbor {
	if g.caps.Gather.NeighborMax <= 0 {
		return nil
	}
	resolved, err := g.access.reads.Resolve(ctx, target.RelPath)
	if err != nil {
		return nil
	}
	raw, err := sourceview.ReadContentCapped("summarize", sourceview.AccessRead, resolved.EffectLocation(), target.RelPath, resolved.Compressed, sourceview.AccessRead.MaxBytes())
	if err != nil {
		return nil
	}
	doc, _, err := textfile.Open(raw, textfile.LimitsForRaw(int64(len(raw))))
	if err != nil {
		return nil
	}
	body := doc.Text()
	tokens := docrefs.Extract(body)
	type hit struct {
		path string
		n    int
	}
	var hits []hit
	seen := map[string]bool{}
	docDir := path.Dir(target.RelPath)
	for _, tok := range tokens {
		display, ok := g.resolveDocRef(ctx, docDir, tok)
		if !ok || display == "" || display == target.RelPath || seen[display] {
			continue
		}
		seen[display] = true
		hits = append(hits, hit{path: display, n: strings.Count(body, tok)})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].n != hits[j].n {
			return hits[i].n > hits[j].n
		}
		return hits[i].path < hits[j].path
	})
	max := g.caps.Gather.NeighborMax
	if len(hits) > max {
		hits = hits[:max]
	}
	out := make([]summarize.PackNeighbor, 0, len(hits))
	for _, h := range hits {
		why := "doc_ref"
		if h.n > 1 {
			why = fmt.Sprintf("doc_ref x%d", h.n)
		}
		out = append(out, summarize.PackNeighbor{Path: h.path, Why: why})
	}
	return out
}

// resolveDocRef resolves references to files.

func (g *summaryRelations) resolveDocRef(ctx context.Context, docDir, tok string) (string, bool) {
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
		info, err := os.Stat(resolved.Abs)
		if err != nil || info.IsDir() {
			continue
		}
		return resolved.DisplayPath, true
	}
	return "", false
}

func (g *summaryRelations) fitCodeEdges(ctx context.Context, target summarize.StructureCandidate) (summarize.FitEdges, error) {
	tokens := fitGrepTokens(target)
	files, err := g.fitCodeReferenceCounts(ctx, g.referenceRoot(ctx, target.RelPath), target.RelPath, tokens)
	if err != nil {
		return summarize.FitEdges{}, err
	}
	return summarize.FitEdges{
		Neighbors: g.fitCodeNeighbors(files),
		CallSites: g.fitCodeCallSites(files),
		Imports:   g.fitImportEdges(ctx, target, files),
	}, nil
}

func (g *summaryRelations) referenceRoot(ctx context.Context, target string) string {
	resolved, err := g.access.reads.Resolve(ctx, target)
	if err != nil {
		return "."
	}
	return g.access.qualify(resolved.Root, resolved.Root.Path)
}

type fitFileHit struct {
	path    string
	count   int
	score   int
	terms   map[string]int
	samples []grepMatch
}

func (g *summaryRelations) fitCodeReferenceCounts(ctx context.Context, grepRoot, targetPath string, tokens []string) ([]fitFileHit, error) {
	byPath := map[string]*fitFileHit{}
	docFreq := make(map[string]int, len(tokens))
	err := g.access.literals(ctx, grepRoot, tokens, g.nested.surveyPruneOpts(sandbox.SurveyOptions{}), func(m grepMatch) {
		if m.Path == "" || m.Path == targetPath {
			return
		}
		fh := byPath[m.Path]
		if fh == nil {
			fh = &fitFileHit{path: m.Path, terms: map[string]int{}}
			byPath[m.Path] = fh
		}
		count := max(1, m.Count)
		if fh.terms[m.Match] == 0 {
			docFreq[m.Match]++
		}
		fh.terms[m.Match] += count
		fh.count += count
		fh.samples = append(fh.samples, m)
	})
	if err != nil {
		return nil, err
	}
	files := make([]fitFileHit, 0, len(byPath))
	for _, fh := range byPath {
		for term, count := range fh.terms {
			fh.score += min(count, 4) * (len(byPath) + 1) * 1024 / (docFreq[term] + 1)
		}
		files = append(files, *fh)
	}
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].score != files[j].score {
			return files[i].score > files[j].score
		}
		if files[i].count != files[j].count {
			return files[i].count > files[j].count
		}
		return files[i].path < files[j].path
	})
	return files, nil
}

func (g *summaryRelations) fitCodeNeighbors(files []fitFileHit) []summarize.PackNeighbor {
	if g.caps.Gather.NeighborMax <= 0 {
		return nil
	}
	files = files[:min(len(files), g.caps.Gather.NeighborMax)]
	neighbors := make([]summarize.PackNeighbor, 0, len(files))
	for _, file := range files {
		neighbors = append(neighbors, summarize.PackNeighbor{
			Path: file.path,
			Why:  fmt.Sprintf("ref x%d", file.count),
		})
	}
	return neighbors
}

func (g *summaryRelations) fitCodeCallSites(files []fitFileHit) []summarize.PackCallSite {
	limit := g.caps.Gather.CallSiteMax
	if limit <= 0 {
		return nil
	}
	sites := make([]summarize.PackCallSite, 0, limit)
	for _, file := range files {
		for _, m := range file.samples {
			if len(sites) >= limit {
				return sites
			}
			sites = append(sites, summarize.PackCallSite{
				Path: m.Path, Line: m.Line,
				Excerpt: clampExcerpt(strings.TrimSpace(m.Content), callSiteExcerptMaxRunes),
			})
		}
	}
	return sites
}

// fitImportEdges builds one-hop inbound and outbound module edges.

func (g *summaryRelations) fitImportEdges(ctx context.Context, target summarize.StructureCandidate, files []fitFileHit) []summarize.PackImportEdge {
	max := g.caps.Gather.ImportEdgeMax
	if max <= 0 {
		return nil
	}
	var out []summarize.PackImportEdge
	seen := map[string]bool{}
	add := func(e summarize.PackImportEdge) {
		if len(out) >= max {
			return
		}
		key := e.Kind + "\x00" + e.From + "\x00" + e.To
		if e.From == "" || e.To == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, e)
	}

	resolved, err := g.access.reads.Resolve(ctx, target.RelPath)
	if err == nil {
		content, rerr := g.sources.readFileCached(ctx, resolved.Abs)
		if rerr == nil {
			for _, mod := range outboundImportModules(ctx, target.RelPath, content) {
				add(summarize.PackImportEdge{
					From: target.RelPath, To: mod, Kind: "outbound",
				})
			}
		}
	}

	if len(out) >= max {
		return out
	}
	importTok := strings.TrimSpace(target.ImportPath)
	if importTok == "" {
		return out
	}
	for _, f := range files {
		if len(out) >= max {
			break
		}
		if f.terms[importTok] == 0 {
			continue
		}
		add(summarize.PackImportEdge{
			From: f.path, To: importTok, Kind: "inbound",
		})
	}
	return out
}

// outboundImportModules returns structured import module paths.

func outboundImportModules(ctx context.Context, rel string, content []byte) []string {
	entry := filekind.Detect(ctx, filekind.DetectReq{
		Filename: filepath.Base(rel),
		Mode:     filekind.DepthShallow,
	}).Grammar
	if entry == nil || entry.Language() == nil {
		return nil
	}
	refs := gotreesitter.ExtractImportsFromSource(entry.Language(), content)
	seen := map[string]bool{}
	var out []string
	for _, r := range refs {
		switch r.Kind {
		case "import", "from_import", "load":
		default:
			continue // skip package clause etc.
		}
		mod := strings.TrimSpace(r.Path)
		if mod == "" || seen[mod] {
			continue
		}
		seen[mod] = true
		out = append(out, mod)
	}
	return out
}

func fitGrepTokens(target summarize.StructureCandidate) []string {
	seen := map[string]bool{}
	var out []string
	add := func(tok string) {
		tok = strings.TrimSpace(tok)
		if tok == "" || seen[tok] {
			return
		}
		seen[tok] = true
		out = append(out, tok)
	}
	if target.ImportPath != "" {
		add(target.ImportPath)
	}
	for _, sym := range target.Symbols {
		add(sym.Name)
	}
	return out
}

// clampExcerpt bounds a call-site excerpt. No ellipsis: the excerpt is matched
// verbatim against captured source when a citation is verified.

func clampExcerpt(s string, max int) string {
	if max <= 0 || s == "" {
		return s
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max])
}

// goPackageImportPath resolves the nearest module path.

func (g *summaryRelations) goPackageImportPath(ctx context.Context, rel string) string {
	if !strings.EqualFold(filepath.Ext(rel), ".go") {
		return ""
	}
	pkgDir := path.Dir(rel)
	if pkgDir == "." {
		pkgDir = ""
	}
	g.memoMu.Lock()
	if g.goImportByPkgDir != nil {
		if ip, ok := g.goImportByPkgDir[pkgDir]; ok {
			g.memoMu.Unlock()
			return ip
		}
	}
	g.memoMu.Unlock()

	ip := g.resolveGoPackageImportPath(ctx, rel)
	g.memoMu.Lock()
	if g.goImportByPkgDir == nil {
		g.goImportByPkgDir = map[string]string{}
	}
	g.goImportByPkgDir[pkgDir] = ip
	g.memoMu.Unlock()
	return ip
}

func (g *summaryRelations) resolveGoPackageImportPath(ctx context.Context, rel string) string {
	dir := path.Dir(rel)
	for {
		modRel := "go.mod"
		if dir != "" && dir != "." {
			modRel = path.Join(dir, "go.mod")
		}
		resolved, err := g.access.reads.Resolve(ctx, modRel)
		if err == nil {
			info, serr := os.Stat(resolved.Abs)
			if serr == nil && !info.IsDir() {
				f := g.parseGoModCached(ctx, resolved.Abs)
				if f == nil || f.Module == nil || f.Module.Mod.Path == "" {
					return ""
				}
				modRoot := path.Dir(resolved.DisplayPath)
				if modRoot == "." {
					modRoot = ""
				}
				pkgDir := path.Dir(rel)
				if pkgDir == "." {
					pkgDir = ""
				}
				if modRoot == "" {
					if pkgDir == "" {
						return f.Module.Mod.Path
					}
					return f.Module.Mod.Path + "/" + pkgDir
				}
				if pkgDir == modRoot {
					return f.Module.Mod.Path
				}
				if strings.HasPrefix(pkgDir, modRoot+"/") {
					return f.Module.Mod.Path + "/" + strings.TrimPrefix(pkgDir, modRoot+"/")
				}
				return f.Module.Mod.Path
			}
		}
		if dir == "" || dir == "." {
			return ""
		}
		parent := path.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func (g *summaryRelations) parseGoModCached(ctx context.Context, abs string) *modfile.File {
	g.memoMu.Lock()
	if g.goModByAbs != nil {
		if f, ok := g.goModByAbs[abs]; ok {
			g.memoMu.Unlock()
			return f
		}
	}
	g.memoMu.Unlock()
	data, err := g.sources.readFileCached(ctx, abs)
	if err != nil {
		return nil
	}
	f, perr := modfile.Parse("go.mod", data, nil)
	if perr != nil {
		return nil
	}
	g.memoMu.Lock()
	if g.goModByAbs == nil {
		g.goModByAbs = map[string]*modfile.File{}
	}
	g.goModByAbs[abs] = f
	g.memoMu.Unlock()
	return f
}
