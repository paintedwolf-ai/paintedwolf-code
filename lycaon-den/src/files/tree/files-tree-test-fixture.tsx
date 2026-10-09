import { resetEditorPrefsForTests } from "../../settings/editor/editor-prefs.ts";
import { createSignal, type ComponentProps } from "solid-js";
import { afterEach, vi } from "vitest";
import { cleanup, render } from "@solidjs/testing-library";
import { FilesTree } from "./FilesTree.tsx";
import type { FilesTreeEntrySelection, FilesTreeFolderScope, FilesTreeHandle } from "./files-tree-context.ts";
import { treeViewFixture, TREE_ROOTS } from "../../test/source-tree-view-fixture.ts";

import { resetFilesTreeViewStateForTests } from "./files-tree-view-state.ts";

import { createNoticeStore } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";

export const projectNotices = (store: ReturnType<typeof createNoticeStore>, projectId: string) =>
  selectProjectNoticeGroups(store.index()).find(group => group.projectId === projectId)?.notices ?? [];

export function installFilesTreeCleanup() { afterEach(() => { cleanup(); resetEditorPrefsForTests(); resetFilesTreeViewStateForTests(); vi.useRealTimers(); }); }
export const echoPath = async (_kind: string, _root: string, path: string) => path;
export const queryButton = (path: string) => document.querySelector<HTMLButtonElement>(`.den-files-tree__label[data-path="${path}"]`);
export function button(path: string): HTMLButtonElement {
  const result = queryButton(path);
  if (!result) throw new Error(`Missing tree button: ${path}`);
  return result;
}
export function mount(fixture = treeViewFixture(), overrides: Partial<ComponentProps<typeof FilesTree>> = {}) {
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
