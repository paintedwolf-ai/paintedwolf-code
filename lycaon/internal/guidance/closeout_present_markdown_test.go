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

func TestCloseoutUnobservedImageSources(t *testing.T) {
	const id = "90dcba36-946a-46c6-933d-e94207b897ab"
	parse := func(text string) citationmd.Document { return citationmd.Parse(text, citationmd.Options{}) }
	const fetched = "https://example.com/diagram.png"
	observed := func(url string) bool { return url == fetched }
	body := "Run:\n\n![Game](https://placeholder.example/648x440?text=Capture)\n\n" +
		"![Capture](" + id + ")\n\n<img src='data:image/png;base64,AAAA' alt=\"x\">\n\n" +
		"![Diagram](" + fetched + " \"from the docs\")\n\n<img src=\"https://placeholder.example/648x440?text=Capture\">\n\nDone."
	got := guidance.CloseoutUnobservedImageSources(parse(body), observed)
	want := []string{"https://placeholder.example/648x440?text=Capture", "data:image/png;base64,AAAA"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("unobserved = %#v, want %#v: artifact ids and fetched URLs are not offenders, repeats list once", got, want)
	}
	if got := guidance.CloseoutUnobservedImageSources(parse(body), nil); len(got) != 3 {
		t.Fatalf("without evidence every non-artifact source is unobserved; got %#v", got)
	}
	// The parse sees what Den renders, which a pattern over the text would miss.
	for name, text := range map[string]string{
		"reference image":   "![a][r]\n\n[r]: https://x.test/ref.png",
		"image in a link":   "[![a](https://x.test/nested.png)](https://x.test/page)",
		"angle brackets":    "![a](<https://x.test/angle.png> \"title\")",
		"HTML block":        "<div>\n<img src=https://x.test/block.png>\n</div>",
		"whole-reply fence": "```markdown\n![a](https://x.test/wrapped.png)\n```",
	} {
		if got := guidance.CloseoutUnobservedImageSources(parse(text), nil); len(got) != 1 || !strings.Contains(got[0], "x.test/") {
			t.Errorf("%s: unobserved = %#v", name, got)
		}
	}
	if got := guidance.CloseoutUnobservedImageSources(parse("A link to [the page](https://example.com) and `![not an image](https://x.test/a.png)` in code.\n\n```\n![fenced](https://x.test/b.png)\n```"), nil); len(got) != 0 {
		t.Fatalf("links and inline code are not image embeds; got %#v", got)
	}
}

func TestStripCloseoutImageEmbeds(t *testing.T) {
	const id = "c62775d4-c746-4e71-a3ce-562eaffc9528"
	const fetched = "https://example.com/diagram.png"
	observed := func(url string) bool { return url == fetched }
	in := "Title\n\n![TUI mockup](" + id + ")\n\n![Fake](https://placeholder.example/1x1.png)\n\n![Real](" + fetched + ")\n\nBody."
	out, ids := guidance.StripCloseoutImageEmbeds(in, observed)
	if len(ids) != 1 || ids[0] != id {
		t.Fatalf("ids = %#v", ids)
	}
	if strings.Contains(out, id) || strings.Contains(out, "placeholder.example") {
		t.Fatalf("strip left embed residue: %q", out)
	}
	if !strings.Contains(out, "![Real]("+fetched+")") {
		t.Fatalf("strip removed an observed image: %q", out)
	}
	if !strings.Contains(out, "Title") || !strings.Contains(out, "Body.") {
		t.Fatalf("strip removed prose: %q", out)
	}
	if clean, ids := guidance.StripCloseoutImageEmbeds("Plain prose.", nil); clean != "Plain prose." || ids != nil {
		t.Fatalf("clean body changed: %q %#v", clean, ids)
	}
}

func TestStripCloseoutImageEmbedsKeepsLiteralExamples(t *testing.T) {
	const id = "c62775d4-c746-4e71-a3ce-562eaffc9528"
	in := "Never `![x](" + id + ")`.\n\n![real](" + id + ")\n\nBody."
	out, ids := guidance.StripCloseoutImageEmbeds(in, nil)
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
