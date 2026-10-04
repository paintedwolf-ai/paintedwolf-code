import { resetEditorPrefsForTests } from "../../settings/editor/editor-prefs.ts";
import { createSignal, type ComponentProps } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { FilesTree } from "./FilesTree.tsx";
import type { FilesTreeEntrySelection, FilesTreeFolderScope, FilesTreeHandle } from "./files-tree-context.ts";
import { treeRow, treeViewFixture, TREE_ROOTS } from "../../test/source-tree-view-fixture.ts";
import { scrollportMotionForViewport } from "../../platform/scrolling/scrollport-motion.ts";
import { receiveSourceViewEvent, resyncSourceViews } from "../../ui/paged-view/source-view-session.ts";
import { resetFilesTreeViewStateForTests, setFilesTreeScrollTop } from "./files-tree-view-state.ts";
import { findFileRow, getFileRow, queryFileRow } from "../../test/files-tree-queries.ts";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";

const projectNotices = (store: ReturnType<typeof createNoticeStore>, projectId: string) =>
  selectProjectNoticeGroups(store.index()).find(group => group.projectId === projectId)?.notices ?? [];

afterEach(() => { cleanup(); resetEditorPrefsForTests(); resetFilesTreeViewStateForTests(); vi.useRealTimers(); });
const echoPath = async (_kind: string, _root: string, path: string) => path;
const button = (path: string) => document.querySelector<HTMLButtonElement>(`.den-files-tree__label[data-path="${path}"]`)!;
function mount(fixture = treeViewFixture(), overrides: Partial<ComponentProps<typeof FilesTree>> = {}) {
  const [selection, setSelection] = createSignal<FilesTreeEntrySelection | null>(null);
  const [visible, setVisible] = createSignal(true);
  const onOpenFile = vi.fn();
  let tree!: FilesTreeHandle;
  const result = render(() => <div class="den-files-tree-scroll-frame">
    <div class="den-files-tree-scroll" style={{ height: "300px", overflow: "auto" }} ref={element => {
      Object.defineProperties(element, {
        clientHeight: { configurable: true, value: 300 },
        clientWidth: { configurable: true, value: 240 },
        scrollHeight: { configurable: true, get: () => Math.max(300,
          Number.parseFloat(element.querySelector<HTMLElement>(".den-files-tree-virtual")?.style.height ?? "0")) },
      });
    }}>
      <FilesTree projectId="p1" workspaceId="ws1" client={fixture.client} roots={TREE_ROOTS}
        visible={visible()} activeFile={null} selectedEntry={selection()} onSelectEntry={setSelection}
        onOpenFile={onOpenFile} createEntry={echoPath}
        onReady={handle => { tree = handle; }} {...overrides} />
    </div><div data-files-tree-sticky-host /></div>);
  return { ...result, fixture, onOpenFile, setVisible, selection, expand: (scope?: FilesTreeFolderScope) => tree.expandAll(scope),
    collapse: (scope?: FilesTreeFolderScope) => tree.collapseAll(scope), create: (kind: "file" | "folder", dir = ".") => tree.startCreate(kind, "r1", dir),
    reveal: (path: string, dir = false) => tree.revealPath("r1", path, dir) };
}

describe("paged Files tree", () => {
  it.each(["automatic", "explicit"] as const)("reports a failed %s reveal to the project's notices, not in the pane", async mode => {
    const notices = createNoticeStore();
    registerNoticePublisher(notices);
    const fixture = treeViewFixture([treeRow(".", "directory", true), treeRow("src", "directory", false)]);
    fixture.update(command => { if (command.kind === "reveal") throw new Error("Reveal unavailable"); });
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      const view = mount(fixture, mode === "automatic" ? { activeFile: { rootId: "r1", path: "src/child.ts" } } : {});
      if (mode === "explicit") {
        await waitFor(() => expect(button("src")).toBeTruthy());
        view.reveal("src/child.ts");
      }
      await waitFor(() => expect(projectNotices(notices, "p1")).toHaveLength(1));
      expect(screen.queryByRole("alert")).toBeNull();
    } finally { logged.mockRestore(); registerNoticePublisher(null); }
  });

  it("names a host preparation failure once in the project's notices", async () => {
    const notices = createNoticeStore();
    registerNoticePublisher(notices);
    try {
      const fixture = treeViewFixture([treeRow(".", "directory", true)]);
      mount(fixture);
      await waitFor(() => expect(button(".")).toBeTruthy());
      fixture.failed("The workspace could not be indexed."); fixture.notify();
      await waitFor(() => expect(projectNotices(notices, "p1")).toHaveLength(1));
      const [notice] = projectNotices(notices, "p1");
      expect(notice).toMatchObject({ code: "files_tree_unavailable", title: "Files unavailable", message: "The workspace could not be indexed." });
      fixture.notify();
      await new Promise(resolve => setTimeout(resolve, 40));
      expect(projectNotices(notices, "p1")).toHaveLength(1);
    } finally { registerNoticePublisher(null); }
  });

  it("accepts collapse during cold recursive preparation and presents complete rows", async () => {
    const fixture = treeViewFixture();
    const view = mount(fixture);
    await findFileRow("README.md");
    fixture.update(command => {
      if (command.kind !== "disclose") return;
      fixture.preparing(command.disclosures[0]?.open === true);
    });
    view.expand();
    await waitFor(() => expect(fixture.state().state).toBe("preparing"));
    expect(button("README.md")).toBeTruthy();
    view.collapse();
    await waitFor(() => expect(fixture.state().state).toBe("ready"));
    await findFileRow("README.md");
    expect(document.querySelector(".den-files-tree__hint--loading")).toBeNull();
    expect(screen.queryByTestId("files-tree-expansion-status")).toBeNull();
  });

  it("loads bounded frames and opens the selected file", async () => {
    const view = mount();
    await findFileRow("README.md");
    fireEvent.click(button("README.md"));
    expect(view.selection()).toEqual({ rootId: "r1", path: "README.md", kind: "file" });
    expect(view.onOpenFile).toHaveBeenCalledWith({ rootId: "r1", rootLabel: "repo", path: "README.md" }, "permanent");
    expect(view.fixture.requests.filter(request => request.path.includes("/rows?")).length).toBeLessThanOrEqual(2);
    expect(view.fixture.requests.some(request => request.path.includes("tree-window") || request.path.includes("/browse"))).toBe(false);
  });

  it("settles boot only after the first frame is usable", async () => {
    const fixture = treeViewFixture();
    let release!: () => void;
    const gate = new Promise<void>(resolve => { release = resolve; });
    fixture.read(async () => gate);
    const onInitialLoadSettled = vi.fn();
    mount(fixture, { onInitialLoadSettled });
    await waitFor(() => expect(fixture.requests.some(request => request.path.includes("/rows?"))).toBe(true));
    expect(onInitialLoadSettled).not.toHaveBeenCalled();
    release();
    await findFileRow("README.md");
    await waitFor(() => expect(onInitialLoadSettled).toHaveBeenCalledWith("ws1"));
  });

  it("retains recursive disclosure while Files is hidden", async () => {
    const view = mount(); await findFileRow("README.md");
    view.expand();
    await waitFor(() => expect(view.fixture.commands.filter(command => command.kind === "disclose" && command.disclosures[0]!.recursive)).toHaveLength(1));
    view.setVisible(false);
    view.fixture.rows([treeRow(".", "directory", true), treeRow("finished.ts")]);
    view.fixture.notify();
    expect(view.fixture.requests.some(request => request.path.endsWith("/cancel") || request.method === "DELETE" && /\/views\/[^/]+$/.test(request.path))).toBe(false);
    view.setVisible(true);
    await findFileRow("finished.ts");
    expect(view.fixture.requests.filter(request => request.method === "POST" && request.path.endsWith("/views"))).toHaveLength(1);
  });

  it.each([false, true])("settles a workspace refresh after its frame has already arrived (empty=%s)", async empty => {
    const fixture = treeViewFixture(empty ? [] : undefined);
    const [workspaceSettled, setWorkspaceSettled] = createSignal(false);
    const ready = vi.fn();
    mount(fixture, { onInitialLoadSettled: workspace => {
      if (workspaceSettled()) ready(workspace);
    } });
    if (empty) await waitFor(() => expect(fixture.requests.some(request => request.method === "POST" && request.path.endsWith("/views"))).toBe(true));
    else await findFileRow("README.md");
    expect(ready).not.toHaveBeenCalled();
    setWorkspaceSettled(true);
    await waitFor(() => expect(ready).toHaveBeenCalledWith("ws1"));
  });

  it("reuses the accepted host view after a component remount", async () => {
    const fixture = treeViewFixture(); const first = mount(fixture); await findFileRow("README.md");
    first.expand(); await waitFor(() => expect(fixture.state().intent.disclosures).toHaveLength(1));
    first.unmount();
    fixture.rows([treeRow(".", "directory", true), treeRow("after.ts")]); fixture.notify();
    mount(fixture); await findFileRow("after.ts");
    expect(fixture.requests.filter(request => request.method === "POST" && request.path.endsWith("/views"))).toHaveLength(1);
    expect(fixture.requests.some(request => request.path.endsWith("/cancel") || request.method === "DELETE" && /\/views\/[^/]+$/.test(request.path))).toBe(false);
  });

  it("returns to a prepared file without waiting for another frame", async () => {
    const fixture = treeViewFixture();
    const target = { rootId: "r1", rootLabel: "repo", path: "README.md" };
    const view = mount(fixture, { activeFile: target });
    await findFileRow("README.md");
    const reveals = fixture.commands.filter(command => command.kind === "reveal").length;
    let release!: () => void;
    fixture.read(() => new Promise<void>(resolve => { release = resolve; }));
    view.setVisible(false);
    view.setVisible(true);
    await findFileRow("README.md");
    expect(fixture.commands.filter(command => command.kind === "reveal")).toHaveLength(reveals);
    expect(button("README.md")).toBeTruthy();
    release?.();
  });

  it("retains published rows while the expanded viewport is prepared", async () => {
    const view = mount(); await findFileRow("README.md");
    view.expand();
    await waitFor(() => expect(view.fixture.commands.some(command => command.kind === "disclose")).toBe(true));
    expect(screen.queryByRole("status")).toBeNull();
    let release!: () => void;
    const gate = new Promise<void>(resolve => { release = resolve; });
    view.fixture.read(async () => gate);
    const reads = view.fixture.requests.length;
    view.fixture.rows([treeRow(".", "directory", true), treeRow("finished.ts")]);
    view.fixture.notify();
    await waitFor(() => expect(view.fixture.requests.slice(reads).some(request => request.path.includes("/rows?"))).toBe(true));
    expect(screen.queryByTestId("files-tree-expansion-status")).toBeNull();
    expect(button("README.md")).toBeTruthy();
    release();
    await findFileRow("finished.ts");
    await waitFor(() => expect(screen.queryByTestId("files-tree-expansion-status")).toBeNull());
    let finishRefresh!: () => void;
    const refresh = new Promise<void>(resolve => { finishRefresh = resolve; });
    view.fixture.read(async () => refresh);
    const completedReads = view.fixture.requests.length;
    view.fixture.notify();
    await waitFor(() => expect(view.fixture.requests.slice(completedReads).some(request => request.path.includes("/rows?"))).toBe(true));
    expect(screen.queryByTestId("files-tree-expansion-status")).toBeNull();
    finishRefresh();
  });

  it("scopes expansion and ordinary disclosure independently", async () => {
    const fixture = treeViewFixture([treeRow(".", "directory", true), treeRow("src", "directory")]);
    fixture.update(command => {
      if (command.kind === "disclose") fixture.rows([treeRow(".", "directory", true), treeRow("src", "directory", command.disclosures[0]!.open)]);
    });
    const view = mount(fixture); await waitFor(() => expect(button("src")).toBeTruthy());
    view.expand({ rootId: "r1", dir: "src" });
    await waitFor(() => expect(fixture.state().intent.disclosures).toHaveLength(1));
    fireEvent.click(button("src"));
    await waitFor(() => expect(fixture.commands).toContainEqual({ kind: "disclose", disclosures: [{ address: { root_id: "r1", path: "src" }, open: true, recursive: false }] }));
    expect(fixture.requests.some(request => request.path.endsWith("/cancel"))).toBe(false);
    expect(fixture.state().intent.disclosures?.[0]?.address.path).toBe("src");
  });

  it("collapses all descendants and leaves the root open", async () => {
    const view = mount(); await findFileRow("README.md"); view.collapse();
    await waitFor(() => expect(view.fixture.commands).toEqual([
      { kind: "disclose", disclosures: [
        { address: { root_id: "r1", path: "." }, open: false, recursive: true },
        { address: { root_id: "r1", path: "." }, open: true, recursive: false },
      ] },
    ]));
  });

  it("preserves visible row identity when a notification changes no projection", async () => {
    const view = mount(); const row = await findFileRow("README.md");
    const current = view.fixture.state();
    receiveSourceViewEvent({ view_id: current.id, kind: "tree", intent_revision: current.intent_revision,
      projection_revision: current.projection_revision, invalidated: false, terminal: true });
    await waitFor(() => expect(view.fixture.requests.filter(request => request.method === "GET" && !request.path.includes("?")).length).toBeGreaterThan(1));
    expect(getFileRow("README.md")).toBe(row);
  });

  it("renders a distant range without downloading intervening rows", async () => {
    const fixture = treeViewFixture();
    fixture.large(1_000_001, index => index === 0 ? treeRow(".", "directory", true) : treeRow(`file-${index}.ts`));
    const view = mount(fixture);
    await findFileRow("file-1.ts");
    expect(document.querySelectorAll(".den-files-tree__label").length).toBeLessThan(200);
    const height = Number.parseFloat(document.querySelector<HTMLElement>(".den-files-tree-virtual")!.style.height);
    expect(height).toBeLessThanOrEqual(4_000_000);
    button("file-1.ts").focus(); fireEvent.keyDown(button("file-1.ts"), { key: "End" });
    await waitFor(() => expect(document.activeElement?.getAttribute("data-path")).toBe("file-1000000.ts"));
    expect(view.fixture.requests.filter(request => request.path.includes("/rows?")).length).toBeLessThan(10);
    const requestsBeforeUpdate = fixture.requests.length;
    fixture.notify();
    await waitFor(() => expect(fixture.requests.slice(requestsBeforeUpdate).some(request => request.path.includes("/rows?"))).toBe(true));
    await findFileRow("file-1000000.ts");
    await new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
    const scroll = document.querySelector<HTMLElement>(".den-files-tree-scroll")!;
    fireEvent.keyDown(button("file-1000000.ts"), { key: "Home" });
    await findFileRow("file-1.ts");
    await waitFor(() => expect(scroll.scrollTop).toBe(0));
    expect(screen.queryByText("Loading…")).toBeNull();
  });
});

describe("Files interactions with host rows", () => {
  it("keeps one sequential focus stop and roves with arrow keys", async () => {
    mount(treeViewFixture([treeRow(".", "directory", true), treeRow("a.ts"), treeRow("b.ts")]));
    await findFileRow("a.ts"); button("a.ts").focus();
    fireEvent.keyDown(button("a.ts"), { key: "ArrowDown" });
    await waitFor(() => expect(document.activeElement).toBe(button("b.ts")));
    expect(document.querySelectorAll('.den-files-tree__label[tabindex="0"]')).toHaveLength(1);
    fireEvent.keyDown(button("b.ts"), { key: "Home" });
    await waitFor(() => expect(document.activeElement).toBe(button(".")));
  });

  it("opens on folder selection, toggles while selected, and transfers selection to a file", async () => {
    const rows = [treeRow(".", "directory", true), treeRow("docs", "directory"), treeRow("other", "directory"), treeRow("README.md")];
    const fixture = treeViewFixture(rows);
    fixture.update(command => {
      if (command.kind === "disclose") {
        const row = rows.find(row => row.address.path === command.disclosures[0]!.address.path)!;
        row.expanded = command.disclosures[0]!.open;
        fixture.rows([...rows]);
      }
    });
    const view = mount(fixture);
    await findFileRow("README.md");
    fireEvent.click(button("docs"));
    await waitFor(() => expect(button("docs").getAttribute("aria-expanded")).toBe("true"));
    fireEvent.click(button("docs"));
    await waitFor(() => expect(button("docs").getAttribute("aria-expanded")).toBe("false"));
    fireEvent.click(button("other"));
    await waitFor(() => expect(button("other").getAttribute("aria-expanded")).toBe("true"));
    fireEvent.click(button("docs"));
    await waitFor(() => expect(button("docs").getAttribute("aria-expanded")).toBe("true"));
    expect(button("docs").getAttribute("aria-current")).toBe("true");
    expect(view.onOpenFile).not.toHaveBeenCalled();
    fireEvent.click(button("README.md"));
    expect(button("docs").getAttribute("aria-current")).toBeNull();
    expect(button("README.md").getAttribute("aria-current")).toBe("true");
    expect(view.onOpenFile).toHaveBeenCalledOnce();
  });

  it("opens context actions from Shift+F10", async () => {
    const onRowMenu = vi.fn(); mount(treeViewFixture(), { onRowMenu }); await findFileRow("README.md");
    fireEvent.keyDown(button("README.md"), { key: "F10", shiftKey: true });
    expect(onRowMenu).toHaveBeenCalledWith(expect.anything(), expect.objectContaining({ path: "README.md", surface: "tree-row" }));
  });

  it("renders added, changed, and deleted marks from their respective sources", async () => {
    const fixture = treeViewFixture([treeRow(".", "directory", true), treeRow("added.ts"), treeRow("changed.ts"), { ...treeRow("gone.ts"), deleted: true }]);
    mount(fixture, { inScope: () => true, addedInScope: (_root, path) => path === "added.ts" });
    await findFileRow("gone.ts");
    expect(getFileRow("gone.ts").closest("[data-deleted]")?.getAttribute("data-deleted")).toBe("true");
    expect(getFileRow("added.ts").parentElement?.querySelector('[data-kind="added"]')).toBeTruthy();
    expect(getFileRow("changed.ts").parentElement?.querySelector('[data-kind="changed"]')).toBeTruthy();
  });

  it("creates under the chosen folder with the existing inline form", async () => {
    const createEntry = vi.fn(echoPath);
    const view = mount(treeViewFixture(), { createEntry }); await findFileRow("README.md");
    view.create("file");
    const input = await screen.findByRole("textbox", { name: /name/i });
    fireEvent.input(input, { target: { value: "new.ts" } }); fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(createEntry).toHaveBeenCalledWith("file", "r1", "new.ts"));
    expect(view.onOpenFile).toHaveBeenCalledWith({ rootId: "r1", rootLabel: "repo", path: "new.ts" }, "permanent");
  });

  it("keeps a rejected name available for correction", async () => {
    const createEntry = vi.fn(async () => { throw new Error("Already exists"); });
    const view = mount(treeViewFixture(), { createEntry }); await findFileRow("README.md"); view.create("file");
    const input = await screen.findByRole("textbox", { name: /name/i });
    fireEvent.input(input, { target: { value: "taken.ts" } }); fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(createEntry).toHaveBeenCalled());
    expect((screen.getByRole("textbox", { name: /name/i }) as HTMLInputElement).value).toBe("taken.ts");
  });

  it("rejects an escaping create path and cancels the form with Escape", async () => {
    const createEntry = vi.fn(echoPath); const view = mount(treeViewFixture(), { createEntry });
    await findFileRow("README.md"); view.create("file");
    const input = await screen.findByRole("textbox", { name: /name/i });
    fireEvent.input(input, { target: { value: "../escape.ts" } }); fireEvent.keyDown(input, { key: "Enter" });
    expect(createEntry).not.toHaveBeenCalled();
    fireEvent.keyDown(input, { key: "Escape" });
    expect(screen.queryByRole("textbox", { name: /name/i })).toBeNull();
  });

  it("creates a folder with a trailing slash without opening a file", async () => {
    const createEntry = vi.fn(echoPath); const view = mount(treeViewFixture(), { createEntry });
    await findFileRow("README.md"); view.create("folder");
    const input = await screen.findByRole("textbox", { name: /name/i });
    fireEvent.input(input, { target: { value: "nested/" } }); fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(createEntry).toHaveBeenCalledWith("folder", "r1", "nested"));
    expect(view.onOpenFile).not.toHaveBeenCalled();
  });

  it("rejects a file name with a trailing slash", async () => {
    const createEntry = vi.fn(echoPath); const view = mount(treeViewFixture(), { createEntry });
    await findFileRow("README.md"); view.create("file");
    const input = await screen.findByRole("textbox", { name: /name/i });
    fireEvent.input(input, { target: { value: "nested/" } }); fireEvent.keyDown(input, { key: "Enter" });
    expect(createEntry).not.toHaveBeenCalled();
    expect(screen.getByRole("textbox", { name: /name/i })).toBe(input);
  });

  it("renames a retained row and keeps a rejected name available", async () => {
    const onCommitRename = vi.fn(async () => { throw new Error("Already exists"); });
    const onCancelRename = vi.fn();
    mount(treeViewFixture(), { renaming: { rootId: "r1", path: "README.md" }, onCommitRename, onCancelRename });
    const input = await screen.findByRole("textbox");
    fireEvent.input(input, { target: { value: "taken.md" } }); fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(onCommitRename).toHaveBeenCalledWith("r1", "README.md", "taken.md", false));
    expect((screen.getByRole("textbox") as HTMLInputElement).value).toBe("taken.md");
    expect(onCancelRename).not.toHaveBeenCalled();
    await screen.findByRole("alert");
    await waitFor(() => expect((input as HTMLInputElement).disabled).toBe(false));
    fireEvent.keyDown(input, { key: "Escape" }); expect(onCancelRename).toHaveBeenCalledOnce();
  });

  it("reopens a linked folder and focuses it after host disclosure", async () => {
    const fixture = treeViewFixture([treeRow(".", "directory", true), treeRow("src", "directory")]);
    fixture.update(command => {
      if (command.kind === "disclose") fixture.rows([treeRow(".", "directory", true), treeRow("src", "directory", command.disclosures[0]!.open)]);
    });
    const view = mount(fixture); await waitFor(() => expect(button("src")).toBeTruthy());
    view.reveal("src", true);
    await waitFor(() => expect(document.activeElement).toBe(button("src")));
    expect(view.selection()).toEqual({ rootId: "r1", path: "src", kind: "folder" });
    expect(button("src").getAttribute("aria-expanded")).toBe("true");
    // A folder reveal opens its ancestors and itself in one command.
    expect(fixture.commands).toEqual([{ kind: "disclose", disclosures: [
      { address: { root_id: "r1", path: "." }, open: true, recursive: false },
      { address: { root_id: "r1", path: "src" }, open: true, recursive: false },
    ] }]);
  });

  it("does not select a linked path that the host cannot locate", async () => {
    const view = mount(); await findFileRow("README.md"); view.reveal("missing.ts");
    await waitFor(() => expect(view.fixture.requests.some(request => request.path.includes("/rows?") && JSON.parse(atob(new URL(request.path, "http://fixture").searchParams.get("anchor") ?? "e30=")).path === "missing.ts")).toBe(true));
    expect(view.selection()).toBeNull(); expect(view.onOpenFile).not.toHaveBeenCalled();
  });

  it("keeps row identity when scope marks change and removes stale marks", async () => {
    const [marked, setMarked] = createSignal(true);
    mount(treeViewFixture(), { inScope: () => marked() });
    const row = await findFileRow("README.md");
    expect(row.parentElement?.querySelector('[data-kind="changed"]')).toBeTruthy();
    setMarked(false);
    expect(getFileRow("README.md")).toBe(row);
    expect(row.parentElement?.querySelector('[data-kind="changed"]')).toBeNull();
  });

  it("keeps deleted entries actionable", async () => {
    const onRevertFile = vi.fn();
    mount(treeViewFixture([treeRow(".", "directory", true), { ...treeRow("gone.ts"), deleted: true }]), { onRevertFile });
    const revert = await screen.findByTestId("files-tree-revert");
    fireEvent.click(revert); expect(onRevertFile).toHaveBeenCalledWith("r1", "gone.ts");
  });

  it("renders agent activity from host facts alongside paged rows", async () => {
    mount(treeViewFixture(), { agentPresenceForPath: (_root, path) => path === "README.md" ? {
      chip: { state: "reading", text: "Reading", description: "Reading this file", sessionId: "session" }, name: null, labels: ["Reading this file"],
    } : null });
    await findFileRow("README.md");
    expect(document.querySelector('.den-files-presence--reading')).toBeTruthy();
  });

  it("filters through a host command and preserves the existing empty state", async () => {
    const fixture = treeViewFixture(); const [query, setQuery] = createSignal("");
    fixture.update(command => { if (command.kind === "filter") fixture.rows(command.query ? [] : [treeRow(".", "directory", true), treeRow("README.md")]); });
    mount(fixture, { get filterQuery() { return query(); }, filterOpen: true, onFilterQueryChange: setQuery });
    await findFileRow("README.md"); fireEvent.input(screen.getByRole("searchbox"), { target: { value: "missing" } });
    await screen.findByTestId("files-tree-filter-empty");
    expect(fixture.commands).toContainEqual({ kind: "filter", query: "missing" });
    fireEvent.keyDown(screen.getByRole("searchbox"), { key: "Escape" });
    expect(query()).toBe("");
  });
});


describe("Files presentation lifecycle", () => {
  it("fences a pending frame when the workspace changes", async () => {
    const old = treeViewFixture([treeRow(".", "directory", true), treeRow("old.ts")]);
    const next = treeViewFixture([treeRow(".", "directory", true), treeRow("new.ts")]);
    let release!: () => void;
    old.read(() => new Promise<void>(resolve => { release = resolve; }));
    const [workspace, setWorkspace] = createSignal("ws1");
    mount(old, { get workspaceId() { return workspace(); }, get client() { return workspace() === "ws1" ? old.client : next.client; } });
    await waitFor(() => expect(release).toBeDefined());
    setWorkspace("ws2");
    await findFileRow("new.ts");
    release();
    await Promise.resolve();
    expect(button("old.ts")).toBeNull();
    expect(button("new.ts")).toBeTruthy();
  });

  it("expands through disclosure without a progress overlay", async () => {
    const view = mount(); await findFileRow("README.md");
    view.expand();
    await waitFor(() => expect(view.fixture.commands).toContainEqual({ kind: "disclose",
      disclosures: [{ address: { root_id: "r1", path: "." }, open: true, recursive: true }] }));
    expect(getFileRow("README.md")).toBeTruthy();
    expect(screen.queryByRole("status")).toBeNull();
    expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
  });

  it("retains complete rows during preparation and reports host directory failures", async () => {
    const fixture = treeViewFixture([treeRow(".", "directory", true)]);
    mount(fixture);
    await waitFor(() => expect(button(".")).toBeTruthy());
    fixture.preparing(true); fixture.notify();
    expect(button(".")).toBeTruthy();
    expect(screen.queryByText("Loading…")).toBeNull();
    fixture.preparing(false);
    fixture.rows([treeRow(".", "directory", true), { ...treeRow("src"), kind: "error", error: "Directory unavailable" }]);
    fixture.notify();
    await screen.findByRole("alert");
    expect(screen.getByRole("alert").textContent).toContain("Directory unavailable");
    expect(screen.queryByText("Loading…")).toBeNull();
  });

  it("does not restore an old frame anchor after the reader scrolls again", async () => {
    const rows = [treeRow(".", "directory", true), ...Array.from({ length: 500 }, (_, i) => treeRow(`file-${i}.ts`))];
    const fixture = treeViewFixture(rows);
    mount(fixture);
    await findFileRow("file-0.ts");
    const scroll = document.querySelector<HTMLElement>(".den-files-tree-scroll")!;
    scroll.scrollTop = 260; fireEvent.scroll(scroll);
    let release!: () => void;
    const gate = new Promise<void>(resolve => { release = resolve; });
    fixture.read(async () => gate);
    const requestsBeforeUpdate = fixture.requests.length;
    fixture.notify();
    await waitFor(() => expect(fixture.requests.slice(requestsBeforeUpdate).some(request => request.path.includes("/rows?"))).toBe(true));
    const pending = fixture.requests.slice(requestsBeforeUpdate).find(request => request.path.includes("/rows?"))!;
    scroll.scrollTop = 286; fireEvent.scroll(scroll);
    await new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
    expect(pending.signal?.aborted).toBe(false);
    release();
    await waitFor(() => expect(document.querySelector('[data-display-key="pending:11"]')).toBeNull());
    await new Promise(resolve => setTimeout(resolve, 40));
    expect(scroll.scrollTop).toBe(286);
  });

  it("keeps scroll subscriptions bounded across host publications and removes them on unmount", async () => {
    const add = vi.spyOn(HTMLElement.prototype, "addEventListener");
    const remove = vi.spyOn(HTMLElement.prototype, "removeEventListener");
    try {
      const fixture = treeViewFixture();
      const view = mount(fixture);
      await findFileRow("README.md");
      const scroll = document.querySelector<HTMLElement>(".den-files-tree-scroll")!;
      const listeners = () => add.mock.calls.flatMap((args, index) =>
        add.mock.contexts[index] === scroll && args[0] === "scroll" ? [args[1]] : []);
      const baseline = listeners().length;
      for (let index = 0; index < 8; index++) {
        fixture.rows([treeRow(".", "directory", true), treeRow(`update-${index}.ts`)]);
        fixture.notify();
        await findFileRow(`update-${index}.ts`);
      }
      expect(listeners()).toHaveLength(baseline);
      view.unmount();
      const removed = remove.mock.calls.flatMap((args, index) =>
        remove.mock.contexts[index] === scroll && args[0] === "scroll" ? [args[1]] : []);
      for (const listener of listeners()) expect(removed).toContain(listener);
    } finally { add.mockRestore(); remove.mockRestore(); }
  });

  it("pins nested ancestors while keeping sticky rows outside sequential focus", async () => {
    const fixture = treeViewFixture([treeRow(".", "directory", true), treeRow("src", "directory", true),
      treeRow("src/components", "directory", true), ...Array.from({ length: 40 }, (_, i) => treeRow(`src/components/file-${i}.ts`)), treeRow("zz.txt")]);
    mount(fixture); await findFileRow("src/components/file-0.ts");
    const scroll = document.querySelector<HTMLElement>(".den-files-tree-scroll")!;
    scroll.scrollTop = 3 * 26; fireEvent.scroll(scroll);
    await waitFor(() => expect(screen.getAllByTestId("files-tree-sticky-dir").map(row => row.getAttribute("data-path"))).toEqual([".", "src", "src/components"]));
    const sticky = screen.getAllByTestId("files-tree-sticky-dir");
    for (const row of sticky) expect(row.querySelector("button")?.tabIndex).toBe(-1);
    expect(document.querySelectorAll('.den-files-tree__label[tabindex="0"]')).toHaveLength(1);
    let release!: () => void;
    const gate = new Promise<void>(resolve => { release = resolve; });
    fixture.read(async () => gate);
    const reads = fixture.requests.length;
    fixture.notify();
    await waitFor(() => expect(fixture.requests.slice(reads).some(request => request.path.includes("/rows?"))).toBe(true));
    expect(screen.getAllByTestId("files-tree-sticky-dir")).toEqual(sticky);
    release();
    await waitFor(() => expect(screen.getAllByTestId("files-tree-sticky-dir")).toEqual(sticky));
    const stickyRoot = sticky[0]!.querySelector<HTMLButtonElement>(".den-files-tree__label")!;
    fireEvent.keyDown(stickyRoot, { key: "End" });
    await waitFor(() => expect(document.activeElement).toBe(button("zz.txt")));
    fireEvent.keyDown(stickyRoot, { key: "Home" });
    await waitFor(() => expect(scroll.scrollTop).toBe(0));
  });

  it("folds deep sticky folders into one row and indents the tree past them", async () => {
    const dirs = Array.from({ length: 8 }, (_, i) => Array.from({ length: i + 1 }, (_, j) => `d${j + 1}`).join("/"));
    const inner = dirs.at(-1)!;
    const fixture = treeViewFixture([treeRow(".", "directory", true), ...dirs.map(path => treeRow(path, "directory", true)),
      ...Array.from({ length: 40 }, (_, i) => treeRow(`${inner}/file-${i}.ts`))]);
    mount(fixture); await findFileRow(`${inner}/file-0.ts`);
    const scroll = document.querySelector<HTMLElement>(".den-files-tree-scroll")!;
    scroll.scrollTop = 8 * 26; fireEvent.scroll(scroll);
    // Folding follows the captured native offset, before a deferred render or scroll settle.
    expect(screen.getByTestId("files-tree").dataset.foldLevels).toBe("5");
    // A 300px pane fits four sticky rows: the root, the fold, and the two innermost folders.
    await waitFor(() => expect(screen.getAllByTestId("files-tree-sticky-dir").map(row => row.getAttribute("data-path")))
      .toEqual([".", dirs[6], dirs[7]]));
    const elision = screen.getByTestId("files-tree-sticky-elision");
    expect(elision.textContent).toContain("6 folders");
    expect(screen.getByTestId("files-tree").dataset.foldLevels).toBe("5");
    const trigger = elision.querySelector("button")!;
    expect(trigger.tabIndex).toBe(-1);
    expect(trigger.hasAttribute("data-tip")).toBe(false);
    fireEvent.click(trigger);
    const items = await screen.findAllByTestId("files-tree-sticky-elision-folder");
    expect(items.map(item => item.textContent)).toEqual(["d1", "d2", "d3", "d4", "d5", "d6"]);
    fireEvent.click(items[2]!);
    await waitFor(() => expect(document.activeElement).toBe(button(dirs[2]!)));
  });

  it("recalculates a scrolled tree when levels change or sticky rows turn off", async () => {
    const dirs = Array.from({ length: 8 }, (_, i) => Array.from({ length: i + 1 }, (_, j) => `d${j + 1}`).join("/"));
    const inner = dirs.at(-1)!;
    const fixture = treeViewFixture([treeRow(".", "directory", true), ...dirs.map(path => treeRow(path, "directory", true)),
      ...Array.from({ length: 40 }, (_, i) => treeRow(`${inner}/file-${i}.ts`))]);
    const view = mount(fixture); await findFileRow(`${inner}/file-0.ts`);
    const scroll = document.querySelector<HTMLElement>(".den-files-tree-scroll")!;
    scroll.scrollTop = 20 * 26; fireEvent.scroll(scroll);
    for (const levels of [3, 10, 0, 5]) {
      resetEditorPrefsForTests({ treeVisibleLevels: levels });
      const expected = levels === 0 ? [] : levels === 3 ? [".", inner] : [".", dirs[6], inner];
      await waitFor(() => expect(screen.queryAllByTestId("files-tree-sticky-dir").map(row => row.getAttribute("data-path"))).toEqual(expected));
      const fold = levels === 0 ? "0" : levels === 3 ? "6" : "5";
      expect(screen.getByTestId("files-tree").dataset.foldLevels).toBe(fold);
      if (levels === 0) {
        expect(screen.queryByTestId("files-tree-sticky-elision")).toBeNull();
        expect(document.querySelector(".den-files-tree-virtual__row--lead")).toBeNull();
      }
      const expectedIndent = (9 - Number(fold)) * 14 + 16;
      for (const row of screen.getByTestId("files-tree").querySelectorAll<HTMLElement>('.den-files-tree__row[data-isdir="false"]')) {
        expect(row.style.paddingLeft).toBe(`${expectedIndent}px`);
      }
    }
    view.reveal(`${inner}/file-0.ts`);
    await waitFor(() => expect(document.activeElement).toBe(button(`${inner}/file-0.ts`)));
  });

  it.each([20, 200])("renders restored rows at their folded indentation on their first publication at row %i", async start => {
    const dirs = Array.from({ length: 8 }, (_, i) => Array.from({ length: i + 1 }, (_, j) => `d${j + 1}`).join("/"));
    const parent = dirs.at(-1)!;
    const fixture = treeViewFixture([treeRow(".", "directory", true), ...dirs.map(path => treeRow(path, "directory", true)),
      ...Array.from({ length: 260 }, (_, i) => treeRow(`${parent}/file-${i}.ts`))]);
    setFilesTreeScrollTop("ws1", start * 26);
    const firstIndent = new Map<Element, string>();
    const observer = new MutationObserver(records => {
      for (const record of records) for (const node of record.addedNodes) {
        if (!(node instanceof HTMLElement)) continue;
        const selector = '.den-files-tree__row[data-isdir="false"]';
        const rows = node.matches(selector) ? [node] : [...node.querySelectorAll<HTMLElement>(selector)];
        for (const row of rows) if (!firstIndent.has(row)) firstIndent.set(row, row.style.paddingLeft);
      }
    });
    observer.observe(document.body, { childList: true, subtree: true });
    try {
      mount(fixture);
      await findFileRow(`${parent}/file-${start - 8}.ts`);
      await waitFor(() => expect(firstIndent.size).toBeGreaterThan(0));
      expect(new Set(firstIndent.values())).toEqual(new Set(["72px"]));
    } finally { observer.disconnect(); }
  });

  it("scrolls the tree from wheel input over sticky rows", async () => {
    const fixture = treeViewFixture([treeRow(".", "directory", true), treeRow("src", "directory", true),
      ...Array.from({ length: 60 }, (_, i) => treeRow(`src/file-${i}.ts`))]);
    mount(fixture); await findFileRow("src/file-0.ts");
    const scroll = document.querySelector<HTMLElement>(".den-files-tree-scroll")!;
    scroll.scrollTop = 2 * 26; fireEvent.scroll(scroll);
    await waitFor(() => expect(screen.getAllByTestId("files-tree-sticky-dir").map(row => row.getAttribute("data-path"))).toEqual([".", "src"]));
    const stickySrc = screen.getAllByTestId("files-tree-sticky-dir")[1]!;
    // Passive forwarding never prevents the default.
    expect(fireEvent.wheel(stickySrc, { deltaY: 120, deltaX: 0, deltaMode: 0 })).toBe(true);
    expect(scroll.scrollTop).toBe(2 * 26 + 120);
    expect(fireEvent.wheel(stickySrc, { deltaY: 3, deltaX: 0, deltaMode: 1 })).toBe(true);
    expect(scroll.scrollTop).toBe(2 * 26 + 120 + 3 * 26);
    expect(fireEvent.wheel(stickySrc, { deltaY: -40, deltaX: 0, deltaMode: 0 })).toBe(true);
    expect(scroll.scrollTop).toBe(2 * 26 + 120 + 3 * 26 - 40);
    // Pinch zoom stays with the platform.
    expect(fireEvent.wheel(stickySrc, { deltaY: 10, deltaX: 0, deltaMode: 0, ctrlKey: true })).toBe(true);
    expect(scroll.scrollTop).toBe(2 * 26 + 120 + 3 * 26 - 40);
  });

  it("does not open a second tab for the second click of a double click", async () => {
    const view = mount(); await findFileRow("README.md");
    fireEvent.click(button("README.md"), { detail: 1 });
    fireEvent.click(button("README.md"), { detail: 2 });
    expect(view.onOpenFile).toHaveBeenCalledTimes(1);
  });
});

it("reads a replacement view instead of keeping the expired view's rows", async () => {
  const fixture = treeViewFixture();
  const [retain, setRetain] = createSignal(false);
  mount(fixture, { get retainPresentation() { return retain(); } });
  await waitFor(() => expect(button(".").textContent).toContain("@repo"));
  // A root relabel ends the host view while the workspace revalidates; the
  // replacement names the root anew.
  setRetain(true);
  fixture.rows([{ ...treeRow(".", "directory", true), name: "renamed" }, treeRow("README.md")]);
  fixture.expire();
  resyncSourceViews("p1");
  await waitFor(() => expect(fixture.requests.filter(request => request.method === "POST" && request.path.endsWith("/views"))).toHaveLength(2));
  setRetain(false);
  await waitFor(() => expect(button(".").textContent).toContain("@renamed"));
});

it("keeps the visible position when expansion finishes after a scroll to the old bottom", async () => {
  const fixture = treeViewFixture();
  const rowAt = (index: number) => index === 0 ? treeRow(".", "directory", true) : treeRow(`file-${index}.ts`);
  fixture.large(601, rowAt);
  const view = mount(fixture);
  await findFileRow("file-1.ts");
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  fixture.read(async () => gate);
  fixture.update(command => { if (command.kind === "disclose") fixture.large(1801, rowAt); });
  view.expand();
  await waitFor(() => expect(fixture.commands.some(command => command.kind === "disclose")).toBe(true));
  const scroll = document.querySelector<HTMLElement>(".den-files-tree-scroll")!;
  scroll.scrollTop = scroll.scrollHeight - scroll.clientHeight;
  fireEvent.scroll(scroll);
  release();
  await waitFor(() => expect(scroll.scrollHeight).toBe(1801 * 26));
  expect(scroll.scrollTop).toBe(601 * 26 - 300);
  await findFileRow("file-600.ts");
});

it("keeps unchanged row elements as the buffered window moves", async () => {
  const fixture = treeViewFixture();
  fixture.large(1001, index => index === 0 ? treeRow(".", "directory", true) : treeRow(`file-${index}.ts`));
  mount(fixture);
  await findFileRow("file-1.ts");
  const scroll = document.querySelector<HTMLElement>(".den-files-tree-scroll")!;
  scroll.scrollTop = 26 * 40;
  fireEvent.scroll(scroll);
  const row = await findFileRow("file-50.ts");
  scroll.scrollTop = 26 * 58;
  fireEvent.scroll(scroll);
  await findFileRow("file-85.ts");
  expect(getFileRow("file-50.ts")).toBe(row);
});

it("retains unchanged roots and siblings across expand and collapse", async () => {
  const fixture = treeViewFixture([treeRow(".", "directory", true), treeRow("src", "directory")]);
  fixture.update(command => {
    if (command.kind !== "disclose") return;
    const open = command.disclosures[0]!.open;
    fixture.large(open ? 601 : 2, index => index === 0 ? treeRow(".", "directory", true)
      : index === 1 ? treeRow("src", "directory", open) : treeRow(`src/file-${index}.ts`));
  });
  const view = mount(fixture);
  await waitFor(() => expect(button("src")).toBeTruthy());
  const root = button("."), directory = button("src");
  view.expand();
  await findFileRow("src/file-2.ts");
  expect(button(".")).toBe(root);
  expect(button("src")).toBe(directory);
  view.collapse();
  await waitFor(() => expect(button("src")?.getAttribute("aria-expanded")).toBe("false"));
  expect(button(".")).toBe(root);
  expect(button("src")).toBe(directory);
});

it("contracts segmented scroll geometry after collapsing a distant expanded view", async () => {
  const fixture = treeViewFixture();
  fixture.large(1_000_001, index => index === 0 ? treeRow(".", "directory", true) : treeRow(`file-${index}.ts`));
  fixture.update(command => {
    if (command.kind === "disclose" && !command.disclosures[0]!.open) {
      fixture.large(101, index => index === 0 ? treeRow(".", "directory", true) : treeRow(`folder-${index}`, "directory"));
    }
  });
  const view = mount(fixture);
  await findFileRow("file-1.ts");
  fireEvent.keyDown(button("."), { key: "End" });
  await findFileRow("file-1000000.ts");
  view.collapse();
  await waitFor(() => expect(button("folder-1")).toBeTruthy());
  fireEvent.keyDown(button("."), { key: "End" });
  await waitFor(() => expect(button("folder-100")).toBeTruthy());
  const scroll = document.querySelector<HTMLElement>(".den-files-tree-scroll")!;
  await waitFor(() => expect(scroll.scrollHeight).toBe(101 * 26));
  await waitFor(() => expect(scroll.scrollTop).toBeLessThanOrEqual(101 * 26 - 300));
  const last = button("folder-100").closest<HTMLElement>(".den-files-tree-virtual__row")!;
  expect(last.style.transform).toBe("translateY(2600px)");
  scroll.scrollTop = 0;
  fireEvent.scroll(scroll);
  await waitFor(() => expect(button("folder-1")).toBeTruthy());
  expect(button(".")?.closest<HTMLElement>(".den-files-tree-virtual__row")?.style.transform).toBe("translateY(0px)");
});

it("mounts row affordances only while a row is hovered or focused", async () => {
  const fixture = treeViewFixture([treeRow(".", "directory", true), treeRow("src", "directory"), treeRow("README.md")]);
  mount(fixture);
  const file = await findFileRow("README.md");
  const fileRow = file.closest<HTMLElement>("[data-files-ctx='tree-row']")!;
  expect(screen.queryByRole("button", { name: "More actions for README.md" })).toBeNull();
  fireEvent.mouseOver(fileRow);
  expect(screen.getByRole("button", { name: "More actions for README.md" })).toBeTruthy();
  fireEvent.mouseOut(fileRow, { relatedTarget: document.body });
  expect(screen.queryByRole("button", { name: "More actions for README.md" })).toBeNull();
  const dirRow = button("src").closest<HTMLElement>("[data-files-ctx='tree-row']")!;
  expect(screen.queryByTestId("files-tree-new-file")).toBeNull();
  fireEvent.focusIn(button("src"));
  expect(dirRow.querySelector('[data-testid="files-tree-new-file"]')).toBeTruthy();
  expect(dirRow.querySelector('[data-testid="files-tree-new-folder"]')).toBeTruthy();
  expect(dirRow.querySelector(".den-files-tree__more")).toBeTruthy();
  fireEvent.focusOut(button("src"), { relatedTarget: button("README.md") });
  expect(dirRow.querySelector('[data-testid="files-tree-new-file"]')).toBeNull();
  expect(dirRow.querySelector(".den-files-tree__more")).toBeNull();
});

it("renders a narrow window while the thumb drags and refills the buffer on release", async () => {
  const fixture = treeViewFixture();
  fixture.large(2001, index => index === 0 ? treeRow(".", "directory", true) : treeRow(`file-${index}.ts`));
  mount(fixture);
  await findFileRow("file-1.ts");
  const rendered = () => document.querySelectorAll(".den-files-tree-virtual__row").length;
  // A 300px viewport shows 12 rows and buffers 18 more on each side.
  await waitFor(() => expect(rendered()).toBe(12 + 18));
  const frame = document.querySelector<HTMLElement>(".den-files-tree-scroll-frame")!;
  const handle = frame.querySelector<HTMLElement>(".os-scrollbar-vertical .os-scrollbar-handle")!;
  const track = frame.querySelector<HTMLElement>(".os-scrollbar-vertical .os-scrollbar-track")!;
  Object.defineProperty(track, "clientHeight", { configurable: true, value: 300 });
  Object.defineProperty(handle, "clientHeight", { configurable: true, value: 30 });
  // jsdom has no PointerEvent; the handler reads only these fields.
  const pointer = (type: string, init: Partial<PointerEvent>) =>
    Object.assign(new Event(type, { bubbles: true, cancelable: true }), { isPrimary: true, button: 0, pointerId: 7 }, init);
  handle.dispatchEvent(pointer("pointerdown", { clientY: 0 }));
  handle.dispatchEvent(pointer("pointermove", { clientY: 100 }));
  // The drag lands deep in the tree: the visible rows plus three on each side.
  await waitFor(() => expect(rendered()).toBeLessThanOrEqual(13 + 6));
  expect(rendered()).toBeGreaterThanOrEqual(12 + 6);
  handle.dispatchEvent(pointer("pointerup", { clientY: 100 }));
  await waitFor(() => expect(rendered()).toBeGreaterThanOrEqual(12 + 36));
});

it("reads one destination at a time while the thumb drags and lands at its latest position", async () => {
  const fixture = treeViewFixture();
  fixture.large(2_001, index => index === 0 ? treeRow(".", "directory", true) : treeRow(`file-${index}.ts`));
  mount(fixture);
  await findFileRow("file-1.ts");
  const frame = document.querySelector<HTMLElement>(".den-files-tree-scroll-frame")!;
  const handle = frame.querySelector<HTMLElement>(".os-scrollbar-vertical .os-scrollbar-handle")!;
  const track = frame.querySelector<HTMLElement>(".os-scrollbar-vertical .os-scrollbar-track")!;
  Object.defineProperty(track, "clientHeight", { configurable: true, value: 300 });
  Object.defineProperty(handle, "clientHeight", { configurable: true, value: 30 });
  const pointer = (type: string, clientY: number) => Object.assign(new Event(type, { bubbles: true, cancelable: true }),
    { isPrimary: true, button: 0, pointerId: 7, clientY });
  const nextFrame = () => new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
  // Every row read blocks here, so a destination read stays in flight until released.
  const holds: (() => void)[] = [];
  fixture.read(() => new Promise<void>(resolve => { holds.push(resolve); }));
  // Read-ahead only pages around the mounted window, so a far page names a thumb destination.
  const destinations = () => fixture.requests
    .filter(request => request.path.includes("/rows?"))
    .map(request => Number(new URL(request.path, "http://fixture").searchParams.get("offset")))
    .filter(offset => offset >= 1_000);

  // The three positions land on destination pages 600, 1000 and 1400.
  handle.dispatchEvent(pointer("pointerdown", 0));
  handle.dispatchEvent(pointer("pointermove", 90));
  await nextFrame();
  await waitFor(() => expect(holds.length).toBeGreaterThan(0));
  for (const y of [150, 210]) {
    handle.dispatchEvent(pointer("pointermove", y));
    await nextFrame();
  }
  // Positions passed during the read moved the destination without reading for themselves.
  expect(destinations()).toEqual([]);
  const thumbAt = () => Number(frame.querySelector<HTMLElement>(".os-scrollbar-vertical")!.style.getPropertyValue("--den-scrollbar-position"));
  expect(thumbAt()).toBeGreaterThan(0.7);

  fixture.read(undefined);
  for (const release of holds.splice(0)) release();
  // The landed read misses the thumb, so its next read is the latest position, not a passed one.
  await waitFor(() => expect(destinations().length).toBeGreaterThan(0));
  expect(destinations()[0]).toBe(1_400);

  const scroll = document.querySelector<HTMLElement>(".den-files-tree-scroll")!;
  await waitFor(() => expect(scroll.scrollTop).toBeGreaterThan(30_000));
  handle.dispatchEvent(pointer("pointerup", 210));
});

it("prepares folded rows before committing a thumb jump across physical scroll segments", async () => {
  const dirs = Array.from({ length: 20 }, (_, i) => Array.from({ length: i + 1 }, (_, j) => `d${j + 1}`).join("/"));
  const prefix = [treeRow(".", "directory", true), ...dirs.map(path => treeRow(path, "directory", true))];
  const parent = dirs.at(-1)!;
  const count = 4_000_001;
  const fixture = treeViewFixture(prefix);
  fixture.large(count, index => prefix[index] ?? treeRow(`${parent}/file-${index}.ts`));
  const getRows = fixture.client.getSourceViewRows;
  fixture.client.getSourceViewRows = async (...args) => {
    const frame = await getRows(...args);
    if (frame.kind === "tree" && frame.span.start >= prefix.length) {
      frame.ancestors = prefix.map((row, index) => ({ row, index, end: count }));
    }
    return frame;
  };
  mount(fixture);
  await findFileRow(`${parent}/file-21.ts`);
  const scroll = document.querySelector<HTMLElement>(".den-files-tree-scroll")!;
  const motion = scrollportMotionForViewport(scroll)!;
  const commit = motion.commit.bind(motion);
  const folds: { levels: string | undefined; padding: string[] }[] = [];
  const commits = vi.spyOn(motion, "commit").mockImplementation((offset, source, options) => {
    if (source === "thumb_drag") folds.push({ levels: screen.getByTestId("files-tree").dataset.foldLevels,
      padding: [...screen.getByTestId("files-tree").querySelectorAll<HTMLElement>('.den-files-tree__row[data-isdir="false"]')].map(row => row.style.paddingLeft) });
    commit(offset, source, options);
  });
  const host = document.querySelector<HTMLElement>(".den-files-tree-scroll-frame")!;
  const handle = host.querySelector<HTMLElement>(".os-scrollbar-vertical .os-scrollbar-handle")!;
  const track = host.querySelector<HTMLElement>(".os-scrollbar-vertical .os-scrollbar-track")!;
  Object.defineProperty(track, "clientHeight", { configurable: true, value: 300 });
  Object.defineProperty(handle, "clientHeight", { configurable: true, value: 30 });
  const pointer = (type: string, clientY: number) => Object.assign(new Event(type, { bubbles: true, cancelable: true }),
    { isPrimary: true, button: 0, pointerId: 7, clientY });
  try {
    handle.dispatchEvent(pointer("pointerdown", 0));
    for (const y of [70, 210, 120]) {
      const before = folds.length;
      handle.dispatchEvent(pointer("pointermove", y));
      await waitFor(() => expect(folds.length).toBeGreaterThan(before));
      expect(folds.at(-1)?.levels).toBe("17");
      expect(folds.at(-1)?.padding.length).toBeGreaterThan(0);
      expect(new Set(folds.at(-1)?.padding)).toEqual(new Set(["72px"]));
    }
    handle.dispatchEvent(pointer("pointerup", 120));
  } finally { commits.mockRestore(); }
});

it("lets prepared rows scroll natively and holds only an unprepared destination", async () => {
  const fixture = treeViewFixture();
  fixture.large(2_001, index => index === 0 ? treeRow(".", "directory", true) : treeRow(`file-${index}.ts`));
  mount(fixture);
  await findFileRow("file-1.ts");
  const scroll = document.querySelector<HTMLElement>(".den-files-tree-scroll")!;
  const inner = document.querySelector<HTMLElement>(".den-files-tree-virtual__inner")!;
  const nextFrame = () => new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
  // The mounted window (rows 0 to 29) covers this movement, so the scroll event holds nothing.
  scroll.scrollTop = 5 * 26; fireEvent.scroll(scroll);
  expect(inner.style.transform).toBe("");
  await nextFrame();
  scroll.scrollTop = 15 * 26; fireEvent.scroll(scroll);
  expect(inner.style.transform).toBe("");
  await nextFrame();
  // Outrunning the mounted window holds the last painted rows for the frame that mounts the next window.
  scroll.scrollTop = 60 * 26; fireEvent.scroll(scroll);
  expect(inner.style.transform).toBe(`translateY(${(60 - 15) * 26}px)`);
  await findFileRow("file-60.ts");
  await waitFor(() => expect(inner.style.transform).toBe(""));
  // A destination past the loaded pages keeps them in place until its rows arrive.
  scroll.scrollTop = 1_500 * 26; fireEvent.scroll(scroll);
  expect(inner.style.transform).toBe(`translateY(${(1_500 - 60) * 26}px)`);
  await findFileRow("file-1500.ts");
  await waitFor(() => expect(inner.style.transform).toBe(""));
});


it.each([-10, 10])("preserves a %s-row scroll while discovery displaces a pending viewport", async delta => {
  const original = [treeRow(".", "directory", true), ...Array.from({ length: 5_000 }, (_, index) => treeRow(`file-${index}.ts`))];
  const fixture = treeViewFixture(original);
  mount(fixture);
  await findFileRow("file-0.ts");
  const scroll = document.querySelector<HTMLElement>(".den-files-tree-scroll")!;
  scroll.scrollTop = 1_000 * 26; fireEvent.scroll(scroll);
  await findFileRow("file-1000.ts");
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  let pending = false;
  fixture.read(async () => { pending = true; await gate; });
  try {
    fixture.rows([original[0]!, ...Array.from({ length: 100 }, (_, index) => treeRow(`added-${index}.ts`)), ...original.slice(1)]);
    fixture.notify();
    await waitFor(() => expect(pending).toBe(true));
    scroll.scrollTop = (1_000 + delta) * 26; fireEvent.scroll(scroll);
    release();
    await waitFor(() => expect(scroll.scrollTop).toBe((1_100 + delta) * 26));
    await findFileRow(`file-${999 + delta}.ts`);
  } finally { release(); }
});


describe("complete presentation handoff", () => {
  const rowAt = (index: number) => index === 0 ? treeRow(".", "directory", true) : treeRow(`file-${index}.ts`);
  it("keeps the displayed extent fixed through preparation and publishes it once ready", async () => {
    const fixture = treeViewFixture();
    fixture.large(201, rowAt);
    mount(fixture);
    await findFileRow("file-1.ts");
    const original = getFileRow("file-1.ts");
    const virtual = document.querySelector<HTMLElement>(".den-files-tree-virtual")!;
    fixture.preparing(true);
    fixture.large(1_001, rowAt);
    fixture.notify();
    await new Promise(resolve => setTimeout(resolve, 350));
    expect(Number.parseFloat(virtual.style.height)).toBe(201 * 26);
    expect(getFileRow("file-1.ts")).toBe(original);
    expect(screen.queryByTestId("files-tree-counting")).toBeNull();
    fixture.preparing(false);
    fixture.notify();
    await waitFor(() => expect(Number.parseFloat(virtual.style.height)).toBe(1_001 * 26));
    expect(getFileRow("file-1.ts")).toBe(original);
  });
});

it("keeps the middle viewport anchored when expansion replaces rows at the same indexes", async () => {
  const original = [treeRow(".", "directory", true), ...Array.from({ length: 500 }, (_, index) => treeRow(`folder-${index}`, "directory"))];
  const fixture = treeViewFixture(original);
  fixture.update(command => {
    if (command.kind !== "disclose") return;
    fixture.rows(original.flatMap(row => row.address.path === "." ? [row] : [
      { ...row, expanded: true }, ...Array.from({ length: 4 }, (_, index) => treeRow(`${row.address.path}/file-${index}.ts`)),
    ]));
  });
  const view = mount(fixture);
  await waitFor(() => expect(button("folder-0")).toBeTruthy());
  const scroll = document.querySelector<HTMLElement>(".den-files-tree-scroll")!;
  scroll.scrollTop = 251 * 26; fireEvent.scroll(scroll);
  await waitFor(() => expect(button("folder-250")).toBeTruthy());
  view.expand();
  await waitFor(() => expect(scroll.scrollTop).toBe(1251 * 26));
  await findFileRow("folder-250/file-0.ts");
  expect(button("folder-250")).toBeTruthy();
});


it("publishes a filesystem change after direct input settles without another scroll", async () => {
  const fixture = treeViewFixture();
  mount(fixture);
  await findFileRow("README.md");
  const scroll = document.querySelector<HTMLElement>(".den-files-tree-scroll")!;
  const motion = scrollportMotionForViewport(scroll)!;
  motion.noteNativeInput("wheel");
  fixture.rows([treeRow(".", "directory", true), treeRow("added.txt"), treeRow("README.md")]);
  fixture.notify();
  await new Promise(resolve => setTimeout(resolve, 350));
  expect(queryFileRow("added.txt")).toBeNull();
  await findFileRow("added.txt");
});
