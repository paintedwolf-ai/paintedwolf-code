package guidance_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
)

func TestCloseoutMarkdownArtifactEmbedIDs(t *testing.T) {
	const id = "90dcba36-946a-46c6-933d-e94207b897ab"
	synth := "Here’s a mockup.\n\n![OpenCode running interface mockup](" + id + ")\n\nWhat it shows:"
	got := guidance.CloseoutMarkdownArtifactEmbedIDs(synth)
	if len(got) != 1 || got[0] != id {
		t.Fatalf("markdown embed = %#v, want [%s]", got, id)
	}

	html := `See <img src="` + id + `" alt="x"> please`
	got = guidance.CloseoutMarkdownArtifactEmbedIDs(html)
	if len(got) != 1 || !strings.EqualFold(got[0], id) {
		t.Fatalf("html embed = %#v, want [%s]", got, id)
	}

	if got := guidance.CloseoutMarkdownArtifactEmbedIDs("![ok](https://example.com/a.png)"); len(got) != 0 {
		t.Fatalf("remote image must not match; got %#v", got)
	}
	if got := guidance.CloseoutMarkdownArtifactEmbedIDs("artifact " + id + " in prose"); len(got) != 0 {
		t.Fatalf("bare uuid in prose must not match; got %#v", got)
	}
}

func TestStripCloseoutMarkdownArtifactEmbeds(t *testing.T) {
	const id = "c62775d4-c746-4e71-a3ce-562eaffc9528"
	in := "Title\n\n![TUI mockup](" + id + ")\n\nBody."
	out, ids := guidance.StripCloseoutMarkdownArtifactEmbeds(in)
	if len(ids) != 1 || ids[0] != id {
		t.Fatalf("ids = %#v", ids)
	}
	if strings.Contains(out, id) || strings.Contains(out, "![") {
		t.Fatalf("strip left embed residue: %q", out)
	}
	if !strings.Contains(out, "Title") || !strings.Contains(out, "Body.") {
		t.Fatalf("strip removed prose: %q", out)
	}
}

func TestMarkdownEmbedOffenderHintData(t *testing.T) {
	data := guidance.OffenderHintData([]string{"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"})
	if data["offender_count"] != 1 {
		t.Fatalf("offender_count = %#v", data["offender_count"])
	}
	sample, _ := data["offenders_sample"].(string)
	if !strings.Contains(sample, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee") {
		t.Fatalf("sample = %q", sample)
	}
}
