
import { createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@solidjs/testing-library";

import { treeRow, treeViewFixture } from "../../test/source-tree-view-fixture.ts";

import { receiveSourceViewEvent } from "../../ui/paged-view/source-view-session.ts";

import { findFileRow, getFileRow } from "../../test/files-tree-queries.ts";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";

import { installFilesTreeCleanup, mount, button, echoPath, projectNotices } from "./files-tree-test-fixture.tsx";
installFilesTreeCleanup();

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
