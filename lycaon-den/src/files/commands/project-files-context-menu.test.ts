// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { seedStockFrame } from "../../contributions/stock-frame-test.ts";
import { resetContributionStoreForTest } from "../../contributions/contribution-store.ts";
import {
  filesContextMenuItems,
  resolveFilesContextTarget,
} from "./project-files-context-menu.ts";
import type { ContextMenuItem } from "../../components/ContextMenu.tsx";
import { bufferTabMenuItems } from "../tabs/project-files-buffer-menu.ts";
import {
  registerCommandHandler,
  resetDispatcherForTests,
} from "../../shortcuts/dispatcher.ts";
import { fileBufferKey } from "../components/project-files-model.ts";

const BUFFER_KEY = fileBufferKey("r1", "a.ts");

const actions = {
  newFile: vi.fn(),
  newFolder: vi.fn(),
  rename: vi.fn(),
  moveTo: vi.fn(),
  duplicate: vi.fn(),
  moveToTrash: vi.fn(),
  copyPath: vi.fn(),
  copyRelativePath: vi.fn(),
  addToChat: vi.fn(),
  openInTarget: () => ({ absolutePath: "/repo/a.ts", projectRoots: ["/repo"], entryKind: "file" as const }),
  collapseAllFolders: vi.fn(),
  expandAllFolders: vi.fn(),
  closeTab: vi.fn(),
  closeOtherTabs: vi.fn(),
  closeToTheRight: vi.fn(),
  closeSavedTabs: vi.fn(),
  closeAllTabs: vi.fn(),
  keepOpenTab: vi.fn(),
  tabIsPreview: vi.fn(() => false),
  togglePinTab: vi.fn(),
  tabIsPinned: vi.fn(() => false),
  openFilesList: vi.fn(),
  renameTab: vi.fn(),
  trashTab: vi.fn(),
  copyTabPath: vi.fn(),
  copyTabRelativePath: vi.fn(),
  addTabToChat: vi.fn(),
  copyPathLine: vi.fn(),
  copyRelativePathLine: vi.fn(),
  addPathLineToChat: vi.fn(),
  revealInTree: vi.fn(),
  tabRevealInTree: () => ({ onSelect: vi.fn() }),
  infoCopyPath: vi.fn(),
  infoCopyRelativePath: vi.fn(),
  infoAddToChat: vi.fn(),
  editorCopyPath: vi.fn(),
  editorCopyRelativePath: vi.fn(),
  editorAddSelectionToChat: vi.fn(),
  editorSelectionVerb: vi.fn(),
  editorSymbol: {
    target: {
      kind: "symbol" as const,
      symbol: "resolveProjectRoot",
      from: 100,
      to: 118,
      source: "pointer" as const,
    },
    rename: vi.fn(),
    goToDefinition: vi.fn(),
  },
  editorHasFinding: false,
  addSelectionShortcutHint: "⌘L",
};

beforeEach(() => seedStockFrame());
afterEach(() => { resetDispatcherForTests(); resetContributionStoreForTest(); });

function rowEl(attrs: Record<string, string>) {
  const el = document.createElement("div");
  el.setAttribute("data-files-ctx", "tree-row");
  for (const [k, v] of Object.entries(attrs)) el.dataset[k] = v;
  document.body.appendChild(el);
  return el;
}

function flattenMenu(items: readonly ContextMenuItem[]): ContextMenuItem[] {
  return items.flatMap((item) => [
    item,
    ...(item.submenu ? flattenMenu(item.submenu) : []),
  ]);
}

describe("resolveFilesContextTarget", () => {
  it("maps a tree row from data attributes", () => {
    const el = rowEl({
      root: "r1",
      path: "src/main.ts",
      name: "main.ts",
      isdir: "false",
      isroot: "false",
    });
    expect(resolveFilesContextTarget(el)).toEqual({
      surface: "tree-row",
      rootId: "r1",
      path: "src/main.ts",
      name: "main.ts",
      isDir: false,
      isRoot: false,
      deleted: false,
    });
    el.remove();
  });

  it("maps tree background", () => {
    const bg = document.createElement("nav");
    bg.setAttribute("data-files-ctx", "tree-background");
    document.body.appendChild(bg);
    expect(resolveFilesContextTarget(bg)).toEqual({ surface: "tree-background" });
    bg.remove();
  });

  it("maps buffer tab without requiring activation", () => {
    const tab = document.createElement("div");
    tab.dataset.filesCtx = "tab";
    tab.dataset.key = BUFFER_KEY;
    tab.dataset.root = "r1";
    tab.dataset.path = "a.ts";
    tab.dataset.name = "a.ts";
    expect(resolveFilesContextTarget(tab)).toEqual({
      surface: "tab",
      bufferKey: BUFFER_KEY,
      rootId: "r1",
      path: "a.ts",
      name: "a.ts",
    });
  });

  it("maps tab strip empty area", () => {
    const strip = document.createElement("div");
    strip.dataset.filesCtx = "tab-strip";
    expect(resolveFilesContextTarget(strip)).toEqual({ surface: "tab-strip" });
  });

  it("reads the line from the gutter element's rendered number", () => {
    const gutters = document.createElement("div");
    gutters.dataset.filesCtx = "gutter";
    const el = document.createElement("div");
    el.className = "cm-gutterElement";
    el.textContent = "42";
    gutters.appendChild(el);
    expect(resolveFilesContextTarget(el)).toEqual({
      surface: "gutter",
      line: 42,
    });
  });

  it("maps a non-numeric gutter element to no line", () => {
    const gutters = document.createElement("div");
    gutters.dataset.filesCtx = "gutter";
    const fold = document.createElement("div");
    fold.className = "cm-gutterElement";
    fold.textContent = "›";
    gutters.appendChild(fold);
    expect(resolveFilesContextTarget(fold)).toEqual({
      surface: "gutter",
      line: null,
    });
  });

  it("maps breadcrumb folder segment", () => {
    const crumb = document.createElement("button");
    crumb.dataset.filesCtx = "breadcrumb";
    crumb.dataset.root = "r1";
    crumb.dataset.path = "src";
    crumb.dataset.isdir = "true";
    expect(resolveFilesContextTarget(crumb)).toEqual({
      surface: "breadcrumb",
      rootId: "r1",
      path: "src",
      isDir: true,
    });
  });

  it("returns no-menu for filter and rename inputs", () => {
    const filter = document.createElement("input");
    filter.dataset.testid = "files-tree-filter";
    expect(resolveFilesContextTarget(filter)).toEqual({ surface: "no-menu" });

    const rename = document.createElement("input");
    rename.dataset.testid = "files-tree-rename-input";
    expect(resolveFilesContextTarget(rename)).toEqual({ surface: "no-menu" });
  });

  it("returns no-menu for status bar sentinel", () => {
    const status = document.createElement("div");
    status.dataset.filesCtx = "no-menu";
    expect(resolveFilesContextTarget(status)).toEqual({ surface: "no-menu" });
  });

  it("falls through for unregistered surfaces", () => {
    expect(resolveFilesContextTarget(document.createElement("p"))).toEqual({
      surface: "fallback",
    });
  });
});

describe("tree file AI actions", () => {
  it("offers Explain in review only for a file and keeps its root-qualified address", () => {
    const explainFile = vi.fn();
    const items = filesContextMenuItems(
      { surface: "tree-row", rootId: "root-a", path: "src/main.ts", name: "main.ts", isDir: false, isRoot: false },
      { ...actions, explainFile },
    ) ?? [];
    const item = items.find((entry) => entry.testId === "files-ctx-explain-file");
    expect(item?.label).toBe("Explain in review");
    item?.onSelect?.();
    expect(explainFile).toHaveBeenCalledWith("root-a", "src/main.ts");
  });
});

describe("filesContextMenuItems", () => {
  it("keeps the editor top level within its menu budget", () => {
    const el = document.createElement("div");
    el.setAttribute("contenteditable", "true");
    const items = filesContextMenuItems(
      {
        surface: "editor",
        hasSelection: false,
        textEdit: {
          kind: "editable",
          snapshot: {
            el,
            start: 0,
            end: 0,
            range: null,
            readOnly: false,
          },
        },
      },
      actions,
    )!;
    expect(items.filter((item) => !item.separator).length).toBeLessThanOrEqual(12);
    expect(items.find((item) => item.testId === "files-ctx-editor-ask")?.submenu)
      .toHaveLength(5);
    expect(items.find((item) => item.testId === "files-ctx-editor-copy-path"))
      .toBeTruthy();
    expect(
      items.find(
        (item) => item.testId === "files-ctx-editor-copy-relative-path",
      ),
    ).toBeTruthy();
  });

  it("returns null for fallback", () => {
    expect(filesContextMenuItems({ surface: "fallback" }, actions)).toBeNull();
  });

  it("returns empty for no-menu", () => {
    expect(filesContextMenuItems({ surface: "no-menu" }, actions)).toEqual([]);
  });

  it("opens a changed file without also revealing it in the tree", () => {
    const openFile = vi.fn();
    const revealInTree = vi.fn();
    const items = filesContextMenuItems(
      {
        surface: "changed-file",
        rootId: "r1",
        path: "src/main.ts",
        name: "main.ts",
      },
      { ...actions, openFile, revealInTree },
    )!;

    items.find((item) => item.testId === "files-ctx-changed-open")?.onSelect!();

    expect(openFile).toHaveBeenCalledWith("r1", "src/main.ts");
    expect(revealInTree).not.toHaveBeenCalled();
  });

  it("omits Open when unavailable and keeps the explicit reveal action", () => {
    const revealInTree = vi.fn();
    const items = filesContextMenuItems(
      {
        surface: "changed-file",
        rootId: "r1",
        path: "src/main.ts",
        name: "main.ts",
      },
      { ...actions, revealInTree },
    )!;

    expect(items.some((item) => item.testId === "files-ctx-changed-open")).toBe(false);
    items.find((item) => item.testId === "files-ctx-changed-reveal-tree")?.onSelect!();

    expect(revealInTree).toHaveBeenCalledWith("r1", "src/main.ts");
  });

  it("lists background actions", () => {
    const items = filesContextMenuItems({ surface: "tree-background" }, actions)!;
    expect(items.map((i) => i.label)).toEqual([
      "New file",
      "New folder",
      "Open in",
      "Collapse all",
      "Expand all",
    ]);
  });

  it("lists row actions for a file", () => {
    const items = filesContextMenuItems(
      {
        surface: "tree-row",
        rootId: "r1",
        path: "a.ts",
        name: "a.ts",
        isDir: false,
        isRoot: false,
      },
      actions,
    )!;
    expect(items.some((i) => i.label === "Rename")).toBe(true);
    expect(items.some((i) => i.label === "Move to…")).toBe(true);
    expect(items.some((i) => i.label === "Move to trash")).toBe(true);
    expect(items.some((i) => i.label === "New file")).toBe(false);
  });

  it("opens the destination picker for a tree row", () => {
    const moveTo = vi.fn();
    const items = filesContextMenuItems(
      {
        surface: "tree-row",
        rootId: "r1",
        path: "src/a.ts",
        name: "a.ts",
        isDir: false,
        isRoot: false,
      },
      { ...actions, moveTo },
    )!;
    items.find((item) => item.testId === "files-ctx-move-to")?.onSelect?.();
    expect(moveTo).toHaveBeenCalledWith("r1", "src/a.ts", false);
  });

  it("opens a tree file in a new window when the desktop action is available", () => {
    const openFileInNewWindow = vi.fn();
    const items = filesContextMenuItems(
      {
        surface: "tree-row",
        rootId: "r1",
        path: "a.ts",
        name: "a.ts",
        isDir: false,
        isRoot: false,
      },
      { ...actions, openFileInNewWindow },
    )!;
    const item = items.find((entry) => entry.testId === "files-ctx-open-window");
    expect(item?.label).toBe("Open in new window");
    item?.onSelect!();
    expect(openFileInNewWindow).toHaveBeenCalledWith("r1", "a.ts", "a.ts");
  });

  it("includes folder create actions for directories", () => {
    const items = filesContextMenuItems(
      {
        surface: "tree-row",
        rootId: "r1",
        path: "src",
        name: "src",
        isDir: true,
        isRoot: false,
      },
      actions,
    )!;
    expect(items.map((i) => i.label)).toContain("New file");
    expect(items.map((i) => i.label)).toContain("New folder");
    expect(items.map((i) => i.label)).toContain("Collapse all");
    expect(items.map((i) => i.label)).toContain("Expand all");
  });

  it("Add to chat on a tree folder passes isDir true", () => {
    actions.addToChat.mockClear();
    const items = filesContextMenuItems(
      {
        surface: "tree-row",
        rootId: "r1",
        path: "src",
        name: "src",
        isDir: true,
        isRoot: false,
      },
      actions,
    )!;
    items.find((i) => i.testId === "files-ctx-add-to-chat")?.onSelect!();
    expect(actions.addToChat).toHaveBeenCalledWith("r1", "src", true);
  });

  it("Add to chat on a breadcrumb folder passes isDir true", () => {
    actions.addToChat.mockClear();
    const items = filesContextMenuItems(
      {
        surface: "breadcrumb",
        rootId: "r1",
        path: "src",
        isDir: true,
      },
      { ...actions, editorRevealInTree: { onSelect: vi.fn() } },
    )!;
    expect(items.map((i) => i.label)).toEqual([
      "Reveal in tree",
      "Open in",
      "Copy path",
      "Copy relative path",
      "Add to chat",
    ]);
    items.find((i) => i.testId === "files-ctx-crumb-add-to-chat")?.onSelect!();
    expect(actions.addToChat).toHaveBeenCalledWith("r1", "src", true);
  });

  it("groups tab-closing commands and keeps copy commands flat", () => {
    const items = filesContextMenuItems(
      {
        surface: "tab",
        bufferKey: BUFFER_KEY,
        rootId: "r1",
        path: "a.ts",
        name: "a.ts",
      },
      actions,
    )!;
    expect(items.map((i) => i.label).filter(Boolean)).toEqual([
      "Close",
      "Pin",
      "Reveal in tree",
      "Open in",
      "Copy path",
      "Copy relative path",
      "Add to chat",
      "Rename",
      "Move to trash",
    ]);
    expect(items[0]?.submenu?.map((item) => item.label)).toEqual([
      "Close current",
      "Close others",
      "Close to the right",
      "Close saved",
      "Close all",
    ]);
  });

  it("lists tab strip items only", () => {
    const items = filesContextMenuItems({ surface: "tab-strip" }, actions)!;
    expect(items.map((i) => i.label)).toEqual([
      "Open files…",
      "Close saved",
      "Close all",
    ]);
  });

  it("adds Open in new window on the tab strip when the action is provided", () => {
    const openInNewWindow = vi.fn();
    const items = filesContextMenuItems(
      { surface: "tab-strip" },
      { ...actions, openInNewWindow },
    )!;
    expect(items.map((i) => i.label)).toEqual([
      "Open files…",
      "Open in new window",
      "Close saved",
      "Close all",
    ]);
    items.find((i) => i.testId === "files-ctx-strip-open-window")?.onSelect!();
    expect(openInNewWindow).toHaveBeenCalledOnce();
  });

  it("adds Open in new window for an individual file tab", () => {
    const openInNewWindow = vi.fn();
    const items = filesContextMenuItems(
      {
        surface: "tab",
        bufferKey: BUFFER_KEY,
        rootId: "r1",
        path: "a.ts",
        name: "a.ts",
      },
      { ...actions, openInNewWindow },
    )!;
    items.find((item) => item.testId === "files-ctx-tab-open-window")?.onSelect!();
    expect(openInNewWindow).toHaveBeenCalledWith(BUFFER_KEY);
  });

  it("keeps shared path actions adjacent and ordered across file surfaces", () => {
    const targets = [
      { surface: "tree-row", rootId: "r1", path: "a.ts", name: "a.ts", isDir: false, isRoot: false },
      { surface: "tab", bufferKey: BUFFER_KEY, rootId: "r1", path: "a.ts", name: "a.ts" },
      { surface: "info-card" },
      { surface: "image-viewer" },
    ] as const;
    const pathLabels = filesContextMenuItems({ surface: "image-viewer" }, actions)!.map((item) => item.label);
    for (const target of targets) {
      const labels = filesContextMenuItems(target, actions)!.map((item) => item.label);
      const start = labels.indexOf(pathLabels[0]);
      expect(start).toBeGreaterThanOrEqual(0);
      expect(labels.slice(start, start + pathLabels.length)).toEqual(pathLabels);
    }
  });

  it("lists gutter items", () => {
    const items = filesContextMenuItems({ surface: "gutter", line: 4 }, actions)!;
    expect(items.map((i) => i.label)).toEqual([
      "Open in",
      "Copy path:line",
      "Copy relative path:line",
      "Add to chat",
    ]);
  });

  it("lists info-card actions from the shared descriptor", () => {
    const items = filesContextMenuItems({ surface: "info-card" }, actions)!;
    expect(items.find((i) => i.label === "Open in")?.submenu?.some((i) => i.testId === "open-in-editor")).toBe(true);
    expect(items.some((i) => i.label === "Copy path")).toBe(true);
  });

  it("lists image-viewer actions from the shared descriptor", () => {
    const items = filesContextMenuItems({ surface: "image-viewer" }, actions)!;
    expect(items.map((i) => i.label)).toEqual([
      expect.any(String),
      "Copy path",
      "Copy relative path",
      "Add to chat",
    ]);
  });

  it("resolves info-card and image-viewer surfaces", () => {
    const info = document.createElement("div");
    info.setAttribute("data-files-ctx", "info-card");
    document.body.appendChild(info);
    expect(resolveFilesContextTarget(info)).toEqual({ surface: "info-card" });
    info.remove();

    const image = document.createElement("div");
    image.setAttribute("data-files-ctx", "image-viewer");
    document.body.appendChild(image);
    expect(resolveFilesContextTarget(image)).toEqual({ surface: "image-viewer" });
    image.remove();
  });

  it("routes renameTab through lifecycle rename action", () => {
    actions.renameTab.mockClear();
    const tabItems = bufferTabMenuItems({
      close: vi.fn(),
      closeOthers: vi.fn(),
      closeToTheRight: vi.fn(),
      closeSaved: vi.fn(),
      closeAll: vi.fn(),
      pin: vi.fn(),
      pinned: false,
      copyPath: vi.fn(),
      copyRelativePath: vi.fn(),
      addToChat: vi.fn(),
      rename: actions.renameTab,
      moveToTrash: vi.fn(),
    });
    tabItems.find((i) => i.label === "Rename")?.onSelect!();
    expect(actions.renameTab).toHaveBeenCalledTimes(1);
  });

  it("claims .cm-content as the editor surface", () => {
    const host = document.createElement("div");
    const content = document.createElement("div");
    content.className = "cm-content";
    content.setAttribute("contenteditable", "true");
    host.appendChild(content);
    document.body.appendChild(host);
    content.focus();
    const sel = window.getSelection();
    sel?.removeAllRanges();
    const range = document.createRange();
    content.textContent = "hello world";
    range.selectNodeContents(content);
    sel?.addRange(range);

    const target = resolveFilesContextTarget(content);
    expect(target.surface).toBe("editor");
    if (target.surface === "editor") {
      expect(target.hasSelection).toBe(true);
    }
    host.remove();
  });

  it("keeps read-only .cm-content on the editor surface", () => {
    const content = document.createElement("div");
    content.className = "cm-content";
    content.setAttribute("contenteditable", "false");

    const target = resolveFilesContextTarget(content);
    expect(target.surface).toBe("editor");
    if (target.surface === "editor") {
      expect(target.textEdit.kind).toBe("editable");
      if (target.textEdit.kind === "editable") {
        expect(target.textEdit.snapshot.readOnly).toBe(true);
      }
    }
  });

  it("lists text-edit items plus selection verbs on the editor surface", () => {
    const el = document.createElement("div");
    el.setAttribute("contenteditable", "true");
    el.textContent = "abc";
    document.body.appendChild(el);
    const items = filesContextMenuItems(
      {
        surface: "editor",
        hasSelection: true,
        textEdit: {
          kind: "editable",
          snapshot: {
            el,
            start: 0,
            end: 3,
            range: null,
            readOnly: false,
          },
        },
      },
      actions,
    )!;
    const labels = flattenMenu(items).map((i) => i.label).filter(Boolean);
    expect(labels).toContain("Add to chat");
    expect(items.find((item) => item.testId === "files-ctx-editor-add-to-chat")?.shortcut).toBe("⌘L");
    expect(labels).toContain("Inline edit");
    expect(labels).toContain("Explain in context");
    expect(labels).toContain("Give me the gist");
    expect(labels).toContain("Add test");
    expect(labels).toContain("Rewrite structurally");
    expect(labels).not.toContain("Improve selection");
    expect(labels).not.toContain("Fix this finding");
    el.remove();
  });

  it("opens the inline-edit prompt from the editor menu", () => {
    const openInlineEdit = vi.fn();
    registerCommandHandler("editor.inlineEdit", openInlineEdit);
    const el = document.createElement("div");
    el.setAttribute("contenteditable", "true");
    document.body.appendChild(el);
    const items = filesContextMenuItems(
      {
        surface: "editor",
        hasSelection: true,
        textEdit: {
          kind: "editable",
          snapshot: {
            el,
            start: 0,
            end: 0,
            range: null,
            readOnly: false,
          },
        },
      },
      actions,
    )!;
    items.find((item) => item.testId === "files-ctx-editor-inline-edit")?.onSelect!();
    expect(openInlineEdit).toHaveBeenCalledOnce();
    el.remove();
  });

  it("keeps read-only editor actions observational", () => {
    const el = document.createElement("div");
    el.setAttribute("contenteditable", "true");
    document.body.appendChild(el);
    const items = filesContextMenuItems(
      {
        surface: "editor",
        hasSelection: true,
        textEdit: {
          kind: "editable",
          snapshot: {
            el,
            start: 0,
            end: 1,
            range: null,
            readOnly: true,
          },
        },
      },
      actions,
    )!;
    const labels = items.map((i) => i.label).filter(Boolean);
    expect(labels).not.toContain("Cut");
    expect(labels).not.toContain("Paste");
    expect(labels).not.toContain("Inline edit");
    expect(labels).not.toContain("Ask about selection");
    expect(labels).not.toContain("Rename “resolveProjectRoot”…");
    expect(labels).not.toContain("Go to definition of “resolveProjectRoot”");
    expect(labels).toContain("Copy");
    expect(labels).toContain("Select all");
    expect(labels).toContain("Add to chat");
    expect(items.find((item) => item.testId === "files-ctx-editor-add-to-chat")?.shortcut).toBe("⌘L");
    el.remove();
  });

  it("keeps selection verbs enabled when the selection is empty (scope falls back)", () => {
    const el = document.createElement("div");
    el.setAttribute("contenteditable", "true");
    document.body.appendChild(el);
    const items = filesContextMenuItems(
      {
        surface: "editor",
        hasSelection: false,
        textEdit: {
          kind: "editable",
          snapshot: {
            el,
            start: 0,
            end: 0,
            range: null,
            readOnly: false,
          },
        },
      },
      actions,
    )!;
    const allItems = flattenMenu(items);
    const explain = allItems.find((i) => i.testId === "files-ctx-editor-explain");
    const documentVerb = allItems.find((i) => i.testId === "files-ctx-editor-document");
    const addTest = allItems.find((i) => i.testId === "files-ctx-editor-add-test");
    expect(explain?.disabled).not.toBe(true);
    expect(documentVerb?.disabled).not.toBe(true);
    expect(addTest?.disabled).not.toBe(true);
    expect(allItems.find((i) => i.testId === "files-ctx-editor-improve")).toBeUndefined();
    expect(
      items.find((i) => i.testId === "files-ctx-editor-add-to-chat")?.disabled,
    ).not.toBe(true);
    const goToDef = items.find(
      (i) => i.testId === "files-ctx-editor-go-to-definition",
    );
    expect(goToDef?.label).toBe(
      "Go to definition of “resolveProjectRoot”",
    );
    const goIdx = items.findIndex(
      (i) => i.testId === "files-ctx-editor-go-to-definition",
    );
    expect(goIdx).toBeGreaterThan(-1);
    expect(
      items.find((i) => i.testId === "files-ctx-editor-ask")?.submenu,
    ).toContain(explain);
    el.remove();
  });

  it("names an available pointer symbol in its commands", () => {
    const el = document.createElement("div");
    el.setAttribute("contenteditable", "true");
    document.body.appendChild(el);
    const rename = vi.fn();
    const goToDefinition = vi.fn();
    const items = filesContextMenuItems(
      {
        surface: "editor",
        hasSelection: false,
        textEdit: {
          kind: "editable",
          snapshot: {
            el,
            start: 0,
            end: 0,
            range: null,
            readOnly: false,
          },
        },
      },
      {
        ...actions,
        editorSymbol: {
          ...actions.editorSymbol,
          rename,
          goToDefinition,
        },
      },
    )!;
    expect(
      items.find((item) => item.testId === "files-ctx-editor-rename"),
    ).toMatchObject({
      label: "Rename “resolveProjectRoot”…",
    });
    expect(
      items.find((item) => item.testId === "files-ctx-editor-rename")?.disabled,
    ).not.toBe(true);
    items
      .find((item) => item.testId === "files-ctx-editor-rename")
      ?.onSelect!();
    items
      .find((item) => item.testId === "files-ctx-editor-go-to-definition")
      ?.onSelect!();
    expect(rename).toHaveBeenCalledOnce();
    expect(goToDefinition).toHaveBeenCalledOnce();
    el.remove();
  });

  it("omits symbol actions when a broad selection has no single target", () => {
    const el = document.createElement("div");
    el.setAttribute("contenteditable", "true");
    document.body.appendChild(el);
    const items = filesContextMenuItems(
      {
        surface: "editor",
        hasSelection: true,
        textEdit: {
          kind: "editable",
          snapshot: {
            el,
            start: 0,
            end: 8,
            range: null,
            readOnly: false,
          },
        },
      },
      {
        ...actions,
        editorSymbol: {
          ...actions.editorSymbol,
          target: { kind: "missing", reason: "range" },
        },
      },
    )!;
    expect(items.find((item) => item.testId === "files-ctx-editor-rename"))
      .toBeUndefined();
    expect(
      items.find((item) => item.testId === "files-ctx-editor-go-to-definition"),
    ).toBeUndefined();
    el.remove();
  });

  it("omits Fix this finding unless a finding annotates the range", () => {
    const el = document.createElement("div");
    el.setAttribute("contenteditable", "true");
    document.body.appendChild(el);
    const withFinding = filesContextMenuItems(
      {
        surface: "editor",
        hasSelection: true,
        textEdit: {
          kind: "editable",
          snapshot: {
            el,
            start: 0,
            end: 1,
            range: null,
            readOnly: false,
          },
        },
      },
      { ...actions, editorHasFinding: true },
    )!;
    expect(
      flattenMenu(withFinding).find(
        (i) => i.testId === "files-ctx-editor-fix-finding",
      ),
    ).toBeTruthy();
    const without = filesContextMenuItems(
      {
        surface: "editor",
        hasSelection: true,
        textEdit: {
          kind: "editable",
          snapshot: {
            el,
            start: 0,
            end: 1,
            range: null,
            readOnly: false,
          },
        },
      },
      { ...actions, editorHasFinding: false },
    )!;
    expect(
      flattenMenu(without).find(
        (i) => i.testId === "files-ctx-editor-fix-finding",
      ),
    ).toBeUndefined();
    el.remove();
  });

  it("omits rows whose actions are unavailable", () => {
    const changed = filesContextMenuItems(
      {
        surface: "changed-file",
        rootId: "r1",
        path: "a.ts",
        name: "a.ts",
      },
      actions,
    )!;
    expect(changed.find((item) => item.testId === "files-ctx-changed-revert"))
      .toBeUndefined();
    expect(changed.find((item) => item.testId === "files-ctx-changed-sep"))
      .toBeUndefined();

    const el = document.createElement("div");
    el.setAttribute("contenteditable", "true");
    document.body.appendChild(el);
    const editor = filesContextMenuItems(
      {
        surface: "editor",
        hasSelection: true,
        textEdit: {
          kind: "editable",
          snapshot: {
            el,
            start: 0,
            end: 1,
            range: null,
            readOnly: false,
          },
        },
      },
      {
        ...actions,
        editorAddSelectionToChat: undefined,
        editorCopyPath: undefined,
        editorCopyRelativePath: undefined,
        editorSelectionVerb: undefined,
      },
    )!;
    expect(editor.find((item) => item.testId === "files-ctx-editor-add-to-chat"))
      .toBeUndefined();
    expect(editor.find((item) => item.testId === "files-ctx-editor-copy-path"))
      .toBeUndefined();
    expect(editor.find((item) => item.testId === "files-ctx-editor-ask"))
      .toBeUndefined();
    el.remove();
  });
});


it("reveals the addressed tab, breadcrumb, and review entry in the tree", () => {
  const reveal = vi.fn();
  const targets = [
    { surface: "tab", rootId: "r1", path: "src/file.ts", name: "file.ts", bufferKey: BUFFER_KEY },
    { surface: "breadcrumb", rootId: "r1", path: "src/file.ts", isDir: false },
    { surface: "changed-file", rootId: "r1", path: "src/file.ts", name: "file.ts" },
  ] as const;
  for (const target of targets) {
    reveal.mockClear();
    const item = filesContextMenuItems(target, { ...actions, revealInTree: reveal, tabRevealInTree: () => ({ onSelect: () => reveal(target.rootId, target.path) }), editorRevealInTree: { onSelect: vi.fn() } })?.find((item) => item.label === "Reveal in tree");
    expect(item).toBeDefined();
    item?.onSelect?.();
    expect(reveal).toHaveBeenCalledWith("r1", "src/file.ts", ...(target.surface === "breadcrumb" ? [false] : []));
  }
});

it("offers explicit reveal from the gutter, image, and file-info surfaces", () => {
  const reveal = vi.fn();
  const targets = [{ surface: "gutter", line: 1 }, { surface: "image-viewer" }, { surface: "info-card" }] as const;
  for (const target of targets) {
    reveal.mockClear();
    const item = filesContextMenuItems(target, { ...actions, editorRevealInTree: { onSelect: reveal } })?.find((item) => item.label === "Reveal in tree");
    expect(item).toBeDefined();
    item?.onSelect?.();
    expect(reveal).toHaveBeenCalledOnce();
  }
});

it.each([true, false])("omits tree reveal for an editor breadcrumb outside the primary workspace (folder: %s)", (isDir) => {
  const items = filesContextMenuItems({ surface: "breadcrumb", rootId: "r1", path: "src/file.ts", isDir }, { ...actions, editorRevealInTree: undefined });
  expect(items?.some((item) => item.testId === "files-ctx-crumb-reveal-tree")).toBe(false);
});


it("keeps Reveal in tree disabled across tab and editor surfaces without a tree item", () => {
  const disabled = { disabled: true as const, description: "This tab has no item in the file tree." };
  const reveal = vi.fn();
  const targets = [
    { surface: "tab", rootId: "", path: "", name: "Tool call", bufferKey: BUFFER_KEY },
    { surface: "gutter", line: 1 },
    { surface: "image-viewer" },
    { surface: "info-card" },
    { surface: "breadcrumb", rootId: "r1", path: "src/file.ts", isDir: false },
  ] as const;
  for (const target of targets) {
    const item = filesContextMenuItems(target, {
      ...actions, revealInTree: reveal, tabRevealInTree: () => disabled, editorRevealInTree: disabled,
    })?.find((entry) => entry.label === "Reveal in tree");
    expect(item).toMatchObject({ disabled: true, description: disabled.description });
    item?.onSelect?.();
  }
  expect(reveal).not.toHaveBeenCalled();
});


it("resolves a tab by buffer identity when it has no file path", () => {
  const tab = document.createElement("div");
  tab.dataset.filesCtx = "tab";
  tab.dataset.key = "walk:review";
  tab.dataset.name = "Review changes";
  expect(resolveFilesContextTarget(tab)).toEqual({
    surface: "tab", bufferKey: "walk:review", rootId: "", path: "", name: "Review changes",
  });
});
