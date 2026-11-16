package security

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// successStage orders the mutating phase: resources are created before they
// are changed, changed before they are deleted, and host-wide resets run last.
type successStage int

const (
	stageCreate successStage = iota
	stageChange
	stageDelete
	stageHostWide
)

// successRecipe adapts one operation's success request where the schema alone
// cannot name a valid one: prepare may create prerequisites through the API
// and return path parameters and body fields to use.
type successRecipe struct {
	stage   successStage
	prepare func(ctx context.Context, s *conformanceSweep, op *conformanceOp) (successInputs, error)
}

// successInputs override what the value generator would send.
type successInputs struct {
	params map[string]string
	query  map[string]string
	fields map[string]any
}

// successRecipes holds the operations whose success needs more than fixture
// ids and a schema-valid body.
var successRecipes = map[string]successRecipe{}

// stageOf places op in the mutating phase.
func stageOf(op *conformanceOp) successStage {
	if recipe, ok := successRecipes[op.ID]; ok {
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

func endsWithParam(path string) bool {
	return strings.HasSuffix(path, "}")
}

// probeSuccessPaths sends one request per mutating operation that should
// succeed. Its answer is validated like every other; ids it returns are bound
// so later operations address the resources it made.
func (s *conformanceSweep) probeSuccessPaths(ctx context.Context) {
	var mutating []*conformanceOp
	for _, op := range s.ops {
		if op.Method != http.MethodGet {
			mutating = append(mutating, op)
		}
	}
	sort.SliceStable(mutating, func(i, j int) bool {
		si, sj := stageOf(mutating[i]), stageOf(mutating[j])
		if si != sj {
			return si < sj
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
		if ex.status/100 == 2 {
			s.bindCreated(op, ex)
		}
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
	if op.JSONBody == nil {
		if op.BodyRequired {
			return conformanceRequest{}, fmt.Errorf("body media %v has no success recipe", op.BodyMedia)
		}
		return conformanceRequest{op: op, probe: "success", params: params, query: query}, nil
	}
	body := s.values.body(op.JSONBody, nil)
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
		if s.succeeded[op.ID] {
			covered++
			continue
		}
		s.findings.add(ruleOperationsSucceed, op.ID, "success", "never answered a success; last answer %s", s.lastAnswer[op.ID])
	}
	s.findings.note("success path: %d of %d operations answered a validated success", covered, len(s.ops))
}
