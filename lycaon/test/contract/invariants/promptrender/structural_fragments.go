package promptrender

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

var structuralFragmentInvariants = []invariants.Invariant{
	{
		Name: "structural-fragments-stay-capability-and-action-free",
		Anchors: []invariants.Anchor{
			invariants.Func("internal/prompts", "StructuralGuidanceFragments"),
			invariants.Func("internal/prompts", "StructuralInjectFragments"),
		},
		Check: structuralFragmentsStayCapabilityAndActionFree,
	},
}

// Allowlisted reader-free structural fragments and their transitive {% include %}
// closures never read capability predicates or emit callable tool/command instructions.
func structuralFragmentsStayCapabilityAndActionFree(r *invariants.Run) {
	parsed, includes, _ := classifyAndParseSources(r)
	if len(parsed) == 0 {
		return
	}
	s := stock(r)
	vocab, err := prompts.BuildReaderFactVocabulary(s.Catalog)
	r.Must(err)

	declared := map[string]bool{}
	for name, on := range vocab.Tools {
		if on {
			declared[name] = true
		}
	}
	if cfg, err := nativemanifest.Load(); err == nil {
		for _, list := range [][]string{cfg.AllTools(), cfg.HostProducedTools, cfg.AllOperations()} {
			for _, name := range list {
				if name = strings.TrimSpace(name); name != "" {
					declared[name] = true
				}
			}
		}
	}

	byGuidanceRef := map[string]*classifiedSource{}
	for _, cs := range parsed {
		if cs.family != "guidance" {
			continue
		}
		if idx := strings.Index(cs.rel, "/guidance/"); idx >= 0 {
			subRef := strings.TrimSuffix(cs.rel[idx+len("/guidance/"):], ".md")
			byGuidanceRef[subRef] = cs
		}
	}

	fullStockManifest := filepath.Join(r.Corpus.Root(), "lycaon", "config", "packs", "painted-wolf", "platform", "pack.yaml")
	if _, err := os.Stat(fullStockManifest); err == nil {
		for _, ref := range prompts.StructuralGuidanceFragments() {
			if _, ok := byGuidanceRef[ref]; !ok {
				r.Report("allowlisted structural guidance fragment %q resolves to no reconciled guidance template", ref)
			}
		}
		for _, ref := range prompts.StructuralInjectFragments() {
			if _, ok := byGuidanceRef[ref]; !ok {
				r.Report("allowlisted structural inject fragment %q resolves to no reconciled guidance template", ref)
			}
		}
	}

	checked := 0
	for _, ref := range sortedKeys(byGuidanceRef) {
		if !prompts.IsStructuralGuidanceFragment(ref) && !prompts.IsStructuralInjectFragment(ref) {
			continue
		}
		checked++
		cs := byGuidanceRef[ref]
		auditStructuralSourceTransitive(r, cs, "", includes, vocab, declared, map[string]bool{cs.rel: true})
	}
	r.Expect(checked, "allowlisted structural fragments")
}

func auditStructuralSourceTransitive(
	r *invariants.Run,
	cs *classifiedSource,
	originSuffix string,
	includes map[string]*classifiedSource,
	vocab prompts.ReaderFactVocabulary,
	declared map[string]bool,
	seen map[string]bool,
) {
	label := cs.rel + originSuffix
	for _, vref := range pongoplain.VariableReferences(cs.body) {
		if prompts.IsCapabilityKeyName(vref) || vocab.AllowsReference(vref) {
			r.Report("%s: structural fragment references capability fact %q", label, vref)
		}
	}
	for _, action := range prompts.StructuralActionClaims(cs.body, declared) {
		r.Report("%s: structural fragment claims callable tool or command %q", label, action)
	}
	if cs.tree == nil {
		return
	}
	var walk func([]*pongoplain.TemplateNode)
	walk = func(nodes []*pongoplain.TemplateNode) {
		for _, n := range nodes {
			if n.Kind == pongoplain.NodeInclude {
				target, ok := includes[n.IncludeRef]
				if !ok {
					r.Report("%s:%d: structural fragment includes unresolved %q", label, n.Line, n.IncludeRef)
				} else if !seen[target.rel] {
					nextSeen := cloneBoolMap(seen)
					nextSeen[target.rel] = true
					auditStructuralSourceTransitive(r, target, fmt.Sprintf(" (included from %s:%d)", cs.rel, n.Line), includes, vocab, declared, nextSeen)
				}
			}
			for _, br := range n.Branches {
				walk(br.Body)
			}
			walk(n.Body)
			walk(n.EmptyBody)
		}
	}
	walk(cs.tree.Nodes)
}
