//go:build darwin || linux

package hostenv

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

const secretToken = "{{paintedwolf-secret:0f8fad5b-d9cb-469f-a165-70867728950e}}"

func TestEnvironmentUtilitiesListTheFrozenEnvironment(t *testing.T) {
	h := newHarness(t, defaultHost())
	h.env = []string{"B=two words", "A=1", "GIT_PAGER=cat", "SECRET=" + secretToken}
	listing := "A=1\nB=two words\nGIT_PAGER=cat\nSECRET=" + secretToken + "\n"
	cases := []struct {
		source, stdout string
		status         int
	}{
		{"printenv", listing, 0},
		{"printenv -- B A", "two words\n1\n", 0},
		{"printenv B missing A", "two words\n1\n", 1},
		{"printenv SECRET", secretToken + "\n", 0},
		{"printenv GIT_PAGER", "cat\n", 0},
		{"env", listing, 0},
		{"env -i", "", 0},
		{"env --ignore-environment C=3", "C=3\n", 0},
		{"env -u A --unset SECRET D=4 B=replaced", "B=replaced\nD=4\nGIT_PAGER=cat\n", 0},
		{"env -i 'Q=a b=c' E=", "E=\nQ=a b=c\n", 0},
	}
	for _, c := range cases {
		t.Run(c.source, func(t *testing.T) {
			h.run(c.source).expectOutput(t, c.source, c.stdout, "", c.status)
		})
	}
}

func TestEnvRefusesReservedAndMalformedAssignments(t *testing.T) {
	h := newHarness(t, defaultHost())
	for _, source := range []string{"env LD_PRELOAD=/tmp/x.so", "env PATH=/tmp", "env -i NODE_OPTIONS=x true", "env 1x=2", "env -u 1x", "env -x"} {
		code := "COMMAND_OPERAND_INVALID"
		if source == "env -x" {
			code = "COMMAND_OPTION_UNSUPPORTED"
		}
		h.run(source).expectRefusal(t, source, code)
	}
	h.env = []string{"LD_PRELOAD=/tmp/x.so"}
	h.run("env").expectRefusal(t, "native env with blocked export", "COMMAND_OPERAND_INVALID")
	h.run("printenv").expectRefusal(t, "native printenv with blocked export", "COMMAND_OPERAND_INVALID")
	h.run("env -i").expectOutput(t, "env -i", "", "", 0)
}

func TestEnvRunsCommandThroughTheNestedPort(t *testing.T) {
	h := newHarness(t, defaultHost())
	h.env = []string{"A=1"}
	h.run("env tool").expectRefusal(t, "env without a nested port", "COMMAND_NATIVE_FORM_UNSUPPORTED")
	nested := &nestedRecord{result: toolcontract.ProducerResult{Status: 7, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.EffectAttempted}}
	h.nested = nested
	r := h.run("env -i C=3 tool -x 'a b' D=4")
	if r.err != nil || r.result != nested.result {
		t.Fatalf("env did not return the nested outcome: %+v %v", r.result, r.err)
	}
	if !reflect.DeepEqual(nested.argv, []string{"tool", "-x", "a b", "D=4"}) || !reflect.DeepEqual(nested.environment, map[string]string{"C": "3"}) {
		t.Fatalf("nested argv %q environment %v", nested.argv, nested.environment)
	}
	h.run("env A=2 tool")
	if !reflect.DeepEqual(nested.environment, map[string]string{"A": "2"}) {
		t.Fatalf("nested environment %v", nested.environment)
	}
}

func TestOutputRefusesPastStreamBytes(t *testing.T) {
	h := newHarness(t, defaultHost())
	h.env = []string{"A=" + strings.Repeat("a", 64)}
	h.owners.stream = int64(len("A=") + 64 + 1)
	h.run("printenv").expectOutput(t, "printenv at the stream limit", "A="+strings.Repeat("a", 64)+"\n", "", 0)
	h.owners.stream--
	reject := h.run("printenv").expectRefusal(t, "printenv past the stream limit", "COMMAND_LIMIT_EXCEEDED")
	if reject.Data["limit_id"] != "stream_bytes" || reject.Data["observed"] != h.owners.stream+1 {
		t.Fatalf("limit data %+v", reject.Data)
	}
}
