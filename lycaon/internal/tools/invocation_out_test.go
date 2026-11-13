package tools_test

import (
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type mergeRule int

const (
	// committedEffect crosses every seam, whether or not the child failed.
	committedEffect mergeRule = iota
	// advisoryFact crosses by scope.
	advisoryFact
	// outerEnvelope describes the child's own call and never crosses.
	outerEnvelope
)

// Every exported field of ToolInvocationOut has exactly one merge rule.
var invocationOutRules = map[string]mergeRule{
	"SourceContext":    committedEffect,
	"FileEdit":         committedEffect,
	"SourceReads":      committedEffect,
	"SourceDeliveries": committedEffect,
	"Listener":         committedEffect,
	"ExternalAccess":   committedEffect,
	"Facts":            advisoryFact,
	"DisplaySubject":   outerEnvelope,
	"OwnerInvoked":     outerEnvelope,
	"OwnerRef":         outerEnvelope,
	"Dispatch":         outerEnvelope,
	"WorkerReport":     outerEnvelope,
	"Completion":       outerEnvelope,
	"OverlayPromotion": outerEnvelope,
	"Visual":           outerEnvelope,
	"Skill":            outerEnvelope,
	"Process":          outerEnvelope,
	"Verdict":          outerEnvelope,
	"CommandProgram":   outerEnvelope,
	"SourceRun":        outerEnvelope,
	"RetrievedFrom":    outerEnvelope,
}

func sourceTarget(path string) api.NavigationTarget {
	return api.NavigationTarget{ProjectID: "project", RootID: "root", Path: path, EntryKind: api.NavigationEntryKindFile}
}

// filledOut sets every exported field to a value a merge can observe.
func filledOut(t *testing.T) *tools.ToolInvocationOut {
	t.Helper()
	out := &tools.ToolInvocationOut{}
	value := reflect.ValueOf(out).Elem()
	for i := range value.NumField() {
		field, info := value.Field(i), value.Type().Field(i)
		if !info.IsExported() {
			continue
		}
		switch field.Kind() {
		case reflect.Pointer:
			field.Set(reflect.New(field.Type().Elem()))
		case reflect.String:
			field.SetString("set")
		case reflect.Bool:
			field.SetBool(true)
		case reflect.Slice:
			field.Set(reflect.MakeSlice(field.Type(), 1, 1))
		case reflect.Struct:
		default:
			t.Fatalf("field %s has kind %s; teach filledOut to set it", info.Name, field.Kind())
		}
	}
	out.SourceContext = &api.SourceContext{Locations: []api.NavigationTarget{sourceTarget("a.go")}}
	out.SourceDeliveries = []tools.SourceDeliveryTag{{Handle: "delivery-1"}}
	out.Facts = guidance.ToolResultFacts{ProviderErrorCode: "provider"}.WithFeedback("STAGE_NOTE", nil, nil)
	out.Facts.Confine.Applied = true
	return out
}

func TestToolInvocationOutFieldsEachHaveOneMergeRule(t *testing.T) {
	outType := reflect.TypeFor[tools.ToolInvocationOut]()
	exported := map[string]bool{}
	for i := range outType.NumField() {
		field := outType.Field(i)
		if !field.IsExported() {
			continue
		}
		exported[field.Name] = true
		if _, ok := invocationOutRules[field.Name]; !ok {
			t.Errorf("field %s has no merge rule: classify it and make MergeChild and TakeCommitted honor the rule", field.Name)
		}
	}
	for name := range invocationOutRules {
		if !exported[name] {
			t.Errorf("merge rule names %s, which is not a field", name)
		}
	}
}

func TestMergeChildAppliesEachFieldRule(t *testing.T) {
	for _, scope := range []tools.MergeScope{tools.MergeStageOK, tools.MergeStageFailed, tools.MergeProgram} {
		t.Run(fmt.Sprint(scope), func(t *testing.T) {
			parent := &tools.ToolInvocationOut{}
			parent.MergeChild(filledOut(t), scope)
			value := reflect.ValueOf(parent).Elem()
			for name, rule := range invocationOutRules {
				merged := !value.FieldByName(name).IsZero()
				switch rule {
				case committedEffect:
					if !merged {
						t.Errorf("committed effect %s did not cross", name)
					}
				case outerEnvelope:
					if merged {
						t.Errorf("envelope field %s crossed", name)
					}
				case advisoryFact:
				}
			}
			facts := parent.Facts
			wantFeedback := scope != tools.MergeStageFailed
			if got := len(facts.Feedback) == 1 && facts.Confine.Applied; got != wantFeedback {
				t.Errorf("feedback and confinement crossed = %v, want %v: %+v", got, wantFeedback, facts)
			}
			if got := facts.ProviderErrorCode != ""; got != (scope == tools.MergeProgram) {
				t.Errorf("whole facts crossed = %v at scope %v", got, scope)
			}
		})
	}
}

func TestMergeChildKeepsSourceContextAndDeliveriesBounded(t *testing.T) {
	parent := &tools.ToolInvocationOut{}
	for range 2 {
		parent.MergeChild(filledOut(t), tools.MergeStageOK)
	}
	if got := len(parent.SourceContext.Locations); got != 1 {
		t.Fatalf("a repeated location was recorded %d times", got)
	}
	if got := len(parent.SourceDeliveries); got != 1 {
		t.Fatalf("a repeated delivery handle was recorded %d times", got)
	}
	wide := &tools.ToolInvocationOut{SourceContext: &api.SourceContext{}}
	for i := range api.MaxSourceContextLocations + 1 {
		wide.SourceContext.Locations = append(wide.SourceContext.Locations, sourceTarget(fmt.Sprintf("wide/%d.go", i)))
	}
	for i := range tools.MaxSourceDeliveryTags + 1 {
		wide.SourceDeliveries = append(wide.SourceDeliveries, tools.SourceDeliveryTag{Handle: fmt.Sprintf("wide-%d", i)})
	}
	parent.MergeChild(wide, tools.MergeStageOK)
	if got := len(parent.SourceContext.Locations); got != api.MaxSourceContextLocations || !parent.SourceContext.Truncated {
		t.Fatalf("locations = %d truncated = %v, want the cap and the truncation mark", got, parent.SourceContext.Truncated)
	}
	if got := len(parent.SourceDeliveries); got != tools.MaxSourceDeliveryTags {
		t.Fatalf("delivery tags = %d, want the cap %d", got, tools.MaxSourceDeliveryTags)
	}
}

func TestTakeCommittedDrainsOnlyCommittedEffects(t *testing.T) {
	out := filledOut(t)
	taken := out.TakeCommitted()
	left, moved := reflect.ValueOf(out).Elem(), reflect.ValueOf(taken).Elem()
	for name, rule := range invocationOutRules {
		stayed, went := !left.FieldByName(name).IsZero(), !moved.FieldByName(name).IsZero()
		if want := rule == committedEffect; went != want || stayed == want {
			t.Errorf("%s: taken = %v, left behind = %v, committed = %v", name, went, stayed, want)
		}
	}
	// A location taken earlier is new to the drained value.
	out.MergeChild(&tools.ToolInvocationOut{SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{sourceTarget("a.go")}}}, tools.MergeStageOK)
	if out.SourceContext == nil || len(out.SourceContext.Locations) != 1 {
		t.Fatalf("a drained value refused a location it no longer holds: %+v", out.SourceContext)
	}
}

// Stage merges, shared writers and a detaching call's take all reach one
// program-level value; every read is recorded exactly once.
func TestConcurrentMergeWriteAndTakeLoseNothing(t *testing.T) {
	const stages = 64
	program := &tools.ToolInvocationOut{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	taken := 0
	for i := range stages {
		wg.Add(3)
		go func() {
			defer wg.Done()
			child := &tools.ToolInvocationOut{SourceReads: []agentpresence.Read{{}}}
			program.MergeChild(child, tools.MergeStageOK)
		}()
		go func() {
			defer wg.Done()
			program.WithLock(func(out *tools.ToolInvocationOut) {
				out.Facts = out.Facts.WithFeedback(fmt.Sprintf("NOTE_%d", i), nil, nil)
			})
			program.AddSourceDelivery(tools.SourceDeliveryTag{Handle: fmt.Sprintf("delivery-%d", i)})
		}()
		go func() {
			defer wg.Done()
			got := len(program.TakeCommitted().SourceReads)
			mu.Lock()
			taken += got
			mu.Unlock()
		}()
	}
	wg.Wait()
	if total := taken + len(program.SourceReads); total != stages {
		t.Fatalf("recorded %d reads across takes and the remainder, want %d", total, stages)
	}
	if got := len(program.Facts.Feedback); got != stages {
		t.Fatalf("recorded %d feedback items, want %d", got, stages)
	}
}
