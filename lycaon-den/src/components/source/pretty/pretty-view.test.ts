// @vitest-environment jsdom
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { afterEach, describe, expect, it } from "vitest";
import { applyPrettyConfig, createPrettyExtension } from "./pretty-layout.ts";
import { PrettyBreakWidget, prettyLayoutActive, prettyLayoutDecorations, prettyVisualLineAt } from "./pretty-view.ts";

const views: EditorView[] = [];
afterEach(() => {
  for (const view of views.splice(0)) {
    view.dom.remove();
    view.destroy();
  }
});

function open(doc: string, enabled = true, path = "data.json"): EditorView {
  const parent = document.createElement("div");
  document.body.appendChild(parent);
  const view = new EditorView({
    parent,
    state: EditorState.create({ doc, extensions: [createPrettyExtension({ enabled, path })] }),
  });
  views.push(view);
  return view;
}

function breaks(state: EditorState): number[] {
  const out: number[] = [];
  state.facet(prettyLayoutDecorations).between(0, state.doc.length, (from, to, value) => {
    if (from === to && value.spec.widget instanceof PrettyBreakWidget) out.push(from);
  });
  return out;
}

function press(view: EditorView, key: string, shiftKey = false): boolean {
  const event = new KeyboardEvent("keydown", { key, shiftKey, bubbles: true, cancelable: true });
  view.contentDOM.dispatchEvent(event);
  return event.defaultPrevented;
}

describe("Pretty presentation", () => {
  const doc = '{"service":"gateway","ports":[80,443],"on":true}';

  it("draws breaks inline and indents each visual line with one mark", () => {
    const view = open(doc);
    const positions = breaks(view.state);
    expect(positions.length).toBeGreaterThan(4);
    expect(view.dom.querySelectorAll("br.pw-pretty-break")).toHaveLength(positions.length);
    for (const mark of view.dom.querySelectorAll(".pw-pretty-line")) {
      expect(mark.getAttribute("style")).toBeNull();
      expect([...mark.classList].some((name) => /^pw-pretty-line-\d+$/.test(name))).toBe(true);
    }
  });

  it("wraps the view and marks it active only while layout is active", () => {
    const view = open(doc);
    expect(view.state.facet(prettyLayoutActive)).toBe(true);
    expect(view.contentDOM.classList.contains("pw-pretty-wrap")).toBe(true);
    expect(view.dom.classList.contains("pw-pretty-active")).toBe(true);
    applyPrettyConfig(view, { enabled: false, path: "data.json" });
    expect(view.state.facet(prettyLayoutActive)).toBe(false);
    expect(view.contentDOM.classList.contains("pw-pretty-wrap")).toBe(false);
    expect(view.dom.classList.contains("pw-pretty-active")).toBe(false);
  });

  it("is inactive for a language without a layout rule", () => {
    const view = open("echo {a,b} | tr , ' '\n", true, "run.sh");
    expect(view.state.facet(prettyLayoutActive)).toBe(false);
  });

  it("resolves the visual line on each side of a break", () => {
    const view = open(doc);
    const [first, second, third] = breaks(view.state);
    expect(prettyVisualLineAt(view.state, second!, -1)).toEqual({ from: first, to: second });
    expect(prettyVisualLineAt(view.state, second!, 1)).toEqual({ from: second, to: third });
    expect(prettyVisualLineAt(view.state, 0, 1)).toEqual({ from: 0, to: first });
    const last = breaks(view.state).at(-1)!;
    expect(prettyVisualLineAt(view.state, doc.length, -1)).toEqual({ from: last, to: doc.length });
  });

  it("stops End and Home at the visual line on repeated presses", () => {
    const view = open(doc);
    const [first, second] = breaks(view.state);
    const inside = first! + 2;
    view.dispatch({ selection: { anchor: inside } });
    for (let i = 0; i < 3; i++) {
      expect(press(view, "End")).toBe(true);
      expect(view.state.selection.main.head).toBe(second);
    }
    for (let i = 0; i < 3; i++) {
      expect(press(view, "Home")).toBe(true);
      expect(view.state.selection.main.head).toBe(first);
    }
    view.dispatch({ selection: { anchor: inside } });
    press(view, "End", true);
    press(view, "End", true);
    expect(view.state.selection.main).toMatchObject({ anchor: inside, head: second });
  });

  it("leaves the editor's own line keys in place while layout is off", () => {
    const view = open(doc, false);
    view.dispatch({ selection: { anchor: 3 } });
    expect(press(view, "End")).toBe(false);
  });

  it("keeps the raw selection when Pretty is toggled", () => {
    const view = open(doc);
    view.dispatch({ selection: { anchor: 5, head: 30 } });
    const before = view.state.selection.toJSON();
    const text = view.state.doc.toString();
    applyPrettyConfig(view, { enabled: false, path: "data.json" });
    applyPrettyConfig(view, { enabled: true, path: "data.json" });
    expect(view.state.selection.toJSON()).toEqual(before);
    expect(view.state.doc.toString()).toBe(text);
  });
});
