package llm

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func redactedSpan(kind api.RedactionKind) *api.HostSecretRedactionMeta {
	return api.NewHostSecretRedactionMeta([]api.RedactedSpan{{Field: "content", Start: 0, Length: 10, Kind: kind}})
}

func TestHostSecretRedactionNoticeUsesStructuredProvenance(t *testing.T) {
	literal := []api.Message{{Role: api.MessageRoleTool, Content: "literal [REDACTED] {{paintedwolf-secret:018ff2db-85f7-7f31-8da2-b9e81cd1a150}} text"}}
	if got := AppendHostSecretRedactionNotice(literal); len(got) != 1 {
		t.Fatalf("literal marker triggered host notice: %+v", got)
	}

	marked := []api.Message{{
		Role: api.MessageRoleTool, Content: "token=[REDACTED]",
		HostSecretRedaction: redactedSpan(api.RedactionKindSecret),
	}}
	got := AppendHostSecretRedactionNotice(marked)
	if len(got) != 2 || got[1].Role != api.MessageRoleSystem || got[1].Content != HostSecretRedactionNotice(true, false) {
		t.Fatalf("notice projection = %+v", got)
	}
	if got[1].Kind != api.MessageKindHostSecretRedactionNotice || got[1].HostSecretRedaction != nil {
		t.Fatalf("notice identity/provenance = %+v", got[1])
	}
	if again := appendHostSecretRedactionNotice(got, secretReplacements{redactions: 2}); len(again) != len(got) {
		t.Fatalf("notice projection duplicated: %+v", again)
	}
}

func TestHostSecretRedactionNoticeNamesReferencesByKind(t *testing.T) {
	referenced := []api.Message{{Role: api.MessageRoleTool, HostSecretRedaction: redactedSpan(api.RedactionKindManagedReference)}}
	got := AppendHostSecretRedactionNotice(referenced)
	if len(got) != 2 || got[1].Content != HostSecretRedactionNotice(false, true) {
		t.Fatalf("reference-only notice = %+v", got)
	}

	definitions := appendHostSecretRedactionNotice([]api.Message{{Role: api.MessageRoleUser}}, secretReplacements{references: 1})
	if len(definitions) != 2 || definitions[1].Content != HostSecretRedactionNotice(false, true) {
		t.Fatalf("tool-definition reference was not announced: %+v", definitions)
	}

	if HostSecretRedactionNotice(false, false) != "" {
		t.Fatal("a notice was written with nothing replaced")
	}
	for _, variant := range []string{HostSecretRedactionNotice(false, true), HostSecretRedactionNotice(true, true)} {
		if variant == HostSecretRedactionNotice(true, false) {
			t.Fatal("a notice naming references reads the same as one naming only redactions")
		}
	}
}

// An earlier projection announces what it saw; a later screen that writes a
// reference restates that one notice rather than adding a second.
func TestHostSecretRedactionNoticeIsRestatedInPlace(t *testing.T) {
	announced := AppendHostSecretRedactionNotice([]api.Message{{
		Role: api.MessageRoleTool, HostSecretRedaction: redactedSpan(api.RedactionKindSecret),
	}})
	later := append(append([]api.Message(nil), announced...), api.Message{
		Role: api.MessageRoleTool, HostSecretRedaction: redactedSpan(api.RedactionKindManagedReference),
	})

	got := AppendHostSecretRedactionNotice(later)

	if len(got) != 3 || got[1].Kind != api.MessageKindHostSecretRedactionNotice || got[1].Content != HostSecretRedactionNotice(true, true) {
		t.Fatalf("restated notice = %+v", got)
	}
	if later[1].Content != HostSecretRedactionNotice(true, false) {
		t.Fatal("restating the notice mutated the caller's messages")
	}
}
