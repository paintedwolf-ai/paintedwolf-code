import type { ProjectFilesViewProps } from "../components/project-files-view-props.ts";
import { createFilesReviewNavigation } from "../history/files-review-navigation.ts";
import { createFilesReviewScope } from "../review/files-review-scope.ts";
import { createVersionNavigation } from "../history/version-navigation.ts";
import { createEffect, createMemo, untrack } from "solid-js";
import { specialTabKind } from "../tabs/files-tab-kind.ts";
import { syncReviewPresence } from "../review/review-live.ts";
import { agentPresenceFor } from "../components/agent-presence-store.ts";
import { createFilesAgentPresence } from "../components/files-agent-presence.ts";
import { briefingSelectionForBuffer } from "../components/file-briefing-target.ts";
import { filesBufferHasCurrentFile } from "../documents/files-buffer-change.ts";
import { createFilesVersionHistory } from "../history/files-version-history.ts";
import type { FilesScope } from "../components/files-scope.ts";
import { renameFilesBuffer } from "../documents/project-files-buffers.ts";
import { type FileBuffer } from "../documents/files-buffer-state.ts";
import { diffsBufferKey } from "../components/project-files-model.ts";
import { resolvedLensView, scopeEffectFor } from "../tree/scope-resolution.ts";
import { diffsAddressKey, diffsPageCopy } from "../review/diffs-address.ts";

type WorkspaceReview = {
  scope: Pick<FilesScope, "projectId" | "client" | "sourceSessionId" | "filesWorkspaceId" |
    "filesRoots" | "activeBuffer" | "versionForBuffer">;
  appStore: ProjectFilesViewProps["appStore"];
  chatSessionId: () => string | undefined;
  filesSourceAddress: () => string | undefined;
  refreshWatchCoverage: () => Promise<unknown>;
  displayedBuffer: () => FileBuffer | null;
  walking: () => boolean;
  walkPresentation: Parameters<typeof createFilesReviewNavigation>[0]["walkPresentation"];
  setSelectedEntry: Parameters<typeof createFilesReviewNavigation>[0]["setSelectedEntry"];
  scrollActiveTabIntoView: () => void;
};

export function createFilesWorkspaceReview(options: WorkspaceReview) {
  const { scope, appStore, chatSessionId, filesSourceAddress, refreshWatchCoverage,
    displayedBuffer, walking, walkPresentation, setSelectedEntry, scrollActiveTabIntoView } = options;
  const versionHistory = createFilesVersionHistory(scope);
  const { versionHistoryForBuffer, loadVersionHistory, forgetVersionHistory } = versionHistory;

  const versions = createVersionNavigation({
    ...scope,
    walking,
    forgetVersionHistory: (key) => forgetVersionHistory(key),
    versionHistoryForBuffer: (b) => versionHistoryForBuffer(b),
    loadVersionHistory: (b) => loadVersionHistory(b),
  });
  const { versionForBuffer, returnToCurrent, dropFileVersionState, selectWalkEffect } = versions;

  const {
    openWalkStepFile,
    openReviewRowFile,
    openFileSummaryLocation,
  } = createFilesReviewNavigation({
    ...scope, walkPresentation, setSelectedEntry,
    scrollActiveTabIntoView, selectWalkEffect,
  });

  const previousVersionForBuffer = (buffer: FileBuffer): boolean => {
    if (specialTabKind(buffer)) return false;
    if (versionForBuffer(buffer) || buffer.deleted) return true;
    if (filesBufferHasCurrentFile(buffer)) return false;
    const history = versionHistoryForBuffer(buffer);
    if (history.status === "ready" && history.current?.state === "absent") return true;
    void scopeContentVersion();
    const change = scopeEffectFor(scope.projectId(), buffer.rootId, buffer.path);
    return !buffer.jobId && Boolean(change?.commit) && change?.tip.state === "absent";
  };
  const activeFileSummarySelection = createMemo(() => {
    const buffer = displayedBuffer();
    return briefingSelectionForBuffer(
      buffer,
      buffer ? versionForBuffer(buffer) : null,
    );
  });

  const { scopeMarksVersion, scopeContentVersion, refreshScopeMarks } = createFilesReviewScope({
    ...scope, appStore: appStore, chatSessionId, refreshWatchCoverage,
    // A workspace the host has not answered for yet stays chat-scoped.
    sessionScoped: () => filesSourceAddress() !== undefined,
  });

  const agentPresenceForPath = createFilesAgentPresence({
    projectId: () => scope.projectId(),
    openSessionId: chatSessionId,
  });

  createEffect(() => {
    syncReviewPresence(scope.projectId(), agentPresenceFor(scope.projectId()));
  });

  // The lens page is named by the comparison it follows, so the eye renames its tab.
  createEffect(() => {
    void scopeContentVersion();
    const name = diffsPageCopy({ kind: "lens" }, resolvedLensView(scope.projectId())).tab;
    untrack(() => renameFilesBuffer(scope.projectId(), diffsBufferKey(diffsAddressKey({ kind: "lens" })), name));
  });

  return { versionHistory, versions, versionHistoryForBuffer, loadVersionHistory,
    versionForBuffer, returnToCurrent, dropFileVersionState, openWalkStepFile,
    openReviewRowFile, openFileSummaryLocation, previousVersionForBuffer,
    activeFileSummarySelection, scopeMarksVersion, scopeContentVersion,
    refreshScopeMarks, agentPresenceForPath };
}
