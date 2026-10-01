package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/pkg/api"
)

// rowSchema names the training row shape scripts/bialy/row.schema.json describes.
const rowSchema = "pw-decide-row/1"

// decideRow is one turn decision as a training example: the state the engine
// read, the candidates the turn offered, what the engine answered, and what
// the session then did.
type decideRow struct {
	Schema           string          `json:"schema"`
	Receipt          int64           `json:"receipt"`
	Session          string          `json:"session"`
	RootSession      string          `json:"root_session"`
	OpeningMessageID string          `json:"opening_message_id"`
	Host             string          `json:"host"`
	Surface          string          `json:"surface"`
	CatalogRevision  string          `json:"catalog_revision"`
	Project          string          `json:"project"`
	Model            string          `json:"model"`
	Partial          bool            `json:"partial"`
	Lang             string          `json:"lang"`
	Engine           rowEngine       `json:"engine"`
	Offered          rowOffered      `json:"offered"`
	State            json.RawMessage `json:"state"`
	Labels           rowLabels       `json:"labels"`
}

// rowEngine is what the engine did at the turn. A tool it preloaded and the
// session then called is not an independent label, so trainers read State.
type rowEngine struct {
	// State is answered or abstained.
	State     string   `json:"state"`
	Reason    string   `json:"reason"`
	Preloaded []string `json:"preloaded"`
	Omitted   []string `json:"omitted"`
}

type rowOffered struct {
	Floor    []string `json:"floor"`
	Loadable []string `json:"loadable"`
	Guides   []string `json:"guides"`
}

// rowRequest is one request_tools call with a free-text need: the loadable
// names it spells out, and the loadable tools first called after it and
// before the next request, which are what the need was for.
type rowRequest struct {
	Need  string   `json:"need"`
	Exact []string `json:"exact"`
	After []string `json:"after"`
}

type rowLabels struct {
	Tools           []string     `json:"tools"`
	Requests        []rowRequest `json:"requests"`
	RequestedNames  []string     `json:"requested_names"`
	RequestedGroups []string     `json:"requested_groups"`
	Skills          []string     `json:"skills"`
	// Kind is nil for a turn that did not complete: what it would have done is unknown.
	Kind   *string          `json:"kind"`
	Guides map[string]*bool `json:"guides"`
}

type receiptDecisions struct {
	Turn       *turnload.Decision `json:"turn"`
	Floor      []string           `json:"floor"`
	Candidates *struct {
		Loadable []string `json:"loadable"`
		Guides   []string `json:"guides"`
	} `json:"candidates"`
}

func runDecideExport(args []string) error {
	fs := flag.NewFlagSet("decide export", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dbPath := fs.String("db", "", "path to the sidecar store.db (required)")
	outPath := fs.String("out", "", "output file (default stdout)")
	after := fs.Int64("after", 0, "export receipts with an id greater than this")
	rootsPath := fs.String("roots", "", "file of root session ids, one per line: export only those sessions and their workers")
	if err := fs.Parse(args); err != nil {
		return exitCodeError{code: 2, err: err}
	}
	roots, err := readRoots(*rootsPath)
	if err != nil {
		return exitCodeError{code: 2, err: err}
	}
	units, err := loadUnitCatalog()
	if err != nil {
		return err
	}
	ctx := context.Background()
	queries, closeDB, err := openReceiptStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer closeDB()
	out := io.Writer(os.Stdout)
	if *outPath != "" {
		f, err := openDecideOutput(*outPath, false)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		out = f
	}
	exporter := rowExporter{queries: queries, units: units, roots: roots, messages: map[string][]db.ListSessionMessagesRow{}, enc: json.NewEncoder(out)}
	if err := exporter.run(ctx, *after); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "exported %d rows (%d partial); skipped %d without candidates, %d outside --roots, %d without an opening message\n",
		exporter.exported, exporter.partial, exporter.noCandidates, exporter.outside, exporter.noOpening)
	return nil
}

func readRoots(path string) (map[string]bool, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	f, err := os.Open(path) // #nosec G304 -- operator-selected input path
	if err != nil {
		return nil, fmt.Errorf("open --roots: %w", err)
	}
	defer func() { _ = f.Close() }()
	roots := map[string]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if id := strings.TrimSpace(scanner.Text()); id != "" {
			roots[id] = true
		}
	}
	return roots, scanner.Err()
}

func loadUnitCatalog() (map[string]promptunit.Unit, error) {
	eff, err := extpacks.CatalogForConsumers()
	if err != nil {
		return nil, fmt.Errorf("effective catalog: %w", err)
	}
	catalog, err := promptunit.Load(eff)
	if err != nil {
		return nil, err
	}
	out := map[string]promptunit.Unit{}
	for _, u := range catalog.Units() {
		out[u.ID] = u
	}
	return out, nil
}

type rowExporter struct {
	queries  *db.Queries
	units    map[string]promptunit.Unit
	roots    map[string]bool
	messages map[string][]db.ListSessionMessagesRow
	enc      *json.Encoder

	exported, partial, noCandidates, outside, noOpening int
}

func (e *rowExporter) run(ctx context.Context, after int64) error {
	const page = 500
	for {
		receipts, err := e.queries.ListTurnReceiptContexts(ctx, db.ListTurnReceiptContextsParams{AfterID: after, RowLimit: page})
		if err != nil {
			return fmt.Errorf("list receipts: %w", err)
		}
		for _, r := range receipts {
			if err := e.export(ctx, r); err != nil {
				return err
			}
			after = r.ID
		}
		if len(receipts) < page {
			return nil
		}
	}
}

func (e *rowExporter) export(ctx context.Context, r db.ListTurnReceiptContextsRow) error {
	root := r.SessionID
	if r.ParentSessionID != "" {
		root = r.ParentSessionID
	}
	if e.roots != nil && !e.roots[root] {
		e.outside++
		return nil
	}
	var decisions receiptDecisions
	if err := json.Unmarshal([]byte(r.DecisionsJson), &decisions); err != nil {
		return fmt.Errorf("decode receipt %d decisions: %w", r.ID, err)
	}
	if decisions.Candidates == nil {
		e.noCandidates++
		return nil
	}
	msgs, err := e.sessionMessages(ctx, r.SessionID)
	if err != nil {
		return err
	}
	offered := rowOffered{Floor: orEmpty(decisions.Floor), Loadable: orEmpty(decisions.Candidates.Loadable), Guides: orEmpty(decisions.Candidates.Guides)}
	labels, found := observedLabels(msgs, r.OpeningMessageID.String, offered.Loadable)
	if !found {
		e.noOpening++
		return nil
	}
	var state turnload.State
	_ = json.Unmarshal([]byte(r.StateJson), &state)
	row := decideRow{
		Schema: rowSchema, Receipt: r.ID, Session: r.SessionID, RootSession: root, OpeningMessageID: r.OpeningMessageID.String,
		Host: state.Host, Surface: r.SurfaceID, CatalogRevision: r.CatalogRevision, Project: r.ProjectName, Model: r.Model,
		Partial: r.TurnStatus != "complete", Lang: guessLang(state.User),
		Engine: receiptEngine(r, decisions.Turn), Offered: offered, State: json.RawMessage(r.StateJson),
	}
	labels.Guides = e.guideLabels(offered.Guides, labels.Tools)
	// Only loadable tools are decisions; floor tools are always offered.
	loadable := nameSet(offered.Loadable)
	kept := labels.Tools[:0]
	for _, name := range labels.Tools {
		if loadable[name] {
			kept = append(kept, name)
		}
	}
	labels.Tools = kept
	row.Labels = labels
	if row.Partial {
		// A turn that did not complete never showed what it needed; its
		// request_tools needs are still observations.
		if len(labels.Requests) == 0 {
			return nil
		}
		row.Labels = rowLabels{Tools: []string{}, Requests: labels.Requests, RequestedNames: []string{}, RequestedGroups: []string{}, Skills: []string{}, Guides: map[string]*bool{}}
		e.partial++
	}
	e.exported++
	return e.enc.Encode(row)
}

func (e *rowExporter) sessionMessages(ctx context.Context, sessionID string) ([]db.ListSessionMessagesRow, error) {
	if msgs, ok := e.messages[sessionID]; ok {
		return msgs, nil
	}
	msgs, err := e.queries.ListSessionMessages(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list messages for %s: %w", sessionID, err)
	}
	e.messages[sessionID] = msgs
	return msgs, nil
}

func (e *rowExporter) guideLabels(offered, used []string) map[string]*bool {
	scored := make([]promptunit.Unit, 0, len(offered))
	for _, id := range offered {
		if u, ok := e.units[id]; ok {
			scored = append(scored, u)
		}
	}
	return turnload.GuideLabels(scored, used)
}

func receiptEngine(r db.ListTurnReceiptContextsRow, turn *turnload.Decision) rowEngine {
	out := rowEngine{State: "answered", Reason: r.Reason, Preloaded: []string{}, Omitted: []string{}}
	if r.Abstained == 1 {
		out.State = "abstained"
	}
	if turn != nil {
		out.Preloaded = turn.ToolIDs()
		out.Omitted = turn.OmittedIDs()
	}
	return out
}

// observedLabels reads the turn a receipt belongs to: from its opening
// message to the next visible user message, collecting what the session did.
func observedLabels(msgs []db.ListSessionMessagesRow, openingMessageID string, loadable []string) (rowLabels, bool) {
	start := -1
	for i, m := range msgs {
		if m.ID == openingMessageID {
			start = i
			break
		}
	}
	if start < 0 {
		return rowLabels{}, false
	}
	cards := make([]turnload.ToolCard, 0, len(loadable))
	for _, name := range loadable {
		cards = append(cards, turnload.ToolCard{Name: name})
	}
	loadableSet := nameSet(loadable)
	out := rowLabels{Tools: []string{}, Requests: []rowRequest{}, RequestedNames: []string{}, RequestedGroups: []string{}, Skills: []string{}}
	called := map[string]bool{}
	for _, m := range msgs[start+1:] {
		if visibleUserRow(m) {
			break
		}
		if m.Role != string(api.MessageRoleAssistant) || !m.ToolCallsJson.Valid {
			continue
		}
		var calls []api.ToolCall
		if json.Unmarshal([]byte(m.ToolCallsJson.String), &calls) != nil {
			continue
		}
		for _, call := range calls {
			out.observe(call, cards, loadableSet)
			if loadableSet[call.Name] && !called[call.Name] {
				called[call.Name] = true
				if n := len(out.Requests); n > 0 {
					out.Requests[n-1].After = append(out.Requests[n-1].After, call.Name)
				}
			}
		}
	}
	kind := turnload.ObservedKind(out.Tools)
	out.Kind = &kind
	sort.Strings(out.Tools)
	return out, true
}

func (l *rowLabels) observe(call api.ToolCall, cards []turnload.ToolCard, loadable map[string]bool) {
	switch call.Name {
	case "request_tools":
		if need, _ := call.Args["need"].(string); strings.TrimSpace(need) != "" {
			need = strings.TrimSpace(need)
			l.Requests = append(l.Requests, rowRequest{Need: need, Exact: orEmpty(turnload.ExactNames(need, cards)), After: []string{}})
		}
		requests, _ := call.Args["requests"].([]any)
		for _, raw := range requests {
			req, _ := raw.(string)
			req = strings.TrimSpace(req)
			switch {
			case strings.HasPrefix(req, "#") && !contains(l.RequestedGroups, req):
				l.RequestedGroups = append(l.RequestedGroups, req)
			case loadable[req] && !contains(l.RequestedNames, req):
				l.RequestedNames = append(l.RequestedNames, req)
			}
		}
	case "skills_read":
		name, _ := call.Args["name"].(string)
		if _, resource := call.Args["resource"]; name != "" && !resource && !contains(l.Skills, name) {
			l.Skills = append(l.Skills, name)
		}
	default:
		if call.Name != "" && !contains(l.Tools, call.Name) {
			l.Tools = append(l.Tools, call.Name)
		}
	}
}

func visibleUserRow(m db.ListSessionMessagesRow) bool {
	return m.Role == string(api.MessageRoleUser) && m.Origin == string(api.MessageOriginUser) && m.Visibility != string(api.MessageVisibilityInternal)
}

func guessLang(text string) string {
	for _, r := range text {
		if r > 0x024F && r != '’' && r != '—' && r != '…' {
			return "other"
		}
	}
	return "en"
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func orEmpty(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}
