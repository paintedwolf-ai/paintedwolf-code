import { filesBufferText } from "../documents/project-files-buffers.ts";
import type { ProjectSourceReadResponse } from "../../api/types.ts";
import { prefersReducedMotion } from "../../platform/interaction/reduced-motion.ts";
import { normalizeEolForEditor } from "../../components/source/editor/eol.ts";
import { sourceSaveErrorMessage } from "../../components/source/editor/source-editor-model.ts";
import { noteReviewChangeStats } from "../review/review-live.ts";
import { burstRemainingMs, consumeBurstMs, lastBufferTypedAt } from "../documents/buffer-typing.ts";
import { evictEditorDocument, observeEditorDocument, editorReplica } from "../documents/editor-document.ts";
import { FilesBufferLatestRequests } from "../documents/files-buffer-request-tracking.ts";
import { flushFilesDraftSyncFor } from "../documents/files-draft-sync.ts";
import { dropFilesEditorHandlers, getFilesEditorView } from "../editor/files-editor-host.ts";
import { readFileBufferSource } from "../source/files-source-read.ts";
import { PLAYBACK_BURST_BUDGET_MS, PLAYBACK_EVENT_BUDGET_MS, computeLineHunks, playHunks } from "../walk/hunk-playback.ts";
import { isComposedBufferKind } from "../documents/project-files-buffer-kind.ts";
import { applyFilesBufferLoad } from "../documents/project-files-buffers.ts";
import { filesBufferNeedsBody, projectFilesState, type FileBuffer } from "../documents/files-buffer-state.ts";
import { applyProjectSourceLoadFailure } from "../components/project-files-load-error.ts";
import { type FileBufferKey } from "../components/project-files-model.ts";
import { bufferRequestIdentity } from "./files-buffer-request-tracking.ts";
import type { FilesScope } from "../components/files-scope.ts";

type BufferRefreshDependencies = Pick<FilesScope, "projectId" | "client" | "sourceSessionId" | "state" |
  "dropFileVersionState" | "loadVersionHistory" | "setSaveError" | "versionForBuffer"> & {
  reloadEditorConfig: (key: FileBufferKey) => void;
  flashUpdatedNote: () => void;
};

export function createBufferRefresh({ projectId, client, sourceSessionId, state, dropFileVersionState,
  loadVersionHistory, setSaveError, versionForBuffer,
  reloadEditorConfig, flashUpdatedNote }: BufferRefreshDependencies) {
  const cleanRefreshRequests = new FilesBufferLatestRequests();
  type CleanBufferRefresh = {
    requestKey: string;
    generation: number;
    editRevision: number;
    baseSha256: string | null;
    path: string;
    rootId: string;
  };
  const canApplyCleanRefresh = (buf: FileBuffer, refresh: CleanBufferRefresh) => {
    const current = projectFilesState(projectId()).byKey[buf.key];
    return (
      cleanRefreshRequests.isCurrent(refresh.requestKey, refresh.generation) &&
      current != null &&
      !filesBufferNeedsBody(current) &&
      current.path === refresh.path &&
      current.rootId === refresh.rootId &&
      !current.loading &&
      !versionForBuffer(current) &&
      !current.dirty &&
      current.editRevision === refresh.editRevision &&
      current.baseSha256 === refresh.baseSha256
    );
  };

  const applyCleanDiskContent = (
    buf: FileBuffer,
    content: string,
    res: ProjectSourceReadResponse,
    refresh: CleanBufferRefresh,
  ) => {
    let staleReported = false;
    const canApply = () => canApplyCleanRefresh(buf, refresh);
    const discardStale = () => {
      if (
        staleReported ||
        !cleanRefreshRequests.isCurrent(
          refresh.requestKey,
          refresh.generation,
        )
      ) {
        return;
      }
      staleReported = true;
      const current = projectFilesState(projectId()).byKey[buf.key];
      cleanRefreshRequests.finish(refresh.requestKey, refresh.generation);
      if (!current || filesBufferNeedsBody(current)) return;
      void observeEditorDocument(projectId(), current).catch(() => undefined);
    };
    flushFilesDraftSyncFor(projectId(), buf.key);
    if (!canApply()) {
      discardStale();
      return;
    }
    const current = projectFilesState(projectId()).byKey[buf.key];
    if (!current) return;
    if (current.kind === "text" && editorReplica(current.documentId) && !res.deleted && !res.binary && !res.over_limit) {
      cleanRefreshRequests.finish(refresh.requestKey, refresh.generation);
      void observeEditorDocument(projectId(), current).catch((error) => setSaveError(sourceSaveErrorMessage(error)));
      return;
    }
    const before = filesBufferText(current);
    const { text: after } = normalizeEolForEditor(content);
    if (res.deleted || current.deleted || before === after || current.kind !== "text" || res.binary || res.over_limit) {
      if (!canApply()) {
        discardStale();
        return;
      }
      if (current.kind !== "text" || res.binary || res.over_limit) {
        dropFilesEditorHandlers(projectId(), buf.key);
      }
      const previousDocumentId = current.documentId;
      const settledKey = applyFilesBufferLoad(projectId(), buf.key, res);
      const settled = projectFilesState(projectId()).byKey[settledKey];
      // A tombstone, binary, or over-limit rewrite leaves no text document behind.
      if (previousDocumentId && (settled?.deleted || res.binary || res.over_limit)) {
        void evictEditorDocument(previousDocumentId).catch((error) => setSaveError(sourceSaveErrorMessage(error)));
      }
      if (settled?.deleted) {
        dropFileVersionState(settledKey);
        loadVersionHistory(settled);
      } else {
        reloadEditorConfig(settledKey);
      }
      cleanRefreshRequests.finish(refresh.requestKey, refresh.generation);
      return;
    }
    const view = getFilesEditorView(projectId(), buf.key);
    const visible = state().activeKey === buf.key && view != null;
    const remaining = burstRemainingMs(
      projectId(),
      buf.key,
      PLAYBACK_BURST_BUDGET_MS,
      PLAYBACK_BURST_BUDGET_MS,
    );
    const started = Date.now();
    const hunks = computeLineHunks(before, after);
    let added = 0;
    let removed = 0;
    for (const h of hunks) {
      if (h.kind === "insert") added += h.afterEnd - h.afterStart;
      if (h.kind === "delete") removed += h.beforeEnd - h.beforeStart;
    }
    if (added > 0 || removed > 0) {
      noteReviewChangeStats(projectId(), buf.rootId, buf.path, { added, removed });
    }
    const finishStore = () => {
      if (!canApply()) {
        discardStale();
        return;
      }
      const settledKey = applyFilesBufferLoad(projectId(), buf.key, res);
      reloadEditorConfig(settledKey);
      cleanRefreshRequests.finish(refresh.requestKey, refresh.generation);

      dropFilesEditorHandlers(projectId(), buf.key);
      flashUpdatedNote();
      consumeBurstMs(
        projectId(),
        buf.key,
        Date.now() - started,
        PLAYBACK_BURST_BUDGET_MS,
      );
    };
    if (!view || !visible) {
      finishStore();
      return;
    }
    playHunks(view, before, after, {
      reducedMotion: prefersReducedMotion(),
      lastTypedAt: lastBufferTypedAt(projectId(), buf.key),
      visible: true,
      eventBudgetMs: PLAYBACK_EVENT_BUDGET_MS,
      burstRemainingMs: remaining,
      canSettle: canApply,
      onDiscarded: discardStale,
      onSettled: () => finishStore(),
    });
  };

  const refreshBufferPresentation = (buf: FileBuffer) => {
    const c = client();
    if (
      !c ||
      isComposedBufferKind(buf.kind) ||
      filesBufferNeedsBody(buf) ||
      buf.loading ||
      versionForBuffer(buf)
    ) return;
    const requestKey = bufferRequestIdentity(projectId(), buf);
    const generation = cleanRefreshRequests.begin(requestKey);
    const refresh = {
      requestKey,
      generation,
      editRevision: buf.editRevision,
      baseSha256: buf.baseSha256,
      path: buf.path,
      rootId: buf.rootId,
    };
    void readFileBufferSource(c, projectId(), buf, sourceSessionId()).then(
      (res) => {
        if (!cleanRefreshRequests.isCurrent(requestKey, generation)) return;
        const expectedKind = buf.jobId ? "worker" : "project";
        if (res.workspace_kind !== expectedKind) {
          cleanRefreshRequests.finish(requestKey, generation);
          return;
        }
        applyCleanDiskContent(buf, res.content, res, refresh);
      },
      (err: unknown) => {
        flushFilesDraftSyncFor(projectId(), buf.key);
        const canApply = canApplyCleanRefresh(buf, refresh);
        cleanRefreshRequests.finish(requestKey, generation);
        if (!canApply) return;
        applyProjectSourceLoadFailure(projectId(), buf.key, err);
      },
    );
  };

  return refreshBufferPresentation;
}
