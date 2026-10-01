import { createComputed, createSignal, on, onCleanup, untrack } from "solid-js";
import type { FilesScope } from "../components/files-scope.ts";
import { emptyFileVersionHistory, type FileVersionHistory } from "./file-version.ts";
import { isComposedBufferKind } from "../documents/project-files-buffer-kind.ts";
import { type FileBuffer } from "../documents/files-buffer-state.ts";
import { type FileBufferKey } from "../components/project-files-model.ts";
import { reportSurfaceFailure } from "../../notices/surface-failure.ts";
import { gitLaneNotice } from "./file-history-model.ts";
import type { SourceGitHistoryState } from "../../api/types.ts";

const versionHistoryFailure = {
  code: "files_history_unavailable",
  title: "File history unavailable",
  suggestedAction: "Reopen the file's history to load it again.",
};
const gitHistoryFailure = {
  code: "files_git_history_unavailable",
  title: "Git history unavailable",
  suggestedAction: "Check the repository is readable, then reopen the file's history.",
};

type HistoryBuffer = Pick<FileBuffer, "key" | "kind" | "jobId" | "fileId">;

export function createFilesVersionHistory({ projectId, client, sourceSessionId, filesWorkspaceId }:
  Pick<FilesScope, "projectId" | "client" | "sourceSessionId" | "filesWorkspaceId">) {
  const historyRequests = new Map<FileBufferKey, AbortController>();
  /** Advances when another workspace replaces the one histories were read for. */
  let epoch = 0;
  const cancelRequests = () => {
    for (const request of historyRequests.values()) request.abort();
    historyRequests.clear();
  };
  const [fileVersionHistories, setFileVersionHistories] = createSignal(
    new Map<FileBufferKey, FileVersionHistory>(),
  );
  const versionHistoryForBuffer = (buffer: HistoryBuffer): FileVersionHistory =>
    fileVersionHistories().get(buffer.key) ?? emptyFileVersionHistory();
  const updateVersionHistory = (
    key: FileBufferKey,
    value: FileVersionHistory,
  ) => {
    setFileVersionHistories((current) => {
      const next = new Map(current);
      next.set(key, value);
      return next;
    });
  };

  const forgetVersionHistory = (key: FileBufferKey) => {
    historyRequests.get(key)?.abort();
    historyRequests.delete(key);
    setFileVersionHistories((current) => {
      if (!current.has(key)) return current;
      const next = new Map(current);
      next.delete(key);
      return next;
    });
  };
  /** A Git lane that timed out or failed still returns a page; it says so once, in the notices. */
  const reportGitLane = (state: SourceGitHistoryState) => {
    const message = gitLaneNotice(state);
    if (message) reportSurfaceFailure(gitHistoryFailure, message, projectId());
  };
  createComputed(on(() => [client(), filesWorkspaceId()] as const, ([c, workspace], previous) => {
    const [previousClient, previousWorkspace] = previous ?? [];
    // A workspace's first answer keeps what already loaded; only another workspace replaces it.
    if (previousClient === c && previousWorkspace === "" && workspace !== "") return;
    epoch += 1;
    cancelRequests();
    setFileVersionHistories(new Map());
  }));
  onCleanup(cancelRequests);
  const loadVersionHistory = (buffer: HistoryBuffer) => {
    const c = client();
    if (isComposedBufferKind(buffer.kind) || buffer.jobId || !buffer.fileId || !c) return;
    const fileId = buffer.fileId;
    const key = buffer.key;
    const held = untrack(fileVersionHistories).get(key) ??
      emptyFileVersionHistory();
    // Refreshes retain visible rows.
    if (
      held.status === "loading" || held.gitStatus === "loading" ||
      held.loadingMore
    ) return;
    historyRequests.get(key)?.abort();
    const request = new AbortController();
    const sessionId = sourceSessionId();
    const readEpoch = epoch;
    const isCurrent = () => !request.signal.aborted && historyRequests.get(key) === request && client() === c && epoch === readEpoch;
    historyRequests.set(key, request);
    updateVersionHistory(key, {
      ...held,
      status: held.versions.length > 0 ? "ready" : "loading",
      loadingMore: held.versions.length > 0,
    });
    void (async () => {
      try {
        const page = await c.listProjectSourceVersions(
          projectId(),
          { fileId },
          { limit: 200, lane: "retained", sessionId, signal: request.signal },
        );
        if (!isCurrent()) return;
        updateVersionHistory(key, {
          status: "ready",
          gitStatus: "loading",
          current: page.current,
          versions: page.versions,
          commits: held.commits,
          arrivals: held.arrivals,
          gitHistoryState: held.gitHistoryState,
          trackedSince: page.tracked_at ?? null,
          nextVersionsCursor: page.next_cursor ?? null,
          nextGitCursor: held.nextGitCursor,
          loadingMore: false,
        });
        const gitPage = await c.listProjectSourceVersions(
          projectId(),
          { fileId },
          { limit: 200, lane: "git", sessionId, signal: request.signal },
        );
        if (!isCurrent()) return;
        const current = untrack(fileVersionHistories).get(key);
        if (!current) return;
        updateVersionHistory(key, {
          ...current,
          gitStatus: "ready",
          commits: gitPage.commits,
          arrivals: gitPage.arrivals,
          gitHistoryState: gitPage.git_history_state,
          trackedSince: current.trackedSince ?? gitPage.tracked_at ?? null,
          nextGitCursor: gitPage.next_cursor ?? null,
        });
        reportGitLane(gitPage.git_history_state);
      } catch (cause) {
        if (!isCurrent()) return;
        const current = untrack(fileVersionHistories).get(key) ?? held;
        if (current.status === "ready" && current.gitStatus === "loading") {
          updateVersionHistory(key, {
            ...current,
            gitStatus: "error",
            loadingMore: false,
          });
          reportSurfaceFailure(gitHistoryFailure, cause, projectId());
          return;
        }
        updateVersionHistory(key, {
          ...current,
          status: current.versions.length > 0 ? "ready" : "error",
          loadingMore: false,
        });
        reportSurfaceFailure(versionHistoryFailure, cause, projectId());
      }
    })();
  };

  const loadEarlierVersions = (buffer: HistoryBuffer) => {
    const c = client();
    const fileId = buffer.fileId?.trim() ?? "";
    const held = untrack(fileVersionHistories).get(buffer.key);
    if (
      !c || !fileId || !held || held.loadingMore ||
      held.status === "loading" || held.gitStatus === "loading" ||
      (held.nextVersionsCursor === null && held.nextGitCursor === null)
    ) return;
    const key = buffer.key;
    historyRequests.get(key)?.abort();
    const request = new AbortController();
    const sessionId = sourceSessionId();
    const readEpoch = epoch;
    const isCurrent = () => !request.signal.aborted && historyRequests.get(key) === request && client() === c && epoch === readEpoch;
    historyRequests.set(key, request);
    updateVersionHistory(key, { ...held, loadingMore: true });
    // Each lane continues with the cursor its own listing issued.
    const continueLane = (lane: "retained" | "git", cursor: string | null) => cursor === null
      ? Promise.resolve(null)
      : c.listProjectSourceVersions(projectId(), { fileId }, { limit: 200, cursor, lane, sessionId, signal: request.signal });
    void Promise.all([
      continueLane("retained", held.nextVersionsCursor),
      continueLane("git", held.nextGitCursor),
    ]).then(
      ([retained, git]) => {
        if (!isCurrent()) return;
        const commits = [...held.commits];
        const knownCommits = new Set(commits.map((commit) => commit.commit));
        for (const commit of git?.commits ?? []) {
          if (!knownCommits.has(commit.commit)) commits.push(commit);
        }
        const arrivals = [...held.arrivals];
        const knownArrivals = new Set(arrivals.map((change) => change.id));
        for (const change of git?.arrivals ?? []) {
          if (!knownArrivals.has(change.id)) arrivals.push(change);
        }
        updateVersionHistory(key, {
          status: "ready",
          gitStatus: git ? "ready" : held.gitStatus,
          current: held.current,
          versions: retained ? [...held.versions, ...retained.versions] : [...held.versions],
          commits,
          arrivals,
          // Only a Git continuation can replace Git history status.
          gitHistoryState: git ? git.git_history_state : held.gitHistoryState,
          trackedSince: held.trackedSince ?? retained?.tracked_at ?? git?.tracked_at ?? null,
          nextVersionsCursor: retained ? retained.next_cursor ?? null : held.nextVersionsCursor,
          nextGitCursor: git ? git.next_cursor ?? null : held.nextGitCursor,
          loadingMore: false,
        });
        if (git) reportGitLane(git.git_history_state);
      },
      (cause) => {
        if (!isCurrent()) return;
        updateVersionHistory(key, { ...held, loadingMore: false });
        reportSurfaceFailure(versionHistoryFailure, cause, projectId());
      },
    );
  };

  return { versionHistoryForBuffer, loadVersionHistory, loadEarlierVersions, forgetVersionHistory };
}
