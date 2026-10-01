package guidance

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Every streamed prefix contains only decoded synthesis text.
func TestStreamingCloseoutNarrativeProjectsEveryPrefix(t *testing.T) {
	full, err := MarshalCoordinatorCompletionReport(CoordinatorCompletionReport{
		Synthesis: "## Answer\n\nThe fix landed in `engine.py` — see the \"retry\" branch.\n",
		CitedURLs: []string{"https://example.test/a"},
	})
	testutil.FailErr(t, "MarshalCoordinatorCompletionReport failed", err)

	for i := 1; i <= len(full); i++ {
		prefix := full[:i]
		prose, ok := StreamingCloseoutNarrative(prefix)
		if !ok {
			t.Fatalf("prefix %d: envelope-shaped content must project", i)
		}
		if CloseoutBodyIsEnvelopeShaped(prose) {
			t.Fatalf("prefix %d: projected machine JSON %q", i, prose)
		}
		if strings.Contains(prose, `"synthesis"`) || strings.Contains(prose, `\n`) {
			t.Fatalf("prefix %d: projected undecoded envelope %q", i, prose)
		}
		want := "## Answer\n\nThe fix landed in `engine.py` — see the \"retry\" branch.\n"
		if !strings.HasPrefix(want, prose) {
			t.Fatalf("prefix %d: projection %q is not a prefix of the synthesis", i, prose)
		}
	}

	final, ok := StreamingCloseoutNarrative(full)
	if !ok {
		t.Fatal("complete envelope must project")
	}
	// Matching the committed text prevents a final-frame jump.
	committed, ok := CoordinatorCloseoutTranscriptNarrative(full)
	if !ok {
		t.Fatal("complete envelope must yield a transcript narrative")
	}
	if final != committed {
		t.Fatalf("last streamed frame %q != committed narrative %q", final, committed)
	}
}

func TestStreamingCloseoutNarrativeEdges(t *testing.T) {
	t.Run("plain prose is outside the envelope contract", func(t *testing.T) {
		if _, ok := StreamingCloseoutNarrative("## Answer\n\nPlain prose."); ok {
			t.Fatal("prose must pass through untouched")
		}
	})

	t.Run("synthesis not yet arrived projects empty", func(t *testing.T) {
		prose, ok := StreamingCloseoutNarrative(`{"cited_urls":["https://example.test/a"],"synth`)
		if !ok {
			t.Fatal("envelope-shaped content must project")
		}
		if prose != "" {
			t.Fatalf("expected empty projection, got %q", prose)
		}
	})

	t.Run("cut mid-escape drops the dangling tail", func(t *testing.T) {
		for _, cut := range []string{`{"synthesis":"line\`, `{"synthesis":"a\u26`, `{"synthesis":"b\u0`} {
			prose, ok := StreamingCloseoutNarrative(cut)
			if !ok {
				t.Fatalf("%q: envelope-shaped content must project", cut)
			}
			if strings.Contains(prose, `\`) {
				t.Fatalf("%q: leaked escape in %q", cut, prose)
			}
		}
	})

	t.Run("raw control byte still decodes", func(t *testing.T) {
		prose, ok := StreamingCloseoutNarrative("{\"synthesis\":\"first\nsecond")
		if !ok {
			t.Fatal("envelope-shaped content must project")
		}
		if prose != "first\nsecond" {
			t.Fatalf("got %q", prose)
		}
	})

	t.Run("fenced envelope projects", func(t *testing.T) {
		prose, ok := StreamingCloseoutNarrative("```json\n{\"synthesis\":\"Fenced answ")
		if !ok {
			t.Fatal("fenced envelope must project")
		}
		if prose != "Fenced answ" {
			t.Fatalf("got %q", prose)
		}
	})

	t.Run("nested envelope synthesis never previews", func(t *testing.T) {
		prose, ok := StreamingCloseoutNarrative(`{"synthesis":"{\"synthesis\":\"nested`)
		if !ok {
			t.Fatal("envelope-shaped content must project")
		}
		if prose != "" {
			t.Fatalf("expected empty projection for nested envelope, got %q", prose)
		}
	})
}
