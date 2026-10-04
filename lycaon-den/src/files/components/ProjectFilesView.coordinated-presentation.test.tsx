import "../../test/document-outbox-fixture.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { PROJECT, ROOTS, loadedBuffer, resetProjectFilesViewTest } from "./project-files-view-test-harness.ts";
import { stubFilesClient } from "../../test/source-client-fixture.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { createAppStore } from "../../store/app-state.ts";
import type { SourceDirListing } from "../../api/types.ts";
import type { LycaonClient } from "../../api/client.ts";
import { ProjectFilesView } from "./ProjectFilesView.tsx";
import { resetEditorPrefsForTests } from "../../settings/editor/editor-prefs.ts";
import { getAppStateSnapshot, setAppStateSnapshot } from "../../store/app-state-snapshot.ts";
import { setProjectFilesRevealRequest } from "../tree/project-files-reveal.ts";
import { openFilesBuffer, setFilesActiveBuffer, closeFilesBuffer } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { applyOpenFilesSurfaceRequest } from "../../platform/navigation/open-files-surface.ts";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((accept) => { resolve = accept; });
  return { promise, resolve };
}

const listing = (dir: string): SourceDirListing => ({
  workspace_id: "workspace-1", root_id: "r1", dir, watch_complete: true,
  entries: dir === "."
    ? [{ name: "nested", is_dir: true }, { name: "first.ts", is_dir: false }]
    : [{ name: "second.ts", is_dir: false }],
});

async function setup(autoReveal = true) {
  setAppStateSnapshot({ ...getAppStateSnapshot(), editor: { revealInTree: autoReveal } });
  resetEditorPrefsForTests({ revealInTree: autoReveal });
  const first = loadedBuffer({ path: "first.ts", content: "first" });
  const source = deferred<Awaited<ReturnType<LycaonClient["getProjectSource"]>>>();
  const tree = deferred<SourceDirListing>();
  const base = getLycaonClient()!;
  const client = stubFilesClient({
    ...base,
    getProjectSource: vi.fn((project, path) => path === "nested/second.ts"
      ? source.promise : base.getProjectSource(project, path)),
    browseProjectSource: vi.fn(async (_project, input) => input.dir === "nested"
      ? tree.promise : listing(input.dir)),
  });
  const appStore = createAppStore();
  appStore.actions.setSidecarStatus("connected");
  render(() => <ProjectFilesView projectId={PROJECT} appStore={appStore} roots={ROOTS} client={client} />);
  const pane = await screen.findByTestId("files-pane-presentation");
  await waitFor(() => expect(pane.dataset.pending).toBe("false"));
  const second = openFilesBuffer(PROJECT, {
    intent: "permanent", rootId: "r1", rootLabel: "repo", path: "nested/second.ts",
  });
  const resolveSource = async () => source.resolve(await base.getProjectSource(PROJECT, "nested/second.ts"));
  const row = () => document.querySelector('[data-files-ctx="tree-row"][data-path="nested/second.ts"]');
  return { first, second, pane, tree, resolveSource, row, client };
}

// The second read stays pending across the editor paint boundary.
const paint = () => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())));

describe("Files tree and editor navigation", () => {
  beforeEach(resetProjectFilesViewTest);

  it.each(["output", "args", "approval", "trust", "walk"] as const)("opens a %s tab without revealing a non-file address in the tree", async (kind) => {
    resetEditorPrefsForTests({ revealInTree: true });
    const first = loadedBuffer({ path: "first.ts", content: "first" });
    const client = stubFilesClient({
      ...getLycaonClient()!,
      browseProjectSource: vi.fn(async (_project, input) => listing(input.dir)),
    });
    const update = vi.spyOn(client, "applySourceViewIntent");
    const rows = vi.spyOn(client, "getSourceViewRows");
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => <ProjectFilesView projectId={PROJECT} appStore={appStore} roots={ROOTS} client={client} />);
    const pane = await screen.findByTestId("files-pane-presentation");
    const file = () => document.querySelector('.den-files-tree__label--file[data-path="first.ts"]');
    await waitFor(() => expect(pane.dataset.pending).toBe("false"));
    await waitFor(() => expect(file()?.getAttribute("aria-current")).toBe("true"));
    await paint();
    update.mockClear();
    rows.mockClear();

    if (kind === "trust") {
      applyOpenFilesSurfaceRequest({ kind: "trust-review", projectId: PROJECT }, ROOTS);
    } else if (kind === "walk") {
      openFilesBuffer(PROJECT, {
        kind: "walk", rootId: "r1", rootLabel: "repo", path: "", intent: "permanent",
        name: "Review changes",
        walkStep: { kind: "outside", key: "review-walk", ordinal: 1, label: "Review changes", toolCallId: null, effects: [] },
      });
    } else if (kind === "approval") {
      applyOpenFilesSurfaceRequest({ kind: "chat-content", projectId: PROJECT, document: {
        kind: "approval", sessionId: "session", checkpointId: "checkpoint",
        pane: "details", title: "Approval details", content: { kind: "inline", text: "Recorded approval details" },
      } }, ROOTS);
    } else applyOpenFilesSurfaceRequest({ kind: "chat-content", projectId: PROJECT, document: {
      kind: "tool", sessionId: "session", messageId: "message", toolCallId: "call",
      pane: kind, title: "Raw output · Git log",
      content: { kind: "inline", text: "[git#4]\nRecorded tool output" },
    } }, ROOTS);
    const key = projectFilesState(PROJECT).activeKey!;
    await waitFor(() => expect(pane.querySelector('[data-resident="active"]')?.getAttribute("data-resident-key")).toContain(key));
    await waitFor(() => expect(pane.dataset.pending).toBe("false"));
    await paint();
    expect(update).not.toHaveBeenCalled();
    expect(rows).not.toHaveBeenCalled();
    expect(file()?.getAttribute("aria-current")).toBe("true");

    fireEvent.click(document.querySelector('.den-files-tree__label--dir[data-path="."]')!);
    expect(file()?.getAttribute("aria-current")).not.toBe("true");
    setFilesActiveBuffer(PROJECT, first.key);
    await waitFor(() => expect(pane.querySelector('[data-resident="active"]')?.getAttribute("data-resident-key")).toContain(first.key));
    await waitFor(() => expect(file()?.getAttribute("aria-current")).toBe("true"));
  });

  it("honors a reveal queued before the tree workspace is ready", async () => {
    const root = deferred<SourceDirListing>();
    const base = getLycaonClient()!;
    const client = stubFilesClient({
      ...base,
      browseProjectSource: vi.fn(async (_project, input) => input.dir === "."
        ? root.promise : listing(input.dir)),
    });
    setProjectFilesRevealRequest({ projectId: PROJECT, rootId: "r1", path: "nested/second.ts", isDir: false });
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => <ProjectFilesView projectId={PROJECT} appStore={appStore} roots={ROOTS} client={client} />);
    await waitFor(() => expect(client.browseProjectSource).toHaveBeenCalled());
    root.resolve(listing("."));
    await waitFor(() => expect(document.querySelector(
      '.den-files-tree__label--file[data-path="nested/second.ts"]',
    )?.getAttribute("aria-current")).toBe("true"));
  });

  it("publishes a loaded file while its tree reveal is still waiting", async () => {
    const view = await setup();
    await view.resolveSource();
    await waitFor(() => expect(view.pane.dataset.pending).toBe("false"));
    expect(view.row()).toBeNull();
    expect(view.pane.querySelector('[data-resident="active"]')?.getAttribute("data-resident-key")).toContain(view.second);
    view.tree.resolve(listing("nested"));
    await waitFor(() => expect(view.row()).not.toBeNull());
  });

  it("closes and switches tabs while tree preparation never completes", async () => {
    const view = await setup();
    await view.resolveSource();
    await waitFor(() => expect(view.pane.dataset.pending).toBe("false"));
    setFilesActiveBuffer(PROJECT, view.first.key);
    await waitFor(() => expect(view.pane.querySelector('[data-resident="active"]')?.getAttribute("data-resident-key")).toContain(view.first.key));
    await closeFilesBuffer(PROJECT, view.first.key);
    await waitFor(() => expect(view.pane.querySelector('[data-resident="active"]')?.getAttribute("data-resident-key")).toContain(view.second));
    expect(view.pane.dataset.pending).toBe("false");
    expect(view.row()).toBeNull();
    await closeFilesBuffer(PROJECT, view.second);
    await waitFor(() => expect(screen.queryByTestId("files-pane-presentation")?.querySelector('.den-files-tab-deck [data-resident="active"]') ?? null).toBeNull());
  });

  it("opens without expanding the tree when disabled, but still allows explicit reveal", async () => {
    const view = await setup(false);
    await view.resolveSource();
    await waitFor(() => expect(view.pane.dataset.pending).toBe("false"));
    expect(view.row()).toBeNull();
    setProjectFilesRevealRequest({ projectId: PROJECT, rootId: "r1", path: "nested/second.ts", isDir: false });
    view.tree.resolve(listing("nested"));
    await waitFor(() => expect(view.row()).not.toBeNull());
  });

  it("selects a file on every navigation after the reader selects a folder", async () => {
    const view = await setup();
    view.tree.resolve(listing("nested"));
    await view.resolveSource();
    await waitFor(() => expect(view.pane.dataset.pending).toBe("false"));
    const row = () => document.querySelector('.den-files-tree__label--file[data-path="nested/second.ts"]');
    await waitFor(() => expect(row()?.getAttribute("aria-current")).toBe("true"));
    const folder = document.querySelector('.den-files-tree__label--dir[data-path="nested"]')!;
    fireEvent.click(folder);
    expect(row()?.getAttribute("aria-current")).not.toBe("true");
    fireEvent.click(folder);
    await waitFor(() => expect(row()).toBeNull());
    openFilesBuffer(PROJECT, { intent: "permanent", rootId: "r1", rootLabel: "repo", path: "nested/second.ts" });
    await waitFor(() => expect(row()?.getAttribute("aria-current")).toBe("true"));
  });

  it("selects an explicitly revealed file until the reader navigates again", async () => {
    const view = await setup();
    view.tree.resolve(listing("nested"));
    await view.resolveSource();
    await waitFor(() => expect(view.pane.dataset.pending).toBe("false"));
    const file = (path: string) => document.querySelector(`.den-files-tree__label--file[data-path="${path}"]`);
    await waitFor(() => expect(file("nested/second.ts")?.getAttribute("aria-current")).toBe("true"));
    setProjectFilesRevealRequest({ projectId: PROJECT, rootId: "r1", path: "first.ts", isDir: false });
    await waitFor(() => expect(file("first.ts")?.getAttribute("aria-current")).toBe("true"));
    expect(view.pane.querySelector('[data-resident="active"]')?.getAttribute("data-resident-key")).toContain("nested/second.ts");
    openFilesBuffer(PROJECT, { intent: "permanent", rootId: "r1", rootLabel: "repo", path: "nested/second.ts" });
    await waitFor(() => expect(file("nested/second.ts")?.getAttribute("aria-current")).toBe("true"));
  });

  it("keeps the active file selected after an abandoned reveal finishes", async () => {
    const view = await setup();
    await view.resolveSource();
    setFilesActiveBuffer(PROJECT, view.first.key);
    await waitFor(() => expect(view.pane.dataset.pending).toBe("false"));
    view.tree.resolve(listing("nested"));
    await paint();
    const active = view.pane.querySelector('[data-resident="active"]');
    expect(active?.getAttribute("data-resident-key")).toContain(view.first.key);
    expect(view.row()?.querySelector("[aria-current=true]")).toBeFalsy();
    expect(document.querySelector('.den-files-tree__label--file[data-path="first.ts"]')?.getAttribute("aria-current")).toBe("true");
  });
});
