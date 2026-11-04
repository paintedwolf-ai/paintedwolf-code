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

func TestCloseoutUnobservedImageSources(t *testing.T) {
	const id = "90dcba36-946a-46c6-933d-e94207b897ab"
	const fetched = "https://example.com/diagram.png"
	observed := func(url string) bool { return url == fetched }
	body := "Run:\n\n![Game](https://placeholder.example/648x440?text=Capture)\n\n" +
		"![Capture](" + id + ")\n\n<img src='data:image/png;base64,AAAA' alt=\"x\">\n\n" +
		"![Diagram](" + fetched + " \"from the docs\")\n\n<img src=\"https://placeholder.example/648x440?text=Capture\">\n\nDone."
	got := guidance.CloseoutUnobservedImageSources(body, observed)
	want := []string{"https://placeholder.example/648x440?text=Capture", "data:image/png;base64,AAAA"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("unobserved = %#v, want %#v: artifact ids and fetched URLs are not offenders, repeats list once", got, want)
	}
	if got := guidance.CloseoutUnobservedImageSources(body, nil); len(got) != 3 {
		t.Fatalf("without evidence every non-artifact source is unobserved; got %#v", got)
	}
	if got := guidance.CloseoutUnobservedImageSources("A link to [the page](https://example.com) and `![not an image]` in code.", nil); len(got) != 0 {
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
