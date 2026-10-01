package secretmatch

import "testing"

func TestDestinationKeyChangesWhenResolvedTransportChanges(t *testing.T) {
	first := DestinationKey("provider", "openai-compatible", "https://first.example/v1", "responses")
	replay := DestinationKey("provider", "openai-compatible", "https://first.example/v1", "responses")
	repointed := DestinationKey("provider", "openai-compatible", "https://second.example/v1", "responses")
	if first != replay {
		t.Fatalf("stable transport produced different keys: %q != %q", first, replay)
	}
	if first == repointed {
		t.Fatalf("repointed transport retained release key %q", first)
	}
	if first == DestinationKey("provider", "openai-compatible ", "https://first.example/v1", "responses") {
		t.Fatalf("whitespace-significant transport change retained release key %q", first)
	}
}

func TestDestinationKeyPreservesPlainIdentityWithoutTransportFacts(t *testing.T) {
	if got := DestinationKey(" provider "); got != "provider" {
		t.Fatalf("key = %q", got)
	}
}
