package jsonfence_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/jsonfence"
)

func TestEnvelopeOnly(t *testing.T) {
	t.Parallel()

	const envelope = `{"synthesis":"Report.","cited_evidence":[{"path":"a.go","line":1}],"cited_urls":[]}`
	valid := func(candidate string) bool { return candidate == envelope }

	t.Run("bare json", func(t *testing.T) {
		t.Parallel()
		if !jsonfence.EnvelopeOnly(envelope, valid) {
			t.Fatal("expected bare envelope")
		}
	})

	t.Run("json fence", func(t *testing.T) {
		t.Parallel()
		content := "```json\n" + envelope + "\n```"
		if !jsonfence.EnvelopeOnly(content, valid) {
			t.Fatal("expected json fence")
		}
	})

	t.Run("blank line after fence opener", func(t *testing.T) {
		t.Parallel()
		content := "```json\n\n" + envelope + "\n```"
		if !jsonfence.EnvelopeOnly(content, valid) {
			t.Fatal("expected fence with leading blank line")
		}
	})

	t.Run("bare code fence", func(t *testing.T) {
		t.Parallel()
		content := "```\n" + envelope + "\n```"
		if !jsonfence.EnvelopeOnly(content, valid) {
			t.Fatal("expected bare code fence")
		}
	})

	t.Run("JSON uppercase tag", func(t *testing.T) {
		t.Parallel()
		content := "```JSON\n" + envelope + "\n```"
		if !jsonfence.EnvelopeOnly(content, valid) {
			t.Fatal("expected case-insensitive json tag")
		}
	})

	t.Run("spaced json tag", func(t *testing.T) {
		t.Parallel()
		content := "``` json\n" + envelope + "\n```"
		if !jsonfence.EnvelopeOnly(content, valid) {
			t.Fatal("expected spaced json tag")
		}
	})

	t.Run("prose before fence rejected", func(t *testing.T) {
		t.Parallel()
		content := "intro\n\n```json\n" + envelope + "\n```"
		if jsonfence.EnvelopeOnly(content, valid) {
			t.Fatal("hybrid content must not be envelope-only")
		}
	})
}

func TestCandidates(t *testing.T) {
	t.Parallel()

	const inner = `{"a":1}`
	content := "prefix\n```json\n" + inner + "\n```\nsuffix"
	candidates := jsonfence.Candidates(content)
	if len(candidates) < 2 {
		t.Fatalf("candidates = %v, want at least raw body and fence interior", candidates)
	}
	foundInner := false
	for _, c := range candidates {
		if c == inner {
			foundInner = true
		}
	}
	if !foundInner {
		t.Fatalf("fence interior missing from candidates: %v", candidates)
	}
}

func TestFirstJSONValue(t *testing.T) {
	t.Parallel()

	malformed := `{"synthesis":"Done."}, "cited_evidence":[]}`
	got, ok := jsonfence.FirstJSONValue(malformed)
	if !ok || got != `{"synthesis":"Done."}` {
		t.Fatalf("FirstJSONValue = %q ok=%v", got, ok)
	}
	candidates := jsonfence.Candidates(malformed)
	found := false
	for _, c := range candidates {
		if c == `{"synthesis":"Done."}` {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Candidates missing first-value salvage: %v", candidates)
	}
}

func TestFirst(t *testing.T) {
	t.Parallel()

	payload, ok := jsonfence.First("```JSON\n{\"k\":1}\n```")
	if !ok || payload != `{"k":1}` {
		t.Fatalf("First = %q, ok=%v", payload, ok)
	}
}
