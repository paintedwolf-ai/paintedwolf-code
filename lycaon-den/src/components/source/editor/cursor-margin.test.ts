// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { EditorState, type TransactionSpec } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { CURSOR_MARGIN_LINES, cursorMargin } from "./cursor-margin.ts";

type ScrollTarget = { y: string; yMargin: number };

function revealTargets(dispatch: ReturnType<typeof vi.fn>): ScrollTarget[] {
  return dispatch.mock.calls.flatMap(([spec]) => {
    const effects = (spec as TransactionSpec).effects;
    const list = Array.isArray(effects) ? effects : effects ? [effects] : [];
    return list.map((effect) => effect.value as ScrollTarget);
  });
}

function mount(): { view: EditorView; dispatch: ReturnType<typeof vi.fn> } {
  const view = new EditorView({
    state: EditorState.create({
      doc: "one\ntwo\nthree\nfour\n",
      extensions: [cursorMargin()],
    }),
    parent: document.body,
  });
  const dispatch = vi.spyOn(view, "dispatch") as unknown as ReturnType<typeof vi.fn>;
  return { view, dispatch };
}

describe("cursor margin", () => {
  it("widens a keyboard reveal to the line margin", async () => {
    const { view, dispatch } = mount();
    try {
      view.dispatch({
        selection: { anchor: 8 },
        scrollIntoView: true,
        userEvent: "select",
      });
      await Promise.resolve();
      const targets = revealTargets(dispatch);
      expect(targets).toHaveLength(1);
      expect(targets[0]).toMatchObject({
        y: "nearest",
        yMargin: CURSOR_MARGIN_LINES * view.defaultLineHeight,
      });
      // The widened reveal does not ask for another one.
      await Promise.resolve();
      expect(revealTargets(dispatch)).toHaveLength(1);
    } finally {
      view.destroy();
    }
  });

  it("leaves pointer selection and plain selection changes alone", async () => {
    const { view, dispatch } = mount();
    try {
      view.dispatch({
        selection: { anchor: 8 },
        scrollIntoView: true,
        userEvent: "select.pointer",
      });
      view.dispatch({ selection: { anchor: 4 } });
      await Promise.resolve();
      expect(revealTargets(dispatch)).toHaveLength(0);
    } finally {
      view.destroy();
    }
  });

  it("drops a reveal the caret has already left", async () => {
    const { view, dispatch } = mount();
    try {
      view.dispatch({
        selection: { anchor: 8 },
        scrollIntoView: true,
        userEvent: "select",
      });
      view.dispatch({ selection: { anchor: 2 }, userEvent: "select.pointer" });
      await Promise.resolve();
      expect(revealTargets(dispatch)).toHaveLength(0);
    } finally {
      view.destroy();
    }
  });
});
