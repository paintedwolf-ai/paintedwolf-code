//go:build darwin || linux

package textutil

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

func TestEnvsubstFormsProduceExactBytes(t *testing.T) {
	h := newHarness(t)
	h.env = []string{"HOST=h1", "USER2=me", "X=", "_u=U", "PATH=/bin"}
	h.goldens(t, []golden{
		{"envsubst", "h=$HOST u=${USER2} x=$X ${HOST} $1 ${A:-b} $$ $ \\$HOST ${BAD-} ${9a} $_u\n", "h=h1 u=me x= h1 $1 ${A:-b} $$ $ \\h1 ${BAD-} ${9a} U\n", 0},
		{"envsubst '$HOST'", "h=$HOST u=${USER2} x=$X\n", "h=h1 u=${USER2} x=$X\n", 0},
		{"envsubst 'only ${USER2} here'", "$HOST ${USER2}", "$HOST me", 0},
		{"envsubst '$MISSING'", "[$MISSING]", "[]", 0},
		{"envsubst 'no vars'", "$HOST", "$HOST", 0},
		{"envsubst", "a$HOSTb ${HOST}b\n", "a h1b\n", 0},
		{"envsubst", "${HOST", "${HOST", 0},
		{"envsubst", "end$", "end$", 0},
		{"envsubst", "", "", 0},
		{"envsubst -v '$Z ${A} $B $A x ${C'", "ignored", "Z\nA\nB\nA\n", 0},
		{"envsubst --variables 'none'", "", "", 0},
	})
}

// TestEnvsubstStreamsLongInputs substitutes across read buffer boundaries.
func TestEnvsubstStreamsLongInputs(t *testing.T) {
	h := newHarness(t)
	h.env = []string{"V=value"}
	input := strings.Repeat("x", 40000) + "$V-" + strings.Repeat("y", 40000) + "${V}"
	h.run("envsubst", input).expectOutput(t, "long input", strings.Repeat("x", 40000)+"value-"+strings.Repeat("y", 40000)+"value", 0)
	h.run("envsubst", "$V"+strings.Repeat("y", 40000)).expectOutput(t, "long name", "", 0)
}

// TestEnvsubstSubstitutesSecretReferencesAsReferences: the frame holds a
// managed secret as its opaque reference, and envsubst copies that text.
func TestEnvsubstSubstitutesSecretReferencesAsReferences(t *testing.T) {
	h := newHarness(t)
	reference := secretmatch.ReferenceToken("0b9d6a3e-4f21-4c8e-9a57-1d2e3f4a5b6c")
	h.env = []string{"TOKEN=" + reference}
	r := h.run("envsubst '$TOKEN'", "token=$TOKEN\n")
	r.expectOutput(t, "secret reference", "token="+reference+"\n", 0)
	if !secretmatch.ContainsReferenceToken(r.stdout) {
		t.Fatalf("substituted reference %q is not a complete reference token", r.stdout)
	}
}

func TestEnvsubstRefusesInvalidOperands(t *testing.T) {
	h := newHarness(t)
	h.run("envsubst -v", "").expectRefusal(t, "-v without format", "COMMAND_OPERAND_INVALID")
	h.run("envsubst '$A' '$B'", "").expectRefusal(t, "two formats", "COMMAND_OPERAND_INVALID")
}
