package commandsurface_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLanguageV1SnapshotsAreDetached(t *testing.T) {
	first, err := commandsurface.LanguageV1()
	testutil.FailErr(t, "first catalog", err)
	first.Commands[0].Name = "changed"
	first.Language.Limits[0].Maximum = 0
	second, err := commandsurface.LanguageV1()
	testutil.FailErr(t, "second catalog", err)
	if second.Commands[0].Name == "changed" || second.Language.Limits[0].Maximum == 0 {
		t.Fatal("catalog snapshots share mutable facts")
	}
	if _, ok := second.CommandHelp("does-not-exist"); ok {
		t.Fatal("unknown utility returned help")
	}
}

func TestLanguageV1PreservesDialectAndPublicationChoices(t *testing.T) {
	c, err := commandsurface.LanguageV1()
	testutil.FailErr(t, "catalog", err)
	for name, want := range map[string]string{"sed": "GNU -i", "stat": "-f always BSD", "base64": "-D", "wc": "counts LF", "rg": "-r/--replace"} {
		help, ok := c.CommandHelp(name)
		if !ok || !strings.Contains(help, want) {
			t.Errorf("%s focused help missing %q", name, want)
		}
	}
	if !strings.Contains(c.Language.Rules["publication"], "comparison 1") {
		t.Fatal("publication lost complete comparison results")
	}
	for _, limit := range c.Language.Limits {
		if limit.ID == "preview_bytes" && (limit.Exceeded != "" || !strings.Contains(limit.Overflow, "truncate preview")) {
			t.Fatal("preview limit interrupts streams")
		}
		if limit.ID == "editable_file_bytes" && limit.Exceeded != "EDIT_FILE_TOO_LARGE" {
			t.Fatal("editable file owner code changed")
		}
	}
	var golden struct {
		Cases []struct {
			ID       string          `json:"id"`
			Program  string          `json:"program"`
			Expected json.RawMessage `json:"expected"`
		} `json:"golden_programs"`
	}
	testutil.FailErr(t, "golden cases", json.Unmarshal(c.Contracts["golden"], &golden))
	if len(golden.Cases) < 45 {
		t.Fatalf("missing concrete program cases: %d", len(golden.Cases))
	}
	for _, fixture := range golden.Cases {
		if fixture.ID == "" || fixture.Program == "" || len(fixture.Expected) == 0 {
			t.Fatal("incomplete authored program fixture")
		}
	}
}

func TestLanguageCatalogDecoderRejectsUnknownAndFutureShapes(t *testing.T) {
	for _, data := range []string{`{"version":2,"language":{"version":2}}`, `{"version":1,"typo":true}`, `{} {}`} {
		if _, err := commandsurface.DecodeLanguageCatalog([]byte(data)); err == nil {
			t.Fatalf("admitted unknown shape %s", data)
		}
	}
}
