// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { EditorView } from "@codemirror/view";
import { createSourceEditorState } from "../../components/source/editor/codemirror-theme.ts";
import { setEditorScrollTop } from "../../components/source/editor/editor-scroll-position.ts";
import {
  beginFilesEditorAttach,
  completeFilesEditorAttach,
  destroyFilesEditor,
  getFilesEditorView,
  rekeyFilesEditorHostsUnderPath,
  resetFilesEditorHostForTests,
  softDetachFilesEditor,
  RETAINED_DETACHED_VIEWS,
} from "./files-editor-host.ts";
import { fileBufferKey } from "../components/project-files-model.ts";

const PROJECT = "project-1";
const KEY = fileBufferKey("root-1", "a.txt");
const PATH = "a.txt";
const DOC = "alpha\nbravo\n";

function attach(
  host: HTMLElement,
  editable: boolean,
  key = KEY,
  path = PATH,
): EditorView {
  const plan = beginFilesEditorAttach({
    projectId: PROJECT,
    key,
    path,
    draft: DOC,
    host,
  });
  if (plan.kind === "reused") return plan.view;
  const view = new EditorView({
    state:
      createSourceEditorState({ surface: "files",
            doc: DOC,
            editable,
            commandBridge: editable,
            language: null,
            handlers: plan.handlers,
          }),
    parent: host,
  });
  completeFilesEditorAttach({
    projectId: PROJECT,
    key,
    path,
    view,
    handlers: plan.handlers,
    host,
  });
  return view;
}

describe("files editor host identity", () => {
  it("detaches using the recorded scroll position and restores it on reattachment", () => {
    const host = document.createElement("div");
    const view = attach(host, true);
    setEditorScrollTop(view, 123);
    const read = vi.spyOn(view.scrollDOM, "scrollTop", "get");
    try {
      expect(softDetachFilesEditor(PROJECT, KEY, host)).toBe(true);
      expect(read).not.toHaveBeenCalled();
      const next = beginFilesEditorAttach({ projectId: PROJECT, key: KEY, path: PATH, draft: DOC, host });
      expect(next.kind).toBe("reused");
      if (next.kind === "reused") expect(next.scrollTop).toBe(123);
    } finally { read.mockRestore(); }
  });

  afterEach(() => {
    resetFilesEditorHostForTests();
  });

  it("does not detach or evict a view attached to another host", () => {
    const oldHost = document.createElement("div");
    const newHost = document.createElement("div");
    const view = attach(oldHost, true);
    view.dispatch({ changes: { from: 0, insert: "unsaved " }, selection: { anchor: 8 } });
    expect(attach(newHost, true)).toBe(view);
    expect(softDetachFilesEditor(PROJECT, KEY, oldHost)).toBe(false);
    for (let index = 0; index <= RETAINED_DETACHED_VIEWS; index++) {
      const path = `other-${index}.txt`;
      const key = fileBufferKey("root-1", path);
      const host = document.createElement("div");
      attach(host, true, key, path);
      softDetachFilesEditor(PROJECT, key, host);
    }
    expect(view.dom.parentElement).toBe(newHost);
    expect(getFilesEditorView(PROJECT, KEY)).toBe(view);
    expect(view.state.doc.toString()).toBe(`unsaved ${DOC}`);
    expect(view.state.selection.main.head).toBe(8);
    expect(softDetachFilesEditor(PROJECT, KEY, newHost)).toBe(true);
  });

  it("reuses the live view when the editing lease changes", () => {
    // Editing mode does not change view identity.
    const host = document.createElement("div");
    const first = attach(host, true);
    first.dispatch({ selection: { anchor: 7, head: 7 } });

    const second = attach(host, false);
    expect(second).toBe(first);
    expect(second.state.selection.main.head).toBe(7);
    expect(getFilesEditorView(PROJECT, KEY)).toBe(first);
  });

  it("releases the editor state when the buffer is destroyed", () => {
    const host = document.createElement("div");
    const first = attach(host, true);
    destroyFilesEditor(PROJECT, KEY, { preserveViewState: true });
    expect(getFilesEditorView(PROJECT, KEY)).toBeUndefined();

    // Reattachment creates fresh editor state.
    const plan = beginFilesEditorAttach({
      projectId: PROJECT,
      key: KEY,
      path: PATH,
      draft: DOC,
      host,
    });
    expect(plan.kind).toBe("fresh");
    expect(first.state.doc.toString()).toBe("");
  });

  it("returns a soft-detached tab to its own live view", () => {
    const host = document.createElement("div");
    const first = attach(host, true);
    first.dispatch({ selection: { anchor: 7, head: 7 } });

    expect(softDetachFilesEditor(PROJECT, KEY, host)).toBe(true);
    expect(host.contains(first.dom)).toBe(false);
    expect(getFilesEditorView(PROJECT, KEY)).toBe(first);

    const reopened = attach(host, true);
    expect(reopened).toBe(first);
    expect(reopened.state.selection.main.head).toBe(7);
    expect(host.contains(first.dom)).toBe(true);
  });

  it("destroys the coldest view once retention is full", () => {
    const hosts: HTMLElement[] = [];
    const keys: string[] = [];
    // One more tab than the cap, each detached in turn.
    for (let i = 0; i <= RETAINED_DETACHED_VIEWS; i += 1) {
      const host = document.createElement("div");
      const key = fileBufferKey("root-1", `f${i}.txt`);
      hosts.push(host);
      keys.push(key);
      attach(host, true, key, `f${i}.txt`);
      softDetachFilesEditor(PROJECT, key, host);
    }

    // The first detached editor releases its text.
    expect(getFilesEditorView(PROJECT, keys[0]!)).toBeUndefined();
    for (const key of keys.slice(1)) {
      expect(getFilesEditorView(PROJECT, key)).toBeDefined();
    }

    const plan = beginFilesEditorAttach({
      projectId: PROJECT,
      key: keys[0]!,
      path: "f0.txt",
      draft: DOC,
      host: hosts[0]!,
    });
    expect(plan.kind).toBe("fresh");
  });

  it("reopening a detached tab keeps it out of the eviction queue", () => {
    const hosts: HTMLElement[] = [];
    const keys: string[] = [];
    for (let i = 0; i < RETAINED_DETACHED_VIEWS; i += 1) {
      const host = document.createElement("div");
      const key = fileBufferKey("root-1", `f${i}.txt`);
      hosts.push(host);
      keys.push(key);
      attach(host, true, key, `f${i}.txt`);
      softDetachFilesEditor(PROJECT, key, host);
    }
    // Reopening removes the coldest editor from the eviction queue.
    attach(hosts[0]!, true, keys[0]!, "f0.txt");

    for (const name of ["extra-a.txt", "extra-b.txt"]) {
      const extraHost = document.createElement("div");
      const extraKey = fileBufferKey("root-1", name);
      attach(extraHost, true, extraKey, name);
      softDetachFilesEditor(PROJECT, extraKey, extraHost);
    }

    // Reopening refreshes the tab’s retention priority.
    expect(getFilesEditorView(PROJECT, keys[0]!)).toBeDefined();
    expect(getFilesEditorView(PROJECT, keys[1]!)).toBeUndefined();
  });

  it("retargets a live view without losing its state", () => {
    const host = document.createElement("div");
    const view = attach(host, true);
    view.dispatch({ selection: { anchor: 7 } });

    rekeyFilesEditorHostsUnderPath(PROJECT, "root-1", "a.txt", "renamed.txt");

    const nextKey = fileBufferKey("root-1", "renamed.txt");
    expect(getFilesEditorView(PROJECT, KEY)).toBeUndefined();
    expect(getFilesEditorView(PROJECT, nextKey)).toBe(view);
    expect(view.state.selection.main.head).toBe(7);
  });

  it("leaves worker overlay views at their workspace address", () => {
    const host = document.createElement("div");
    const overlayKey = fileBufferKey("root-1", "a.txt", "worker-1");
    const view = attach(host, false, overlayKey);

    rekeyFilesEditorHostsUnderPath(PROJECT, "root-1", "a.txt", "renamed.txt");

    expect(getFilesEditorView(PROJECT, overlayKey)).toBe(view);
    expect(getFilesEditorView(
      PROJECT,
      fileBufferKey("root-1", "renamed.txt", "worker-1"),
    ))
      .toBeUndefined();
  });

  it("keeps a file-identity worker overlay reusable after an unrelated rename", () => {
    const host = document.createElement("div");
    const overlayKey = fileBufferKey("root-1", "a.txt", "worker-1", "file-abc");
    const view = attach(host, false, overlayKey, "a.txt");

    rekeyFilesEditorHostsUnderPath(PROJECT, "root-1", "a.txt", "renamed.txt");

    const plan = beginFilesEditorAttach({
      projectId: PROJECT,
      key: overlayKey,
      path: "a.txt",
      draft: DOC,
      host,
    });
    expect(plan.kind).toBe("reused");
    if (plan.kind === "reused") expect(plan.view).toBe(view);
  });

  it("retargets a stable file buffer", () => {
    const host = document.createElement("div");
    const liveKey = fileBufferKey("root-1", "a.txt", undefined, "file-abc");
    const view = attach(host, true, liveKey, "a.txt");

    rekeyFilesEditorHostsUnderPath(PROJECT, "root-1", "a.txt", "renamed.txt");

    const plan = beginFilesEditorAttach({
      projectId: PROJECT,
      key: liveKey,
      path: "renamed.txt",
      draft: DOC,
      host,
    });
    expect(plan.kind).toBe("reused");
    if (plan.kind === "reused") expect(plan.view).toBe(view);
  });
});
