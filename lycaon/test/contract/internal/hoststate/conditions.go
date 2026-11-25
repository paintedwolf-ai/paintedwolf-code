package hoststate

import (
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/test/contract/internal/independence"
)

// ObservedVector is one distinct evaluation of a guard: its atoms' truths, the
// decision the production evaluator took, the recipient partition and the
// include tags enclosing the guard, outermost first, as unit:line.
type ObservedVector struct {
	Atoms      []independence.Truth
	Decision   bool
	Recomposes bool
	Partition  string
	Path       []string
	Count      int
}

// ObservedCondition is every recorded evaluation of one guard. Site is the
// template branch ID or the rule#branch the coverage report uses.
type ObservedCondition struct {
	Site    string
	Family  string
	Source  string
	Atoms   []string
	Vectors []ObservedVector
	Decide  func(map[string]bool) bool
}

// ObserveConditions turns on atom recording for template arms and OAR
// conditions; each partition function names a render's or occurrence's
// recipient. Call it before Install.
func (t *CoverageTracker) ObserveConditions(templatePartition func(map[string]any) string, rulePartition func(*oar.GuardContext) string) {
	t.templateRec.ObserveConditions, t.templateRec.Partition = true, templatePartition
	t.ruleRec.ObserveConditions, t.ruleRec.Partition = true, rulePartition
}

// Conditions returns every observed guard, template guards keyed by their
// registered unit like Report's branches.
func (t *CoverageTracker) Conditions() []ObservedCondition {
	t.mu.Lock()
	specs := make(map[string][]pongoplain.BranchSpec, len(t.templateSpecs))
	for k, v := range t.templateSpecs {
		specs[k] = v
	}
	resolve := t.resolveRef
	t.mu.Unlock()

	var out []ObservedCondition
	t.templateRec.Mu.Lock()
	byID := map[string]*ObservedCondition{}
	for _, hit := range t.templateRec.Conditions {
		unit, ok := attributeTemplate(hit.Template, specs, resolve)
		if !ok {
			continue
		}
		site := strings.Replace(hit.BranchID, hit.Template, unit, 1)
		oc := byID[site]
		if oc == nil {
			oc = &ObservedCondition{Site: site, Family: "template", Source: hit.Source, Atoms: hit.Atoms, Decide: templateDecider(hit.Source)}
			byID[site] = oc
		}
		for _, v := range hit.Vectors {
			oc.Vectors = mergeVector(oc.Vectors, ObservedVector{
				Atoms: templateTruths(v.Atoms), Decision: v.Decision, Recomposes: v.Recomposes,
				Partition: v.Partition, Path: attributePath(v.Path, specs, resolve), Count: v.Count,
			})
		}
	}
	t.templateRec.Mu.Unlock()
	for _, oc := range byID {
		out = append(out, *oc)
	}

	t.ruleRec.Mu.Lock()
	for key, hit := range t.ruleRec.Conditions {
		oc := ObservedCondition{Site: key, Family: "oar", Source: hit.Source, Atoms: hit.Atoms, Decide: hit.Decide}
		for _, v := range hit.Vectors {
			oc.Vectors = append(oc.Vectors, ObservedVector{
				Atoms: ruleTruths(v.Atoms), Decision: v.Decision, Recomposes: v.Recomposes,
				Partition: v.Partition, Count: v.Count,
			})
		}
		out = append(out, oc)
	}
	t.ruleRec.Mu.Unlock()
	slices.SortFunc(out, func(a, b ObservedCondition) int { return strings.Compare(a.Site, b.Site) })
	return out
}

func mergeVector(vs []ObservedVector, v ObservedVector) []ObservedVector {
	for i := range vs {
		if vs[i].Decision == v.Decision && vs[i].Partition == v.Partition &&
			slices.Equal(vs[i].Atoms, v.Atoms) && slices.Equal(vs[i].Path, v.Path) {
			vs[i].Count += v.Count
			return vs
		}
	}
	return append(vs, v)
}

// attributePath names each enclosing include tag by its registered unit.
func attributePath(path []string, specs map[string][]pongoplain.BranchSpec, resolve func(string) (string, bool)) []string {
	out := make([]string, len(path))
	for i, p := range path {
		out[i] = p
		cut := strings.LastIndex(p, ":L")
		if cut < 0 {
			continue
		}
		if unit, ok := attributeTemplate(p[:cut], specs, resolve); ok {
			out[i] = unit + p[cut:]
		}
	}
	return out
}

func templateDecider(source string) func(map[string]bool) bool {
	expr, err := pongoplain.ParseExpression(source)
	if err != nil {
		return nil
	}
	return expr.Decide
}

func templateTruths(in []pongoplain.Truth) []independence.Truth {
	out := make([]independence.Truth, len(in))
	for i, v := range in {
		switch v {
		case pongoplain.True:
			out[i] = independence.True
		case pongoplain.False:
			out[i] = independence.False
		}
	}
	return out
}

func ruleTruths(in []oarcore.Truth) []independence.Truth {
	out := make([]independence.Truth, len(in))
	for i, v := range in {
		switch v {
		case oarcore.True:
			out[i] = independence.True
		case oarcore.False:
			out[i] = independence.False
		}
	}
	return out
}
