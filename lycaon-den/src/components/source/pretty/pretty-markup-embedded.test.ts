import { indentUnit } from "@codemirror/language";
import { EditorState } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import { languageExtensionForPath } from "../editor/codemirror-lang.ts";
import { createPrettyExtension } from "./pretty-layout.ts";
import { prettyLayoutDecorations } from "./pretty-view.ts";

function open(doc: string, path: string): EditorState {
  return EditorState.create({
    doc,
    extensions: [languageExtensionForPath(path)!, indentUnit.of("  "), createPrettyExtension({ enabled: true, path })],
  });
}

function breaks(state: EditorState): number[] {
  const out: number[] = [];
  state.facet(prettyLayoutDecorations).between(0, state.doc.length, (from, to, value) => {
    if (from === to && value.spec.widget) out.push(from);
  });
  return out;
}

function writtenOut(state: EditorState): string {
  const decos = state.facet(prettyLayoutDecorations);
  const marks = new Map<number, number>();
  const breakSet = new Set<number>();
  decos.between(0, state.doc.length, (from, to, value) => {
    if (from === to && value.spec.widget) breakSet.add(from);
    else if (value.spec.attributes?.style) {
      const m = /--pretty-indent:\s*(\d+)/.exec(value.spec.attributes.style);
      if (m) marks.set(from, Number(m[1]));
    }
  });
  const doc = state.doc.toString();
  let out = "";
  for (let i = 0; i < doc.length; i++) {
    if (breakSet.has(i)) out += "\n" + " ".repeat(marks.get(i) ?? 0);
    out += doc[i];
  }
  return out;
}

describe("embedded markup regions and start-tag attribute wrapping", () => {
  it("lays out embedded script, JSON script, style, and Astro frontmatter while preserving raw tags and </script> strings", () => {
    const pad = "x".repeat(200);
    const html = `<html><head><style>.btn{color:red;padding:4px 8px;margin:${pad ? "0" : "1"}</style><script>const tag = "</script>"; const cfg = { a: "${pad}", b: 2 };</script><script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"title":"${pad}","ok":true}}}</script></head><body><pre><div>${pad}</div></pre></body></html>`;
    const htmlState = open(html, "index.html");
    const pos = breaks(htmlState);
    expect(pos.length).toBeGreaterThan(4);
    const strClose = html.indexOf('"</script>"') + 1;
    expect(pos.some((p) => p > strClose && p < strClose + 9)).toBe(false);
    const preDiv = html.indexOf("<pre><div>") + 5;
    expect(pos).not.toContain(preDiv);
    expect(breaks(open(writtenOut(htmlState), "index.html"))).toEqual([]);

    const vue = `<template><div><span>${pad}</span></div></template><script setup lang="ts">const items = ["${pad}", "b", "c"];</script><style scoped lang="scss">.card { color: red; // ${pad}\n  .inner { margin: 0; } }</style>`;
    const vueState = open(vue, "App.vue");
    expect(breaks(vueState).length).toBeGreaterThan(2);
    expect(breaks(open(writtenOut(vueState), "App.vue"))).toEqual([]);

    const astro = `---\nconst data = { title: "${pad}", count: 42, tags: ["a", "b"] };\n---\n<html><body><main><p>${pad}</p></main></body></html>`;
    const astroState = open(astro, "Page.astro");
    expect(breaks(astroState).length).toBeGreaterThan(2);
    expect(breaks(open(writtenOut(astroState), "Page.astro"))).toEqual([]);
  });

  it("wraps wide start-tag attributes and passes second-pass idempotence", () => {
    const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 240 240" width="240" height="240" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="icon icon-large icon-primary icon-outline" aria-hidden="true" data-slot="icon"></svg>`;
    const state = open(svg, "icon.svg");
    const pos = breaks(state);
    expect(pos.length).toBeGreaterThan(0);
    expect(breaks(open(writtenOut(state), "icon.svg"))).toEqual([]);
  });
});
