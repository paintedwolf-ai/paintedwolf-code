package summarize

import (
	"context"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/textrank"
)

// childNameIndex caches names and imports before drill selection.
type childNameIndex struct {
	Names      map[string][]string           // child Path → symbol / tag names
	ImportPath map[string]string             // child Path → module import path
	Outlined   map[string]StructureCandidate // file Path → outline (reuse on drill)
}

func taskDepthActive(alpha float64, scores map[string]float64) bool {
	if alpha <= 0 || len(scores) == 0 {
		return false
	}
	for _, s := range scores {
		if s > 0 {
			return true
		}
	}
	return false
}

func maxTaskScore(scores map[string]float64) float64 {
	var max float64
	for _, s := range scores {
		if s > max {
			max = s
		}
	}
	return max
}

// collectChildNameIndex gathers names for fanout children.
func collectChildNameIndex(
	ctx context.Context,
	children []*SubtreeNode,
	structure []StructureCandidate,
	provider OutlineProvider,
	nameIndexMax int,
	stats *CuratorStats,
) childNameIndex {
	out := childNameIndex{
		Names:      map[string][]string{},
		ImportPath: map[string]string{},
		Outlined:   map[string]StructureCandidate{},
	}
	if len(children) == 0 {
		return out
	}
	idx := newStructureIndex(structure)
	var needOutline []int // indices into children, in fanout order (caps nameIndexMax)
	for i, c := range children {
		if c == nil || strings.HasPrefix(c.Path, "+") {
			continue
		}
		scoped := idx.forNode(c)
		names, imp := namesFromStructure(scoped)
		if len(names) > 0 || imp != "" {
			if len(names) > 0 {
				out.Names[c.Path] = names
			}
			if imp != "" {
				out.ImportPath[c.Path] = imp
			}
			for _, sc := range scoped {
				if sc.Kind == StructureKindFile && sc.RelPath != "" {
					out.Outlined[sc.RelPath] = sc
				}
			}
			continue
		}
		if provider == nil || nameIndexMax <= 0 {
			continue
		}
		if c.Kind != SubtreeKindFile {
			continue
		}
		if len(needOutline) >= nameIndexMax {
			continue
		}
		needOutline = append(needOutline, i)
	}
	if len(needOutline) == 0 {
		return out
	}
	type outlineResult struct {
		sc StructureCandidate
		ok bool
	}
	results := make([]outlineResult, len(needOutline))
	for j, childIdx := range needOutline {
		var sc StructureCandidate
		var ok bool
		if budgeted, yes := provider.(*curatorOutliner); yes {
			sc, ok = budgeted.indexOutline(ctx, children[childIdx].Path)
		} else {
			sc, ok = provider.Outline(ctx, children[childIdx].Path)
		}
		results[j] = outlineResult{sc: sc, ok: ok}
	}

	for j, childIdx := range needOutline {
		r := results[j]
		if !r.ok {
			continue
		}
		c := children[childIdx]
		if stats != nil {
			stats.FilesOutlined++
			stats.NameIndexed++
		}
		out.Outlined[c.Path] = r.sc
		n, ip := namesFromStructure([]StructureCandidate{r.sc})
		if len(n) > 0 {
			out.Names[c.Path] = n
		}
		if ip != "" {
			out.ImportPath[c.Path] = ip
		}
	}
	return out
}

func namesFromStructure(scoped []StructureCandidate) (names []string, importPath string) {
	seen := map[string]bool{}
	for _, sc := range scoped {
		if importPath == "" {
			importPath = strings.TrimSpace(sc.ImportPath)
		}
		for _, sym := range sc.Symbols {
			n := strings.TrimSpace(sym.Name)
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			names = append(names, n)
		}
		for _, tag := range sc.RollupRows {
			n := strings.TrimSpace(tag)
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			names = append(names, n)
		}
	}
	return names, importPath
}

// childTaskScores ranks paths and indexed symbol names.
func childTaskScores(task string, children []*SubtreeNode, index childNameIndex) map[string]float64 {
	if len(taskQueryTerms(task)) == 0 || len(children) < 2 {
		return nil
	}
	var nodes []*SubtreeNode
	var docs [][]textrank.Field
	for _, c := range children {
		if c == nil {
			continue
		}
		nodes = append(nodes, c)
		docs = append(docs, []textrank.Field{
			{Text: c.Path, Weight: 3},
			{Text: strings.Join(index.Names[c.Path], " "), Weight: 4},
		})
	}
	if len(nodes) < 2 {
		return nil
	}
	scores := taskFieldScores(task, docs)
	out := make(map[string]float64, len(nodes))
	for i, c := range nodes {
		out[c.Path] = scores[i]
	}
	return out
}

// applyImportPullThrough connects task hits across one import edge.
func applyImportPullThrough(
	scores map[string]float64,
	parent *SubtreeNode,
	imports []PackImportEdge,
	index childNameIndex,
) map[string]float64 {
	if len(scores) == 0 || parent == nil || len(imports) == 0 {
		return scores
	}
	max := maxTaskScore(scores)
	if max <= 0 {
		return scores
	}
	// module import path → child Path
	byImport := map[string]string{}
	for path, ip := range index.ImportPath {
		if ip != "" {
			byImport[ip] = path
		}
	}
	resolve := func(ref string) string {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return ""
		}
		if c := childContaining(parent, ref); c != nil {
			return c.Path
		}
		if p, ok := byImport[ref]; ok {
			return p
		}
		// Suffix match: edge To is a full module path; child ImportPath may equal it.
		for ip, path := range byImport {
			if ref == ip || strings.HasSuffix(ref, "/"+pathBase(ip)) {
				return path
			}
		}
		return ""
	}
	const pullFrac = 0.5
	for _, e := range imports {
		a := resolve(e.From)
		b := resolve(e.To)
		if a == "" || b == "" || a == b {
			continue
		}
		sa, sb := scores[a], scores[b]
		if sa > 0 && sb == 0 {
			scores[b] = sa * pullFrac
		} else if sb > 0 && sa == 0 {
			scores[a] = sb * pullFrac
		}
	}
	return scores
}

func pathBase(p string) string {
	p = strings.TrimSuffix(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// rankChildrenTaskDepth combines task, material, and importance ranks.
func rankChildrenTaskDepth(node *SubtreeNode, imp SubtreeImportance, taskScores map[string]float64) []*SubtreeNode {
	if node == nil || len(node.Children) == 0 {
		return nil
	}
	if !taskDepthActive(1, taskScores) {
		return rankChildrenImportance(node, imp, "")
	}
	out := append([]*SubtreeNode(nil), node.Children...)
	useImp := len(imp) > 0
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		sa, sb := taskScores[a.Path], taskScores[b.Path]
		if sa != sb {
			return sa > sb
		}
		ma, mb := material(a), material(b)
		if ma != mb {
			return ma > mb
		}
		if useImp {
			ia, ib := importanceScore(imp[a.Path]), importanceScore(imp[b.Path])
			if ia != ib {
				return ia > ib
			}
		}
		if a.Material.Bytes != b.Material.Bytes {
			return a.Material.Bytes > b.Material.Bytes
		}
		return a.Path < b.Path
	})
	return out
}

func recordTaskDepthBoosts(drilled, rolled []*SubtreeNode, scores map[string]float64, stats *CuratorStats) {
	if stats == nil || len(scores) == 0 || len(drilled) == 0 || len(rolled) == 0 {
		return
	}
	for _, d := range drilled {
		if d == nil || scores[d.Path] <= 0 {
			continue
		}
		for _, r := range rolled {
			if r == nil || material(d) != material(r) {
				continue
			}
			if scores[r.Path] <= 0 {
				stats.TaskDepthBoosts++
			}
		}
	}
}

// softStarveWeights reweights free-pool shares toward task-scoring children.
// Zero-score children get weight ≈ 0 so they keep only the coverage floor.
func softStarveWeights(children []*SubtreeNode, materialWeights []float64, scores map[string]float64, alpha float64) []float64 {
	if len(children) == 0 {
		return materialWeights
	}
	if !taskDepthActive(alpha, scores) {
		return materialWeights
	}
	max := maxTaskScore(scores)
	out := make([]float64, len(children))
	var sum float64
	for i, c := range children {
		w := 0.001
		if i < len(materialWeights) && materialWeights[i] > w {
			w = materialWeights[i]
		}
		s := scores[c.Path]
		w *= 1 + alpha*(s/max)
		if s <= 0 {
			w *= 0.25 // Preserve breadth when lexical matches are sparse.
		}
		out[i] = w
		sum += w
	}
	if sum <= 0 {
		return materialWeights
	}
	for i := range out {
		out[i] /= sum
	}
	return out
}
