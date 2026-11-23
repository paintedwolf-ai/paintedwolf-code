// @vitest-environment jsdom
import { EditorState, StateEffect } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { afterEach, describe, expect, it, vi } from "vitest";
import { setShortcutPlatformForTests, TAURI_PLATFORMS } from "../../../shortcuts/platform.ts";
import { installedEditingStyle } from "../editor/editor-editing-style-state.ts";
import { applyPrettyConfig, createPrettyExtension } from "./pretty-layout.ts";
import { PrettyBreakWidget, prettyLayoutActive, prettyLayoutDecorations, prettyVisualLineAt } from "./pretty-view.ts";

const views: EditorView[] = [];
afterEach(() => {
  setShortcutPlatformForTests(null);
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

function press(view: EditorView, key: string, shiftKey = false, modifiers: KeyboardEventInit = {}): boolean {
  const event = new KeyboardEvent("keydown", { key, shiftKey, ...modifiers, bubbles: true, cancelable: true });
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
    const view = open("key=value\n", true, "app.ini");
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

  it("keeps each line key on the same visual line from either selection direction", () => {
    for (const platform of TAURI_PLATFORMS) {
      setShortcutPlatformForTests(platform);
      for (const style of ["standard", "emacs", "vim"] as const) {
        const view = open(doc);
        const [from, to] = breaks(view.state) as [number, number, ...number[]];
        const inside = from + 2;
        view.dispatch({ effects: StateEffect.appendConfig.of(installedEditingStyle.of(style)) });
        const keys = [
          { key: "Home", forward: false, active: true, modifiers: {} },
          { key: "End", forward: true, active: true, modifiers: {} },
          ...["ArrowLeft", "ArrowRight"].map((key) => ({
            key, forward: key === "ArrowRight", active: platform === "macos", modifiers: { metaKey: true },
          })),
          ...["a", "e"].map((key) => ({
            key, forward: key === "e", modifiers: { ctrlKey: true },
            active: style === "emacs" || (platform === "macos" && style === "standard"),
          })),
        ];
        for (const { key, forward, active, modifiers } of keys) {
          for (const anchor of [0, inside, doc.length]) {
            view.dispatch({ selection: { anchor, head: inside } });
            for (let repeat = 0; repeat < 2; repeat++) {
              expect(press(view, key, true, modifiers), `${platform} ${style} ${key}`).toBe(active);
              const range = view.state.selection.main;
              expect(range.head).toBe(active ? (forward ? to : from) : inside);
              expect(range.anchor).toBe(anchor);
              expect(prettyVisualLineAt(view.state, range.head, range.assoc)).toEqual({ from, to });
            }
          }
        }
      }
    }
  });

  it("stops trailing-edge lookup at a nested break", () => {
    const widget = new PrettyBreakWidget();
    const current = widget.toDOM();
    expect(widget.coordsAt(current)).toBeNull();
    const parent = document.createElement("span");
    const nested = document.createElement("span");
    const empty = document.createElement("span");
    parent.append("previous line", nested, empty, current);
    nested.append(widget.toDOM());
    empty.append(document.createTextNode(""));
    expect(widget.coordsAt(current)).toBeNull();
    const text = document.createTextNode("x");
    nested.append(text);
    const range = document.createRange();
    Object.defineProperty(range, "getClientRects", { value: () => [{ top: 2, right: 7, bottom: 14, height: 12 }] });
    vi.spyOn(document, "createRange").mockReturnValueOnce(range);
    expect(widget.coordsAt(current)).toEqual({ top: 2, left: 7, right: 7, bottom: 14 });
    expect(range.startContainer).toBe(text);
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
