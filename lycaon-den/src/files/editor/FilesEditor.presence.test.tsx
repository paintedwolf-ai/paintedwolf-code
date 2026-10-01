import { loadedBuffer, editorProps, resetProjectFilesViewTest } from "../components/project-files-view-test-harness.ts";
import { beforeEach, describe, expect, it } from "vitest";
import { render, waitFor } from "@solidjs/testing-library";
import { batch, createSignal } from "solid-js";
import { EditorView } from "@codemirror/view";
import { FilesEditor } from "./FilesEditor.tsx";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import type { ResidentPresence } from "../../ui/resident-surfaces.ts";

describe("retained file editor attachment", () => {
  beforeEach(resetProjectFilesViewTest);

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
});
