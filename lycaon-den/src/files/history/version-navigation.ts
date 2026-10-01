import type { DenEditorVersionComparison } from "../../../shared/app-state-types.ts";
import type { SourceFileVersion, SourceWalkEffect } from "../../api/types.ts";
import { buildFileHistoryRows, type FileHistoryCommit } from "../history/file-history-model.ts";
import { fileVersionFromComparison, type FileVersionHistory, type FileVersionView } from "../history/file-version.ts";
import { beginFileVersionSelection, selectedFileVersion, setSelectedFileVersion } from "../history/files-version-selection.ts";
import { type FileBuffer } from "../documents/files-buffer-state.ts";
import { fileBufferKey, type FileBufferKey } from "../components/project-files-model.ts";
import { loadSourceComparison } from "../source/source-comparison-cache.ts";
import { leaveWalk } from "../walk/walk-store.ts";
import type { FilesScope } from "../components/files-scope.ts";

type VersionNavigationDependencies = Pick<FilesScope, "projectId" | "client" | "sourceSessionId"> & {
  walking: () => boolean;
  forgetVersionHistory: (key: FileBufferKey) => void;
  versionHistoryForBuffer?: (buffer: FileBuffer) => FileVersionHistory;
  loadVersionHistory?: (buffer: FileBuffer) => void;
};

export function createVersionNavigation({ projectId, client, sourceSessionId, walking, forgetVersionHistory, versionHistoryForBuffer, loadVersionHistory }: VersionNavigationDependencies) {
  const versionForBuffer = (buffer: FileBuffer): FileVersionView | null => {
    const preview = buffer.diffPreview;
    if (buffer.kind === "diff" && preview) return preview.version;
    return selectedFileVersion(projectId(), buffer.key);
  };
  /** Leaves a past version for the editable working file. */
  const returnToCurrent = (key: FileBufferKey) => {
    if (walking()) leaveWalk(projectId());
    setSelectedFileVersion(projectId(), key, null);
  };
  const dropFileVersionState = (key: FileBufferKey) => {
    forgetVersionHistory(key);
    setSelectedFileVersion(projectId(), key, null);

  };

  const selectVersion = (selection: {
    versionId: string;
    effectId?: string;
    fileId: string;
    rootId: string;
    path: string;
    op: SourceFileVersion["op"];
    cause?: string;
    gitChange?: SourceFileVersion["git_change"] | null;
    ts: string;
    initialComparison?: DenEditorVersionComparison;
  }) => {
    const c = client();
    const key = fileBufferKey(
      selection.rootId,
      selection.path,
      undefined,
      selection.fileId,
    );
    const request = beginFileVersionSelection(projectId(), key);
    const versionId = selection.versionId.trim();
    if (!c || !versionId) { request.finish(); return; }
    void loadSourceComparison(
      c,
      projectId(),
      selection.effectId?.trim()
        ? { effectId: selection.effectId.trim() }
        : { versionId },
      sourceSessionId(),
    )
      .then(
        (diff) => {
          if (!request.current()) return;
          const view = fileVersionFromComparison(
            { ...selection, versionId },
            diff,
          );
          // A comparison with no endpoints keeps the last coherent presentation.
          if (view) setSelectedFileVersion(projectId(), key, view);
        },
        () => {
          if (!request.current()) return;
          // Failed navigation leaves the last coherent presentation intact.
        },
      ).finally(request.finish);
  };

  const selectStoredVersion = (
    version: SourceFileVersion,
  ) => selectVersion({
    versionId: version.id,
    effectId: version.effect_id,
    fileId: version.file_id,
    rootId: version.root_id,
    path: version.path,
    op: version.op,
    cause: version.cause,
    gitChange: version.git_change,
    ts: version.created_at,
  });

  const selectWalkEffect = (effect: SourceWalkEffect) => selectVersion({
    versionId: effect.after_version_id,
    effectId: effect.id,
    fileId: effect.file_id,
    rootId: effect.root_id,
    path: effect.path,
    op: effect.op,
    cause: effect.cause,
    ts: effect.observed_at,
    initialComparison: "before",
  });

  const selectCommitPreview = (buffer: FileBuffer, entry: FileHistoryCommit) => {
    const c = client();
    const blobOid = entry.commit.blob_oid ?? "";
    const key = buffer.key;
    const request = beginFileVersionSelection(projectId(), key);
    if (!c || !blobOid) { request.finish(); return; }
    void loadSourceComparison(
      c,
      projectId(),
      {
        blobOid,
        rootId: buffer.rootId,
        beforeBlobOid: entry.previousBlobOid ?? undefined,
        displayPath: buffer.path,
      },
      sourceSessionId(),
    ).then(
      (diff) => {
        if (!request.current()) return;
        const view = fileVersionFromComparison({
          versionId: "",
          fileId: buffer.fileId ?? "",
          rootId: buffer.rootId,
          path: buffer.path,
          op: entry.previousBlobOid ? "write" : "create",
          ts: entry.commit.committed_at,
        }, diff);
        if (view) setSelectedFileVersion(projectId(), key, { ...view, commit: entry.commit });
      },
      () => {
        // Failed navigation leaves the last coherent presentation intact.
      },
    ).finally(request.finish);
  };

  const stepVersion = (buffer: FileBuffer, direction: 1 | -1) => {
    if (!versionHistoryForBuffer) return;
    const history = versionHistoryForBuffer(buffer);
    if (history.status === "idle" && loadVersionHistory) {
      loadVersionHistory(buffer);
    }
    const { rows } = buildFileHistoryRows(history);
    type SelectableEntry =
      | { kind: "version"; version: SourceFileVersion }
      | { kind: "commit"; entry: FileHistoryCommit };

    const items: SelectableEntry[] = [];
    for (const row of rows) {
      if (row.kind === "version") {
        items.push({ kind: "version", version: row.version });
      } else if (row.kind === "commit") {
        items.push({ kind: "commit", entry: row.entry });
      } else if (row.kind === "arrival") {
        for (const entry of row.commits) {
          items.push({ kind: "commit", entry });
        }
      }
    }
    if (!items.length) return;

    const currentSelected = versionForBuffer(buffer);
    if (!currentSelected) {
      if (direction === -1) {
        const item = items[0]!;
        if (item.kind === "version") selectStoredVersion(item.version);
        else selectCommitPreview(buffer, item.entry);
      }
      return;
    }

    let currentIndex = -1;
    if (currentSelected.commit) {
      currentIndex = items.findIndex(
        (it) => it.kind === "commit" && it.entry.commit.commit === currentSelected.commit?.commit,
      );
    } else if (currentSelected.versionId) {
      currentIndex = items.findIndex(
        (it) => it.kind === "version" && it.version.id === currentSelected.versionId,
      );
    }

    if (direction === -1) {
      if (currentIndex === -1) {
        const item = items[0]!;
        if (item.kind === "version") selectStoredVersion(item.version);
        else selectCommitPreview(buffer, item.entry);
      } else if (currentIndex + 1 < items.length) {
        const item = items[currentIndex + 1]!;
        if (item.kind === "version") selectStoredVersion(item.version);
        else selectCommitPreview(buffer, item.entry);
      }
    } else {
      if (currentIndex > 0) {
        const item = items[currentIndex - 1]!;
        if (item.kind === "version") selectStoredVersion(item.version);
        else selectCommitPreview(buffer, item.entry);
      } else if (currentIndex === 0) {
        returnToCurrent(buffer.key);
      }
    }
  };

  return { versionForBuffer, returnToCurrent, dropFileVersionState, selectStoredVersion, selectWalkEffect, selectCommitPreview, stepVersion };
}

