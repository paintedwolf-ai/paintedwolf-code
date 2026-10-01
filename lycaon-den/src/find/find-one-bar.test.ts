// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import {
  createSourceEditorState,
  openEditorFind,
} from "../components/source/editor/codemirror-theme.ts";
import { EditorState } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { search, searchKeymap } from "@codemirror/search";
import {
  findController,
  resetFindControllerForTests,
  registerFindableView,
} from "./find-controller.ts";

describe("one find bar", () => {
  // Compare command functions, not key labels.
  const stockRuns = new Set(searchKeymap.map((b) => b.run));

  for (const editable of [true, false]) {
    it(`installs no stock search command (editable: ${editable})`, () => {
      const composed = createSourceEditorState({ surface: "files",
        doc: "x",
        editable,
        commandBridge: editable,
      })
        .facet(keymap)
        .flat();
      expect(composed.filter((b) => b.run && stockRuns.has(b.run))).toEqual([]);
    });
  }

  it("openEditorFind opens the shared Find bar", () => {
    resetFindControllerForTests();
    const parent = document.createElement("div");
    document.body.appendChild(parent);
    const view = new EditorView({
      state: EditorState.create({ doc: "x", extensions: [search()] }),
      parent,
    });
    registerFindableView({
      id: "files-editor",
      provider: "codemirror",
      rootEl: () => parent,
      getEditorView: () => view,
      scrollMatchIntoView: () => {},
    });
    expect(openEditorFind(view)).toBe(true);
    expect(findController.isOpen()).toBe(true);
    expect(findController.activeViewId()).toBe("files-editor");
    view.destroy();
    resetFindControllerForTests();
    document.body.replaceChildren();
  });

});
