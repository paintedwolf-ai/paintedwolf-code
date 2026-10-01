// @vitest-environment jsdom
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { search } from "@codemirror/search";
import { beforeEach, describe, expect, it } from "vitest";
import { createCodeMirrorFindProvider } from "./find-provider-codemirror.ts";
import { FIND_MATCH_COUNT_CAP, FIND_SELECT_ALL_CURSOR_CAP } from "./find-provider.ts";

function mountDoc(doc: string): EditorView {
  const parent = document.createElement("div");
  document.body.appendChild(parent);
  return new EditorView({
    state: EditorState.create({
      doc,
      extensions: [search(), EditorState.allowMultipleSelections.of(true)],
    }),
    parent,
  });
}

describe("find-provider-codemirror", () => {
  beforeEach(() => {
    document.body.replaceChildren();
  });

  it("finds an off-viewport match in a 5000-line fixture", () => {
    const lines = Array.from({ length: 5000 }, (_, i) =>
      i === 4799 ? "unique-needle-here" : `line-${i}`,
    );
    const view = mountDoc(lines.join("\n"));
    view.dispatch({ selection: { anchor: 0, head: 0 } });
    const provider = createCodeMirrorFindProvider({ getView: () => view });
    provider.setQuery("unique-needle-here", { caseSensitive: false });
    const count = provider.count();
    expect(count.total).toBe(1);
    expect(count.activeIndex).toBe(0);
    expect(view.state.selection.main.from).toBeGreaterThan(0);
    const line = view.state.doc.lineAt(view.state.selection.main.from);
    expect(line.number).toBe(4800);
    view.destroy();
  });

  it("reports count fixtures: N of M, No results path, and 999+", () => {
    const view = mountDoc(Array(47).fill("hit").join("\n"));
    const provider = createCodeMirrorFindProvider({ getView: () => view });
    provider.setQuery("hit", { caseSensitive: false });
    expect(provider.count().total).toBe(47);
    provider.next();
    provider.next();
    expect(provider.count().activeIndex).toBe(2);

    provider.setQuery("zzz-missing", { caseSensitive: false });
    expect(provider.count().total).toBe(0);

    const huge = mountDoc(Array(12_000).fill("x").join(" "));
    const hugeProvider = createCodeMirrorFindProvider({ getView: () => huge });
    hugeProvider.setQuery("x", { caseSensitive: false });
    const capped = hugeProvider.count();
    expect(capped.capped).toBe(true);
    expect(capped.total).toBe(FIND_MATCH_COUNT_CAP);
    view.destroy();
    huge.destroy();
  });

  it("freezes find-in-selection scope across replaceAll", () => {
    const doc = [
      "alpha outside",
      "keep",
      "alpha in",
      "alpha in",
      "alpha in",
      "alpha in",
      "alpha in",
      "alpha in",
      "alpha in",
      "alpha in",
      "alpha in",
      "alpha in",
      "alpha outside-end",
    ].join("\n");
    const view = mountDoc(doc);
    // Select the 10 "alpha in" lines (lines 3–12).
    const from = view.state.doc.line(3).from;
    const to = view.state.doc.line(12).to;
    view.dispatch({ selection: { anchor: from, head: to } });
    const provider = createCodeMirrorFindProvider({ getView: () => view });
    expect(provider.scopeToSelection(true)).toBe(true);
    provider.setQuery("alpha", { caseSensitive: false });
    expect(provider.count().total).toBe(10);
    provider.replaceAll("beta");
    const text = view.state.doc.toString();
    expect(text.startsWith("alpha outside")).toBe(true);
    expect(text.endsWith("alpha outside-end")).toBe(true);
    expect(text).toContain("beta in");
    expect((text.match(/alpha in/g) ?? []).length).toBe(0);
    // The frozen scope contains ten beta matches.
    provider.setQuery("beta", { caseSensitive: false });
    expect(provider.count().total).toBe(10);
    view.destroy();
  });

  it("advances past replacement text that still matches", () => {
    const view = mountDoc("a a");
    const provider = createCodeMirrorFindProvider({ getView: () => view });
    provider.setQuery("a", { caseSensitive: false });
    provider.replaceCurrent("aa");
    expect(view.state.doc.toString()).toBe("aa a");
    expect(view.state.selection.main.from).toBe(3);
    expect(view.state.selection.main.to).toBe(4);
    view.destroy();
  });

  it("refuses replace-all when matching reaches the shared cap", () => {
    const view = mountDoc(Array(FIND_MATCH_COUNT_CAP + 1).fill("x").join(" "));
    const provider = createCodeMirrorFindProvider({ getView: () => view });
    provider.setQuery("x", { caseSensitive: false });

    expect(provider.replaceAll("y")).toEqual({ replaced: 0, capped: true });
    expect(view.state.doc.toString()).toContain("x");
    view.destroy();
  });

  it("selectAllMatches caps at 1000 cursors", () => {
    const view = mountDoc(Array(1200).fill("m").join(" "));
    const provider = createCodeMirrorFindProvider({ getView: () => view });
    provider.setQuery("m", { caseSensitive: false });
    const result = provider.selectAllMatches();
    expect(result?.cursors).toBe(FIND_SELECT_ALL_CURSOR_CAP);
    expect(result?.capped).toBe(true);
    expect(view.state.selection.ranges.length).toBe(FIND_SELECT_ALL_CURSOR_CAP);
    view.destroy();
  });
});
