package surface

import (
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
)

type flowTableYAML struct {
	HitPolicy string          `yaml:"hit_policy"`
	Rules     []flowRuleYAML  `yaml:"rules"`
	Default   flowDefaultYAML `yaml:"default"`
}

type flowRuleYAML struct {
	When    map[string]any `yaml:"when"`
	Surface any            `yaml:"surface"`
}

type flowDefaultYAML struct {
	Surface any `yaml:"surface"`
}

// LoadCoordinatorFlow reads and validates the coordinator flow.
func LoadCoordinatorFlow() (FlowTable, error) {
	data, err := readCoordinatorFlowBytes()
	if err != nil {
		return FlowTable{}, err
	}
	table, err := parseFlowTableYAML(data)
	if err != nil {
		return FlowTable{}, err
	}
	if err := ValidateFlowTable(table); err != nil {
		return FlowTable{}, err
	}
	return table, nil
}

// ValidateFlowTable checks flow references and comparators.
func ValidateFlowTable(table FlowTable) error {
	if strings.TrimSpace(strings.ToLower(table.HitPolicy)) != "first" {
		return fmt.Errorf("coordinator flow: hit_policy must be first, got %q", table.HitPolicy)
	}
	if table.Default.DispatchOn == "" && strings.TrimSpace(table.Default.SurfaceID) == "" && strings.TrimSpace(table.Default.UseFact) == "" {
		return fmt.Errorf("coordinator flow: default surface is required")
	}
	surfaces, err := CompileToolPlans(1)
	if err != nil {
		return fmt.Errorf("coordinator flow: load surfaces: %w", err)
	}
	seenSurfaces := make(map[string]struct{})
	collectOutputSurfaces(table.Default, seenSurfaces)
	for i, rule := range table.Rules {
		for _, cond := range rule.When {
			if !IsKnownFact(cond.Fact) {
				return fmt.Errorf("coordinator flow: rule %d references unknown fact %q", i, cond.Fact)
			}
		}
		if rule.Out.UseFact != "" && !IsKnownFact(rule.Out.UseFact) {
			return fmt.Errorf("coordinator flow: rule %d use_fact references unknown fact %q", i, rule.Out.UseFact)
		}
		if rule.Out.DispatchOn != "" && !IsKnownFact(rule.Out.DispatchOn) {
			return fmt.Errorf("coordinator flow: rule %d dispatch_on references unknown fact %q", i, rule.Out.DispatchOn)
		}
		collectOutputSurfaces(rule.Out, seenSurfaces)
	}
	for id := range seenSurfaces {
		if _, ok := surfaces[id]; !ok {
			return fmt.Errorf("coordinator flow: unknown surface %q", id)
		}
	}
	if table.Default.DispatchOn != "" && !IsKnownFact(table.Default.DispatchOn) {
		return fmt.Errorf("coordinator flow: default dispatch_on references unknown fact %q", table.Default.DispatchOn)
	}
	if table.Default.UseFact != "" && !IsKnownFact(table.Default.UseFact) {
		return fmt.Errorf("coordinator flow: default use_fact references unknown fact %q", table.Default.UseFact)
	}
	return nil
}

func readCoordinatorFlowBytes() ([]byte, error) {
	data, err := config.Read(config.CoordinatorFlow)
	if err != nil {
		return nil, fmt.Errorf("read coordinator flow: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, fmt.Errorf("coordinator flow: empty file %s", config.CoordinatorFlow)
	}
	return data, nil
}

func parseFlowTableYAML(data []byte) (FlowTable, error) {
	var raw flowTableYAML
	if err := config.DecodeYAML(data, &raw); err != nil {
		return FlowTable{}, fmt.Errorf("parse coordinator flow: %w", err)
	}
	table := FlowTable{HitPolicy: strings.TrimSpace(raw.HitPolicy)}
	for i, row := range raw.Rules {
		rule := FlowRule{}
		for fact, cell := range row.When {
			fact = strings.TrimSpace(fact)
			cond, err := parseFlowCondition(fact, cell)
			if err != nil {
				return FlowTable{}, fmt.Errorf("parse coordinator flow: rule %d when[%s]: %w", i, fact, err)
			}
			rule.When = append(rule.When, cond)
		}
		out, err := parseFlowOutput(row.Surface)
		if err != nil {
			return FlowTable{}, fmt.Errorf("parse coordinator flow: rule %d surface: %w", i, err)
		}
		rule.Out = out
		table.Rules = append(table.Rules, rule)
	}
	defOut, err := parseFlowOutput(raw.Default.Surface)
	if err != nil {
		return FlowTable{}, fmt.Errorf("parse coordinator flow: default surface: %w", err)
	}
	table.Default = defOut
	return table, nil
}

func parseFlowCondition(fact string, cell any) (FlowCondition, error) {
	if fact == "" {
		return FlowCondition{}, fmt.Errorf("empty fact name")
	}
	switch v := cell.(type) {
	case bool:
		return FlowCondition{Fact: fact, Comparator: FlowCmpEq, EqBool: v}, nil
	case int:
		return FlowCondition{Fact: fact, Comparator: FlowCmpEq, EqString: fmt.Sprintf("%d", v)}, nil
	case float64:
		return FlowCondition{}, fmt.Errorf("unsupported comparator type float64")
	case string:
		s := strings.TrimSpace(v)
		switch s {
		case ">0":
			return FlowCondition{Fact: fact, Comparator: FlowCmpGt0}, nil
		case "present":
			return FlowCondition{Fact: fact, Comparator: FlowCmpPresent}, nil
		case "absent":
			return FlowCondition{Fact: fact, Comparator: FlowCmpAbsent}, nil
		case "":
			return FlowCondition{}, fmt.Errorf("empty comparator")
		default:
			return FlowCondition{Fact: fact, Comparator: FlowCmpEq, EqString: s}, nil
		}
	default:
		return FlowCondition{}, fmt.Errorf("unsupported comparator type %T", cell)
	}
}

func parseFlowOutput(raw any) (FlowOutput, error) {
	switch v := raw.(type) {
	case string:
		id := strings.TrimSpace(v)
		if id == "" {
			return FlowOutput{}, fmt.Errorf("empty surface id")
		}
		return FlowOutput{SurfaceID: id}, nil
	case map[string]any:
		return parseFlowOutputMap(v)
	case map[any]any:
		normalized := make(map[string]any, len(v))
		for k, val := range v {
			ks, ok := k.(string)
			if !ok {
				return FlowOutput{}, fmt.Errorf("non-string map key %T", k)
			}
			normalized[ks] = val
		}
		return parseFlowOutputMap(normalized)
	default:
		return FlowOutput{}, fmt.Errorf("unsupported surface form %T", raw)
	}
}

func parseFlowOutputMap(m map[string]any) (FlowOutput, error) {
	if uf, ok := m["use_fact"].(string); ok {
		uf = strings.TrimSpace(uf)
		if uf == "" {
			return FlowOutput{}, fmt.Errorf("empty use_fact")
		}
		return FlowOutput{UseFact: uf}, nil
	}
	dispatchOn, _ := m["dispatch_on"].(string)
	dispatchOn = strings.TrimSpace(dispatchOn)
	if dispatchOn == "" {
		return FlowOutput{}, fmt.Errorf("surface map requires use_fact or dispatch_on")
	}
	casesRaw, _ := m["cases"].(map[string]any)
	if casesRaw == nil {
		if casesAny, ok := m["cases"].(map[any]any); ok {
			casesRaw = make(map[string]any, len(casesAny))
			for k, v := range casesAny {
				ks, ok := k.(string)
				if !ok {
					return FlowOutput{}, fmt.Errorf("cases key %T is not string", k)
				}
				casesRaw[ks] = v
			}
		}
	}
	cases := make(map[string]string)
	for k, v := range casesRaw {
		s, ok := v.(string)
		if !ok {
			return FlowOutput{}, fmt.Errorf("cases[%s] is not a string", k)
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return FlowOutput{}, fmt.Errorf("cases[%s] is empty", k)
		}
		cases[strings.TrimSpace(k)] = s
	}
	def, _ := m["default"].(string)
	def = strings.TrimSpace(def)
	if def == "" {
		return FlowOutput{}, fmt.Errorf("dispatch output requires default surface")
	}
	return FlowOutput{
		DispatchOn: dispatchOn,
		Cases:      cases,
		Default:    def,
	}, nil
}

func collectOutputSurfaces(out FlowOutput, seen map[string]struct{}) {
	if id := strings.TrimSpace(out.SurfaceID); id != "" {
		seen[id] = struct{}{}
	}
	for _, id := range out.Cases {
		seen[strings.TrimSpace(id)] = struct{}{}
	}
	if id := strings.TrimSpace(out.Default); id != "" {
		seen[id] = struct{}{}
	}
}

var (
	shippedFlowOnce  sync.Once
	shippedFlowTable FlowTable
	shippedFlowErr   error
)

// ShippedCoordinatorFlowTable returns the bundled flow table.
func ShippedCoordinatorFlowTable() (FlowTable, error) {
	shippedFlowOnce.Do(func() {
		shippedFlowTable, shippedFlowErr = LoadCoordinatorFlow()
	})
	return shippedFlowTable, shippedFlowErr
}
