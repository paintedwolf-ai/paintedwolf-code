package sourceview

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHTMLTreeControlsScriptExecution(t *testing.T) {
	for _, source := range []string{
		`<template><script>eval(location.hash)</script></template>`,
		`<div><template><table><script>eval(location.hash)</script></table></template></div>`,
		`<noscript><script>eval(location.hash)</script></noscript>`,
		`<iframe><script>eval(location.hash)</script></iframe>`,
		`<xmp><script>eval(location.hash)</script></xmp>`,
		`<plaintext><script>eval(location.hash)</script>`,
	} {
		t.Run(source, func(t *testing.T) {
			projected, _, err := Scripts(t.Context(), []byte(source), false)
			testutil.FailErr(t, "project HTML tree", err)
			if len(projected) != 0 {
				t.Fatalf("inert HTML projected: %+v", projected)
			}
		})
	}
}

func TestHTMLProjectionUsesOriginalBytesAfterTreeRecovery(t *testing.T) {
	for _, source := range []string{
		`<noscript><script>inert</noscript><script>eval(location.hash)</script>`,
		`<template><script>inert</script></template><script>eval(location.hash)</script>`,
		`<table><div><script>eval(location.hash)</script></div></table>`,
		`<script/>eval(location.hash)</script>`,
		`<script data-paintedwolf-source="0">eval(location.hash)</script>`,
		`<script DATA-PAINTEDWOLF-SOURCE="0">eval(location.hash)</script>`,
		`<script>const data = '<script foo="bar">'; eval(location.hash)</script>`,
		"\xff狼\r\n<script>eval(location.hash)</script>",
	} {
		t.Run(source, func(t *testing.T) {
			projected, _, err := Scripts(t.Context(), []byte(source), false)
			testutil.FailErr(t, "project recovered HTML", err)
			if len(projected) != 1 {
				t.Fatalf("script count = %d", len(projected))
			}
			got := projected[0].Source
			body := []byte("eval(location.hash)")
			offset := bytes.Index([]byte(source), body)
			generatedOffset := bytes.Index(got, body)
			if generatedOffset < 0 {
				t.Fatal("script bytes lost")
			}
			position := Position{Line: bytes.Count(got[:generatedOffset], []byte("\n")) + 1, Column: generatedOffset - bytes.LastIndexByte(got[:generatedOffset], '\n')}
			mapped, ok := projected[0].Map.MapPosition(position)
			want := Position{Line: strings.Count(source[:offset], "\n") + 1, Column: offset - strings.LastIndexByte(source[:offset], '\n')}
			if !ok || mapped != want {
				t.Fatalf("mapped=%+v want=%+v valid=%v", mapped, want, ok)
			}
			if bytes.Contains(got, []byte("inert")) {
				t.Fatal("inert script became executable")
			}
		})
	}
}

func TestClassicScriptsFollowSourceOrderAfterTreeRecovery(t *testing.T) {
	projected, _, err := Scripts(t.Context(), []byte(`<table><script>let code = location.hash;</script><div><script>eval(code);</script></div></table>`), false)
	testutil.FailErr(t, "project parser execution order", err)
	if len(projected) != 1 || !bytes.Equal(projected[0].Source, []byte("let code = location.hash;\n;\neval(code);")) {
		t.Fatalf("script execution order = %+v", projected)
	}
}
