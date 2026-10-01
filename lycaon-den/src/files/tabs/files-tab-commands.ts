import { createSignal } from "solid-js";
import { popClosedBuffer } from "./closed-buffer-ring.ts";
import { putBufferViewState, rememberClosedBufferForProject, scheduleSessionFidelityPersist } from "../editor/editor-session-fidelity.ts";
import { dropFilesEditorHandlers, getFilesEditorView } from "../editor/files-editor-host.ts";
import { type CloseFamily } from "./files-tab-strip.ts";
import { type JumpEntry } from "../history/jump-history.ts";
import { moveFilesBuffer, closeFilesBuffer, filesCloseTargetKeys, openFilesBuffer, setFilesActiveBuffer, type FilesBufferRelease } from "../documents/project-files-buffers.ts";
import { type FileBufferKey } from "../components/project-files-model.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import type { ConfirmTarget, BulkCloseState } from "../commands/file-confirmations.ts";
import type { FilesScope } from "../components/files-scope.ts";
import { focusRegion } from "../../shortcuts/focus-region.ts";

type FilesTabCommandsOptions = Pick<FilesScope, "projectId" | "state" | "aimedKey" | "filesRoots" | "rootLabelFor" |
  "stripOrder" | "tabsElement" | "dropFileVersionState" | "setSaveError"> & {
  loadBuffer: (buffer: FileBuffer) => void;
  setBulkClose: (state: BulkCloseState) => unknown;
  setConfirm: (target: ConfirmTarget) => unknown;
};

export function createFilesTabCommands(options: FilesTabCommandsOptions) {
  const { projectId, state, aimedKey, filesRoots, rootLabelFor, stripOrder, tabsElement,
    loadBuffer, dropFileVersionState, setBulkClose, setConfirm, setSaveError } = options;
  const [filesOpenListOpen, setFilesOpenListOpen] = createSignal(false);
  const [rovingTabKey, setRovingTabKey] = createSignal<FileBufferKey | null>(
    null,
  );
  const closeBuffers = (
    keys: readonly FileBufferKey[],
    options?: { discardDraft?: boolean },
  ) => {
    for (const key of keys) {
      if (!closeBuffer(key, options)) return false;
    }
    return true;
  };

  const requestCloseFamily = (
    family: CloseFamily,
    focusKey: FileBufferKey | null,
  ) => {
    const keys = filesCloseTargetKeys(projectId(), family, focusKey);
    if (keys.length === 0) return;
    const s = state();
    const dirty = keys.filter((key) => s.byKey[key]?.dirty);
    if (dirty.length === 0) {
      closeBuffers(keys);
      return;
    }
    setBulkClose({ keys, dirtyKeys: dirty });
    setConfirm({
      intent: "bulk-close",
      fileNames: dirty.map((key) => s.byKey[key]?.name ?? key),
    });
  };

  const openFilesList = () => {
    setFilesOpenListOpen(true);
  };

  const toggleFilesOpenList = () => {
    setFilesOpenListOpen((open) => !open);
  };

  const restoreJumpEntry = (entry: JumpEntry) => {
    const key = entry.bufferKey;
    const existing = state().byKey[key];
    if (existing) {
      openFilesBuffer(projectId(), {
        rootId: existing.rootId,
        rootLabel: existing.rootLabel,
        path: existing.path,
        jobId: existing.jobId,
        intent: "transient",
        revealLine: entry.line,
      });
      return;
    }
    const reopenedKey = openFilesBuffer(projectId(), {
      rootId: entry.rootId,
      rootLabel: rootLabelFor(entry.rootId),
      path: entry.path,
      jobId: entry.jobId,
      intent: "transient",
      revealLine: entry.line,
    });
    const buf = state().byKey[reopenedKey];
    if (buf?.loading) loadBuffer(buf);
  };

  const cycleTab = (dir: 1 | -1) => {
    const order = state().order;
    if (order.length === 0) return;
    const aimed = aimedKey();
    const idx = aimed ? order.indexOf(aimed) : -1;
    const next =
      idx < 0
        ? order[dir === 1 ? 0 : order.length - 1]
        : order[(idx + dir + order.length) % order.length];
    if (next === undefined) return;
    setFilesActiveBuffer(projectId(), next);
  };

  // Closing or reloading ends the parked editor session.
  const closeBuffer = (
    key: FileBufferKey,
    options?: { discardDraft?: boolean },
  ): Promise<FilesBufferRelease> | null => {
    const buf = state().byKey[key];
    if (!buf) return null;
    const view = getFilesEditorView(projectId(), key);
    const released = closeFilesBuffer(projectId(), key, options);
    if (!released) {
      setSaveError("Couldn't close the editor safely. Your draft is still open; please retry.");
      return null;
    }
    rememberClosedBufferForProject(projectId(), buf, view);
    dropFilesEditorHandlers(projectId(), key);
    dropFileVersionState(key);
    return released;
  };

  const reopenClosedTab = () => {
    const entry = popClosedBuffer(projectId());
    if (!entry) return;
    scheduleSessionFidelityPersist();
    const existing = state().byKey[entry.key];
    if (existing) {
      setFilesActiveBuffer(projectId(), existing.key);
      return;
    }
    const root =
      filesRoots().find((r) => r.id === entry.rootId) ?? filesRoots()[0];
    const rootLabel = root?.label ?? entry.rootLabel ?? entry.rootId;
    if (entry.viewState) {
      putBufferViewState(entry.rootId, entry.path, entry.viewState);
    }
    openFilesBuffer(projectId(), {
      rootId: entry.rootId,
      rootLabel,
      path: entry.path,
      intent: "permanent",
      pinned: entry.pinned === true,
      decodeAs: entry.decodeAs,
    });
  };
  const activateRovingTab = (key: FileBufferKey) => {
    setFilesActiveBuffer(projectId(), key);
    setRovingTabKey(key);
  };

  const moveRovingTab = (dir: 1 | -1) => {
    const order = stripOrder();
    if (order.length === 0) return;
    const current = rovingTabKey() ?? aimedKey();
    const idx = current ? order.indexOf(current) : -1;
    const next =
      idx < 0
        ? order[dir === 1 ? 0 : order.length - 1]
        : order[(idx + dir + order.length) % order.length];
    if (next === undefined) return;
    setRovingTabKey(next);
    for (const tab of tabsElement()?.querySelectorAll<HTMLElement>(".den-files-tab") ??
      []) {
      if (tab.dataset.key === next) {
        tab.querySelector<HTMLElement>(".den-files-tab__label")?.focus();
        break;
      }
    }
  };

  const moveTab = (direction: 1 | -1) => {
    const key = tabsElement()?.contains(document.activeElement) ? rovingTabKey() ?? aimedKey() : aimedKey();
    const order = stripOrder();
    const index = key ? order.indexOf(key) : -1;
    const neighbor = order[index + direction];
    if (!key || index < 0 || !neighbor || state().byKey[key]?.pinned !== state().byKey[neighbor]?.pinned) return;
    moveFilesBuffer(projectId(), key, state().order.indexOf(neighbor));
    scheduleSessionFidelityPersist();
  };

  const onTabListKeyDown = (e: KeyboardEvent) => {
    if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
    if (e.key === "ArrowRight") {
      e.preventDefault();
      moveRovingTab(1);
      return;
    }
    if (e.key === "ArrowLeft") {
      e.preventDefault();
      moveRovingTab(-1);
      return;
    }
    if (e.key === "ArrowDown") {
      e.preventDefault();
      focusRegion("files");
      return;
    }
    if (e.key === "Home") {
      e.preventDefault();
      const first = stripOrder()[0];
      if (first) {
        setRovingTabKey(first);
        for (const tab of tabsElement()?.querySelectorAll<HTMLElement>(
          ".den-files-tab",
        ) ?? []) {
          if (tab.dataset.key === first) {
            tab.querySelector<HTMLElement>(".den-files-tab__label")?.focus();
            break;
          }
        }
      }
      return;
    }
    if (e.key === "End") {
      e.preventDefault();
      const order = stripOrder();
      const last = order[order.length - 1];
      if (last) {
        setRovingTabKey(last);
        for (const tab of tabsElement()?.querySelectorAll<HTMLElement>(
          ".den-files-tab",
        ) ?? []) {
          if (tab.dataset.key === last) {
            tab.querySelector<HTMLElement>(".den-files-tab__label")?.focus();
            break;
          }
        }
      }
    }
  };

  return { moveTab, filesOpenListOpen, setFilesOpenListOpen, rovingTabKey, setRovingTabKey, closeBuffers, requestCloseFamily, openFilesList, toggleFilesOpenList, restoreJumpEntry, cycleTab, closeBuffer, reopenClosedTab, activateRovingTab, onTabListKeyDown };
}
