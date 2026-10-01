package secretcap

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

const referenceTestID = "123e4567-e89b-42d3-a456-426614174000"

func TestReferenceGrammarSeparatesTokensFromLiteralText(t *testing.T) {
	reference := secretmatch.ReferenceToken(referenceTestID)
	cases := []struct {
		name, text                    string
		contains, complete, malformed bool
	}{
		{"complete", reference, true, true, false},
		{"trimmed", " \n" + reference + "\t", true, true, false},
		{"embedded", "Bearer " + reference, true, false, false},
		{"quoted", `"` + reference + `"`, true, false, false},
		{"adjacent", reference + reference, true, false, false},
		{"partial", "{{paintedwolf-secret:", false, false, true},
		{"example", "{{paintedwolf-secret:example}}", false, false, true},
		{"rewritten ID", "{{paintedwolf-secret:[REDACTED]}}", false, false, true},
		{"missing close", strings.TrimSuffix(reference, "}}"), false, false, true},
		{"uppercase ID", "{{paintedwolf-secret:" + strings.ToUpper(referenceTestID) + "}}", false, false, true},
		{"wrong UUID groups", "{{paintedwolf-secret:123e4567e-89b-42d3-a456-426614174000}}", false, false, true},
		{"complete then damaged", reference + " {{paintedwolf-secret:[REDACTED]}}", true, false, true},
		{"foreign namespace", "{{secret:" + referenceTestID + "}}", false, false, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			use := ReferenceUseInSlots(map[string]any{"nested": []any{tt.text}}, nil)
			if use.Complete != tt.contains || use.Malformed != tt.malformed {
				t.Fatalf("use = %+v, want complete %v malformed %v", use, tt.contains, tt.malformed)
			}
			id, err := ParseReference(tt.text)
			if tt.complete {
				testutil.FailErr(t, "parse complete reference", err)
				if id != referenceTestID {
					t.Fatalf("id = %q", id)
				}
			} else if !errors.Is(err, ErrInvalidReference) {
				t.Fatalf("parse error = %v, want invalid reference", err)
			}
			if !tt.contains {
				args := map[string]any{"value": tt.text, "nested": []any{tt.text}}
				out, err := (&Service{}).Resolve(t.Context(), args, ResolveContext{})
				testutil.FailErr(t, "resolve literal text without a store", err)
				if !reflect.DeepEqual(out.Arguments, args) {
					t.Fatalf("literal text changed: %#v", out)
				}
			}
		})
	}
}

func TestResolveTreatsInsertedSecretBytesAsOpaque(t *testing.T) {
	service, _, _ := testService(t)
	second, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "opaque-second", Name: "Second", Purpose: "opaque substitution", Value: "second-protected-value",
	})
	testutil.FailErr(t, "create second secret", err)
	first, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "opaque-first", Name: "First", Purpose: "opaque substitution", Value: "temporary-first-value",
	})
	testutil.FailErr(t, "create first secret", err)
	opaque := "前\x00'" + second.Reference + ":" + first.Reference + " {{paintedwolf-secret:partial\n"
	_, err = service.ReplaceValue(t.Context(), ReplaceValueRequest{ProjectID: testdbseed.DefaultProjectID, Reference: first.Reference, Value: opaque})
	testutil.FailErr(t, "store marker-bearing bytes", err)
	cases := []struct{ input, want string }{
		{first.Reference, opaque},
		{first.Reference + second.Reference, opaque + "second-protected-value"},
		{second.Reference + first.Reference, "second-protected-value" + opaque},
		{"prefix" + first.Reference + "/" + first.Reference + "suffix", "prefix" + opaque + "/" + opaque + "suffix"},
		{first.Reference + " {{paintedwolf-secret:example}}", opaque + " {{paintedwolf-secret:example}}"},
	}
	for _, tt := range cases {
		args := map[string]any{"value": tt.input}
		out, err := service.Resolve(t.Context(), args, ResolveContext{ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1"})
		testutil.FailErr(t, "resolve original spans", err)
		if out.Arguments["value"] != tt.want {
			t.Fatal("substitution changed opaque bytes or repeated a replacement")
		}
		if args["value"] != tt.input {
			t.Fatal("canonical argument changed")
		}
	}
}

func TestResolveFailsClosedForCompleteUnknownReferences(t *testing.T) {
	service, _, _ := testService(t)
	out, err := service.Resolve(t.Context(), map[string]any{"value": secretmatch.ReferenceToken(referenceTestID)}, ResolveContext{ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1"})
	if !errors.Is(err, ErrNotFound) || out != nil {
		t.Fatalf("unknown reference = %#v, %v", out, err)
	}
}
