package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/promptunit"
)

const decideUsage = `usage:
  lycaon-debug decide corpus [--out FILE]
  lycaon-debug decide export --db <store.db> [--out FILE] [--after ID] [--roots FILE]
  lycaon-debug decide generate --addr URL --token TOKEN --tasks FILE --project DIR --manifest FILE --unattended FILE [--project-name NAME] [--timeout D]
  lycaon-debug decide stats --db <store.db> [--after ID] [--limit N]
  lycaon-debug decide probe --request TEXT [--surface ID]

corpus  writes the unit corpus the turn decision scores: every coordinator
        surface's floor and loadable tools, every tool's option text and
        request card, every instruction unit with its front matter, every
        loaded skill with the card the engine ranks, and the question
        templates from decisions.yaml. Offline tooling reads this instead of
        the packs.
export  writes every turn receipt, coordinator and worker, as a training row
        (scripts/bialy/row.schema.json): the state the engine read, the
        candidates the turn offered, what the engine answered, and what the
        session then did.
generate drives every task of a JSON-lines task file through its own session
        of a running sidecar, answering questions and leftover approvals from
        the unattended policy, and writes one manifest entry per task. A task
        that times out or fails is aborted and recorded; the run continues.
stats   reports per-unit load and use counts from receipts, so a unit that
        always loads and is never used shows up.
probe   runs the turn decision through the engine resolved from the
        environment (LYCAON_DECIDE_BINARY, LYCAON_DECIDE_MODEL_DIR,
        LYCAON_DECIDE_HEADS) and prints what it would load.`

func runDecide(args []string) error {
	if len(args) == 0 {
		return exitCodeError{code: 2, err: fmt.Errorf("%s", decideUsage)}
	}
	switch args[0] {
	case "corpus":
		return runDecideCorpus(args[1:])
	case "export":
		return runDecideExport(args[1:])
	case "generate":
		return runDecideGenerate(args[1:])
	case "stats":
		return runDecideStats(args[1:])
	case "probe":
		return runDecideProbe(args[1:])
	default:
		return exitCodeError{code: 2, err: fmt.Errorf("unknown decide subcommand %q\n%s", args[0], decideUsage)}
	}
}

// corpusSurface is one surface's candidate set.
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
	// NeededWith names the tools whose call labels the unit as needed without
	// gating its rendering; the trainer reads it to relabel archived rows.
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
	CatalogRevision string               `json:"catalog_revision"`
	State           turnload.StateSpec   `json:"state"`
	Questions       turnload.TurnSpec    `json:"questions"`
	Request         turnload.RequestSpec `json:"request"`
	Lookup          turnload.LookupSpec  `json:"lookup"`
	Kinds           []string             `json:"kinds"`
	Surfaces        []corpusSurface      `json:"surfaces"`
	Tools           []corpusTool         `json:"tools"`
	Units           []corpusUnit         `json:"units"`
	Skills          []corpusSkill        `json:"skills"`
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
	out := corpus{CatalogRevision: units.Revision(), State: catalog.State, Questions: catalog.Turn, Request: catalog.Request, Lookup: catalog.Lookup, Kinds: turnload.Kinds()}
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
	// Every tool's text, not only the coordinator surfaces': a worker profile's
	// loadable tools are scored against the same option texts.
	names := make([]string, 0, len(schemas.Tools))
	for name := range schemas.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		meta, _ := schemas.ToolMeta(name)
		desc := turnload.BoundDescription(meta.Description)
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

func writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if path == "" {
		_, err = os.Stdout.Write(raw)
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func openReceiptStore(ctx context.Context, dbPath string) (*db.Queries, func(), error) {
	if strings.TrimSpace(dbPath) == "" {
		return nil, nil, exitCodeError{code: 2, err: fmt.Errorf("--db is required\n%s", decideUsage)}
	}
	if info, err := os.Stat(dbPath); err != nil {
		return nil, nil, exitCodeError{code: 2, err: fmt.Errorf("open --db %q: %w", dbPath, err)}
	} else if info.IsDir() {
		return nil, nil, exitCodeError{code: 2, err: fmt.Errorf("open --db %q: is a directory", dbPath)}
	}
	sqlDB, err := db.OpenReadOnly(ctx, dbPath)
	if err != nil {
		return nil, nil, exitCodeError{code: 2, err: fmt.Errorf("open store: %w", err)}
	}
	return db.New(sqlDB), func() { _ = sqlDB.Close() }, nil
}

// unitStats is one unit's load and use counts across receipts.
type unitStats struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Loaded   int    `json:"loaded"`
	Omitted  int    `json:"omitted,omitempty"`
	Used     int    `json:"used,omitempty"`
	Receipts int    `json:"receipts"`
}

func runDecideStats(args []string) error {
	fs := flag.NewFlagSet("decide stats", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dbPath := fs.String("db", "", "path to the sidecar store.db (required)")
	after := fs.Int64("after", 0, "read receipts with an id greater than this")
	limit := fs.Int("limit", 10000, "maximum receipts to read")
	if err := fs.Parse(args); err != nil {
		return exitCodeError{code: 2, err: err}
	}
	ctx := context.Background()
	queries, closeDB, err := openReceiptStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer closeDB()
	receipts, err := queries.ListTurnLoadReceiptsAfter(ctx, db.ListTurnLoadReceiptsAfterParams{AfterID: *after, RowLimit: int64(*limit)})
	if err != nil {
		return fmt.Errorf("list receipts: %w", err)
	}
	stats := map[string]*unitStats{}
	get := func(kind, id string) *unitStats {
		key := kind + "." + id
		if stats[key] == nil {
			stats[key] = &unitStats{ID: id, Kind: kind}
		}
		return stats[key]
	}
	messagesBySession := map[string][]db.ListSessionMessagesRow{}
	turns := 0
	for _, receipt := range receipts {
		if receipt.Trigger != "turn" {
			continue
		}
		var decisions struct {
			Turn turnload.Decision `json:"turn"`
		}
		if json.Unmarshal([]byte(receipt.DecisionsJson), &decisions) != nil {
			continue
		}
		turns++
		msgs, ok := messagesBySession[receipt.SessionID]
		if !ok {
			msgs, err = queries.ListSessionMessages(ctx, receipt.SessionID)
			if err != nil {
				return fmt.Errorf("list messages for %s: %w", receipt.SessionID, err)
			}
			messagesBySession[receipt.SessionID] = msgs
		}
		labels, _ := observedLabels(msgs, receipt.OpeningMessageID.String, nil)
		used := nameSet(labels.Tools)
		for _, c := range decisions.Turn.Candidates {
			row := get(string(c.Kind), c.ID)
			row.Receipts++
			switch c.Kind {
			case turnload.KindTool:
				if _, loaded := decisions.Turn.Tools[c.ID]; loaded {
					row.Loaded++
				}
				if used[c.ID] {
					row.Used++
				}
			case turnload.KindGuide:
				if _, omitted := decisions.Turn.Omitted[c.ID]; omitted {
					row.Omitted++
				} else {
					row.Loaded++
				}
			}
		}
	}
	rows := make([]unitStats, 0, len(stats))
	for _, row := range stats {
		rows = append(rows, *row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Kind != rows[j].Kind {
			return rows[i].Kind < rows[j].Kind
		}
		return rows[i].ID < rows[j].ID
	})
	return writeJSON("", map[string]any{"turns": turns, "units": rows})
}

func runDecideProbe(args []string) error {
	fs := flag.NewFlagSet("decide probe", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	request := fs.String("request", "", "the request text to decide over (required)")
	surfaceID := fs.String("surface", "implement_investigate", "coordinator surface id")
	if err := fs.Parse(args); err != nil {
		return exitCodeError{code: 2, err: err}
	}
	if strings.TrimSpace(*request) == "" {
		return exitCodeError{code: 2, err: fmt.Errorf("--request is required\n%s", decideUsage)}
	}
	corpus, err := buildCorpus()
	if err != nil {
		return err
	}
	var row *corpusSurface
	for i := range corpus.Surfaces {
		if corpus.Surfaces[i].ID == *surfaceID {
			row = &corpus.Surfaces[i]
		}
	}
	if row == nil {
		return exitCodeError{code: 2, err: fmt.Errorf("unknown surface %q", *surfaceID)}
	}
	descriptions := map[string]string{}
	for _, tool := range corpus.Tools {
		descriptions[tool.Name] = tool.Description
	}
	cards := make([]turnload.ToolCard, 0, len(row.Loadable))
	for _, name := range row.Loadable {
		cards = append(cards, turnload.ToolCard{Name: name, Description: descriptions[name]})
	}
	var guides []promptunit.Unit
	for _, u := range corpus.Units {
		if contains(row.Guides, u.ID) {
			guides = append(guides, promptunit.Unit{ID: u.ID, Description: u.Description})
		}
	}
	client := bialy.New(bialy.ConfigFromEnvironment())
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := client.Warm(ctx); err != nil {
		return fmt.Errorf("decision engine: %w", err)
	}
	spec := corpus.Questions
	spec.DeadlineMS = 60000
	state := turnload.State{Host: string(promptunit.HostCoordinator), User: turnload.BoundUser(*request, 900), RootCount: 1, Surface: *surfaceID}
	decision := turnload.Decide(ctx, client, spec, state, turnload.Candidates(turnload.ToolCandidates(cards), turnload.GuideCandidates(guides)))
	decision.Candidates = nil
	return writeJSON("", map[string]any{"engine": client.Engine(), "state": state, "decision": decision, "elapsed_ms": decision.Elapsed.Milliseconds()})
}
