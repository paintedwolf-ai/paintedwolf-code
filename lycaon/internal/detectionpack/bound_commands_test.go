package detectionpack

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestBoundDetectionConsumesExactArgvAndChildIdentity(t *testing.T) {
	stages := []toolcontract.ExecutionStage{
		{ExecutionID: "read-child", OperationID: "read", Argv: []string{"cat", "a name with spaces", "'literal-quotes'"}, CommandLine: "cat 'a name with spaces' \"'literal-quotes'\"", PipelineCommandLine: "cat input | aws s3 cp - s3://fixture/output"},
		{ExecutionID: "external-child", OperationID: "external", Argv: []string{"aws-vault", "exec", "fixture", "--", "aws", "s3", "cp", "-", "s3://fixture/output"}, CommandLine: "aws-vault exec fixture -- aws s3 cp - s3://fixture/output", PipelineCommandLine: "cat input | aws s3 cp - s3://fixture/output"},
	}
	event := NewEvent(ActionObservation{Tool: "command", SessionID: "session", ActionID: "outer-model-call", CommandLine: "misleading display; never tokenize as argv", BoundStages: stages, DeclaredDestinations: []string{"declared.example"}, Boundary: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy}})
	if len(event.executionGroups) != 2 {
		t.Fatalf("bound stages lost: %+v", event)
	}
	first, second := event.executionGroups[0], event.executionGroups[1]
	if !reflect.DeepEqual(first.Argv, stages[0].Argv) || !reflect.DeepEqual(first.Image, []string{"cat"}) || first.ActionID != "read-child" {
		t.Fatalf("native bound identity/argv changed: %+v", first)
	}
	if !reflect.DeepEqual(second.Image, []string{"aws"}) || !reflect.DeepEqual(second.ApiAction, []string{"s3:cp"}) || second.ActionID != "external-child" || second.PipelineCommandLine != stages[1].PipelineCommandLine {
		t.Fatalf("wrapper/pipeline identity lost: %+v", second)
	}
	stages[0].Argv[1] = "changed after boundary"
	if first.Argv[1] != "a name with spaces" {
		t.Fatal("event retained mutable argv")
	}
	if !reflect.DeepEqual(first.DeclaredDestination, []string{"declared.example"}) {
		t.Fatal("declared destination facts lost")
	}
	literalAssignment := NewEvent(ActionObservation{Tool: "command", BoundStages: []toolcontract.ExecutionStage{{ExecutionID: "literal-assignment", Argv: []string{"FIXTURE=program", "argument"}}}})
	if !reflect.DeepEqual(literalAssignment.Image, []string{"fixture=program"}) {
		t.Fatalf("bound executable became a variable assignment: %+v", literalAssignment.Image)
	}
	quoted := NewEvent(ActionObservation{Tool: "command", BoundStages: []toolcontract.ExecutionStage{{ExecutionID: "literal", Argv: []string{"'curl'", "fixture"}}}})
	if !reflect.DeepEqual(quoted.Image, []string{"'curl'"}) {
		t.Fatalf("bound literal quote became syntax: %+v", quoted.Image)
	}
}

func TestBoundActionUsesSharedDetectionProjectionWithoutInventedReach(t *testing.T) {
	source := NewGateSource(nil)
	action := hitl.ProposedAction{Invocation: hitl.ActionInvocation{Tool: "command", ActionID: "outer", Args: map[string]any{"command": "rm misleading"}, BoundStages: []toolcontract.ExecutionStage{{ExecutionID: "child", Argv: []string{"cat", "input"}, CommandLine: "cat input", PipelineCommandLine: "cat input"}}}, Scope: hitl.ActionScope{SessionID: "session"}}
	observation := source.observationFor(action)
	event := NewEvent(observation)
	if event.ActionID != "child" || !reflect.DeepEqual(event.Image, []string{"cat"}) || event.EffectReach != "unproven" {
		t.Fatalf("bound action used legacy text or invented local proof: %+v", event)
	}
}
