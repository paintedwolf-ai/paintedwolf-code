// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { createSignal } from "solid-js";
import { render, screen, waitFor } from "@solidjs/testing-library";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { ResidentSurface } from "./ResidentSurface.tsx";
import { applyWebEngineFact } from "../../platform/desktop/webkit-atspi-workaround.ts";

function select(element: HTMLElement) {
  const range = document.createRange();
  range.selectNodeContents(element);
  const selection = document.getSelection()!;
  selection.removeAllRanges();
  selection.addRange(range);
  return selection;
}

afterEach(() => {
  document.getSelection()?.removeAllRanges();
  applyWebEngineFact(null);
});

describe("retained surface selection", () => {
  it.each(["webkitgtk", "wkwebview", null])("releases only inaccessible WebKitGTK ranges: %s", async (engine) => {
    applyWebEngineFact(engine ? { engine, version: "2.54.1" } : null);
    const [presence, setPresence] = createSignal<"active" | "idle">("active");
    render(() => <ResidentSurface surfaceKey="files" presence={presence()}>
      <div contentEditable data-testid="editor">Selected document text</div>
    </ResidentSurface>);
    await waitFor(() => expect(screen.getByTestId("resident-surface").dataset.presentation).toBe("published"));
    const selection = select(screen.getByTestId("editor"));
    expect(selection.toString()).toBe("Selected document text");
    setPresence("idle");
    expect(selection.rangeCount).toBe(engine === "webkitgtk" ? 0 : 1);
    expect(screen.getByTestId("editor").textContent).toBe("Selected document text");
    setPresence("active");
    expect(screen.getByTestId("editor").textContent).toBe("Selected document text");
  });

  it("restores the editor selection after a retained surface returns", async () => {
    applyWebEngineFact({ engine: "webkitgtk", version: "2.54.1" });
    const [presence, setPresence] = createSignal<"active" | "idle">("active");
    let editor!: EditorView;
    render(() => <ResidentSurface surfaceKey="files" presence={presence()}>
      <div ref={(el) => { editor = new EditorView({ parent: el,
        state: EditorState.create({ doc: "selected text", selection: { anchor: 1, head: 5 } }) }); }} />
    </ResidentSurface>);
    try {
      await waitFor(() => expect(screen.getByTestId("resident-surface").dataset.presentation).toBe("published"));
      editor.focus();
      await waitFor(() => expect(document.getSelection()?.toString()).toBe("elec"));
      const before = editor.state.selection.toJSON();
      setPresence("idle");
      expect(document.getSelection()?.rangeCount).toBe(0);
      expect(editor.state.selection.toJSON()).toEqual(before);
      setPresence("active");
      editor.focus();
      await waitFor(() => expect(document.getSelection()?.toString()).toBe("elec"));
      expect(editor.state.selection.toJSON()).toEqual(before);
    } finally {
      editor.destroy();
    }
  });

  it("preserves a selection owned by another visible surface", async () => {
    applyWebEngineFact({ engine: "webkitgtk", version: "2.54.1" });
    const [presence, setPresence] = createSignal<"active" | "idle">("active");
    render(() => <><div data-testid="visible">Visible text</div>
      <ResidentSurface surfaceKey="files" presence={presence()}><span>Other text</span></ResidentSurface></>);
    await waitFor(() => expect(screen.getByTestId("resident-surface").dataset.presentation).toBe("published"));
    const selection = select(screen.getByTestId("visible"));
    setPresence("idle");
    expect(selection.toString()).toBe("Visible text");
  });
});
