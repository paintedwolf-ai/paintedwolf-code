import { createEffect, onCleanup } from "solid-js";
import type { ProjectRoot } from "../../api/types.ts";
import type { ContextMenuAnchor } from "../../components/ContextMenu.tsx";
import type { FilesContextTarget } from "../commands/project-files-context-menu.ts";
import { focusRegion } from "../../shortcuts/focus-region.ts";
import { findTreePrefix } from "./tree-typeahead.ts";
import { parentDir } from "./files-tree-drag.ts";
import { treeLayoutRange } from "./files-tree-sticky.ts";
import { FILES_TREE_ROW_HEIGHT_PX } from "./project-files-tree-flat.ts";
import type { PagedTreeModel } from "./files-tree-paged-model.ts";
import type { FilesTreeSession } from "./files-tree-paged-session.ts";
type KeyboardPorts = {
  navElement(): HTMLElement | undefined;
  roots(): readonly ProjectRoot[];
  surfaceLive(): boolean;
  displayModel(): PagedTreeModel;
  sourceView(): FilesTreeSession | undefined;
  virtualViewportHeight(): number;
  advanceNavigation(): number;
  navigationGeneration(): number;
  cancelTreeReveal(): void;
  cancelScrollbarNavigation(): void;
  setNavigatingViewport(active: boolean): void;
  cancelRebase(): void;
  clearViewport(): void;
  scrollToDisplayIndex(index: number, align?: "nearest" | "start", complete?: () => void): boolean;
  reportTreeError(error: unknown): void;
  filterOpen(): boolean;
  clearFilter(): void;
  openRowMenuAt(anchor: ContextMenuAnchor, target: FilesContextTarget): void;
};
export function createFilesTreeKeyboard(ports: KeyboardPorts) {
  let focusNavigation: AbortController | undefined;
  onCleanup(() => focusNavigation?.abort());
  createEffect(() => { if (!ports.surfaceLive()) focusNavigation?.abort(); });
  const focusEntryByDisplayIndex = async (
    displayIndex: number,
    align: "nearest" | "start" = "nearest",
    end = false,
  ) => {
    // Capture the navigation root before the scroll completes.
    const nav = ports.navElement();
    if (displayIndex < 0 || !nav) return;
    const view = ports.sourceView();
    if (!view) return;
    ports.cancelTreeReveal();
    ports.cancelScrollbarNavigation();
    const abort = new AbortController(); focusNavigation = abort;
    const navigation = ports.advanceNavigation();
    ports.setNavigatingViewport(true);
    ports.cancelRebase();
    ports.clearViewport();
    try {
    let row = ports.displayModel().rowAt(displayIndex);
    if (end) {
      const root = ports.roots().at(-1);
      if (!root) return;
      const frame = await view.frameAt({ root_id: root.id, path: "." }, abort.signal, 199, Number.MAX_SAFE_INTEGER);
      displayIndex = ports.displayModel().displayIndex(frame.span.end - 1);
    } else if (row?.kind !== "entry") {
      const at = ports.displayModel().sourceIndex(displayIndex);
      await view.range(at, at + 1, abort.signal);
    }
    if (abort.signal.aborted || navigation !== ports.navigationGeneration() || view !== ports.sourceView() || nav !== ports.navElement() || !ports.surfaceLive()) return;
    row = ports.displayModel().rowAt(displayIndex);
    if (row?.kind !== "entry") {
      displayIndex = ports.displayModel().nextEntryIndex(displayIndex, displayIndex === ports.displayModel().length - 1 ? -1 : 1);
      row = ports.displayModel().rowAt(displayIndex);
    }
    if (!row || row.kind !== "entry") return;
    const entry = row.entry;
    const visibleRows = Math.min(99, Math.ceil(ports.virtualViewportHeight() / FILES_TREE_ROW_HEIGHT_PX) + 8);
    const before = align === "start" ? 0 : visibleRows;
    const model = ports.displayModel();
    const first = Math.max(0, displayIndex - before);
    const last = Math.min(model.length, displayIndex + visibleRows + 1);
    const context = treeLayoutRange(first, last, model.length);
    let missing = false;
    for (let index = context.start; index < context.end; index++) {
      if (!model.rowAt(index)) { missing = true; break; }
    }
    if (missing) {
      // Destination context uses the displayed coordinates.
      await view.range(model.sourceIndex(first), model.sourceIndex(last), abort.signal);
      if (abort.signal.aborted || navigation !== ports.navigationGeneration() || view !== ports.sourceView() || nav !== ports.navElement() || !ports.surfaceLive()) return;
      displayIndex = ports.displayModel().indexOfEntry(entry.rootId, entry.path);
      if (displayIndex < 0) return;
    }
    const selector = row.entry.isDir ? ".den-files-tree__label--dir" : ".den-files-tree__label--file";
    const focus = () => queueMicrotask(() => {
      const button = [...nav.querySelectorAll<HTMLButtonElement>(selector)].find(
        (candidate) => candidate.dataset.root === entry.rootId && candidate.dataset.path === entry.path,
      );
      button?.focus({ preventScroll: true });
    });
    if (!ports.scrollToDisplayIndex(displayIndex, align, focus)) focus();
    } finally { if (focusNavigation === abort) ports.setNavigatingViewport(false); }
  };

  const focusEntryOffset = (target: HTMLButtonElement, delta: number) => {
    const rootId = target.dataset.root ?? "";
    const path = target.dataset.path ?? ".";
    const model = ports.displayModel();
    const displayIndex = model.indexOfEntry(rootId, path);
    if (displayIndex < 0) return;
    const next = model.nextEntryIndex(displayIndex, delta < 0 ? -1 : 1);
    if (next < 0) return;
    void focusEntryByDisplayIndex(next).catch(ports.reportTreeError);
  };

  let typeahead = "";
  let typedAt = 0;
  let typeaheadRequest: AbortController | undefined;
  onCleanup(() => typeaheadRequest?.abort());
  const onKeyDown = (e: KeyboardEvent) => {
    if (ports.filterOpen() && e.key === "Escape") {
      ports.clearFilter();
      e.preventDefault();
      e.stopPropagation();
      return;
    }
    if (!ports.navElement()) return;
    const target =
      e.target instanceof HTMLElement
        ? e.target.closest<HTMLButtonElement>(".den-files-tree__label")
        : null;
    if (!target) return;
    const isDir = target.classList.contains("den-files-tree__label--dir");
    const expanded = target.getAttribute("aria-expanded") === "true";
    const rootId = target.dataset.root ?? "";
    const path = target.dataset.path ?? ".";
    const model = ports.displayModel();
    typeaheadRequest?.abort();
    if (e.key.length === 1 && e.key !== " " && !e.ctrlKey && !e.metaKey && !e.altKey && !e.isComposing) {
      e.preventDefault();
      const now = performance.now();
      typeahead = now - typedAt < 750 ? typeahead + e.key : e.key;
      typedAt = now;
      let repeated = true;
      for (const key of typeahead) {
        if (key.toLocaleLowerCase() !== e.key.toLocaleLowerCase()) {
          repeated = false;
          break;
        }
      }
      const prefix = repeated ? e.key : typeahead;
      const view = ports.sourceView();
      if (!view) return;
      const abort = new AbortController(); typeaheadRequest = abort;
      const current = model.sourceIndex(model.indexOfEntry(rootId, path));
      void findTreePrefix({ prefix, from: current + (prefix.length === 1 ? 1 : 0),
        count: view.presentation().state?.extent.rows ?? 0, signal: abort.signal,
        frame: (offset, limit, signal) => view.frame(offset, limit, signal),
      }).then(index => {
        if (index == null || abort.signal.aborted || view !== ports.sourceView() || document.activeElement !== target) return;
        return focusEntryByDisplayIndex(ports.displayModel().displayIndex(index));
      }).catch(error => { if (!abort.signal.aborted) ports.reportTreeError(error); });
      return;
    }
    if (
      (e.key === "F10" && e.shiftKey) ||
      e.key === "ContextMenu"
    ) {
      const row = target.closest<HTMLElement>("[data-files-ctx='tree-row']");
      if (!row) return;
      e.preventDefault();
      e.stopPropagation();
      const rect = target.getBoundingClientRect();
      ports.openRowMenuAt(
        { x: rect.left, y: rect.bottom },
        {
          surface: "tree-row",
          rootId,
          path,
          name: row.dataset.name ?? path,
          isDir,
          isRoot: row.dataset.isroot === "true",
          deleted: row.dataset.deleted === "true",
        },
      );
      return;
    }
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        focusEntryOffset(target, 1);
        break;
      case "ArrowUp":
        e.preventDefault();
        focusEntryOffset(target, -1);
        break;
      case "Home":
        e.preventDefault();
        void focusEntryByDisplayIndex(model.firstEntryIndex()).catch(ports.reportTreeError);
        break;
      case "End":
        e.preventDefault();
        void focusEntryByDisplayIndex(model.lastEntryIndex(), "nearest", true).catch(ports.reportTreeError);
        break;
      case "Enter":
        if (!isDir) {
          e.preventDefault();
          target.click();
          focusRegion("files");
        }
        break;
      case "ArrowRight":
        e.preventDefault();
        if (isDir && !expanded) target.click();
        else if (isDir) focusEntryOffset(target, 1);
        else focusRegion("files");
        break;
      case "ArrowLeft":
        e.preventDefault();
        if (isDir && expanded) {
          target.click();
          break;
        }
        if (rootId) {
          const parent = parentDir(path);
          const view = ports.sourceView();
          if (view) void view.locate({ root_id: rootId, path: parent }).then(index => {
            if (view === ports.sourceView() && ports.surfaceLive() && index >= 0) return focusEntryByDisplayIndex(index);
          }).catch(ports.reportTreeError);
        }
        break;
    }
  };

  return { focusEntryByDisplayIndex, onKeyDown, cancelNavigation: () => focusNavigation?.abort(),
    clearTypeahead: () => { typeaheadRequest?.abort(); typeahead = ""; },
  };
}
