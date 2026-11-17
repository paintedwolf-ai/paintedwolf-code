package sweeps

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/http"
	"slices"
	"sort"
	"strings"
)

// successStage orders the mutating phase: resources are created before they
// are changed, changed before they are deleted, and host-wide resets run last.
type successStage int

const (
	// stageAuto places an operation by its method and path.
	stageAuto successStage = iota
	stageCreate
	stageChange
	stageDelete
	stageHostWide
	// stageTerminal leaves its host refusing every later request until it
	// restarts, so each such operation runs first on a host of its own.
	stageTerminal
)

// successRecipe adapts one operation's success request where the schema alone
// cannot name a valid one: prepare may create prerequisites through the API
// and return path parameters and body fields to use.
type successRecipe struct {
	stage successStage
	// order places the operation within its stage, before the default.
	order   int
	prepare func(ctx context.Context, s *conformanceSweep, op *conformanceOp) (successInputs, error)
	// seed adds rows to a paged list that holds fewer than two, for lists
	// no create operation posts to.
	seed func(ctx context.Context, s *conformanceSweep) error
}

// successInputs override what the value generator would send.
type successInputs struct {
	params map[string]string
	query  map[string]string
	// fields are set on the generated JSON body.
	fields map[string]any
	// body replaces the generated body, sent as contentType.
	body        []byte
	contentType string
}

// successRecipes holds the operations whose success needs more than fixture
// ids and a schema-valid body, one map per domain.
var successRecipes = mergeRecipes(hostWideRecipes, projectRecipes, sourceRecipes, hostRecipes, sessionRecipes)

// hostWideRecipes change state every other operation depends on, so they
// run last, in this order, or end the host's service and run alone.
var hostWideRecipes = map[string]successRecipe{
	"createProjectRemoval":    {stage: stageHostWide, order: 1, prepare: hostRecipeCreateProjectRemoval},
	"clearLocalData":          {stage: stageHostWide, order: 2},
	"lockVault":               {stage: stageHostWide, order: 3},
	"resetStore":              {stage: stageTerminal},
	"restoreBackup":           {stage: stageTerminal, prepare: hostRecipeRestoreBackup},
	"restoreRecoverySnapshot": {stage: stageTerminal, prepare: terminalRecoverySnapshot},
}

func mergeRecipes(domains ...map[string]successRecipe) map[string]successRecipe {
	out := map[string]successRecipe{}
	for _, domain := range domains {
		for id, recipe := range domain {
			if _, dup := out[id]; dup {
				panic("two success recipes for " + id)
			}
			out[id] = recipe
		}
	}
	return out
}

// stageOf places op in the mutating phase.
func stageOf(op *conformanceOp) successStage {
	if recipe := successRecipes[op.ID]; recipe.stage != stageAuto {
		return recipe.stage
	}
	switch {
	case op.Method == http.MethodDelete:
		return stageDelete
	case op.Method == http.MethodPost && !endsWithParam(op.Path):
		return stageCreate
	}
	return stageChange
}

// orderOf places an operation in its stage; an unordered one runs last.
func orderOf(op *conformanceOp) int {
	if order := successRecipes[op.ID].order; order > 0 {
		return order
	}
	return math.MaxInt
}

func endsWithParam(path string) bool {
	return strings.HasSuffix(path, "}")
}

// probeSuccessPaths sends one request per mutating operation that should
// succeed. Its answer is validated like every other; ids it returns are bound
// so later operations address the resources it made.
func (s *conformanceSweep) probeSuccessPaths(ctx context.Context, stages ...successStage) {
	var mutating []*conformanceOp
	for _, op := range s.ops {
		if op.Method != http.MethodGet && slices.Contains(stages, stageOf(op)) {
			mutating = append(mutating, op)
		}
	}
	sort.SliceStable(mutating, func(i, j int) bool {
		si, sj := stageOf(mutating[i]), stageOf(mutating[j])
		if si != sj {
			return si < sj
		}
		if oi, oj := orderOf(mutating[i]), orderOf(mutating[j]); oi != oj {
			return oi < oj
		}
		return len(mutating[i].PathParams) < len(mutating[j].PathParams)
	})
	for _, op := range mutating {
		req, err := s.successRequest(ctx, op)
		if err != nil {
			s.findings.add(ruleOperationsSucceed, op.ID, "success", "could not prepare: %v", err)
			continue
		}
		ex := s.send(ctx, req)
		s.lastMutation = op.ID
		if ex.status/100 != 2 {
			continue
		}
		if op.keyed() {
			s.checkIdempotent(ctx, ex)
			s.outcomes.replayed++
		}
		s.bindCreated(op, ex)
	}
}

// successRequest builds the success request for op from fixtures, the schema
// and its recipe.
func (s *conformanceSweep) successRequest(ctx context.Context, op *conformanceOp) (conformanceRequest, error) {
	var in successInputs
	if recipe, ok := successRecipes[op.ID]; ok && recipe.prepare != nil {
		var err error
		if in, err = recipe.prepare(ctx, s, op); err != nil {
			return conformanceRequest{}, err
		}
	}
	params, _ := s.values.pathParams(op)
	for name, value := range in.params {
		params[name] = value
	}
	query := s.values.requiredQuery(op)
	for name, value := range in.query {
		query.Set(name, value)
	}
	if in.body != nil {
		return conformanceRequest{op: op, probe: "success", params: params, query: query, body: in.body, contentType: in.contentType}, nil
	}
	if op.JSONBody == nil {
		if op.BodyRequired {
			return conformanceRequest{}, fmt.Errorf("body media %v has no success recipe", op.BodyMedia)
		}
		return conformanceRequest{op: op, probe: "success", params: params, query: query}, nil
	}
	// Write-only fields hold fresh secrets the sweep scans for, so no shared
	// value becomes a protected secret that screens fixture text.
	body := s.values.body(op.JSONBody, func(name string) string { return s.secrets.issue(op.ID + "." + name) })
	if len(in.fields) > 0 {
		fields, ok := body.(map[string]any)
		if !ok {
			return conformanceRequest{}, fmt.Errorf("recipe sets fields on a %T body", body)
		}
		for name, value := range in.fields {
			fields[name] = value
		}
	}
	return jsonRequest(op, "success", params, query, body), nil
}

// prerequisite runs another operation's success request with inputs and
// returns its decoded answer, for recipes that build on what it makes.
func (s *conformanceSweep) prerequisite(ctx context.Context, operationID string, in successInputs) (map[string]any, error) {
	op := s.op(operationID)
	if op == nil {
		return nil, fmt.Errorf("no operation %s", operationID)
	}
	params, _ := s.values.pathParams(op)
	maps.Copy(params, in.params)
	query := s.values.requiredQuery(op)
	for name, value := range in.query {
		query.Set(name, value)
	}
	req := conformanceRequest{op: op, probe: "prerequisite", params: params, query: query, body: in.body, contentType: in.contentType}
	if in.body == nil && op.JSONBody != nil {
		body, ok := s.values.body(op.JSONBody, nil).(map[string]any)
		if !ok {
			body = map[string]any{}
		}
		maps.Copy(body, in.fields)
		req = jsonRequest(op, "prerequisite", params, query, body)
	}
	ex := s.send(ctx, req)
	if ex.status/100 != 2 {
		return nil, fmt.Errorf("%s answered %s", operationID, ex.outcome())
	}
	s.bindCreated(op, ex)
	var answer map[string]any
	_ = json.Unmarshal(ex.body, &answer)
	return answer, nil
}

func (s *conformanceSweep) op(id string) *conformanceOp {
	for _, op := range s.ops {
		if op.ID == id {
			return op
		}
	}
	return nil
}

// bindCreated binds the id a create answered to the resource address below
// the collection it was posted to, and its named ids to their parameters.
func (s *conformanceSweep) bindCreated(op *conformanceOp, ex conformanceExchange) {
	var answer map[string]any
	if json.Unmarshal(ex.body, &answer) != nil {
		return
	}
	for _, name := range s.children[op.Path] {
		value, _ := answer[name].(string)
		if id, ok := answer["id"].(string); ok && value == "" {
			value = id
		}
		prefix := op.Path + "/{" + name + "}"
		if name == "id" {
			s.values.bind(prefix, "", value)
		} else {
			s.values.bind(prefix, name, value)
		}
	}
}

// checkCoverage: every operation answered at least one success, validated
// against its declaration.
func (s *conformanceSweep) checkCoverage() {
	covered := 0
	for _, op := range s.ops {
		if s.outcomes.succeeded[op.ID] {
			covered++
			continue
		}
		s.findings.add(ruleOperationsSucceed, op.ID, "success", "never answered a success; last answer %s", s.outcomes.lastAnswer[op.ID])
	}
	keyed := 0
	for _, op := range s.ops {
		if op.keyed() {
			keyed++
		}
	}
	s.findings.note("success path: %d of %d operations answered a validated success", covered, len(s.ops))
	s.findings.note("idempotency: %d of %d keyed operations replayed", s.outcomes.replayed, keyed)
	s.findings.note("pagination: %d lists walked one row at a time", s.outcomes.paged)
}
