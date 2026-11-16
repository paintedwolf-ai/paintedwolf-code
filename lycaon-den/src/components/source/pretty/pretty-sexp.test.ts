import { forceParsing, indentUnit } from "@codemirror/language";
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

describe("S-expression and EDN layout", () => {
  it("lays out Clojure, Scheme, Common Lisp, and EDN with paired maps, attached closers, and protected literals", () => {
    const pad = "x".repeat(210);
    const clj = `(defn process [items] (let [cfg {:alpha "${pad}" :beta 2 :gamma 3}] (map #(str ";" % "\\)") items))) ; (comment)\n`;
    const scm = `(define (fold-all xs) (let ((s "${pad}")) (map (lambda (x) (list x "\\)")) xs)))\n`;
    const lisp = `(defun compute (a b) (let ((msg "${pad}")) (format nil "~a ; )" msg)))\n`;
    const edn = `{:name "app" :deps {:ring/ring-core {:mvn/version "1.10.0"} :compojure {:mvn/version "1.7.0"}}}\n`;

    for (const [path, src] of [
      ["core.clj", clj],
      ["core.cljs", clj],
      ["core.cljc", clj],
      ["main.scm", scm],
      ["main.ss", scm],
      ["init.lisp", lisp],
      ["deps.edn", edn],
    ] as const) {
      const state = open(src, path);
      const pos = breaks(state);
      expect(pos.length, `${path} breaks`).toBeGreaterThan(0);
      const rendered = writtenOut(state);
      for (const line of rendered.split("\n")) {
        expect(line.trim(), `${path} must not have standalone closer line`).not.toMatch(/^[)\]}]+$/);
      }
      const pass2 = open(rendered, path);
      expect(breaks(pass2), `${path} pass 2`).toEqual([]);
    }

    const cljRendered = writtenOut(open(clj, "core.clj"));
    expect(cljRendered).toContain(`:alpha "${pad}"`);
    expect(cljRendered).toContain(":beta 2");
    expect(cljRendered).not.toContain('; "\n');

    expect(breaks(open("(defn f [x]\n  (+ x 1))\n", "ok.clj"))).toEqual([]);
    void forceParsing;
  });
});
