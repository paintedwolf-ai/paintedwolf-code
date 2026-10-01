import {
  setupFilesEditorTests,
  copyTextToClipboard,
} from "./files-editor-test-harness.ts";

import "../../test/document-outbox-fixture.ts";

import { stubClient } from "../../test/client-fixture.ts";
import {
  PROJECT,
  PROJECT_SOURCE_IDENTITY,
  BLUEPRINT_PATH,
  loadedBuffer,
  editorProps,
} from "../components/project-files-view-test-harness.ts";
import {
  describe,
  expect,
  it,
  vi,
} from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
} from "@solidjs/testing-library";
import * as Y from "yjs";
import { foldedRanges } from "@codemirror/language";
import { DocumentFixture } from "../../test/document-fixture.ts";
import {
  configureEditorDocuments,
  resolveEditorDocument,
} from "../documents/editor-document.ts";
import { encodeUpdate } from "../documents/document-outbox.ts";
import { EditorView } from "@codemirror/view";
import { undo } from "@codemirror/commands";
import {
  Show,
  createSignal,
} from "solid-js";

import { FilesEditor } from "./FilesEditor.tsx";

import {
  applyFilesBufferDraft,
  applyFilesBufferEditorDocument,
  applyFilesBufferLoad,
  discardFilesBufferDraft,
  markFilesBufferLoading,
  openFilesBuffer,
} from "../documents/project-files-buffers.ts";
import {
  projectFilesState,
  type FileBuffer,
} from "../documents/files-buffer-state.ts";
import { destroyFilesEditor } from "./files-editor-host.ts";
import { putBufferViewState } from "./editor-session-fidelity.ts";
import { flushFilesDraftSync } from "../documents/files-draft-sync.ts";

import { focusRegion } from "../../shortcuts/focus-region.ts";

describe("FilesEditor", () => {
  setupFilesEditorTests();

  it("retains and dims code while a large preview prepares, and cancels back to code", async () => {
    const terminate = vi.fn();
    const postMessage = vi.fn();
    vi.stubGlobal("Worker", class {
      postMessage = postMessage;
      terminate = terminate;
    });
    try {
      const buf = loadedBuffer({ path: BLUEPRINT_PATH, content: "# Large file\n\nText.\n\n".repeat(4000) });
      const view = render(() => <FilesEditor {...editorProps(buf)} />);
      const host = screen.getByTestId("files-editor-host").parentElement!;
      fireEvent.click(screen.getByTestId("files-editor-md-preview"));
      expect(host.hidden).toBe(false);
      expect(host.inert).toBe(true);
      expect(await screen.findByText("Preparing preview…")).toBeTruthy();
      expect(host.dataset.retained).toBe("true");
      expect(screen.getByTestId("files-editor-preview").classList.contains("den-files-editor__preview--preparing")).toBe(true);
      await waitFor(() => expect(postMessage).toHaveBeenCalledOnce());
      fireEvent.click(screen.getByTestId("files-editor-md-code"));
      expect(host.hidden).toBe(false);
      expect(host.inert).not.toBe(true);
      expect(host.dataset.retained).toBe("false");
      expect(screen.queryByText("Preparing preview…")).toBeNull();
      expect(terminate).toHaveBeenCalledOnce();
      view.unmount();
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("honors an editor focus command issued before initial file bytes arrive", async () => {
    const loaded = loadedBuffer();
    const [buffer, setBuffer] = createSignal<FileBuffer>({ ...loaded, content: { state: "unloaded" }, baseSha256: null, loading: true });
    render(() => <FilesEditor {...editorProps(buffer())} />);
    const host = screen.getByTestId("files-editor-host");
    expect(host.querySelector(".cm-content")).toBeNull();
    expect(focusRegion("files")).toBe(true);
    expect(document.activeElement).toBe(host);
    setBuffer(loaded);
    await waitFor(() => expect(document.activeElement).toBe(host.querySelector(".cm-content")));
  });

  it("mounts an editable CodeMirror surface with content and language", async () => {
    const buf = loadedBuffer();
    render(() => <FilesEditor {...editorProps(buf)} />);
    await waitFor(() => {
      expect(screen.getByTestId("files-editor-host").textContent).toContain(
        "line3 target",
      );
    });
    expect(document.querySelector(".den-files-editor__status")?.getAttribute("data-editor-identity")).toBe("editing");
    expect(screen.queryByTestId("files-editor-mode")).toBeNull();
    expect(
      EditorView.findFromDOM(screen.getByTestId("files-editor-host"))!.state.facet(
        EditorView.cursorScrollMargin,
      ),
    ).toEqual({ x: 24, y: 5 });
    expect(screen.getByText("TypeScript")).toBeTruthy();
  });

  it("keeps routine synchronization silent and exposes blocked editing", () => {
    const loaded = loadedBuffer();
    const [buffer, setBuffer] = createSignal<FileBuffer>({ ...loaded, editorSynchronization: "preserving" });
    render(() => <FilesEditor {...editorProps(buffer())} />);
    expect(screen.queryByTestId("files-editor-synchronization")).toBeNull();
    setBuffer({ ...loaded, editorSynchronization: "pending" });
    expect(screen.queryByTestId("files-editor-synchronization")).toBeNull();
    setBuffer({ ...loaded, editorSynchronization: "error", editorEditingBlock: "storage" });
    expect(screen.getByTestId("files-editor-synchronization").textContent).toContain("draft storage failed");
  });

  it("can start the same markdown editor in preview mode", async () => {
    const buf = loadedBuffer({
      path: BLUEPRINT_PATH,
      content: "# Blueprint\n\nShared Files preview.",
    });
    render(() => (
      <FilesEditor
        {...editorProps(buf)}
        initialMarkdownView="preview"
      />
    ));
    expect(screen.getByTestId("files-editor-md-preview").getAttribute("aria-pressed"))
      .toBe("true");
    expect(screen.getByTestId("files-editor-preview").textContent).toContain(
      "Shared Files preview.",
    );
    expect(screen.getByTestId("files-editor-mode").textContent).toBe("Preview");
  });

  it("can adapt markdown preview content for a document host", () => {
    const buf = loadedBuffer({
      path: BLUEPRINT_PATH,
      content: "---\nstatus: draft\n---\n# Blueprint",
    });
    render(() => (
      <FilesEditor
        {...editorProps(buf)}
        initialMarkdownView="preview"
        previewSource={(source) => source.replace(/^---[\s\S]*?---\n/, "")}
      />
    ));
    expect(screen.getByTestId("files-editor-preview").textContent?.trim()).toBe(
      "Blueprint",
    );
  });

  it("opens copy variants from right-click on the Copy path button", async () => {
    const buf = loadedBuffer();
    render(() => <FilesEditor {...editorProps(buf)} />);
    await waitFor(() => {
      expect(screen.getByTestId("files-editor-host").textContent).toContain(
        "line3 target",
      );
    });
    fireEvent.contextMenu(screen.getByTestId("files-editor-copy"));
    fireEvent.click(await screen.findByTestId("files-editor-copy-relative-path"));
    expect(copyTextToClipboard).toHaveBeenCalledWith("src/main.ts");

    fireEvent.contextMenu(screen.getByTestId("files-editor-copy"));
    fireEvent.click(await screen.findByTestId("files-editor-copy-file-name"));
    expect(copyTextToClipboard).toHaveBeenCalledWith("main.ts");

    fireEvent.contextMenu(screen.getByTestId("files-editor-copy"));
    fireEvent.click(await screen.findByTestId("files-editor-copy-contents"));
    expect(copyTextToClipboard).toHaveBeenCalledWith(
      "line1\nline2\nline3 target\nline4\n",
    );
  });

  it("renders a sanitized markdown preview behind the Code/Preview toggle", async () => {
    const buf = loadedBuffer({
      path: "README.md",
      content: "# Title\n\n<script>window.__pwn = 1;</script>ok\n",
    });
    render(() => <FilesEditor {...editorProps(buf)} />);
    expect(screen.getByTestId("files-editor-md-toggle")).toBeTruthy();
    fireEvent.click(screen.getByTestId("files-editor-md-preview"));
    await waitFor(() => {
      expect(screen.getByTestId("files-editor-preview")).toBeTruthy();
    });
    expect(
      screen.getByTestId("files-editor-host").parentElement?.hidden,
    ).toBe(true);
    const preview = screen.getByTestId("files-editor-preview");
    expect(preview.querySelector("script")).toBeNull();
    expect(preview.textContent).toContain("Title");
    fireEvent.click(screen.getByTestId("files-editor-md-code"));
    expect(screen.queryByTestId("files-editor-preview")).toBeNull();
    expect(
      screen.getByTestId("files-editor-host").parentElement?.hidden,
    ).toBe(false);
  });

  it("never offers the markdown toggle for non-markdown files", () => {
    const buf = loadedBuffer();
    render(() => <FilesEditor {...editorProps(buf)} />);
    expect(screen.queryByTestId("files-editor-md-toggle")).toBeNull();
  });

  it("keeps the live editor mounted and focused while typing", async () => {
    const buf = loadedBuffer();
    render(() => <FilesEditor {...editorProps(buf)} />);
    const host = screen.getByTestId("files-editor-host");
    await waitFor(() => {
      expect(host.querySelector(".cm-content")).toBeTruthy();
    });
    const view = EditorView.findFromDOM(host)!;
    const content = view.contentDOM;
    view.focus();

    view.dispatch({ changes: { from: 0, insert: "X" }, userEvent: "input.type" });
    await waitFor(() => {
      expect(projectFilesState(PROJECT).byKey[buf.key]!.dirty).toBe(true);
    });

    expect(EditorView.findFromDOM(host)).toBe(view);
    expect(host.querySelector(".cm-content")).toBe(content);
    expect(document.activeElement).toBe(content);
    expect(view.state.doc.toString().startsWith("Xline1")).toBe(true);
  });

  it("reparents the same EditorView across a Soft remount of the host", async () => {
    const buf = loadedBuffer();
    const props = editorProps(buf);
    const [mounted, setMounted] = createSignal(true);
    render(() => (
      <Show when={mounted()}>
        <FilesEditor {...props} />
      </Show>
    ));
    const host = screen.getByTestId("files-editor-host");
    await waitFor(() => {
      expect(host.querySelector(".cm-content")).toBeTruthy();
    });
    const view = EditorView.findFromDOM(host)!;
    view.focus();
    const target = view.state.doc.line(1).from;
    view.dispatch({
      selection: { anchor: target },
      changes: { from: target, insert: "Z" },
      userEvent: "input.type",
    });
    const caretAfterType = view.state.selection.main.head;

    setMounted(false);
    setMounted(true);
    const reopened = screen.getByTestId("files-editor-host");
    await waitFor(() => {
      expect(reopened.querySelector(".cm-content")).toBeTruthy();
    });
    expect(EditorView.findFromDOM(reopened)).toBe(view);
    expect(view.state.doc.toString().startsWith("Zline1")).toBe(true);
    expect(view.state.selection.main.head).toBe(caretAfterType);
    await waitFor(() => expect(document.activeElement).toBe(view.contentDOM));
  });

  it.each([false, true])("restores relative view state after late replica adoption unless navigation intervened (%s)", async (navigated) => {
    const content = "first\nsecond\nthird\nlast\n";
    openFilesBuffer(PROJECT, { rootId: "r1", rootLabel: "repo", path: "resumed.ts", documentId: "resumed-doc", intent: "permanent" });
    const buf = loadedBuffer({ path: "resumed.ts", content });
    const fixture = new DocumentFixture(content, { id: "resumed-doc", project_id: PROJECT, root_id: buf.rootId, path: buf.path,
      file_id: buf.fileId ?? "", base_sha256: buf.baseSha256! });
    const relative = (index: number) => encodeUpdate(Y.encodeRelativePosition(Y.createRelativePositionFromTypeIndex(fixture.text, index)));
    putBufferViewState(buf.rootId, buf.path, {
      sha: buf.baseSha256!, cursor: { anchor: 20, head: 20 }, scrollTop: 0, folds: [{ from: 5, to: 18 }], capturedAt: Date.now(),
      document: { id: fixture.wire.id, epoch: 1, selections: [{ anchor: relative(20), head: relative(20) }],
        folds: [{ from: relative(5), to: relative(18) }] },
    });
    fixture.text.insert(0, "peer\n");
    fixture.wire.revision++;
    const client = stubClient({ createEditorDocumentSnapshot: async () => fixture.snapshot(), syncEditorDocument: fixture.sync,
      submitEditorDocumentUpdate: fixture.submit });
    const disconnect = configureEditorDocuments(PROJECT, () => client, () => undefined, { get: () => buf, all: () => [buf] });
    render(() => <FilesEditor {...editorProps(buf)} />);
    const host = screen.getByTestId("files-editor-host");
    await waitFor(() => expect(host.querySelector(".cm-content")).toBeTruthy());
    const view = EditorView.findFromDOM(host)!;
    expect(view.state.selection.main.head).toBe(0);
    if (navigated) view.dispatch({ selection: { anchor: 2 }, userEvent: "select.pointer" });
    const state = await resolveEditorDocument(PROJECT, buf);
    expect(state).not.toBeNull();
    applyFilesBufferEditorDocument(PROJECT, buf.key, state!);
    await waitFor(() => expect(view.state.doc.toString()).toBe("peer\n" + content));
    expect(view.state.selection.main.head).toBe(navigated ? 7 : 25);
    const folds: Array<[number, number]> = [];
    foldedRanges(view.state).between(0, view.state.doc.length, (from, to) => { folds.push([from, to]); });
    expect(folds).toEqual(navigated ? [] : [[10, 23]]);
    disconnect();
  });

  it("restores saved-base offsets when the recorded document identity is gone", async () => {
    const buf = loadedBuffer({ path: "reopened.ts", content: "first\nsecond\nthird\nlast\n" });
    putBufferViewState(buf.rootId, buf.path, {
      sha: buf.baseSha256!, cursor: { anchor: 20, head: 20 }, scrollTop: 0, folds: [{ from: 5, to: 18 }], capturedAt: Date.now(),
      document: { id: "forgotten-doc", epoch: 1, selections: [], folds: [] },
    });
    render(() => <FilesEditor {...editorProps(buf)} />);
    const host = screen.getByTestId("files-editor-host");
    await waitFor(() => expect(host.querySelector(".cm-content")).toBeTruthy());
    const view = EditorView.findFromDOM(host)!;
    await waitFor(() => expect(view.state.selection.main.head).toBe(20));
    const folds: Array<[number, number]> = [];
    foldedRanges(view.state).between(0, view.state.doc.length, (from, to) => { folds.push([from, to]); });
    expect(folds).toEqual([[5, 18]]);
  });

  it("does not restore a stale durable caret onto a dirty buffer after rebuild", async () => {
    const buf = loadedBuffer();
    putBufferViewState(buf.rootId, buf.path, {
      sha: buf.baseSha256!,
      cursor: { anchor: 20, head: 20 },
      scrollTop: 0,
      folds: [],
      capturedAt: Date.now(),
    });
    const props = editorProps(buf);
    const [editable, setEditable] = createSignal(true);
    render(() => <FilesEditor {...props} editable={editable()} />);
    const host = screen.getByTestId("files-editor-host");
    await waitFor(() => {
      expect(host.querySelector(".cm-content")).toBeTruthy();
    });
    const view = EditorView.findFromDOM(host)!;
    view.dispatch({
      selection: { anchor: 0 },
      changes: { from: 0, insert: "KEEP" },
      userEvent: "input.type",
    });
    await waitFor(() => {
      expect(projectFilesState(PROJECT).byKey[buf.key]!.dirty).toBe(true);
    });
    const caret = view.state.selection.main.head;

    setEditable(false);
    await waitFor(() => {
      const next = EditorView.findFromDOM(
        screen.getByTestId("files-editor-host"),
      );
      expect(next).toBeTruthy();
      expect(next!.state.readOnly).toBe(true);
    });
    setEditable(true);
    await waitFor(() => {
      const next = EditorView.findFromDOM(
        screen.getByTestId("files-editor-host"),
      );
      expect(next).toBeTruthy();
      expect(next!.state.readOnly).toBe(false);
    });
    const again = EditorView.findFromDOM(
      screen.getByTestId("files-editor-host"),
    )!;
    expect(again.state.doc.toString().startsWith("KEEPline1")).toBe(true);
    expect(again.state.selection.main.head).not.toBe(20);
    expect(Math.abs(again.state.selection.main.head - caret)).toBeLessThan(8);
  });

  it("does not read a discard pushed into the view as typing", async () => {
    const buf = loadedBuffer();
    render(() => <FilesEditor {...editorProps(buf)} />);
    const host = screen.getByTestId("files-editor-host");
    await waitFor(() => {
      expect(host.querySelector(".cm-content")).toBeTruthy();
    });
    const view = EditorView.findFromDOM(host)!;
    const base = view.state.doc.toString();

    view.dispatch({ changes: { from: 0, insert: "X" }, userEvent: "input.type" });
    await waitFor(() => {
      expect(projectFilesState(PROJECT).byKey[buf.key]!.dirty).toBe(true);
    });

    discardFilesBufferDraft(PROJECT, buf.key);
    await waitFor(() => {
      expect(view.state.doc.toString()).toBe(base);
    });
    expect(projectFilesState(PROJECT).byKey[buf.key]!.dirty).toBe(false);
    flushFilesDraftSync();
    expect(projectFilesState(PROJECT).byKey[buf.key]!.dirty).toBe(false);
  });

  it("pushes an external draft reset into the live view without rebuilding it", async () => {
    const buf = loadedBuffer();
    render(() => <FilesEditor {...editorProps(buf)} />);
    const host = screen.getByTestId("files-editor-host");
    await waitFor(() => {
      expect(host.querySelector(".cm-content")).toBeTruthy();
    });
    const view = EditorView.findFromDOM(host)!;

    applyFilesBufferDraft(PROJECT, buf.key, "replaced\n");
    await waitFor(() => {
      expect(view.state.doc.toString()).toBe("replaced\n");
    });
    expect(EditorView.findFromDOM(host)).toBe(view);
  });

  it("keeps the live view and the caret when the editing lease moves", async () => {
    const buf = loadedBuffer();
    const [canEdit, setCanEdit] = createSignal(true);
    render(() => <FilesEditor {...editorProps(buf)} editable={canEdit()} />);
    const host = screen.getByTestId("files-editor-host");
    await waitFor(() => {
      expect(host.querySelector(".cm-content")).toBeTruthy();
    });
    const view = EditorView.findFromDOM(host)!;
    view.dispatch({ selection: { anchor: 4, head: 4 } });

    setCanEdit(false);
    await waitFor(() => {
      expect(view.state.readOnly).toBe(true);
    });
    expect(EditorView.findFromDOM(host)).toBe(view);
    expect(view.state.selection.main.head).toBe(4);

    setCanEdit(true);
    await waitFor(() => {
      expect(view.state.readOnly).toBe(false);
    });
    expect(EditorView.findFromDOM(host)).toBe(view);
    expect(view.state.selection.main.head).toBe(4);
  });

  it("keeps undo history across a tab switch and drops it on close", async () => {
    const buf = loadedBuffer();
    const props = editorProps(buf);
    const [mounted, setMounted] = createSignal(true);
    render(() => (
      <Show when={mounted()}>
        <FilesEditor {...props} />
      </Show>
    ));
    const host = screen.getByTestId("files-editor-host");
    await waitFor(() => {
      expect(host.querySelector(".cm-content")).toBeTruthy();
    });
    EditorView.findFromDOM(host)!.dispatch({
      changes: { from: 0, insert: "X" },
      userEvent: "input.type",
    });

    setMounted(false);
    setMounted(true);
    const reopened = screen.getByTestId("files-editor-host");
    await waitFor(() => {
      expect(reopened.querySelector(".cm-content")).toBeTruthy();
    });
    const view = EditorView.findFromDOM(reopened)!;
    expect(view.state.doc.toString().startsWith("Xline1")).toBe(true);
    expect(undo(view)).toBe(true);
    expect(view.state.doc.toString().startsWith("line1")).toBe(true);

    setMounted(false);
    destroyFilesEditor(PROJECT, buf.key, { preserveViewState: false, dropHandlers: true });
    setMounted(true);
    await waitFor(() => {
      expect(
        screen.getByTestId("files-editor-host").querySelector(".cm-content"),
      ).toBeTruthy();
    });
    const fresh = EditorView.findFromDOM(
      screen.getByTestId("files-editor-host"),
    )!;
    expect(undo(fresh)).toBe(false);
  });

  it("does not retain view history across a reload from disk", async () => {
    const buf = loadedBuffer();
    const props = editorProps(buf);
    const [mounted, setMounted] = createSignal(true);
    render(() => (
      <Show when={mounted()}>
        <FilesEditor {...props} />
      </Show>
    ));
    await waitFor(() => {
      expect(
        screen.getByTestId("files-editor-host").querySelector(".cm-content"),
      ).toBeTruthy();
    });
    EditorView.findFromDOM(screen.getByTestId("files-editor-host"))!.dispatch({
      changes: { from: 0, insert: "X" },
      userEvent: "input.type",
    });

    markFilesBufferLoading(PROJECT, buf.key);
    setMounted(false);
    applyFilesBufferLoad(PROJECT, buf.key, {
      ...PROJECT_SOURCE_IDENTITY,
      file_id: "",
      version_id: "",
      path: buf.path,
      content: "line1\nline2\nline3 target\nline4\n",
      over_limit: false,
      writable: true,
      binary: false,
      size_bytes: 100,
      sha256: "base-sha",
    });
    setMounted(true);
    await waitFor(() => {
      expect(
        screen.getByTestId("files-editor-host").querySelector(".cm-content"),
      ).toBeTruthy();
    });
    const view = EditorView.findFromDOM(
      screen.getByTestId("files-editor-host"),
    )!;
    expect(view.state.doc.toString().startsWith("line1")).toBe(true);
    expect(undo(view)).toBe(false);
  });
});
