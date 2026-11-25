package interactions

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

func (inv *inventory) readerFactRows() []ReaderFactInventoryRow {
	scalars := prompts.DeclaredReaderFactScalars()
	names := make([]string, 0, len(scalars))
	for name := range scalars {
		names = append(names, name)
	}
	slices.Sort(names)
	var out []ReaderFactInventoryRow
	collect := func(rec, name string, each func(func(id string, facts map[string]any))) {
		seen := map[string]string{}
		each(func(id string, facts map[string]any) {
			cls := classifyFactValue(facts, name)
			if _, ok := seen[cls]; !ok {
				seen[cls] = id
			}
		})
		classes := sortedKeysOf(boolSet(seen))
		witnesses := make([]string, 0, len(classes))
		for _, c := range classes {
			witnesses = append(witnesses, seen[c])
		}
		out = append(out, ReaderFactInventoryRow{
			FactName: name, Recipient: rec, ObservedClasses: classes, WitnessStateIDs: witnesses,
			ConstraintSource: readerFactConstraints[rec][name],
		})
	}
	for _, name := range names {
		collect(RecipientCoordinator, name, func(visit func(string, map[string]any)) {
			for _, st := range inv.in.CoordinatorStates {
				if _, ok := inv.coord[st.ID]; ok {
					visit(st.ID, inv.in.CoordinatorAuthorities[st.ID].Reader.Facts())
				}
			}
		})
		collect(RecipientWorker, name, func(visit func(string, map[string]any)) {
			for _, ws := range inv.in.WorkerStates {
				if _, ok := inv.workers[ws.ID]; ok {
					visit(ws.ID, inv.in.WorkerAuthorities[ws.ID].Reader.Facts())
				}
			}
		})
	}
	return out
}

func boolSet(m map[string]string) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// classifyFactValue partitions a fact value by the boundaries shipped
// conditions compare: truth, emptiness, one, and more than one.
func classifyFactValue(facts map[string]any, name string) string {
	if name == "caps.vision" {
		caps, _ := facts["caps"].(map[string]any)
		return fmt.Sprintf("%v", caps["vision"] == true)
	}
	v, ok := facts[name]
	if !ok || v == nil {
		return "unset"
	}
	count := func(n int) string {
		switch n {
		case 0:
			return "0"
		case 1:
			return "1"
		default:
			return ">=2"
		}
	}
	switch x := v.(type) {
	case bool:
		return fmt.Sprintf("%t", x)
	case int:
		return "int:" + count(x)
	case string:
		if x == "" {
			return "string:empty"
		}
		return "string:" + x
	default:
		if rv := reflect.ValueOf(v); rv.Kind() == reflect.Slice || rv.Kind() == reflect.Map {
			return "len:" + count(rv.Len())
		}
		return fmt.Sprintf("%T", v)
	}
}

func buildBoundaryPartitions(specs map[string]hoststate.AnchorEnvelopeSpec, sites []InstructionSite) ([]BoundaryPartitionRow, error) {
	predByVar := map[string]map[string]bool{}
	for _, site := range sites {
		for _, g := range site.Guards {
			for _, occ := range site.OccurrenceFacts {
				short := strings.TrimPrefix(occ, "paintedwolf.")
				if predByVar[short] == nil {
					predByVar[short] = map[string]bool{}
				}
				predByVar[short][g.Expression] = true
			}
		}
	}
	ids := make([]string, 0, len(specs))
	for id := range specs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var out []BoundaryPartitionRow
	for _, id := range ids {
		envs, err := hoststate.AnchorEnvelopes(id)
		if err != nil {
			return nil, err
		}
		for _, v := range specs[id].Vars {
			seen := map[string]bool{}
			for _, m := range envs {
				seen[classifyEnvelopeValue(m[v.Name], v.Type)] = true
			}
			out = append(out, BoundaryPartitionRow{
				AnchorID: id, VarName: v.Name, VarType: v.Type,
				ShippedPredicates: sortedKeysOf(predByVar[v.Name]),
				GeneratedClasses:  sortedKeysOf(seen),
			})
		}
	}
	return out, nil
}

func classifyEnvelopeValue(val any, typ string) string {
	if val == nil {
		return "nil"
	}
	switch typ {
	case "bool":
		return fmt.Sprintf("bool:%v", val)
	case "int":
		n, _ := val.(int)
		return fmt.Sprintf("int:%d", n)
	case "string":
		if s, _ := val.(string); s == "" {
			return "string:empty"
		}
		return "string:nonempty"
	case "object":
		return "object:present"
	default:
		if rv := reflect.ValueOf(val); rv.Kind() == reflect.Slice {
			return fmt.Sprintf("len:%d", rv.Len())
		}
		return fmt.Sprintf("%T", val)
	}
}

// joinInstructionProducers joins every input a discovered site reads to its
// producer: a traced host write, a declared reader fact, an anchor envelope
// variable, an OAR host fact, or a delivered render that took both arms of the
// site's guard. Anything else lands in the unmatched bucket.
func joinInstructionProducers(in HostStateInventoryInput, specs map[string]hoststate.AnchorEnvelopeSpec) (matched, unmatched []string) {
	anchorVars := map[string]bool{}
	for _, sp := range specs {
		for _, v := range sp.Vars {
			anchorVars[v.Name] = true
		}
	}
	oarFacts := map[string]bool{}
	if decl := oar.InstalledEngineDeclaration(); decl != nil {
		for _, hf := range decl.HostFacts {
			oarFacts[hf.Name] = true
		}
	}
	branches := deliveredBranchIndex(in.Coverage)
	matchedSet, unmatchedSet := map[string]bool{}, map[string]bool{}
	for _, site := range in.Sites {
		for _, f := range append(slices.Clone(site.CapabilityFacts), site.OccurrenceFacts...) {
			if in.Graph != nil {
				if tr := in.Graph.TraceFactForSite(f, site); len(tr.ProducerSites) > 0 {
					for _, ps := range tr.ProducerSites {
						matchedSet[f+"@"+ps] = true
					}
					continue
				}
			}
			short := strings.TrimPrefix(f, "paintedwolf.")
			switch {
			case prompts.IsDeclaredOrReservedReaderFact(f):
				matchedSet[f+"@reader_fact"] = true
			case anchorVars[short] || oarFacts[short] || oarFacts[f]:
				matchedSet[f+"@catalog_decl"] = true
			default:
				if at, ok := branches.bothArms(site, f); ok {
					matchedSet[f+"@delivered_branch:"+at] = true
				} else {
					unmatchedSet[site.ID+" -> "+f] = true
				}
			}
		}
	}
	return sortedKeysOf(matchedSet), sortedKeysOf(unmatchedSet)
}

// branchIndex maps a template and guard line to its delivered branch outcomes.
type branchIndex map[string]map[int][]hoststate.TemplateBranchSummary

func deliveredBranchIndex(cov *hoststate.CoverageReport) branchIndex {
	idx := branchIndex{}
	if cov == nil {
		return idx
	}
	for _, b := range cov.TemplateBranches {
		key := strings.TrimSuffix(b.Template, ".md")
		if idx[key] == nil {
			idx[key] = map[int][]hoststate.TemplateBranchSummary{}
		}
		idx[key][b.Line] = append(idx[key][b.Line], b)
	}
	return idx
}

// bothArms finds a guard of site that reads fact and whose branch delivered
// renders took both ways.
func (idx branchIndex) bothArms(site InstructionSite, fact string) (string, bool) {
	tmpl := strings.TrimSuffix(site.ContributionID, ".md")
	for _, g := range site.Guards {
		expr, err := pongoplain.ParseExpression(g.Expression)
		if err != nil || expr == nil || !slices.Contains(expr.Identifiers(), fact) {
			continue
		}
		for _, b := range idx[tmpl][g.Line] {
			if b.TakenTrue > 0 && b.TakenFalse > 0 {
				return fmt.Sprintf("%s:L%d", tmpl, g.Line), true
			}
		}
	}
	return "", false
}
