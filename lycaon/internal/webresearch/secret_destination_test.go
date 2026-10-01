package webresearch

import (
	"net/url"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	testutil.FailErr(t, "parse url", err)
	return u
}

func TestFetchDestinationBindsSchemeAndPort(t *testing.T) {
	t.Parallel()
	secure := httpDestination(mustParseURL(t, "https://api.example.com/v1/x"))
	plain := httpDestination(mustParseURL(t, "http://api.example.com/v1/x"))
	altPort := httpDestination(mustParseURL(t, "http://api.example.com:8080/v1/x"))
	explicitDefault := httpDestination(mustParseURL(t, "https://API.Example.com:443/other"))

	if secure.id == plain.id {
		t.Fatalf("scheme change kept the release identity %q", secure.id)
	}
	if plain.id == altPort.id {
		t.Fatalf("port change kept the release identity %q", plain.id)
	}
	if secure.id != explicitDefault.id {
		t.Fatalf("default port changed the identity: %q vs %q", secure.id, explicitDefault.id)
	}
	if secure.id == "api.example.com" {
		t.Fatal("release identity is a bare hostname")
	}
	if secure.label != "https://api.example.com:443" {
		t.Fatalf("label = %q", secure.label)
	}
}

func TestWebSearchDestinationBindsResolvedProviderSet(t *testing.T) {
	t.Parallel()
	reg := testRegistry(t)
	base := Settings{SearchEnabled: true, Config: map[string]map[string]string{}}
	cat := reg.Catalog()
	for _, entry := range cat.Entries() {
		base.Config[entry.ID] = resolveProviderConfig(entry, nil)
	}

	none := webSearchDestination(base, reg)
	if none.id == string(secretmatch.SurfaceWebSearch) {
		t.Fatal("an empty fan-out reused the bare surface name as its release identity")
	}

	withDirect := base
	withDirect.EnabledProviders = []string{directWireProviderID}
	expandDirectBundledResults(&withDirect, cat)
	fanout := webSearchDestination(withDirect, reg)
	if fanout.id == none.id {
		t.Fatalf("configuring providers kept the release identity %q", none.id)
	}
	if fanout.id == string(secretmatch.SurfaceWebSearch) {
		t.Fatal("release identity is the constant surface name")
	}
	if fanout.label == "" {
		t.Fatal("card has no destination to name")
	}

	// Repoint one provider endpoint.
	ids := resolvedSearchProviders(withDirect, reg)
	if len(ids) == 0 {
		t.Fatal("expected a resolved provider set")
	}
	repointed := withDirect
	repointed.Config = map[string]map[string]string{}
	for id, cfg := range withDirect.Config {
		copied := map[string]string{}
		for k, v := range cfg {
			copied[k] = v
		}
		repointed.Config[id] = copied
	}
	repointed.Config[ids[0].id]["endpoint"] = "https://impostor.example/search"
	if webSearchDestination(repointed, reg).id == fanout.id {
		t.Fatalf("repointed provider endpoint kept the release identity %q", fanout.id)
	}
}

func TestWebSearchDestinationLabelNamesProviders(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":                  "the configured web search providers",
		"brave":             "the configured providers brave",
		"brave,kagi,tavily": "the configured providers brave, kagi, tavily",
		"a,b,c,d,e":         "the configured providers a, b, c and 2 more",
	}
	for input, want := range cases {
		var ids []string
		if input != "" {
			ids = splitLabelInput(input)
		}
		if got := webSearchDestinationLabel(ids); got != want {
			t.Fatalf("label(%q) = %q, want %q", input, got, want)
		}
	}
}

func splitLabelInput(input string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(input); i++ {
		if i == len(input) || input[i] == ',' {
			out = append(out, input[start:i])
			start = i + 1
		}
	}
	return out
}
