// @vitest-environment jsdom

import { startCompletion } from "@codemirror/autocomplete";
import { EditorView } from "@codemirror/view";
import { waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it } from "vitest";
import { createSourceEditorState } from "./codemirror-theme.ts";

describe("CodeMirror tooltip layer", () => {
  let view: EditorView | undefined;
  let host: HTMLDivElement | undefined;

  afterEach(() => {
    view?.destroy();
    view = undefined;
    host?.remove();
    host = undefined;
  });

  it("hosts completion dropdowns outside the contained editor", async () => {
    host = document.createElement("div");
    document.body.appendChild(host);
    view = new EditorView({
      parent: host,
      state: createSourceEditorState({ surface: "files",
        doc: "painted painted\npai",
        editable: true,
      }),
    });
    view.dispatch({ selection: { anchor: view.state.doc.length } });

    expect(startCompletion(view)).toBe(true);
    await waitFor(() =>
      expect(
        document.body.querySelector(".cm-tooltip-autocomplete"),
      ).toBeTruthy(),
    );

    const dropdown = document.body.querySelector(".cm-tooltip-autocomplete");
    expect(dropdown).toBeTruthy();
    expect(host.contains(dropdown)).toBe(false);
    expect(dropdown?.parentElement?.parentElement).toBe(document.body);
    expect(getComputedStyle(dropdown!).zIndex).toBe(
      "var(--den-z-anchored-surface)",
    );
  });
});
