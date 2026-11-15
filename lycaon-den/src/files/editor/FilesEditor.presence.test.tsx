import { loadedBuffer, editorProps, resetProjectFilesViewTest } from "../components/project-files-view-test-harness.ts";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { batch, createSignal } from "solid-js";
import { EditorSelection } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { applyAgentPresenceSnapshot, resetAgentPresenceForTests } from "../components/agent-presence-store.ts";
import { FilesEditor } from "./FilesEditor.tsx";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import type { ResidentPresence } from "../../ui/resident-surfaces.ts";

describe("retained file editor attachment", () => {
  beforeEach(resetProjectFilesViewTest);
  afterEach(() => { cleanup(); resetAgentPresenceForTests(); });

  it("keeps the editor attached to the active surface across switches", async () => {
    const buffer = loadedBuffer({ content: "retained content\n" });
    const [left, setLeft] = createSignal<ResidentPresence>("active");
    const [right, setRight] = createSignal<ResidentPresence>("idle");
    const a = render(() => <ResidentPresenceProvider presence={left()}>
      <FilesEditor {...editorProps(buffer)} client={null} />
    </ResidentPresenceProvider>);
    await waitFor(() => expect(a.container.querySelector(".cm-editor")).toBeTruthy());
    const dom = a.container.querySelector<HTMLElement>(".cm-editor")!;
    const editor = EditorView.findFromDOM(dom)!;
    editor.dispatch({ changes: { from: 0, insert: "unsaved " }, selection: { anchor: 8 } });
    const b = render(() => <ResidentPresenceProvider presence={right()}>
      <FilesEditor {...editorProps(buffer)} client={null} />
    </ResidentPresenceProvider>);
    expect(a.container.querySelector(".cm-editor")).toBe(dom);
    expect(b.container.querySelector(".cm-editor")).toBeNull();

    for (let cycle = 0; cycle < 3; cycle++) {
      batch(() => { setLeft("idle"); setRight("pending"); });
      await waitFor(() => expect(b.container.querySelector(".cm-editor")).toBe(dom));
      setRight("active");
      expect(editor.state.doc.toString()).toBe("unsaved retained content\n");
      expect(editor.state.selection.main.head).toBe(8);
      batch(() => { setRight("idle"); setLeft("active"); });
      await waitFor(() => expect(a.container.querySelector(".cm-editor")).toBe(dom));
      expect(b.container.querySelector(".cm-editor")).toBeNull();
    }
  });

  it("toggles the line facts card on handle click and dismisses from the editor without collapsing selection", async () => {
    const buffer = loadedBuffer({ content: "alpha\nbeta\ngamma\n" });
    const props = editorProps(buffer);
    const view = render(() => <ResidentPresenceProvider presence="active">
      <FilesEditor {...props} client={null} />
    </ResidentPresenceProvider>);
    await waitFor(() => expect(view.container.querySelector(".cm-editor")).toBeTruthy());
    applyAgentPresenceSnapshot({
      project_id: props.projectId,
      revision: 1,
      sessions: [{
        session_id: "reader",
        title: "Inspect",
        turn: 1,
        activities: [],
        intents: [],
        worker_drafts: [],
        reads: [{
          id: "r1",
          sequence: 1,
          tool_call_id: "tc1",
          tool: "command",
          root_id: buffer.rootId,
          path: buffer.path,
          extent: "range",
          ranges: [{ start_line: 2, end_line: 2 }],
          stale: false,
        }],
      }],
    });

    const handle = await waitFor(() => {
      const found = view.container.querySelector<HTMLButtonElement>('.files-line-gutter__facts[data-line="2"]');
      expect(found).toBeTruthy();
      return found!;
    });
    fireEvent.mouseOver(handle);
    expect(screen.queryByTestId("files-line-facts")).toBeNull();

    fireEvent.click(handle);
    await waitFor(() => expect(screen.getByTestId("files-line-facts")).toBeTruthy());
    expect(handle.getAttribute("aria-expanded")).toBe("true");

    // A second click on the same handle toggles the card closed.
    fireEvent.click(handle);
    await waitFor(() => expect(screen.queryByTestId("files-line-facts")).toBeNull());
    expect(handle.getAttribute("aria-expanded")).toBe("false");

    const editor = EditorView.findFromDOM(view.container.querySelector<HTMLElement>(".cm-editor")!)!;
    editor.dispatch({ selection: EditorSelection.range(0, 4) });
    editor.contentDOM.focus();
    fireEvent.click(handle);
    await waitFor(() => expect(screen.getByTestId("files-line-facts")).toBeTruthy());

    editor.contentDOM.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    await waitFor(() => expect(screen.queryByTestId("files-line-facts")).toBeNull());
    expect(editor.state.selection.main.from).toBe(0);
    expect(editor.state.selection.main.to).toBe(4);
  });
});
