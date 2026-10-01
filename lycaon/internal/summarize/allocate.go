package summarize

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/decide"
)

// KindDirectoryRollup is a skeleton row representing an undrilled subtree child.
// Metered like a single skeleton line; no substance windows.
const KindDirectoryRollup = "directory_rollup"

const rollupTopSymbols = 5

// allocateContextPack divides the budget across a material tree.
func allocateContextPack(
	ctx context.Context,
	rr decide.Reranker,
	task string,
	root *SubtreeNode,
	structure []StructureCandidate,
	caps Caps,
	fit FitEdges,
	importance SubtreeImportance,
	provider OutlineProvider,
) (ContextPack, CuratorStats, []NextAction) {
	budget := caps.Pack.InputBudgetTokens
	if budget <= 0 {
		budget = DefaultCaps().Pack.InputBudgetTokens
	}
	if root == nil || (len(root.Children) == 0 && root.Remainder == nil) {
		scoped := structure
		var outlineStats CuratorStats
		if root != nil && root.Kind == SubtreeKindFile && provider != nil {
			if sc, ok := provider.Outline(ctx, root.Path); ok {
				scoped = []StructureCandidate{sc}
				outlineStats.FilesOutlined = 1
			}
		}
		pack, stats, next := assembleContextPackBudget(ctx, rr, task, scoped, caps, fit, budget)
		stats.FilesOutlined += outlineStats.FilesOutlined
		return pack, stats, next
	}
	imp := filterImportance(caps, importance)
	return allocate(ctx, rr, task, root, structure, caps, fit, imp, budget, 0, provider)
}

// filterImportance drops signals when the corresponding cap is 0.
func filterImportance(caps Caps, imp SubtreeImportance) SubtreeImportance {
	if len(imp) == 0 {
		return nil
	}
	docOn := caps.Pack.SubtreeDoclinkMax > 0
	fanOn := caps.Pack.SubtreeFaninMax > 0
	if !docOn && !fanOn {
		return nil
	}
	out := SubtreeImportance{}
	for path, c := range imp {
		if !docOn {
			c.DocLinks = 0
		}
		if !fanOn {
			c.FanIn = 0
		}
		if c.DocLinks == 0 && c.FanIn == 0 {
			continue
		}
		out[path] = c
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func allocate(
	ctx context.Context,
	rr decide.Reranker,
	task string,
	node *SubtreeNode,
	structure []StructureCandidate,
	caps Caps,
	fit FitEdges,
	importance SubtreeImportance,
	budget int,
	depth int,
	provider OutlineProvider,
) (ContextPack, CuratorStats, []NextAction) {
	stats := CuratorStats{PrimaryLimitReason: "none", RecursionDepth: depth}
	if node == nil {
		return ContextPack{}, stats, nil
	}
	if node.LoadChildren != nil && !node.RollupOnly {
		node.LoadChildren(ctx, node)
	}
	sidx := newStructureIndex(structure)
	if node.Kind == SubtreeKindFile || (len(node.Children) == 0 && node.Remainder == nil) {
		scoped := sidx.forNode(node)
		if provider != nil && node.Kind == SubtreeKindFile {
			if sc, ok := provider.Outline(ctx, node.Path); ok {
				scoped = []StructureCandidate{sc}
				stats.FilesOutlined++
			}
		}
		pack, st, next := assembleContextPackBudget(ctx, rr, task, scoped, caps, fit, budget)
		st.RecursionDepth = depth
		st.FilesOutlined += stats.FilesOutlined
		return pack, st, next
	}

	children := rankChildrenImportance(node, importance, task)
	children = childrenFromCursor(children, node.Cursor)
	// Keep synthetic tails outside ranking.
	var real []*SubtreeNode
	var synthTail []*SubtreeNode
	for _, c := range children {
		if c == nil {
			continue
		}
		if strings.HasPrefix(c.Path, "+") && c.RollupOnly {
			synthTail = append(synthTail, c)
			continue
		}
		real = append(real, c)
	}
	children = real

	nameIndexMax := 0
	if strings.TrimSpace(task) != "" {
		nameIndexMax = caps.Pack.SubtreeNameIndexMax
	}
	index := collectChildNameIndex(ctx, children, structure, provider, nameIndexMax, &stats)
	taskScores := childTaskScores(task, children, index)
	if depth == 0 {
		taskScores = applyImportPullThrough(taskScores, node, fit.Imports, index)
	}
	alpha := caps.Pack.SubtreeTaskDepthAlpha
	if taskDepthActive(alpha, taskScores) {
		tmp := &SubtreeNode{Path: node.Path, Kind: node.Kind, Children: children}
		children = rankChildrenTaskDepth(tmp, importance, taskScores)
	}

	fanout := caps.Pack.SubtreeFanoutMax
	if fanout <= 0 {
		fanout = DefaultCaps().Pack.SubtreeFanoutMax
	}
	var fanoutTail []*SubtreeNode
	children, fanoutTail = splitFanoutChildren(children, fanout)
	fanoutTail = append(synthTail, fanoutTail...)

	pack := ContextPack{}
	spent := admitDirIdentity(&pack, node, caps, budget)
	perChildFloor, free, weights := childBudgetShares(children, caps, budget, spent)
	weights = softStarveWeights(children, weights, taskScores, alpha)
	maxDepth := caps.Gather.MaxListDepth
	if maxDepth <= 0 {
		maxDepth = DefaultCaps().Gather.MaxListDepth
	}
	stats.ChildrenAdmitted = len(children) + len(fanoutTail)

	var next []NextAction
	var drilled, rolled []*SubtreeNode
	spent, next, drilled, rolled = allocateChildren(ctx, rr, task, node, children, sidx, caps, importance, budget, depth, maxDepth,
		perChildFloor, free, weights, taskScores, alpha, index, &pack, &stats, spent, provider)

	if len(fanoutTail) > 0 {
		spent, next = emitFanoutTail(node, fanoutTail, caps, budget, &pack, &stats, spent, next)
		rolled = append(rolled, fanoutTail...)
	}
	if node.Remainder != nil && node.Remainder.Children > 0 {
		spent, next = emitIndexedRemainder(node, task, caps, budget, &pack, &stats, spent, next)
	}
	recordImportanceBoosts(drilled, rolled, importance, &stats)
	recordTaskDepthBoosts(drilled, rolled, taskScores, &stats)

	if depth == 0 {
		spent = admitFitEdges(ctx, rr, task, fit, caps, budget, &pack, &stats, spent)
	}
	stats.BudgetTokensSpent = spent
	finalizeAllocateLimit(&stats, spent, budget)
	return pack, stats, next
}

// splitFanoutChildren separates individually represented children.
func splitFanoutChildren(children []*SubtreeNode, fanoutMax int) (kept, tail []*SubtreeNode) {
	var real []*SubtreeNode
	for _, c := range children {
		if c == nil {
			continue
		}
		if strings.HasPrefix(c.Path, "+") && c.RollupOnly {
			tail = append(tail, c)
			continue
		}
		real = append(real, c)
	}
	if fanoutMax <= 0 || len(real) <= fanoutMax {
		return real, tail
	}
	return real[:fanoutMax], append(tail, real[fanoutMax:]...)
}

func childrenFromCursor(children []*SubtreeNode, cursor string) []*SubtreeNode {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return children
	}
	for i, child := range children {
		if child != nil && child.Path == cursor {
			return children[i:]
		}
	}
	return children
}

func emitFanoutTail(
	parent *SubtreeNode,
	tail []*SubtreeNode,
	caps Caps,
	budget int,
	pack *ContextPack,
	stats *CuratorStats,
	spent int,
	next []NextAction,
) (int, []NextAction) {
	// Collapse the tail into one rollup row.
	n := 0
	var sum Material
	for _, c := range tail {
		if c == nil {
			continue
		}
		if strings.HasPrefix(c.Path, "+") {
			n += parsePlusMoreCount(c.Path)
			sum.Defs += c.Material.Defs
			sum.SourceFiles += c.Material.SourceFiles
			sum.Bytes += c.Material.Bytes
			continue
		}
		n++
		sum.Defs += c.Material.Defs
		sum.SourceFiles += c.Material.SourceFiles
		sum.Bytes += c.Material.Bytes
	}
	if n <= 0 {
		return spent, next
	}
	tailNode := &SubtreeNode{
		Path: fmt.Sprintf("+%d more", n), Kind: SubtreeKindDir,
		RollupOnly: true, Material: sum,
	}
	row := rollupRow(tailNode, nil)
	cost := caps.EstimateTokens(rollupSkeletonLine(row))
	if spent+cost <= budget || stats.ChildrenRolledUp == 0 {
		pack.Skeleton = append(pack.Skeleton, row)
		spent += cost
		stats.BreadthAdmits++
	}
	parentPath := ""
	if parent != nil {
		parentPath = parent.Path
	}
	action := drillNextAction(tailNode, parentPath)
	if parent != nil && len(parent.ScopePaths) > 0 {
		action.Path = ""
		action.Paths = append([]string(nil), parent.ScopePaths...)
	}
	revision := uint64(0)
	if parent != nil {
		revision = parent.Revision
	}
	for _, child := range tail {
		if child != nil && !strings.HasPrefix(child.Path, "+") {
			action.Cursor = encodeCursor(revision, parent.CursorScope, child.Path)
			break
		}
	}
	next = append(next, action)
	stats.ChildrenRolledUp++
	return spent, next
}

func parsePlusMoreCount(path string) int {
	// "+6 more" → 6; unknown → 1
	path = strings.TrimPrefix(path, "+")
	var n int
	for _, r := range path {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	if n <= 0 {
		return 1
	}
	return n
}

func admitDirIdentity(pack *ContextPack, node *SubtreeNode, caps Caps, budget int) int {
	rootID := PackIdentity{
		Path: node.Path, Kind: StructureKindDirMap, LineCount: node.Material.SourceFiles,
		ParseHealth: parseHealthOK,
	}
	if rootID.Path == "" {
		rootID.Path = "."
	}
	idCost := caps.EstimateTokens(identityLine(rootID))
	if idCost > budget {
		return 0
	}
	pack.Identity = append(pack.Identity, rootID)
	return idCost
}

func childBudgetShares(
	children []*SubtreeNode,
	caps Caps,
	budget, spent int,
) (perChildFloor, free int, weights []float64) {
	floorPct := caps.Pack.SubtreeMinRollupSharePct
	if floorPct <= 0 {
		floorPct = DefaultCaps().Pack.SubtreeMinRollupSharePct
	}
	rollupUnit := 1
	if len(children) > 0 {
		sample := rollupRow(children[0], nil)
		if c := caps.EstimateTokens(rollupSkeletonLine(sample)); c > rollupUnit {
			rollupUnit = c
		}
	}
	floor := budget * floorPct / 100
	if minFloor := rollupUnit * len(children); floor < minFloor {
		floor = minFloor
	}
	if floor+spent > budget {
		floor = budget - spent
		if floor < 0 {
			floor = 0
		}
	}
	if len(children) > 0 {
		perChildFloor = floor / len(children)
		if perChildFloor < 1 {
			perChildFloor = 1
		}
	}
	free = budget - spent - floor
	if free < 0 {
		free = 0
	}
	weights = make([]float64, len(children))
	var weightSum float64
	for i, c := range children {
		w := dampMaterial(material(c), caps.Pack.SubtreeDamping)
		if w < 0.001 {
			w = 0.001
		}
		weights[i] = w
		weightSum += w
	}
	if weightSum <= 0 {
		weightSum = 1
	}
	for i := range weights {
		weights[i] /= weightSum
	}
	return perChildFloor, free, weights
}

func allocateChildren(
	ctx context.Context,
	rr decide.Reranker,
	task string,
	parent *SubtreeNode,
	children []*SubtreeNode,
	sidx structureIndex,
	caps Caps,
	importance SubtreeImportance,
	budget, depth, maxDepth, perChildFloor, free int,
	weights []float64,
	taskScores map[string]float64,
	alpha float64,
	index childNameIndex,
	pack *ContextPack,
	stats *CuratorStats,
	spent int,
	provider OutlineProvider,
) (int, []NextAction, []*SubtreeNode, []*SubtreeNode) {
	var next []NextAction
	var drilled, rolled []*SubtreeNode
	maxDrilled := caps.Gather.MaxFilesRead
	if maxDrilled <= 0 {
		maxDrilled = DefaultCaps().Gather.MaxFilesRead
	}
	taskActive := taskDepthActive(alpha, taskScores)

	plans := make([]childPlan, 0, len(children))
	for i, c := range children {
		bi := perChildFloor + int(float64(free)*weights[i])
		if bi < perChildFloor {
			bi = perChildFloor
		}
		if bi < 1 {
			bi = 1
		}
		var scoped []StructureCandidate
		if sc, ok := index.Outlined[c.Path]; ok {
			scoped = []StructureCandidate{sc}
		}
		rollupOnly := c.RollupOnly || depth+1 >= maxDepth
		if !rollupOnly && maxDrilled > 0 && stats.FilesOutlined >= maxDrilled {
			rollupOnly = true
		}
		pile := fitEstimatePile(c, caps)
		entry := estimatePileEntryCost(c, caps)
		drill := !rollupOnly && pile <= bi
		if taskActive {
			s := taskScores[c.Path]
			if s > 0 {
				// Task hits use the entry cost.
				drill = !rollupOnly && entry <= bi
			}
		}
		plans = append(plans, childPlan{child: c, bi: bi, scoped: scoped, drill: drill})
	}

	plans = applyMinDrillRescue(plans, caps, budget, spent, perChildFloor, free, maxDrilled, stats)
	plans = applyMaxDrillCap(plans, caps)
	redistributeRolledUpDepthBudget(plans, caps, budget-spent)

	for _, p := range plans {
		c := p.child
		if p.drill && maxDrilled > 0 && stats.FilesOutlined >= maxDrilled {
			p.drill = false
			p.forced = false
		}
		if !p.drill {
			spent = emitChildRollup(c, caps, budget, len(children), pack, stats, spent)
			next = append(next, drillNextAction(c, parent.Path))
			rolled = append(rolled, c)
			continue
		}
		spent, next = emitChildDrill(ctx, rr, task, c, sidx, p.scoped, caps, importance, p.bi, budget, depth, pack, stats, spent, next, provider, index)
		drilled = append(drilled, c)
		if p.forced {
			stats.ForcedDrills++
		}
	}
	return spent, next, drilled, rolled
}

// applyMinDrillRescue ensures a minimum useful depth.
func applyMinDrillRescue(
	plans []childPlan,
	caps Caps,
	budget, spent, perChildFloor, free, maxDrilled int,
	stats *CuratorStats,
) []childPlan {
	minDrills := caps.Pack.SubtreeMinDrills
	if minDrills <= 0 || len(plans) == 0 {
		return plans
	}
	natural := 0
	for _, p := range plans {
		if p.drill {
			natural++
		}
	}
	if natural > 0 {
		return plans
	}

	var eligible []int
	outlined := 0
	if stats != nil {
		outlined = stats.FilesOutlined
	}
	for i, p := range plans {
		c := p.child
		if c == nil || c.RollupOnly {
			continue
		}
		// Minimum drills apply only to file leaves.
		if c.Kind != SubtreeKindFile && (len(c.Children) > 0 || c.LoadChildren != nil) {
			continue
		}
		if maxDrilled > 0 && outlined+len(eligible) >= maxDrilled {
			break
		}
		eligible = append(eligible, i)
		if len(eligible) >= minDrills {
			break
		}
	}
	if len(eligible) == 0 {
		return plans
	}

	// Residual after identity + one rollup row per non-drilled sibling.
	rollupUnit := 1
	if perChildFloor > rollupUnit {
		rollupUnit = perChildFloor
	}
	rollupReserve := rollupUnit * (len(plans) - len(eligible))
	pool := budget - spent - rollupReserve
	if pool < len(eligible) {
		pool = free + perChildFloor*len(eligible)
	}
	if pool < len(eligible) {
		return plans
	}
	share := pool / len(eligible)
	if share < 1 {
		share = 1
	}
	for _, i := range eligible {
		plans[i].drill = true
		plans[i].forced = true
		if share > plans[i].bi {
			plans[i].bi = share
		}
	}
	return plans
}

// applyMaxDrillCap bounds detailed children after ranking.
func applyMaxDrillCap(plans []childPlan, caps Caps) []childPlan {
	maxDrills := caps.Pack.SubtreeMaxDrills
	if maxDrills <= 0 || len(plans) == 0 {
		return plans
	}
	kept := 0
	for i := range plans {
		if !plans[i].drill {
			continue
		}
		kept++
		if kept > maxDrills {
			plans[i].drill = false
			plans[i].forced = false
		}
	}
	return plans
}

// childPlan is one allocateChildren decision (fit-or-rollup + optional rescue).
type childPlan struct {
	child  *SubtreeNode
	bi     int
	scoped []StructureCandidate
	drill  bool
	forced bool
}

func emitChildRollup(
	c *SubtreeNode,
	caps Caps,
	budget, childCount int,
	pack *ContextPack,
	stats *CuratorStats,
	spent int,
) int {
	row := rollupRow(c, nil)
	cost := caps.EstimateTokens(rollupSkeletonLine(row))
	if spent+cost <= budget || stats.ChildrenRolledUp+stats.ChildrenDrilled < childCount {
		pack.Skeleton = append(pack.Skeleton, row)
		spent += cost
		stats.BreadthAdmits++
	}
	stats.ChildrenRolledUp++
	stats.OutlinesSkippedRolledUp++
	return spent
}

func emitChildDrill(
	ctx context.Context,
	rr decide.Reranker,
	task string,
	c *SubtreeNode,
	sidx structureIndex,
	scoped []StructureCandidate,
	caps Caps,
	importance SubtreeImportance,
	bi, budget, depth int,
	pack *ContextPack,
	stats *CuratorStats,
	spent int,
	next []NextAction,
	provider OutlineProvider,
	index childNameIndex,
) (int, []NextAction) {
	var childPack ContextPack
	var childStats CuratorStats
	var childNext []NextAction
	if c.Kind == SubtreeKindDir && (len(c.Children) > 0 || c.LoadChildren != nil) {
		if c.LoadChildren != nil {
			c.CursorScope = cursorScope(Request{Path: c.Path, Task: task})
		}
		childPack, childStats, childNext = allocate(ctx, rr, task, c, sidx.all, caps, FitEdges{}, importance, bi, depth+1, provider)
	} else if provider != nil {
		sc, ok := index.Outlined[c.Path]
		if !ok {
			sc, ok = provider.Outline(ctx, c.Path)
			if ok {
				stats.FilesOutlined++
			}
		}
		if !ok {
			spent = emitChildRollup(c, caps, budget, 1, pack, stats, spent)
			return spent, append(next, drillNextAction(c, ""))
		}
		// Root assembly produces the follow-up shortlist.
		childPack, childStats, _ = assembleContextPackBudget(ctx, rr, task, []StructureCandidate{sc}, caps, FitEdges{}, bi)
	} else {
		if len(scoped) == 0 {
			scoped = sidx.forNode(c)
		}
		childPack, childStats, _ = assembleContextPackBudget(ctx, rr, task, scoped, caps, FitEdges{}, bi)
	}
	spent += mergePackDelta(caps, pack, childPack)
	if spent > budget {
		stats.PrimaryLimitReason = "pack_budget"
	}
	stats.BreadthAdmits += childStats.BreadthAdmits
	stats.DepthAdmits += childStats.DepthAdmits
	stats.DeepenHits += childStats.DeepenHits
	stats.FitAdmits += childStats.FitAdmits
	stats.ChildrenAdmitted += childStats.ChildrenAdmitted
	stats.ChildrenDrilled += childStats.ChildrenDrilled + 1
	stats.ChildrenRolledUp += childStats.ChildrenRolledUp
	stats.DoclinkBoosts += childStats.DoclinkBoosts
	stats.FaninBoosts += childStats.FaninBoosts
	stats.TaskDepthBoosts += childStats.TaskDepthBoosts
	stats.FilesOutlined += childStats.FilesOutlined
	stats.NameIndexed += childStats.NameIndexed
	stats.OutlinesSkippedRolledUp += childStats.OutlinesSkippedRolledUp
	stats.ForcedDrills += childStats.ForcedDrills
	if childStats.RecursionDepth > stats.RecursionDepth {
		stats.RecursionDepth = childStats.RecursionDepth
	}
	return spent, append(next, childNext...)
}

func finalizeAllocateLimit(stats *CuratorStats, spent, budget int) {
	if spent >= budget && stats.PrimaryLimitReason == "none" {
		stats.PrimaryLimitReason = "pack_budget"
	}
}

func dampMaterial(score int, mode string) float64 {
	s := float64(score)
	if s < 0 {
		s = 0
	}
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "proportional":
		if s < 1 {
			return 1
		}
		return s
	case "log":
		return math.Log1p(s)
	default: // sqrt
		return math.Sqrt(s + 1)
	}
}

// fitEstimatePile estimates the minimum useful detail for a child.
func fitEstimatePile(node *SubtreeNode, caps Caps) int {
	if node != nil && (node.Kind == SubtreeKindFile || len(node.Children) == 0) {
		return estimatePileEntryCost(node, caps)
	}
	return estimatePileFromMaterial(node, caps)
}

func estimatePileEntryCost(node *SubtreeNode, caps Caps) int {
	if node == nil {
		return 1
	}
	lineCount := node.Material.SourceFiles
	if lineCount <= 0 {
		lineCount = 1
	}
	id := PackIdentity{
		Path: node.Path, Kind: StructureKindFile,
		LineCount: lineCount, ParseHealth: parseHealthOK,
	}
	total := caps.EstimateTokens(identityLine(id))
	d := DefinitionItem{
		RelPath: node.Path, Kind: "func", Name: "sym0", Line: 2,
		FileKind: StructureKindFile,
	}
	total += caps.EstimateTokens(skeletonLine(d))
	window := caps.Gather.SymbolWindowLines
	if window <= 0 {
		window = DefaultCaps().Gather.SymbolWindowLines
	}
	total += windowProxyTokens(caps, window)
	if total < 1 {
		total = 1
	}
	return total
}

func estimatePileFromMaterial(node *SubtreeNode, caps Caps) int {
	if node == nil {
		return 1
	}
	if node.Kind == SubtreeKindDir && len(node.Children) > 0 {
		total := 0
		id := PackIdentity{
			Path: node.Path, Kind: StructureKindDirMap,
			LineCount: node.Material.SourceFiles, ParseHealth: parseHealthOK,
		}
		if id.Path == "" {
			id.Path = "."
		}
		total += caps.EstimateTokens(identityLine(id))
		for _, ch := range node.Children {
			total += estimatePileFromMaterial(ch, caps)
		}
		if total < 1 {
			total = 1
		}
		return total
	}
	return estimatePileFromMaterialFile(node, caps)
}

func estimatePileFromMaterialFile(node *SubtreeNode, caps Caps) int {
	if node == nil {
		return 1
	}
	lineCount := node.Material.SourceFiles
	if lineCount <= 0 {
		lineCount = 1
	}
	id := PackIdentity{
		Path: node.Path, Kind: StructureKindFile,
		LineCount: lineCount, ParseHealth: parseHealthOK,
	}
	total := caps.EstimateTokens(identityLine(id))
	defs := node.Material.Defs
	if defs <= 0 {
		defs = 1
	}
	for i := 0; i < defs; i++ {
		d := DefinitionItem{
			RelPath: node.Path, Kind: "func",
			Name: fmt.Sprintf("sym%d", i), Line: i + 2,
			FileKind: StructureKindFile,
		}
		total += caps.EstimateTokens(skeletonLine(d))
	}
	window := caps.Gather.SymbolWindowLines
	if window <= 0 {
		window = DefaultCaps().Gather.SymbolWindowLines
	}
	total += windowProxyTokens(caps, window) * defs
	if total < 1 {
		total = 1
	}
	return total
}

// windowProxyTokens is EstimateTokens(strings.Repeat("x\n", window)) without
// allocating the proxy string on every estimate.
func windowProxyTokens(caps Caps, window int) int {
	if window <= 0 {
		return 0
	}
	// "x\n" × window → 2*window runes (each line is 'x' + '\n').
	n := 2 * window
	div := caps.Pack.SizeDivisor
	if div <= 0 {
		div = DefaultCaps().Pack.SizeDivisor
	}
	return (n + div - 1) / div
}

// structureIndex speeds prefix/exact lookups over a gather structure slice.
type structureIndex struct {
	all    []StructureCandidate
	sorted []StructureCandidate
}

func newStructureIndex(all []StructureCandidate) structureIndex {
	if len(all) == 0 {
		return structureIndex{}
	}
	sorted := append([]StructureCandidate(nil), all...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].RelPath < sorted[j].RelPath
	})
	return structureIndex{all: all, sorted: sorted}
}

func (idx structureIndex) forNode(node *SubtreeNode) []StructureCandidate {
	if node == nil {
		return nil
	}
	prefix := strings.TrimSuffix(node.Path, "/")
	if prefix == "" || prefix == "." {
		return idx.all
	}
	if strings.HasPrefix(prefix, "+") {
		return nil
	}
	sorted := idx.sorted
	if len(sorted) == 0 {
		return nil
	}
	lo := sort.Search(len(sorted), func(i int) bool {
		return sorted[i].RelPath >= prefix
	})
	var out []StructureCandidate
	for i := lo; i < len(sorted); i++ {
		p := sorted[i].RelPath
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			out = append(out, sorted[i])
			continue
		}
		break
	}
	return out
}

// rollupRow builds a one-line skeleton representation of an undrilled child.
func rollupRow(child *SubtreeNode, scoped []StructureCandidate) PackSymbol {
	if child == nil {
		return PackSymbol{Kind: KindDirectoryRollup, Line: 1}
	}
	detail := fmt.Sprintf("%d files", child.Material.SourceFiles)
	if child.UnknownMaterial {
		detail = "counts pending indexing"
	}
	langs := collectLangs(scoped)
	if langs != "" {
		detail = fmt.Sprintf("%s · %s", detail, langs)
	}
	tops := topSymbolNames(scoped, rollupTopSymbols)
	if tops != "" {
		detail = fmt.Sprintf("%s · top: %s", detail, tops)
	} else if child.Material.Defs > 0 {
		detail = fmt.Sprintf("%s · %d defs", detail, child.Material.Defs)
	}
	return PackSymbol{
		Path: child.Path,
		Kind: KindDirectoryRollup,
		Name: detail,
		Line: 1,
	}
}

func rollupSkeletonLine(s PackSymbol) string {
	return fmt.Sprintf("%s %s %s %d", s.Path, s.Kind, s.Name, s.Line)
}

func collectLangs(scoped []StructureCandidate) string {
	seen := map[string]bool{}
	var langs []string
	for _, s := range scoped {
		lang := strings.TrimSpace(s.Language)
		if lang == "" || seen[lang] {
			continue
		}
		seen[lang] = true
		langs = append(langs, lang)
	}
	sort.Strings(langs)
	if len(langs) > 3 {
		langs = langs[:3]
	}
	return strings.Join(langs, ",")
}

func topSymbolNames(scoped []StructureCandidate, n int) string {
	if n <= 0 {
		return ""
	}
	var names []string
	seen := map[string]bool{}
	for _, s := range scoped {
		for _, sym := range s.Symbols {
			name := strings.TrimSpace(sym.Name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
			if len(names) >= n {
				return strings.Join(names, ", ")
			}
		}
		for _, tag := range s.RollupRows {
			name := strings.TrimSpace(tag)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
			if len(names) >= n {
				return strings.Join(names, ", ")
			}
		}
	}
	return strings.Join(names, ", ")
}

func drillNextAction(child *SubtreeNode, parentPath string) NextAction {
	path := ""
	if child != nil {
		path = child.Path
	}
	why := "Explore this area"
	if child != nil && strings.HasPrefix(child.Path, "+") {
		path = parentPath
		why = "Explore more children"
	} else if child != nil && child.Material.SourceFiles > 0 {
		why = fmt.Sprintf("Explore %d source files", child.Material.SourceFiles)
	}
	kind := ""
	if child != nil {
		kind = child.Kind
	}
	return NextAction{Tool: "summarize", Path: path, Why: why, TargetKind: kind}
}

// mergePackDelta merges src into dst and returns the size-proxy tokens added
// (identity rows already present in dst are skipped, matching merge semantics).
func mergePackDelta(caps Caps, dst *ContextPack, src ContextPack) int {
	seenID := map[string]bool{}
	for _, id := range dst.Identity {
		seenID[id.Path] = true
	}
	delta := 0
	for _, id := range src.Identity {
		if id.Path == "" || seenID[id.Path] {
			continue
		}
		seenID[id.Path] = true
		dst.Identity = append(dst.Identity, id)
		delta += caps.EstimateTokens(identityLine(id))
	}
	for _, s := range src.Skeleton {
		dst.Skeleton = append(dst.Skeleton, s)
		delta += caps.EstimateTokens(fmt.Sprintf("%s %s %s %d", s.Path, s.Kind, s.Name, s.Line))
	}
	for _, w := range src.Substance {
		dst.Substance = append(dst.Substance, w)
		delta += caps.EstimateTokens(windowLine(w))
	}
	for _, n := range src.Neighbors {
		dst.Neighbors = append(dst.Neighbors, n)
		delta += caps.EstimateTokens(neighborLine(n))
	}
	for _, cs := range src.CallSites {
		dst.CallSites = append(dst.CallSites, cs)
		delta += caps.EstimateTokens(callSiteLine(cs))
	}
	for _, e := range src.Imports {
		dst.Imports = append(dst.Imports, e)
		delta += caps.EstimateTokens(importEdgeLine(e))
	}
	dst.Gaps = append(dst.Gaps, src.Gaps...)
	return delta
}
