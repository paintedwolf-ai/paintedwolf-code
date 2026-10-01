package secretmatch

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// rememberStore is the evidence base the app wires to secretharvest: values go
// in through Remember and come back out through the harvest lens.
type rememberStore struct {
	mu     sync.Mutex
	values []HarvestedValue
}

func (s *rememberStore) remember(_ string, values []Remembered) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range values {
		s.values = append(s.values, HarvestedValue{
			Name: v.Name, Container: v.Origin, Secret: v.Secret,
			RuleID: v.RuleID, Title: v.Title, Source: v.Source,
		})
	}
}

func (s *rememberStore) source(context.Context) []HarvestedValue {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]HarvestedValue, len(s.values))
	copy(out, s.values)
	return out
}

// Remembered values match even where the catalog rule lacks keyword context.
func TestRememberedMatchRedactsWhereTheRuleCannotReach(t *testing.T) {
	ctx := context.Background()
	m := loadBundled(t)
	store := &rememberStore{}
	m.SetRemember(store.remember)
	m.SetHarvestSource(store.source)

	const token = "67ff6e39282cb4d81f8da08b44df3e8b524a5960"
	mint := "gitea admin user generate-access-token --username releasebot " +
		"--token-name release-cli --scopes all\nstdout: Access token was successfully created: " + token

	if out, spans := redactMatches(mint, m.screenRaw(ctx, mint)); len(spans) != 0 {
		t.Fatalf("precondition failed: the mint line already matches a rule, so this test proves nothing: %s", out)
	}

	// The same value also matches with the rule's keyword present.
	use := "GITEA_TOKEN=" + token + " ./release-cli create-repo"
	hits := m.ScreenLabeledContext(ctx, "", use)
	if len(hits) == 0 {
		t.Fatal("the use site did not match; the fixture no longer exercises the rule")
	}
	remembered := make([]Remembered, 0, len(hits))
	for _, hit := range hits {
		remembered = append(remembered, Remembered{
			Secret: runeRange(use, hit.Start, hit.End),
			Name:   "command", Origin: "tool_result",
			RuleID: hit.RuleID, Title: hit.Title, Source: SourceRememberedMatch,
		})
	}
	m.Remember("root-1", remembered)

	out, spans := redactMatches(mint, m.screenRaw(ctx, mint))
	if strings.Contains(out, token) {
		t.Fatalf("the minting line still holds the value after the same value was confirmed elsewhere:\n%s", out)
	}
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	if spans[0].Source != SourceRememberedMatch {
		t.Errorf("source = %q, want %q", spans[0].Source, SourceRememberedMatch)
	}
	if spans[0].RuleID != "kingfisher.gitea.1" {
		t.Errorf("rule = %q, want the identity the value was first recognized under", spans[0].RuleID)
	}
}

// A span reports where its marker sits in the redacted output and how wide the
// marker is. Reporting the original's width would leak the secret's length to
// anyone reading the transcript.
func TestRedactionSpanDescribesTheMarkerNotTheSecret(t *testing.T) {
	m := harvestOf(t, HarvestedValue{
		Name: "SESSION_SECRET", Container: ".env", Secret: "zebra-purple-42-and-longer-still",
	})
	in := "prefix zebra-purple-42-and-longer-still suffix"
	out, spans := redactMatches(in, m.screenRaw(context.Background(), in))
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	if spans[0].Length != PlaceholderRunes() {
		t.Errorf("length = %d, want the marker width %d", spans[0].Length, PlaceholderRunes())
	}
	marker := []rune(out)[spans[0].Start : spans[0].Start+spans[0].Length]
	if string(marker) != "[REDACTED]" {
		t.Errorf("span points at %q, not the marker it describes", string(marker))
	}
}

func runeRange(s string, start, end int) string {
	r := []rune(s)
	return string(r[start:end])
}
