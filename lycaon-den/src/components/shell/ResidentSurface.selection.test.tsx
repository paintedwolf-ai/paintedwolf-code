// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { createSignal, Show } from "solid-js";
import { render, screen, waitFor } from "@solidjs/testing-library";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { createRetainedWebKitSelection } from "../../platform/desktop/webkit-atspi-selection.ts";
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
  it("holds and restores endpoints at the direct lifecycle boundary", () => {
    applyWebEngineFact({ engine: "webkitgtk", version: "2.54.1" });
    const surface = document.createElement("section");
    surface.textContent = "Reader text";
    document.body.append(surface);
    const selection = select(surface);
    const lifecycle = createRetainedWebKitSelection(surface);
    lifecycle.hide();
    expect(selection.rangeCount).toBe(0);
    lifecycle.restore();
    expect(selection.toString()).toBe("Reader text");
    lifecycle.dispose();
    surface.remove();
  });

  it("binds the actual document after a template-owned surface is adopted", () => {
    applyWebEngineFact({ engine: "webkitgtk", version: "2.54.1" });
    const template = document.createElement("template");
    template.innerHTML = "<section>Adopted reader</section>";
    const surface = template.content.firstElementChild as HTMLElement;
    const lifecycle = createRetainedWebKitSelection(surface);
    document.body.append(surface);
    const selection = select(surface);
    lifecycle.hide();
    expect(selection.rangeCount).toBe(0);
    lifecycle.restore();
    expect(selection.toString()).toBe("Adopted reader");
    lifecycle.dispose();
    surface.remove();
  });

  it.each([false, true])("distinguishes its own clear from a newer cleared selection: %s", (newerClear) => {
    applyWebEngineFact({ engine: "webkitgtk", version: "2.54.1" });
    const surface = document.createElement("section");
    surface.textContent = "Reader text";
    document.body.append(surface);
    const lifecycle = createRetainedWebKitSelection(surface);
    const selection = select(surface);
    lifecycle.hide();
    document.dispatchEvent(new Event("selectionchange"));
    if (newerClear) document.dispatchEvent(new Event("selectionchange"));
    lifecycle.restore();
    expect(selection.toString()).toBe(newerClear ? "" : "Reader text");
    lifecycle.dispose();
    surface.remove();
  });

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
    await waitFor(() => expect(selection.toString()).toBe("Selected document text"));
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

  it("releases a surface-owned range before the surface is removed", async () => {
    applyWebEngineFact({ engine: "webkitgtk", version: "2.54.1" });
    const [visible, setVisible] = createSignal(true);
    render(() => <Show when={visible()}><ResidentSurface surfaceKey="files" presence="active">
      <div data-testid="editor">Selected text</div>
    </ResidentSurface></Show>);
    await waitFor(() => expect(screen.getByTestId("resident-surface").dataset.presentation).toBe("published"));
    const selection = select(screen.getByTestId("editor"));
    setVisible(false);
    expect(screen.queryByTestId("editor")).toBeNull();
    expect(selection.rangeCount).toBe(0);
  });

  it("restores backward reader selection without focusing the surface", async () => {
    applyWebEngineFact({ engine: "webkitgtk", version: "2.54.1" });
    const [presence, setPresence] = createSignal<"active" | "idle">("active");
    render(() => <ResidentSurface surfaceKey="reader" presence={presence()}>
      <p data-testid="reader">Reader selection</p>
    </ResidentSurface>);
    await waitFor(() => expect(screen.getByTestId("resident-surface").dataset.presentation).toBe("published"));
    const text = screen.getByTestId("reader").firstChild!;
    const selection = document.getSelection()!;
    selection.setBaseAndExtent(text, 10, text, 2);
    setPresence("idle");
    expect(selection.rangeCount).toBe(0);
    setPresence("active");
    await waitFor(() => expect(selection.anchorNode).toBe(text));
    expect([selection.anchorOffset, selection.focusOffset]).toEqual([10, 2]);
    expect(document.activeElement).toBe(document.body);
  });

  it.each(["changed", "replaced"])("refuses stale reader content: %s", async (mutation) => {
    applyWebEngineFact({ engine: "webkitgtk", version: "2.54.1" });
    const [presence, setPresence] = createSignal<"active" | "idle">("active");
    render(() => <ResidentSurface surfaceKey="reader" presence={presence()}>
      <p data-testid="reader">Reader selection</p>
    </ResidentSurface>);
    await waitFor(() => expect(screen.getByTestId("resident-surface").dataset.presentation).toBe("published"));
    const reader = screen.getByTestId("reader");
    const selection = select(reader);
    setPresence("idle");
    if (mutation === "changed") reader.firstChild!.textContent = "Changed selection";
    else reader.replaceChildren(document.createTextNode("Reader selection"));
    // Restore in the same task: queued MutationObserver records must also invalidate it.
    setPresence("active");
    expect(selection.rangeCount).toBe(0);
  });

  it("never revives an old selection after a newer selection was cleared", async () => {
    applyWebEngineFact({ engine: "webkitgtk", version: "2.54.1" });
    const [presence, setPresence] = createSignal<"active" | "idle">("active");
    render(() => <><p data-testid="other">Newer selection</p>
      <ResidentSurface surfaceKey="reader" presence={presence()}>
        <p data-testid="reader">Old selection</p>
      </ResidentSurface></>);
    await waitFor(() => expect(screen.getByTestId("resident-surface").dataset.presentation).toBe("published"));
    const selection = select(screen.getByTestId("reader"));
    setPresence("idle");
    select(screen.getByTestId("other"));
    document.dispatchEvent(new Event("selectionchange"));
    selection.removeAllRanges();
    setPresence("active");
    expect(selection.rangeCount).toBe(0);
  });

  it("preserves a newer foreign editable caret", async () => {
    applyWebEngineFact({ engine: "webkitgtk", version: "2.54.1" });
    const [presence, setPresence] = createSignal<"active" | "idle">("active");
    render(() => <><input aria-label="Other draft" value="new draft" />
      <ResidentSurface surfaceKey="reader" presence={presence()}>
        <p data-testid="reader">Old selection</p>
      </ResidentSurface></>);
    await waitFor(() => expect(screen.getByTestId("resident-surface").dataset.presentation).toBe("published"));
    const selection = select(screen.getByTestId("reader"));
    setPresence("idle");
    const input = screen.getByRole("textbox") as HTMLInputElement;
    input.focus();
    input.setSelectionRange(4, 4);
    setPresence("active");
    expect(selection.toString()).toBe("");
    expect(document.activeElement).toBe(input);
    expect([input.selectionStart, input.selectionEnd]).toEqual([4, 4]);
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
