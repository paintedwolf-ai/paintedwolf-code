package query

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// TestJqEditPreservesFormatAndPublishesAtItsPins runs the shared
// format-preserving edit and lands it through the pinned write door.
func TestJqEditPreservesFormatAndPublishesAtItsPins(t *testing.T) {
	yamlSource := "# service settings\nname: api # the service\nversion: 1.0\nport: 8080\n"
	tomlSource := "[package]\nname = \"demo\"\nversion = \"0.1.0\"\nratio = 1.0\n"
	h := newHarness(t, map[string]string{"settings.json": "{\"enabled\":false,\"n\":1.50}\n", "svc.yaml": yamlSource, "Cargo.toml": tomlSource, "tagged.yaml": "ref: !Ref bucket\n", "commented.toml": "[a]\n# keep\nb = 1\n"})

	r := h.exec("", "jq-edit", "--path", "settings.json", "--query", ".enabled = true")
	r.expect(t, "json edit", "", 0)
	if got := h.disk("settings.json"); got != "{\"enabled\":true,\"n\":1.50}\n" {
		t.Fatalf("json literals or layout changed: %q", got)
	}
	if r.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("published edit certainty = %+v", r.result)
	}
	pins := h.pins.pins[r.execution]
	if len(pins) != 2 || pins[0].Role != toolcontract.SourceRevision || pins[1].Role != toolcontract.DestinationRevision || pins[0].ContentSHA256 != textfile.SHA256([]byte("{\"enabled\":false,\"n\":1.50}\n")) {
		t.Fatalf("pins = %+v", pins)
	}

	h.exec("", "jq-edit", "--path", "svc.yaml", "--query", ".port = 9090").expect(t, "yaml edit", "", 0)
	if got := h.disk("svc.yaml"); got != strings.Replace(yamlSource, "8080", "9090", 1) {
		t.Fatalf("yaml comments or literals lost: %q", got)
	}
	h.exec("", "jq-edit", "--path", "Cargo.toml", "--query", `.package.version = "0.2.0"`, "--format", "toml").expect(t, "toml edit", "", 0)
	if got := h.disk("Cargo.toml"); got != strings.Replace(tomlSource, "0.1.0", "0.2.0", 1) {
		t.Fatalf("toml literals changed: %q", got)
	}

	unchanged := h.exec("", "jq-edit", "--path", "svc.yaml", "--query", ".")
	unchanged.expect(t, "identity edit", "", 0)
	if unchanged.result.EffectCertainty != toolcontract.NoEffect {
		t.Fatalf("identity edit published: %+v", unchanged.result)
	}

	for _, c := range []struct{ path, query, code string }{
		{"tagged.yaml", `.ref = "other"`, "JQ_EDIT_LOSSY"},
		{"commented.toml", ".a.b = 2", "JQ_EDIT_LOSSY"},
		{"settings.json", "empty", "JQ_EDIT_EMPTY"},
		{"settings.json", ".[", "JQ_QUERY_INVALID"},
	} {
		before := h.disk(c.path)
		h.exec("", "jq-edit", "--path", c.path, "--query", c.query).refused(t, c.path+" "+c.query, c.code)
		if h.disk(c.path) != before {
			t.Fatalf("%s changed after a refused edit", c.path)
		}
	}
	h.exec("", "jq-edit", "--query", ".").refused(t, "missing path", "COMMAND_OPERAND_INVALID")
	h.exec("", "jq-edit", "--path", "settings.json").refused(t, "missing query", "COMMAND_OPERAND_INVALID")
	h.exec("", "jq-edit", "--path", "/etc/hosts", "--query", ".").refused(t, "outside roots", "SURVEY_PATH_ESCAPE")
	missing := h.exec("", "jq-edit", "--path", "missing.json", "--query", ".")
	if missing.result.Status != 2 || missing.stderr == "" {
		t.Fatalf("missing document = %+v %q", missing.result, missing.stderr)
	}
}

// TestPlainQueryOutputIsNotFormatPreserving: jq's serialization of a
// document drops what jq-edit keeps, which is why a jq redirect is a plain
// rewrite and jq-edit is the format-aware edit. yq keeps YAML comments
// itself, as the yq binary does.
func TestPlainQueryOutputIsNotFormatPreserving(t *testing.T) {
	h := newHarness(t, map[string]string{"svc.json": "{\"port\":8080,\"n\":1.50}\n", "svc.yaml": "# settings\nport: 8080 # public\n"})
	h.exec("", "jq", ".port = 9090", "svc.json").expect(t, "plain jq", "{\n  \"port\": 9090,\n  \"n\": 1.50\n}\n", 0)
	h.exec("", "jq-edit", "--path", "svc.json", "--query", ".port = 9090").expect(t, "jq-edit", "", 0)
	if got := h.disk("svc.json"); got != "{\"port\":9090,\"n\":1.50}\n" {
		t.Fatalf("jq-edit lost layout: %q", got)
	}
	h.exec("", "yq", ".port = 9090", "svc.yaml").expect(t, "plain yq", "# settings\nport: 9090 # public\n", 0)
}

func TestJqEditKeepsCapturedDiskEncoding(t *testing.T) {
	for _, encoding := range []string{textfile.UTF8BOM, textfile.UTF16LEBOM, textfile.UTF16BEBOM} {
		raw, err := textfile.EncodeBounded("{\"x\":1}\n", encoding, textfile.LimitsForRaw(1024))
		testutil.FailErr(t, "textfile.EncodeBounded failed", err)
		h := newHarness(t, map[string]string{"encoded.json": string(raw)})
		h.exec("", "jq-edit", "--path", "encoded.json", "--query", ".x=2").expect(t, encoding, "", 0)
		want, err := textfile.EncodeBounded("{\"x\":2}\n", encoding, textfile.LimitsForRaw(1024))
		if err != nil || h.disk("encoded.json") != string(want) {
			t.Fatalf("%s encoding changed err %v", encoding, err)
		}
	}
}
