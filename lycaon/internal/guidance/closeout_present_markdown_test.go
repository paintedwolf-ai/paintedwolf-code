package guidance_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/guidance"
)

func TestCloseoutMarkdownArtifactEmbedIDs(t *testing.T) {
	const id = "90dcba36-946a-46c6-933d-e94207b897ab"
	synth := "Here’s a mockup.\n\n![OpenCode running interface mockup](" + id + ")\n\nWhat it shows:"
	embeds := func(text string) []string {
		return guidance.CloseoutMarkdownArtifactEmbedIDs(citationmd.Parse(text, citationmd.Options{}))
	}
	got := embeds(synth)
	if len(got) != 1 || got[0] != id {
		t.Fatalf("markdown embed = %#v, want [%s]", got, id)
	}

	html := `See <img src="` + id + `" alt="x"> please`
	got = embeds(html)
	if len(got) != 1 || !strings.EqualFold(got[0], id) {
		t.Fatalf("html embed = %#v, want [%s]", got, id)
	}

	if got := embeds("![ok](https://example.com/a.png)"); len(got) != 0 {
		t.Fatalf("remote image must not match; got %#v", got)
	}
	if got := embeds("artifact " + id + " in prose"); len(got) != 0 {
		t.Fatalf("bare uuid in prose must not match; got %#v", got)
	}
	for _, literal := range []string{
		"Never write `![x](" + id + ")`.",
		"Never write:\n\n```md\n![x](" + id + ")\n```\n",
		"Never write:\n\n    <img src=\"" + id + "\">\n",
	} {
		if got := embeds(literal); len(got) != 0 {
			t.Fatalf("literal example in %q was refused as an embed: %#v", literal, got)
		}
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

func TestStripCloseoutMarkdownArtifactEmbedsKeepsLiteralExamples(t *testing.T) {
	const id = "c62775d4-c746-4e71-a3ce-562eaffc9528"
	in := "Never `![x](" + id + ")`.\n\n![real](" + id + ")\n\nBody."
	out, ids := guidance.StripCloseoutMarkdownArtifactEmbeds(in)
	if len(ids) != 1 || !strings.Contains(out, "`![x]("+id+")`") || strings.Contains(out, "![real]") {
		t.Fatalf("out = %q ids = %v", out, ids)
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
