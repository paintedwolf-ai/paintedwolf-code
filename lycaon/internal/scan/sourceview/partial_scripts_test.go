package sourceview

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/odvcencio/gotreesitter/grammars"
)

func TestHTMLNamespaceLimitationsPreserveOrdinaryScripts(t *testing.T) {
	for _, container := range []string{"svg", "math"} {
		source := []byte("<script>let value = location.hash;</script>\n<" + container + "><script>foreign()</script></" + container + ">\n<script>eval(value)</script>")
		projected, limitations, err := Scripts(t.Context(), source, false)
		testutil.FailErr(t, "project mixed HTML namespaces", err)
		if len(projected) != 1 || len(limitations) != 1 || limitations[0].Construct != "embedded_namespace" || limitations[0].Start.Line != 2 {
			t.Fatalf("projections=%+v limitations=%+v", projected, limitations)
		}
		if !bytes.Equal(projected[0].Source, []byte("let value = location.hash;\n;\neval(value)")) {
			t.Fatalf("ordinary script bytes changed: %q", projected[0].Source)
		}
		point, ok := projected[0].Map.MapPosition(Position{Line: 3, Column: 1})
		if !ok || point != (Position{Line: 3, Column: 9}) {
			t.Fatalf("sink maps to %+v, valid=%v", point, ok)
		}
	}
}

func TestVueKeepsIndependentScriptAfterTemplateRecovery(t *testing.T) {
	for _, source := range []string{
		`<template><div>{{ '</div>' }}</div></template><script>eval(location.hash)</script>`,
		`<script>eval(location.hash)</script><template><div>{{ '</div>' }}</div></template>`,
		`<template><div>{{ '</div>' }}</div></template><script setup lang="ts">eval(location.hash)</script>`,
		"<template><div>狼 {{ '</div>' }}</div></template>\r\n<script>eval(location.hash)</script>",
	} {
		projected, limitations, err := Scripts(t.Context(), []byte(source), true)
		if err != nil || len(projected) != 1 || len(limitations) != 1 || string(projected[0].Source) != "eval(location.hash)" {
			grammar := grammars.VueLanguage()
			tree, parseErr := tsparse.Parse(context.Background(), grammar, []byte(source), tsparse.Analysis)
			testutil.FailErr(t, "inspect Vue recovery", parseErr)
			description := tree.RootNode().SExpr(grammar)
			tree.Release()
			t.Fatalf("projections=%+v limitations=%+v err=%v tree=%s", projected, limitations, err, description)
		}
		point, ok := projected[0].Map.MapPosition(Position{Line: 1, Column: 1})
		offset := strings.Index(source, "eval(location.hash)")
		want := Position{Line: strings.Count(source[:offset], "\n") + 1, Column: offset - strings.LastIndexByte(source[:offset], '\n')}
		if !ok || point != want {
			t.Fatalf("script origin=%+v valid=%v", point, ok)
		}
	}
}

func TestVueRecoveryDoesNotExposeNestedScripts(t *testing.T) {
	for _, source := range []string{
		`<template>{{ '</template>' }}<script>eval(location.hash)</script></template>`,
		`<template><div>{{ '</div>' }}<script>eval(location.hash)</script></div></template>`,
		`<template><div>{{ '</div>' }}</div><script>eval(location.hash)</script>`,
		`<custom><div>{{ '</div>' }}</div><script>eval(location.hash)</script></custom>`,
	} {
		projected, _, _ := Scripts(t.Context(), []byte(source), true)
		if len(projected) != 0 {
			t.Fatalf("recovery exposed a nested script: %q in %s", projected[0].Source, source)
		}
	}
}
