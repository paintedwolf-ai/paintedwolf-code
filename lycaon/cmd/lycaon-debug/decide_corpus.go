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
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

const corpusSchema = "pw-decide-corpus/2"

type corpusSurface struct {
	ID       string   `json:"id"`
	Mode     string   `json:"mode"`
	Floor    []string `json:"floor"`
	Loadable []string `json:"loadable"`
	// Guides names the instruction units the surface scores.
	Guides []string `json:"guides"`
}

type corpusTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Option is the tool's text in the turn question, as the host renders it.
	Option string `json:"option"`
	// Card is the text the engine ranks against a request_tools need.
	Card string `json:"card"`
}

type corpusUnit struct {
	ID          string   `json:"id"`
	PackID      string   `json:"pack_id"`
	Option      string   `json:"option"`
	Stock       bool     `json:"stock"`
	Description string   `json:"description"`
	Slot        string   `json:"slot"`
	Attaches    []string `json:"attaches,omitempty"`
	// NeededWith names tools that require the unit without gating its rendering.
	NeededWith []string `json:"needed_with,omitempty"`
	Modes      []string `json:"modes,omitempty"`
	Hosts      []string `json:"hosts"`
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
	Units           []corpusUnit           `json:"units"`
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
	units, err := promptunit.Load(eff)
	if err != nil {
		return corpus{}, err
	}
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	if err != nil {
		return corpus{}, fmt.Errorf("tool schemas: %w", err)
	}
	plans, err := surface.CompileToolPlans(1)
	if err != nil {
		return corpus{}, err
	}
	out := corpus{Schema: corpusSchema, CatalogRevision: units.Revision(), State: catalog.State, Questions: catalog.Turn, Request: catalog.Request, Lookup: catalog.Lookup, Retention: catalog.Retention, Kinds: turnload.Kinds()}
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
		mode := surface.ExecutionModeFamily(id)
		guides := units.Candidates(promptunit.HostCoordinator, mode, nameSet(floor), nameSet(loadable))
		row := corpusSurface{ID: id, Mode: mode, Floor: floor, Loadable: loadable}
		for _, g := range guides {
			row.Guides = append(row.Guides, g.ID)
		}
		out.Surfaces = append(out.Surfaces, row)
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
	for _, u := range units.Units() {
		hosts := make([]string, 0, len(u.Hosts))
		for _, h := range u.Hosts {
			hosts = append(hosts, string(h))
		}
		out.Units = append(out.Units, corpusUnit{ID: u.ID, PackID: u.PackID, Stock: u.Stock, Description: u.Description, Option: turnload.OptionText(u.Description, catalog.Turn.Guides.OptionWords), Slot: string(u.Slot), Attaches: u.Attaches, NeededWith: u.NeededWith, Modes: u.Modes, Hosts: hosts})
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

