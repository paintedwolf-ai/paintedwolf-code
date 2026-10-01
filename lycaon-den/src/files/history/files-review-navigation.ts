import type { ComparisonSnapshot } from "../../api/source-reader.ts";
import { batch, createEffect, on } from "solid-js";
import type { SourceWalkEffect } from "../../api/types.ts";
import { type FileBriefingTarget } from "../components/file-briefing-target.ts";
import { recordInventoryOpen } from "../tree/file-inventory.ts";
import { walkVersionFromComparison } from "./file-version.ts";
import { setSelectedFileVersion } from "./files-version-selection.ts";
import { openFilesBuffer } from "../documents/project-files-buffers.ts";
import { pushProjectJump } from "../components/project-files-jump-bridge.ts";
import { fileBufferKey } from "../components/project-files-model.ts";
import { loadSourceComparison } from "../source/source-comparison-cache.ts";
import { type WalkGroupStep } from "../walk/walk-model.ts";
import { consumeWalkFocusRequest, leaveWalk } from "../walk/walk-store.ts";
import { walkPageTitle } from "../walk/WalkStepPage.tsx";
import type { WalkStep } from "../walk/walk-model.ts";
import type { FilesTreeEntrySelection } from "../tree/files-tree-context.ts";
import { scopeChangeKindFor, scopeEffectFor } from "../tree/scope-resolution.ts";
import type { FilesScope } from "../components/files-scope.ts";

type ReviewNavigationOptions = Pick<FilesScope, "projectId" | "client" | "sourceSessionId" | "filesRoots" |
  "activeBuffer" | "versionForBuffer"> & {
  walkPresentation: () => { step: WalkStep; comparison: ComparisonSnapshot | null; selectionRevision: number } | null;
  scrollActiveTabIntoView: () => void;
  setSelectedEntry: (entry: FilesTreeEntrySelection) => unknown;
  selectWalkEffect: (effect: SourceWalkEffect) => void;
};

export function createFilesReviewNavigation(options: ReviewNavigationOptions) {
  const { projectId, client, sourceSessionId, filesRoots, walkPresentation,
    scrollActiveTabIntoView, setSelectedEntry, selectWalkEffect, activeBuffer, versionForBuffer } = options;
  // Completed Walk selections take focus; refreshes preserve pending file opens.
  createEffect(on(walkPresentation, (selected) => batch(() => {
    if (!selected) return;
    const { step, comparison } = selected;
    const selection = selected.selectionRevision;
    const origin = consumeWalkFocusRequest(projectId(), selection) ? "reader" : "presentation";
    // Same-file selections also reveal tabs scrolled out of view.
    if (origin === "reader") scrollActiveTabIntoView();
    if (step.kind !== "effect") {
      openFilesBuffer(projectId(), {
        kind: "walk",
        walkStep: step,
        name: walkPageTitle(step),
        rootId: "",
        rootLabel: "",
        path: "",
        intent: "transient",
        origin,
      });
      return;
    }
    if (!comparison) return;
    const effect = step.effect;
    const rootId = effect.root_id;
    const path = effect.path;
    const root = filesRoots().find((entry) => entry.id === rootId);
    openFilesBuffer(projectId(), {
      rootId,
      rootLabel: root?.label ?? "repo",
      fileId: effect.file_id,
      path,
      intent: "transient",
      origin,
    });
    const key = fileBufferKey(rootId, path, undefined, effect.file_id);
    const view = walkVersionFromComparison(effect, comparison);
    if (view) setSelectedFileVersion(projectId(), key, view);
  })));

  // Group files open at their historical version without advancing Walk.
  let walkStepFileOpen = 0;
  const openWalkStepFile = async (step: WalkGroupStep, effect: SourceWalkEffect) => {
    const c = client();
    if (!c) throw new Error("Sidecar is not connected.");
    const attempt = ++walkStepFileOpen;
    const comparison = await loadSourceComparison(
      c,
      projectId(),
      { effectId: effect.id },
      sourceSessionId(),
    );
    // A later pick replaces this one.
    if (attempt !== walkStepFileOpen) return;
    const root = filesRoots().find((entry) => entry.id === effect.root_id);
    openFilesBuffer(projectId(), {
      rootId: effect.root_id,
      rootLabel: root?.label ?? "repo",
      fileId: effect.file_id,
      path: effect.path,
      intent: "permanent",
      origin: "reader",
    });
    const key = fileBufferKey(effect.root_id, effect.path, undefined, effect.file_id);
    const view = walkVersionFromComparison(
      effect,
      comparison,
      step.kind === "git" ? step.change : null,
    );
    if (view) setSelectedFileVersion(projectId(), key, view);
    scrollActiveTabIntoView();
  };

  /** Review rows open the ordinary file; only an explicit step selects history. */
  const openReviewRowFile = (
    rootId: string,
    path: string,
    version?: SourceWalkEffect,
    fileId?: string,
  ) => {
    leaveWalk(projectId());
    const root = filesRoots().find((r) => r.id === rootId);
    if (version) {
      setSelectedEntry({ rootId, path, kind: "file" });
      const key = openFilesBuffer(projectId(), {
        rootId,
        rootLabel: root?.label ?? "repo",
        fileId: version.file_id,
        path,
        intent: "transient",
      });
      recordInventoryOpen(projectId(), { rootId, path });
      pushProjectJump(projectId(), { bufferKey: key, rootId, path, line: 1 });
      selectWalkEffect(version);
      return;
    }
    setSelectedEntry({ rootId, path, kind: "file" });
    const change = scopeEffectFor(projectId(), rootId, path);
    const deleted = change?.tip.state === "absent" || change?.entryOp === "delete" || change?.commit?.op === "delete" || scopeChangeKindFor(projectId(), rootId, path) === "deleted";
    const key = openFilesBuffer(projectId(), {
      rootId,
      rootLabel: root?.label ?? "repo",
      fileId,
      path,
      intent: "transient",
      condenseContext: !deleted,
    });
    recordInventoryOpen(projectId(), { rootId, path });
    pushProjectJump(projectId(), { bufferKey: key, rootId, path, line: 1 });
    setSelectedFileVersion(projectId(), key, null);
  };

  const openReviewLocation = (
    rootId: string,
    path: string,
    line = 1,
    endLine?: number,
  ) => {
    const root = filesRoots().find((entry) => entry.id === rootId);
    const key = openFilesBuffer(projectId(), {
      rootId,
      rootLabel: root?.label ?? "repo",
      path,
      intent: "transient",
      revealLine: line,
      revealEndLine: endLine,
    });
    recordInventoryOpen(projectId(), { rootId, path });
    pushProjectJump(projectId(), { bufferKey: key, rootId, path, line });
  };

  const openFileSummaryLocation = (
    target: FileBriefingTarget,
    line = 1,
  ) => {
    if (target.presentation !== "version") {
      openReviewLocation(target.root_id, target.path, line);
      return;
    }
    const buffer = activeBuffer();
    const version = buffer ? versionForBuffer(buffer) : null;
    if (!buffer || !version || version.versionId !== target.version_id) return;
    const key = openFilesBuffer(projectId(), {
      rootId: buffer.rootId,
      rootLabel: buffer.rootLabel,
      fileId: version.fileId,
      path: buffer.path,
      jobId: buffer.jobId,
      intent: "permanent",
      revealLine: line,
    });
    pushProjectJump(projectId(), {
      bufferKey: key,
      rootId: buffer.rootId,
      path: buffer.path,
      ...(buffer.jobId ? { jobId: buffer.jobId } : {}),
      line,
    });
  };

  return { openWalkStepFile, openReviewRowFile, openFileSummaryLocation };
}
