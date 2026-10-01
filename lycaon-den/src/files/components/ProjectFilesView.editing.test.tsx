import "../../test/document-outbox-fixture.ts";
import { PROJECT, ROOTS, loadedBuffer, editorDocumentFixtureFor, resetProjectFilesViewTest, PROJECT_SOURCE_IDENTITY, waitForFilesStageReady } from "./project-files-view-test-harness.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, fireEvent } from "@solidjs/testing-library";
import { EditorView } from "@codemirror/view";
import { ProjectFilesView } from "./ProjectFilesView.tsx";
import { editorReplica, receiveEditorDocumentEvent } from "../documents/editor-document.ts";
import { createAppStore } from "../../store/app-state.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { filesBufferText, filesBufferBase, openFilesBuffer, applyFilesBufferLoad } from "../documents/project-files-buffers.ts";
import { required } from "../../test/at.ts";

import { StateEffect } from "@codemirror/state";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { stubFilesClient } from "../../test/source-client-fixture.ts";
import { findFileRow } from "../../test/files-tree-queries.ts";


describe("ProjectFilesView stage chrome", () => {
  beforeEach(resetProjectFilesViewTest);
  afterEach(() => vi.useRealTimers());

  it("keeps the stage editor focused while typing through the keyed pane", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const buf = loadedBuffer();
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={null}
      />
    ));
    const host = await waitFor(() => screen.getByTestId("files-editor-host"));
    await waitFor(() => {
      expect(host.querySelector(".cm-content")).toBeTruthy();
    });
    const view = EditorView.findFromDOM(host)!;
    const content = view.contentDOM;
    view.focus();

    view.dispatch({
      changes: { from: 0, insert: "Y" },
      userEvent: "input.type",
    });
    await waitFor(() => {
      expect(projectFilesState(PROJECT).byKey[buf.key]!.dirty).toBe(true);
    });

    expect(EditorView.findFromDOM(host)).toBe(view);
    expect(document.activeElement).toBe(content);
    expect(view.state.doc.toString().startsWith("Yline1")).toBe(true);
  });

  async function collaborativeView() {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const buffer = loadedBuffer();
    render(() => <ProjectFilesView projectId={PROJECT} appStore={appStore} roots={ROOTS} client={null} />);
    const host = await waitFor(() => screen.getByTestId("files-editor-host"));
    await waitFor(() => expect(host.querySelector(".cm-content")).toBeTruthy());
    const view = EditorView.findFromDOM(host)!;
    const fixture = editorDocumentFixtureFor(PROJECT, buffer.rootId, buffer.path);
    await waitFor(() => expect(projectFilesState(PROJECT).byKey[buffer.key]?.documentId).toBe(fixture.wire.id));
    return { buffer, view, fixture, replica: editorReplica(fixture.wire.id)! };
  }

  it("folds an agent write into the person's unpublished typing", async () => {
    const { buffer, view, fixture } = await collaborativeView();
    const base = view.state.doc.toString();
    view.dispatch({ changes: { from: 0, insert: "Y" }, userEvent: "input.type" });
    const index = base.indexOf("line3 target");
    fixture.doc.transact(() => {
      fixture.text.delete(index, "line3 target".length);
      fixture.text.insert(index, "line3 done");
    });
    const saved = fixture.text.toString();
    Object.assign(fixture.wire, { revision: 2, base_content: saved, base_sha256: "saved-sha" });
    receiveEditorDocumentEvent({ ...fixture.snapshot(), content_changed: true });
    await waitFor(() => expect(view.state.doc.toString()).toBe(`Y${saved}`));
    expect(projectFilesState(PROJECT).byKey[buffer.key]).toMatchObject({ baseSha256: "saved-sha", dirty: true });
    expect(filesBufferText(required(projectFilesState(PROJECT).byKey[buffer.key]))).toEqual(`Y${saved}`);
    expect(filesBufferBase(required(projectFilesState(PROJECT).byKey[buffer.key]))).toEqual(saved);
  });

  it("keeps the caret when a screen verdict overtakes the draft event", async () => {
    const { view, fixture, replica } = await collaborativeView();
    const caret = view.state.doc.toString().indexOf("line3");
    view.dispatch({ selection: { anchor: caret } });
    view.dispatch({ changes: { from: caret, insert: "new " }, userEvent: "input.type" });
    const typed = view.state.doc.toString();
    const head = view.state.selection.main.head;
    await replica.flush();
    const docs: string[] = [];
    view.dispatch({
      effects: StateEffect.appendConfig.of(EditorView.updateListener.of((update) => {
        if (update.docChanged) docs.push(update.state.doc.toString());
      }))
    });
    fixture.wire.secret_screen_status = "complete";
    fixture.wire.secret_screen = { truncated: false, spans: [] };
    receiveEditorDocumentEvent({ ...fixture.snapshot(), content_changed: false });
    await replica.synchronize();
    receiveEditorDocumentEvent({ ...fixture.snapshot(), content_changed: true });
    await replica.synchronize();
    expect(docs).toEqual([]);
    expect(view.state.doc.toString()).toBe(typed);
    expect(view.state.selection.main.head).toBe(head);
  });

  it("keeps unpublished typing through a same-revision metadata event", async () => {
    const { buffer, view, fixture, replica } = await collaborativeView();
    view.dispatch({ changes: { from: 0, insert: "Y" }, userEvent: "input.type" });
    const typed = view.state.doc.toString();
    fixture.wire.secret_screen_status = "complete";
    fixture.wire.secret_screen = { truncated: false, spans: [] };
    receiveEditorDocumentEvent({ ...fixture.snapshot(), content_changed: false });
    await replica.synchronize();
    expect(view.state.doc.toString()).toBe(typed);
    expect(projectFilesState(PROJECT).byKey[buffer.key]).toMatchObject({ dirty: true });
    expect(filesBufferText(required(projectFilesState(PROJECT).byKey[buffer.key]))).toEqual(typed);
  });

  it("opens the secret's menu wherever the right-click lands in the span", async () => {
    const { view, fixture, replica } = await collaborativeView();
    const start = view.state.doc.toString().indexOf("target");
    fixture.wire.secret_screen_status = "complete";
    fixture.wire.secret_screen = {
      truncated: false,
      spans: [{ start, end: start + "target".length, state: "detected", rule_id: "aws", rule_title: "AWS access key" }],
    };
    receiveEditorDocumentEvent({ ...fixture.snapshot(), content_changed: false });
    await replica.synchronize();
    const mark = await waitFor(() => {
      const found = document.querySelector<HTMLElement>(".cm-den-secret--detected");
      if (!found) throw new Error("secret mark did not render");
      return found;
    });
    // A right-click leaves the caret where it was; the span under the pointer is what it addresses.
    view.dispatch({ selection: { anchor: 0 } });
    vi.spyOn(view, "posAtCoords").mockReturnValue(start + 3);

    fireEvent.contextMenu(mark, { clientX: 12, clientY: 12, button: 2 });

    expect(await screen.findByTestId("files-ctx-editor-secret-fact")).toBeTruthy();
    expect(screen.queryByTestId("files-ctx-editor-track-secret")).toBeTruthy();
    expect(screen.queryByTestId("files-ctx-editor-ignore-secret")).toBeTruthy();
  });

  it("focuses a newly attached editor as the Files task target", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    loadedBuffer();
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={null}
      />
    ));
    const host = await waitFor(() => screen.getByTestId("files-editor-host"));
    await waitFor(() => {
      const view = EditorView.findFromDOM(host);
      expect(view).toBeTruthy();
      expect(document.activeElement).toBe(view!.contentDOM);
    });
  });

  it("keeps tree focus when a tree entry opens an editor", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const base = getLycaonClient()!;
    const client = stubFilesClient({
      ...base,
      browseProjectSource: vi.fn(async (_projectId, input) => ({
        workspace_id: "workspace-1",
        root_id: input.rootId,
        dir: input.dir,
        watch_complete: true,
        entries: input.dir === "."
          ? [{ name: "current.ts", is_dir: false }]
          : [],
      })),
    });
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={client}
      />
    ));
    const row = await findFileRow("current.ts");
    row.focus();
    loadedBuffer({ path: "current.ts" });

    const host = await screen.findByTestId("files-editor-host");
    await waitFor(() => expect(EditorView.findFromDOM(host)).toBeTruthy());
    expect(document.activeElement).toBe(row);
  });

  it("remounts image/info panes when buffer kind changes after open", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const key = openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "logo.png",
    });
    applyFilesBufferLoad(PROJECT, key, {
      ...PROJECT_SOURCE_IDENTITY,
      file_id: "",
      version_id: "",
      path: "logo.png",
      content: "placeholder",
      over_limit: false,
      writable: true,
      binary: false,
      mime: "text/plain",
      size_bytes: 11,
      sha256: "abc",
    });
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
      />
    ));
    expect(screen.getByTestId("files-editor-host")).toBeTruthy();

    applyFilesBufferLoad(PROJECT, key, {
      ...PROJECT_SOURCE_IDENTITY,
      file_id: "",
      version_id: "",
      path: "logo.png",
      content: "",
      over_limit: false,
      writable: true,
      binary: true,
      mime: "image/png",
      size_bytes: 70,
      modified_at: "2026-01-01T00:00:00Z",
    });
    await waitFor(() => {
      expect(screen.getByTestId("files-image-viewer")).toBeTruthy();
    });
    expect(screen.queryByTestId("files-editor-host")).toBeNull();
    expect(
      screen
        .getByTestId("files-image-viewer")
        .querySelector(".den-files-toolbar"),
    ).toBeTruthy();

    applyFilesBufferLoad(PROJECT, key, {
      ...PROJECT_SOURCE_IDENTITY,
      file_id: "",
      version_id: "",
      path: "logo.png",
      content: "",
      over_limit: false,
      writable: true,
      binary: true,
      mime: "application/octet-stream",
      size_bytes: 70,
      modified_at: "2026-01-01T00:00:00Z",
    });
    await waitFor(() => {
      expect(screen.getByTestId("files-info-card")).toBeTruthy();
    });
  });

  it("promotes a group preview in place while retaining its type icon", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    openFilesBuffer(PROJECT, {
      rootId: "r1", rootLabel: "repo", path: "", name: "Review changes",
      kind: "walk", intent: "transient",
      walkStep: { kind: "outside", key: "review-walk", ordinal: 1, label: "Review changes", toolCallId: null, effects: [] },
    });
    render(() => <ProjectFilesView projectId={PROJECT} appStore={appStore} roots={ROOTS} />);
    await waitForFilesStageReady();
    const tab = screen.getByRole("tab", { name: "Review changes, Group summary, Preview" });
    const cell = tab.closest(".den-files-tab")!;
    expect(cell.getAttribute("data-tauri-drag-region")).toBe("false");
    expect(tab.textContent).toBe("Review changes");
    const header = cell.querySelector(".den-files-tab__header")!;
    expect(header.textContent).toBe("");
    fireEvent.pointerDown(header, { button: 0, clientX: 10, clientY: 10 });
    fireEvent.pointerMove(window, { clientX: 70, clientY: 10 });
    expect(cell.classList.contains("den-files-tab--dragging")).toBe(false);
    fireEvent.pointerUp(window);
    fireEvent.pointerEnter(tab);
    fireEvent.focus(tab);
    const icon = tab.querySelector(".den-files-tab-kind-icon")!;
    expect(icon.getAttribute("data-tip")).toBe("Group summary");
    expect(icon.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
    expect(tab.hasAttribute("data-tip")).toBe(false);
    expect(cell.querySelectorAll("[data-tip]")).toHaveLength(1);
    expect(tab.querySelector(".den-files-tab__name-end")).toBeNull();
    fireEvent.dblClick(tab);
    expect(screen.getByRole("tab", { name: "Review changes, Group summary" })).toBe(tab);
    expect(tab.querySelector(".den-files-tab-kind-icon")).toBe(icon);
    expect(cell.classList.contains("den-files-tab--preview")).toBe(false);
    fireEvent.contextMenu(cell, { clientX: 10, clientY: 10 });
    const reveal = await screen.findByTestId("files-ctx-tab-reveal-tree");
    expect(reveal.getAttribute("aria-disabled")).toBe("true");
    expect(reveal.textContent).toContain("This tab has no item in the file tree.");
  });
});
