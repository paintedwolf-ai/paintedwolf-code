package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

const corpusSchema = "pw-decide-corpus/3"

type corpusSurface struct {
	ID       string   `json:"id"`
	Floor    []string `json:"floor"`
	Loadable []string `json:"loadable"`
}

type corpusTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Option is the tool's text in the turn question, as the host renders it.
	Option string `json:"option"`
	// Card is the text the engine ranks against a request_tools need.
	Card string `json:"card"`
}

// corpusSkill is one loaded skill with the card the engine ranks.
type corpusSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Card        string `json:"card"`
}

type corpus struct {
	Schema          string                 `json:"schema"`
	CatalogRevision string                 `json:"catalog_revision"`
	State           turnload.StateSpec     `json:"state"`
	Questions       turnload.TurnSpec      `json:"questions"`
	Request         turnload.RequestSpec   `json:"request"`
	Lookup          turnload.LookupSpec    `json:"lookup"`
	Retention       turnload.RetentionSpec `json:"retention"`
	Kinds           []string               `json:"kinds"`
	Surfaces        []corpusSurface        `json:"surfaces"`
	Tools           []corpusTool           `json:"tools"`
	Skills          []corpusSkill          `json:"skills"`
}

func buildCorpus() (corpus, error) {
	catalog, err := turnload.LoadCatalog()
	if err != nil {
		return corpus{}, err
	}
	eff, err := extpacks.CatalogForConsumers()
	if err != nil {
		return corpus{}, fmt.Errorf("effective catalog: %w", err)
	}
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	if err != nil {
		return corpus{}, fmt.Errorf("tool schemas: %w", err)
	}
	plans, err := surface.CompileToolPlans(1)
	if err != nil {
		return corpus{}, err
	}
	out := corpus{Schema: corpusSchema, CatalogRevision: eff.Revision, State: catalog.State, Questions: catalog.Turn, Request: catalog.Request, Lookup: catalog.Lookup, Retention: catalog.Retention, Kinds: turnload.Kinds()}
	ids := make([]string, 0, len(plans))
	for id := range plans {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		plan := plans[id]
		floor, loadable := plan.ImmediateNames(), plan.DeferredNames()
		sort.Strings(floor)
		sort.Strings(loadable)
		out.Surfaces = append(out.Surfaces, corpusSurface{ID: id, Floor: floor, Loadable: loadable})
	}
	// Worker profiles share the callable option vocabulary.
	names := make([]string, 0, len(schemas.Tools))
	for name := range schemas.Tools {
		if !toolcontract.IsCommandOperation(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out.Questions.Tools.Options = make(map[string]string, len(names))
	for _, name := range names {
		meta, _ := schemas.ToolMeta(name)
		desc := turnload.BoundDescription(meta.Description)
		out.Questions.Tools.Options[name] = turnload.OptionText(desc, catalog.Turn.Tools.OptionWords)
		card := turnload.RequestCandidate(turnload.ToolCard{Name: name, Description: meta.Description})
		out.Tools = append(out.Tools, corpusTool{Name: name, Description: desc, Option: turnload.OptionText(desc, catalog.Turn.Tools.OptionWords), Card: card})
	}
	loaded, _ := extpacks.LoadEffectiveSkills(eff)
	for _, sk := range loaded {
		out.Skills = append(out.Skills, corpusSkill{Name: sk.Name, Description: strings.TrimSpace(sk.Description), Card: turnload.SkillCandidate(sk)})
	}
	sort.Slice(out.Skills, func(i, j int) bool { return out.Skills[i].Name < out.Skills[j].Name })
	return out, nil
}

func nameSet(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = true
	}
	return out
}

func runDecideCorpus(args []string) error {
	fs := flag.NewFlagSet("decide corpus", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	outPath := fs.String("out", "", "output file (default stdout)")
	if err := fs.Parse(args); err != nil {
		return exitCodeError{code: 2, err: err}
	}
	out, err := buildCorpus()
	if err != nil {
		return err
	}
	return writeJSON(*outPath, out)
}
