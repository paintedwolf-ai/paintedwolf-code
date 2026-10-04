import { retainFileDocument } from "../documents/files-residency.ts";
import { DocumentCapacityError, type DocumentReservation } from "../documents/document-residency.ts";
import { onCleanup } from "solid-js";
import { unwrap } from "solid-js/store";
import { LycaonApiError } from "../../api/http.ts";
import { BackendTransportError } from "../../platform/connection/request-connectivity.ts";
import { loadEditorConfigForBuffer } from "../../components/source/editor/editorconfig-load.ts";
import { sourceSaveErrorMessage } from "../../components/source/editor/source-editor-model.ts";
import { evictEditorDocument } from "../documents/editor-document.ts";
import { flushFilesDraftSyncFor } from "../documents/files-draft-sync.ts";
import { scheduleFilesHotExitPersist } from "../documents/files-hot-exit.ts";
import { presentOpenedDocument, receiveOpenedEditorDocument, releaseUnclaimedEditorDocument } from "../documents/editor-document.ts";
import { openFileBufferSource } from "../source/files-source-read.ts";
import { setSelectedFileVersion } from "../history/files-version-selection.ts";
import { isComposedBufferKind } from "../documents/project-files-buffer-kind.ts";
import { applyFilesBufferEditorDocument, applyFilesBufferLoad, applyFilesBufferLoadError, setFilesBufferCapacityError, closeFilesBuffer, finishFilesBufferLoad, markFilesBufferLoading } from "../documents/project-files-buffers.ts";
import { projectFilesState, type FileBuffer } from "../documents/files-buffer-state.ts";
import { applyProjectSourceLoadFailure } from "../components/project-files-load-error.ts";
import { setProjectFilesRevealRequest } from "../tree/project-files-reveal.ts";
import { bufferRequestIdentity, sharedBufferLoadRequests as bufferLoadRequests } from "./files-buffer-request-tracking.ts";
import type { FilesScope } from "../components/files-scope.ts";

/** Transport failures reopen a file this many times before its read reports the failure. */
const TRANSPORT_RETRY_LIMIT = 4;

type BufferLoadingDependencies = Pick<FilesScope, "projectId" | "client" | "sourceSessionId" | "live" |
  "filesWorkspaceId" | "dropFileVersionState" | "loadVersionHistory" | "setSaveError"> & {
  refreshProjectWorkspace: (workspaceId: string) => boolean;
};

export function createBufferLoading({ projectId, client, sourceSessionId, dropFileVersionState,
  loadVersionHistory, setSaveError, live, filesWorkspaceId,
  refreshProjectWorkspace }: BufferLoadingDependencies) {
  const retries = new Set<ReturnType<typeof setTimeout>>();
  onCleanup(() => { for (const retry of retries) clearTimeout(retry); retries.clear(); });
  const loadBuffer = (buf: FileBuffer, transportFailures = 0) => {
    if (isComposedBufferKind(buf.kind)) return;
    const c = client();
    if (!c) {
      applyFilesBufferLoadError(
        projectId(),
        buf.key,
        "Sidecar is not connected.",
      );
      return;
    }
    const bufferKey = buf.key;
    const editRevision = buf.editRevision;
    const requestKey = bufferRequestIdentity(projectId(), buf);
    let settledKey = bufferKey;
    const loadToken = bufferLoadRequests.begin(requestKey, unwrap(buf));
    if (loadToken == null) return;
    const isCurrent = () => {
      const current = projectFilesState(projectId()).byKey[settledKey];
      return (
        bufferLoadRequests.isCurrent(requestKey, loadToken) &&
        current != null &&
        current.path === buf.path &&
        current.rootId === buf.rootId &&
        (current.jobId ?? "") === (buf.jobId ?? "")
      );
    };
    void (async () => {
      let sourceLoaded = false;
      let reservation: DocumentReservation | undefined;
      let adoption: ReturnType<typeof receiveOpenedEditorDocument> | undefined;
      try {
        const selected = projectFilesState(projectId()).pendingKey ?? projectFilesState(projectId()).activeKey;
        const wanted = () => {
          if (!live() || !isCurrent()) return false;
          const state = projectFilesState(projectId());
          const aimed = state.pendingKey ?? state.activeKey;
          if (aimed === buf.key) return true;
          const index = aimed ? state.order.indexOf(aimed) : -1;
          return state.order[index - 1] === buf.key || state.order[index + 1] === buf.key;
        };
        const opened = await openFileBufferSource(c, projectId(), buf, sourceSessionId(), selected === buf.key ? "foreground" : "background", wanted);
        reservation = opened.reservation;
        const res = opened.source;
        const releaseOpened = () => { if (opened.document) void releaseUnclaimedEditorDocument(projectId(), opened.document).catch(() => undefined); };
        if (!isCurrent()) { releaseOpened(); return; }
        const expectedKind = buf.jobId ? "worker" : "project";
        if (res.workspace_kind !== expectedKind) {
          releaseOpened();
          throw new Error("Source read returned the wrong workspace kind.");
        }
        if (!buf.jobId && res.workspace_id !== filesWorkspaceId()) {
          refreshProjectWorkspace(res.workspace_id);
          releaseOpened();
          return;
        }
        flushFilesDraftSyncFor(projectId(), bufferKey);
        const beforeLoad = projectFilesState(projectId()).byKey[bufferKey];
        if (!beforeLoad || (beforeLoad.dirty && beforeLoad.content.state !== "suspended") || beforeLoad.editRevision !== editRevision) {
          finishFilesBufferLoad(projectId(), bufferKey);
          releaseOpened();
          return;
        }
        const presented = presentOpenedDocument(opened.document, opened.retained);
        adoption = opened.document ? receiveOpenedEditorDocument(projectId(), c, opened.document, presented?.confirmed, reservation, opened.offline) : undefined;
        const previousDocumentId = beforeLoad.documentId;
        settledKey = applyFilesBufferLoad(projectId(), bufferKey, res, presented?.loaded);
        if (settledKey !== bufferKey) setSelectedFileVersion(projectId(), settledKey, null);
        sourceLoaded = true;
        const painted = projectFilesState(projectId()).byKey[settledKey];
        if (reservation && painted) retainFileDocument(projectId(), painted, reservation);
        const accepted = await adoption;
        if (accepted && isCurrent()) applyFilesBufferEditorDocument(projectId(), settledKey, accepted);
        const updated = projectFilesState(projectId()).byKey[settledKey];
        if (!updated) {
          scheduleFilesHotExitPersist(projectId());
          return;
        }
        if (reservation) retainFileDocument(projectId(), updated, reservation);
        if (updated.deleted) {
          if (previousDocumentId) {
            void evictEditorDocument(previousDocumentId).catch((error) => setSaveError(sourceSaveErrorMessage(error)));
          }
          dropFileVersionState(settledKey);
          loadVersionHistory(updated);
          scheduleFilesHotExitPersist(projectId());
          return;
        }
        if (!isCurrent()) return;
        const forConfig = projectFilesState(projectId()).byKey[settledKey];
        if (forConfig && c) {
          await loadEditorConfigForBuffer({
            client: c,
            projectId: projectId(),
            key: settledKey,
            buffer: forConfig,
            sessionId: sourceSessionId(),
          });
        }
        if (!isCurrent()) return;
        scheduleFilesHotExitPersist(projectId());
      } catch (err: unknown) {
        if (!isCurrent() || (err instanceof DOMException && err.name === "AbortError")) return;
        flushFilesDraftSyncFor(projectId(), bufferKey);
        const current = projectFilesState(projectId()).byKey[bufferKey];
        if (!sourceLoaded && current && current.content.state !== "suspended" && (current.dirty || current.editRevision !== editRevision)) {
          finishFilesBufferLoad(projectId(), bufferKey);
          return;
        }
        if (err instanceof DocumentCapacityError) {
          setFilesBufferCapacityError(projectId(), bufferKey, err.message);
          return;
        }
        // Directory targets reveal folders in the tree.
        if (
          !sourceLoaded &&
          err instanceof LycaonApiError &&
          err.code === "source_not_found" &&
          !buf.jobId
        ) {
          const listing = await c
            .browseProjectSource(
              projectId(),
              { rootId: buf.rootId, dir: buf.path },
              sourceSessionId(),
            )
            .catch(() => null);
          if (!isCurrent()) return;
          if (listing) {
            if (!closeFilesBuffer(projectId(), bufferKey)) return;
            setProjectFilesRevealRequest({
              projectId: projectId(),
              rootId: listing.root_id?.trim() || buf.rootId,
              path: listing.dir?.trim() || buf.path,
              isDir: true,
            });
            return;
          }
        }
        if (!sourceLoaded && err instanceof BackendTransportError && transportFailures < TRANSPORT_RETRY_LIMIT) {
          // The buffer stays loading, so a dropped open retries instead of replacing the file with an error.
          const retry = setTimeout(() => {
            retries.delete(retry);
            const pending = projectFilesState(projectId()).byKey[bufferKey];
            if (pending?.loading && live()) loadBuffer(pending, transportFailures + 1);
          }, 1000 * 2 ** transportFailures);
          retries.add(retry);
          return;
        }
        if (!sourceLoaded) {
          applyProjectSourceLoadFailure(projectId(), bufferKey, err);
          if (
            err instanceof LycaonApiError &&
            err.code === "source_not_found"
          ) {
            const missing = projectFilesState(projectId()).byKey[bufferKey];
            if (missing?.fileId) loadVersionHistory(missing);
          }
        } else {
          scheduleFilesHotExitPersist(projectId());
        }
      } finally {
        await adoption?.catch(() => undefined);
        reservation?.release();
        bufferLoadRequests.finish(requestKey, loadToken);
      }
    })();
  };

  /** An explicit encoding choice rereads the source without joining its document. */
  const reopenBufferAs = (buf: FileBuffer, decodeAs: "utf-16le" | "utf-16be") => {
    const c = client();
    if (!c) return;
    const bufferKey = buf.key;
    const requestKey = bufferRequestIdentity(projectId(), buf);
    let settledKey = bufferKey;
    const reopenToken = bufferLoadRequests.begin(requestKey, unwrap(buf));
    if (reopenToken == null) return;
    markFilesBufferLoading(projectId(), bufferKey);
    const isCurrent = () =>
      bufferLoadRequests.isCurrent(requestKey, reopenToken) && projectFilesState(projectId()).byKey[settledKey] != null;
    void (async () => {
      try {
        const res = await c.getProjectSource(projectId(), buf.path, {
          workerId: buf.jobId,
          rootId: buf.rootId || undefined,
          sessionId: sourceSessionId(),
          decodeAs,
        });
        if (!isCurrent()) return;
        if (res.workspace_kind !== (buf.jobId ? "worker" : "project")) {
          throw new Error("Source read returned the wrong workspace kind.");
        }
        if (!buf.jobId && res.workspace_id !== filesWorkspaceId()) {
          refreshProjectWorkspace(res.workspace_id);
          return;
        }
        settledKey = applyFilesBufferLoad(projectId(), bufferKey, res);
        const loaded = projectFilesState(projectId()).byKey[settledKey];
        if (loaded) {
          void loadEditorConfigForBuffer({ client: c, projectId: projectId(), key: settledKey, buffer: loaded, sessionId: sourceSessionId() });
        }
      } catch (err: unknown) {
        if (!isCurrent()) return;
        const painted = projectFilesState(projectId()).byKey[settledKey];
        if (painted && !painted.loading) return;
        applyProjectSourceLoadFailure(projectId(), settledKey, err);
      } finally {
        bufferLoadRequests.finish(requestKey, reopenToken);
      }
    })();
  };

  return { loadBuffer, reopenBufferAs };
}
