package command

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

func TestExternalResolvedValuesPreserveArgumentAndProcessBoundaries(t *testing.T) {
	substitution := &commandprogram.ProcessSubstitution{}
	field := commandprogram.NewFieldWithParts("ref/dev/fd/3", commandprogram.Field{}.Span, []commandprogram.FieldPart{{Value: "ref"}, {Value: "/dev/fd/3", Substitution: substitution}})
	request := commandprogram.OperationRequest{Argv: commandprogram.Fields{{Value: "external"}, {Value: "--"}, {Value: "consumer"}, {Value: "ref"}, field}, Environment: []string{"TOKEN=ref", "EMPTY="}}
	args := externalValueArguments(request, "consumer ref ref/dev/fd/3", ".")
	args["argument_values"] = []any{"a b; echo nope", "literal="}
	args["environment_values"] = []any{"value=with\nbytes", ""}
	actual, err := externalResolvedRequest(request, args)
	if err != nil {
		t.Fatalf("resolve external values: %v", err)
	}
	if got := actual.Argv.Values(); !reflect.DeepEqual(got, []string{"external", "--", "consumer", "a b; echo nope", "literal=/dev/fd/3"}) {
		t.Fatalf("argv = %#v", got)
	}
	if actual.Argv[4].Parts()[1].Substitution != substitution {
		t.Fatal("process substitution identity changed")
	}
	if !reflect.DeepEqual(actual.Environment, []string{"TOKEN=value=with\nbytes", "EMPTY="}) {
		t.Fatalf("environment = %#v", actual.Environment)
	}
	if request.Argv[3].Value != "ref" || request.Environment[0] != "TOKEN=ref" || field.Parts()[0].Value != "ref" {
		t.Fatal("canonical input mutated")
	}
}

func TestExternalResolvedValuesRefuseChangedShapes(t *testing.T) {
	request := commandprogram.OperationRequest{Argv: commandprogram.Fields{{Value: "consumer"}, {Value: "ref"}}, Environment: []string{"TOKEN=ref"}}
	for _, change := range []struct {
		name, key string
		value     any
	}{
		{"executable", "executable", "other"},
		{"arguments missing", "argument_values", []string{}},
		{"arguments extra", "argument_values", []string{"one", "two"}},
		{"arguments type", "argument_values", []any{1}},
		{"environment missing", "environment_values", []string{}},
		{"environment type", "environment_values", false},
	} {
		t.Run(change.name, func(t *testing.T) {
			args := externalValueArguments(request, "consumer ref", ".")
			args[change.key] = change.value
			if _, err := externalResolvedRequest(request, args); err == nil {
				t.Fatal("changed input accepted")
			}
		})
	}
}

func TestExternalSecretContractSeparatesValueSinks(t *testing.T) {
	definition, err := compiledExternalDefinition()
	if err != nil {
		t.Fatalf("external definition: %v", err)
	}
	facts := definition.ReadOnlyFacts()
	if facts.Version != 2 {
		t.Fatalf("external version = %d", facts.Version)
	}
	reference := secretmatch.ReferenceToken("123e4567-e89b-42d3-a456-426614174000")
	request := commandprogram.OperationRequest{Argv: commandprogram.Fields{{Value: "consumer"}, {Value: reference}}, Environment: []string{"TOKEN=" + reference}}
	args := externalValueArguments(request, "consumer "+reference, ".")
	slots := facts.Contract.SecretReferenceArgs
	if use := secretcap.ReferenceUseOutsideSlots(args, slots); use.Complete || use.Malformed {
		t.Fatal("value reference lies outside declared sink")
	}
	for _, key := range []string{"executable", "cwd", "environment_names"} {
		changed := externalValueArguments(request, "consumer "+reference, ".")
		changed[key] = reference
		if !secretcap.ReferenceUseOutsideSlots(changed, slots).Complete {
			t.Fatalf("%s became a value sink", key)
		}
	}
}
