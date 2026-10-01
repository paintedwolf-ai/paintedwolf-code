package summarize

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/textrank"
)

// ChildImportance holds grounded depth-tiebreak signals for one subtree child.
// Counts are machine state only — link targets and import edges.
type ChildImportance struct {
	DocLinks int
	FanIn    int
}

// SubtreeImportance maps child Path → signals. Keys are SubtreeNode.Path values
// for direct children of the ranked parent (and optionally deeper nodes).
type SubtreeImportance map[string]ChildImportance

func importanceScore(c ChildImportance) int {
	return c.DocLinks + c.FanIn
}

func childContaining(parent *SubtreeNode, targetPath string) *SubtreeNode {
	if parent == nil || targetPath == "" {
		return nil
	}
	targetPath = strings.TrimSuffix(targetPath, "/")
	for _, c := range parent.Children {
		if c == nil {
			continue
		}
		p := strings.TrimSuffix(c.Path, "/")
		if p == "" {
			continue
		}
		if targetPath == p || strings.HasPrefix(targetPath, p+"/") {
			return c
		}
	}
	return nil
}

// AccrueDocLinks increments DocLinks for children that contain each link target.
// Stops after maxTargets resolved mappings (maxTargets ≤ 0 → no-op).
func AccrueDocLinks(parent *SubtreeNode, linkTargets []string, maxTargets int, into SubtreeImportance) SubtreeImportance {
	if into == nil {
		into = SubtreeImportance{}
	}
	if parent == nil || maxTargets <= 0 {
		return into
	}
	n := 0
	for _, target := range linkTargets {
		if n >= maxTargets {
			break
		}
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		child := childContaining(parent, target)
		if child == nil || strings.HasPrefix(child.Path, "+") {
			continue
		}
		cur := into[child.Path]
		cur.DocLinks++
		into[child.Path] = cur
		n++
	}
	return into
}

// AccrueFanIn increments FanIn for children that contain each inbound edge
// target path (the imported module's file/dir under the child). Stops after
// maxEdges mappings (maxEdges ≤ 0 → no-op).
func AccrueFanIn(parent *SubtreeNode, inboundTargets []string, maxEdges int, into SubtreeImportance) SubtreeImportance {
	if into == nil {
		into = SubtreeImportance{}
	}
	if parent == nil || maxEdges <= 0 {
		return into
	}
	n := 0
	for _, target := range inboundTargets {
		if n >= maxEdges {
			break
		}
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		child := childContaining(parent, target)
		if child == nil || strings.HasPrefix(child.Path, "+") {
			continue
		}
		cur := into[child.Path]
		cur.FanIn++
		into[child.Path] = cur
		n++
	}
	return into
}

// RepresentativeFilePath returns one source file path under n (first file leaf).
func RepresentativeFilePath(n *SubtreeNode) string {
	if n == nil {
		return ""
	}
	if n.Kind == SubtreeKindFile {
		return n.Path
	}
	if n.Representative != "" {
		return n.Representative
	}
	for _, c := range n.Children {
		if p := RepresentativeFilePath(c); p != "" {
			return p
		}
	}
	return ""
}

// ContentionBandChildren returns direct children that share a comparable-material
// band with at least one sibling — the only cases where a depth tiebreak can
// change drill vs rollup.
func ContentionBandChildren(node *SubtreeNode) []*SubtreeNode {
	if node == nil || len(node.Children) == 0 {
		return nil
	}
	byMaterial := map[int][]*SubtreeNode{}
	for _, c := range node.Children {
		if c == nil || strings.HasPrefix(c.Path, "+") {
			continue
		}
		m := material(c)
		byMaterial[m] = append(byMaterial[m], c)
	}
	var out []*SubtreeNode
	for _, group := range byMaterial {
		if len(group) < 2 {
			continue
		}
		out = append(out, group...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// rankChildren orders children by observed material.
func rankChildren(node *SubtreeNode) []*SubtreeNode {
	return rankChildrenImportance(node, nil, "")
}

// rankChildrenImportance adds reference and task tie-breaks.
func rankChildrenImportance(node *SubtreeNode, imp SubtreeImportance, task string) []*SubtreeNode {
	if node == nil || len(node.Children) == 0 {
		return nil
	}
	out := append([]*SubtreeNode(nil), node.Children...)
	useImp := len(imp) > 0
	taskScores := childPathTaskScores(task, out)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
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
		if taskScores != nil {
			sa, sb := taskScores[a.Path], taskScores[b.Path]
			if sa != sb {
				return sa > sb
			}
		}
		if a.Material.Bytes != b.Material.Bytes {
			return a.Material.Bytes > b.Material.Bytes
		}
		return a.Path < b.Path
	})
	return out
}

// childPathTaskScores is IDF-weighted overlap of task terms against each
// child's Path. Empty task or <2 children → nil (no-op).
func childPathTaskScores(task string, children []*SubtreeNode) map[string]float64 {
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
		docs = append(docs, []textrank.Field{{Text: c.Path, Weight: 1}})
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

func recordImportanceBoosts(drilled, rolled []*SubtreeNode, imp SubtreeImportance, stats *CuratorStats) {
	if stats == nil || len(imp) == 0 || len(drilled) == 0 || len(rolled) == 0 {
		return
	}
	for _, d := range drilled {
		if d == nil {
			continue
		}
		di := imp[d.Path]
		for _, r := range rolled {
			if r == nil || material(d) != material(r) {
				continue
			}
			ri := imp[r.Path]
			if di.DocLinks > ri.DocLinks {
				stats.DoclinkBoosts++
			}
			if di.FanIn > ri.FanIn {
				stats.FaninBoosts++
			}
		}
	}
}
