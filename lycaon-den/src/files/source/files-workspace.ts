import { batch, createEffect, createMemo, createSignal, onCleanup, untrack } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type {
  ProjectRoot,
  SourceChange,
  SourceWatchCoverage,
  SourceWorkspace,
  SourceWorkspaceRoot
} from "../../api/types.ts";
import { reportProjectNoticeError } from "../../notices/notice-store.ts";
import { createPresentation } from "../../ui/presentation.ts";
import { createResidentActivity } from "../../ui/resident-activity.ts";
import { restoreFilesHotExitForProject } from "../documents/files-hot-exit.ts";
import {
  WORKSPACE_IDENTITY_RECONCILE_LIMIT,
  WORKSPACE_SYNC_RETRYING,
  workspaceFaultMessage,
  workspaceLookupRetryable,
} from "./files-workspace-fault.ts";
import { isComposedBufferKind } from "../documents/project-files-buffer-kind.ts";
import { dropFilesBuffersForRoot, switchFilesRootBranches } from "../documents/project-files-buffers.ts";
import { type FileBuffer } from "../documents/files-buffer-state.ts";
import { connectSourceTreeWorkspace, prepareSourceTreeWorkspace, type SourceTreeConnection } from "../tree/source-tree-store.ts";
import { workspaceMismatchId } from "./source-workspace-identity.ts";
import { resolveSourceWorkspaceRoots } from "./source-workspace-roots.ts";
import { observeWorkspaceInvalidation } from "./workspace-invalidation.ts";

type FilesWorkspacePresentation = {
  client: LycaonClient | null;
  scopeKey: string;
  workspaceId: string;
  roots: ProjectRoot[];
  /** The chat every read of this workspace carries; absent when the project's checkout answers. */
  address: string | undefined;
};

/** True when the displayed snapshot is this answer, so resolving it again publishes nothing. */
function samePresentation(shown: FilesWorkspacePresentation | undefined, next: FilesWorkspacePresentation): boolean {
  return shown != null &&
    shown.client === next.client &&
    shown.scopeKey === next.scopeKey &&
    shown.workspaceId === next.workspaceId &&
    shown.address === next.address &&
    JSON.stringify(shown.roots) === JSON.stringify(next.roots);
}

export function createFilesWorkspace(options: {
  projectId: string;
  roots: () => ProjectRoot[];
  client: () => LycaonClient | null;
  /** The chat whose workspace to resolve; it is never part of the scope key. */
  chatSessionId: () => string | undefined;
  reachable: () => boolean;
  restoreHotExit: () => boolean;
}) {
  const client = options.client;
  const chatSessionId = options.chatSessionId;
  let previousRootIds: Set<string> | undefined;
  createEffect(() => {
    const current = new Set(options.roots().map((root) => root.id));
    if (previousRootIds) {
      for (const rootId of previousRootIds) {
        if (!current.has(rootId)) {
          dropFilesBuffersForRoot(options.projectId, rootId);
        }
      }
    }
    previousRootIds = current;
  });
  let workspaceRequest = 0;
  let hotExitRestored = false;
  const workspacePresentation = createPresentation<FilesWorkspacePresentation>();
  onCleanup(workspacePresentation.dispose);
  const displayedWorkspace = workspacePresentation.displayed;
  const filesWorkspaceId = createMemo(() => displayedWorkspace()?.workspaceId ?? "");
  const displayedClient = createMemo(() => displayedWorkspace()?.client ?? null);
  /** The chat that addresses this workspace: itself for its own worktree, none for a shared checkout. */
  const filesSourceAddress = createMemo(() => {
    const shown = displayedWorkspace();
    // Before the first answer, the selected chat addresses it.
    return shown ? shown.address : chatSessionId();
  });
  const filesRoots = () =>
    displayedWorkspace()?.roots ?? (chatSessionId() ? [] : options.roots());
  const [workspaceError, setWorkspaceError] = createSignal<string | null>(null);
  const [workspaceFault, setWorkspaceFault] = createSignal<string | null>(null);
  const [workspaceSettled, setWorkspaceSettled] = createSignal(false);
  // Watch gaps explain why outside edits may be missing.
  const [watchCoverage, setWatchCoverage] = createSignal<
    Record<string, SourceWatchCoverage>
  >({});
  const recordWatchCoverage = (roots: readonly SourceWorkspaceRoot[]) => {
    setWatchCoverage(
      Object.fromEntries(roots.map((root) => [root.id.trim(), root.watch])),
    );
  };
  const refreshWatchCoverage = async () => {
    const c = client();
    if (!c || !options.reachable()) return;
    const sessionId = untrack(chatSessionId);
    const request = workspaceRequest;
    const workspace = await c.getSourceWorkspace(options.projectId, sessionId);
    if (request !== workspaceRequest || c !== client() || sessionId !== untrack(chatSessionId)) return;
    recordWatchCoverage(workspace.roots);
  };
  const observationNoticeFor = (buffer: FileBuffer): string | null => {
    if (buffer.jobId || isComposedBufferKind(buffer.kind)) return null;
    const coverage = watchCoverage()[buffer.rootId];
    if (!coverage) return null;
    switch (coverage.state) {
      case "live":
        return coverage.policy_unwatched ? "Some folders are refreshed on demand" : null;
      case "partial":
        return "Outside changes in some folders are not watched";
      case "faulted":
        return "Watching for outside changes stopped; reopen the file to refresh";
      case "unwatched":
        return "Outside changes to this file are not being watched yet";
    }
  };
  const [workspaceRefreshRevision, setWorkspaceRefreshRevision] = createSignal(0);
  createEffect(() => {
    const c = client();
    if (!c) return;
    onCleanup(observeWorkspaceInvalidation(c, options.projectId, chatSessionId, () => {
      setWorkspaceRefreshRevision((revision) => revision + 1);
    }));
  });
  const sourceRootsIdentity = createMemo(() =>
    options.roots()
      .map(
        (root) =>
          `${root.id}\u0000${root.path}\u0000${root.kind}\u0000${root.is_primary}\u0000${root.label}`,
      )
      .join("\u0001"),
  );
  const refreshProjectWorkspace = (workspaceId: string): boolean => {
    const nextWorkspace = workspaceId.trim();
    if (!nextWorkspace) return false;
    if (filesWorkspaceId() === nextWorkspace) return false;
    workspaceRequest += 1;
    setWorkspaceError(null);
    setTreeSettled(false);
    setWorkspaceSettled(false);
    setWorkspaceRefreshRevision((revision) => revision + 1);
    return true;
  };
  const [treeSettled, setTreeSettled] = createSignal(options.roots().length === 0);
  createResidentActivity(() => {
    createEffect(() => {
      const c = client();
      const reachable = options.reachable();
      const sessionId = chatSessionId();
      const scopeKey = JSON.stringify([options.projectId, sourceRootsIdentity()]);
      workspaceRefreshRevision();
      const request = ++workspaceRequest;
      const candidate = untrack(workspacePresentation.begin);
      if (!reachable) {
        if (!sessionId) {
          candidate.publish({
            client: c,
            scopeKey,
            workspaceId: "",
            roots: options.roots(),
            address: undefined,
          });
        }
        setTreeSettled(true);
        setWorkspaceSettled(filesRoots().length > 0 || options.roots().length === 0);
        return;
      }
      if (!c) {
        setTreeSettled(true);
        setWorkspaceSettled(filesWorkspaceId().length > 0 || options.roots().length === 0);
        return;
      }
      const current = untrack(displayedWorkspace);
      const retainWorkspace = current?.client === c && current.scopeKey === scopeKey;
      if (!retainWorkspace) {
        setTreeSettled(false);
        setWorkspaceSettled(false);
      }
      setWorkspaceFault(null);
      let disposed = false;
      let retryTimer: ReturnType<typeof setTimeout> | undefined;
      // A listing naming another workspace re-resolves a bounded number of times.
      let identityReconciliations = 0;
      onCleanup(() => {
        disposed = true;
        if (retryTimer) clearTimeout(retryTimer);
      });
      const resolveWorkspace = (attempt: number) => {
        if (disposed || request !== workspaceRequest) return;
        void c
          .getSourceWorkspace(options.projectId, sessionId)
          .then(async (workspace: SourceWorkspace) => {
            if (disposed || request !== workspaceRequest) return;
            const workspaceId = workspace.workspace_id.trim();
            if (!workspaceId) {
              throw new Error("Files workspace response is missing its identity.");
            }
            const address = workspace.session_scoped ? sessionId : undefined;
            if (filesWorkspaceId() !== workspaceId) {
              setWorkspaceSettled(false);
              setTreeSettled(false);
            }
            const resolvedRoots = resolveSourceWorkspaceRoots(
              options.roots(),
              workspace.roots,
            );
            recordWatchCoverage(workspace.roots);
            await prepareSourceTreeWorkspace({
              projectId: options.projectId,
              workspaceId,
              rootIds: resolvedRoots.map((root) => root.id),
              browse: (rootId, dir) =>
                c.browseProjectSource(
                  options.projectId,
                  { rootId, dir },
                  address,
                ),
            });
            if (disposed || request !== workspaceRequest) return;
            if (!(await switchFilesRootBranches(options.projectId,
              Object.fromEntries(workspace.roots.map((root) => [root.id, root.branch_id ?? ""])),
              () => !disposed && request === workspaceRequest))) {
              if (disposed || request !== workspaceRequest) return;
              setWorkspaceError(WORKSPACE_SYNC_RETRYING);
              retryTimer = setTimeout(() => resolveWorkspace(attempt + 1), 1000);
              return;
            }
            const answer: FilesWorkspacePresentation = {
              client: c,
              scopeKey,
              workspaceId,
              roots: resolvedRoots,
              address,
            };
            const published = samePresentation(untrack(displayedWorkspace), answer)
              ? candidate.retain()
              : candidate.publish(answer);
            if (!published) return;
            // A tree settles itself once its first rows or a preparing view paint; no roots means no tree.
            batch(() => {
              setWorkspaceError(null);
              setWorkspaceFault(null);
              if (resolvedRoots.length === 0) setTreeSettled(true);
              if (options.restoreHotExit() !== false && !hotExitRestored) {
                hotExitRestored = true;
                restoreFilesHotExitForProject(options.projectId, resolvedRoots);
              }
              setWorkspaceSettled(true);
            });
          })
          .catch((err: unknown) => {
            if (disposed || request !== workspaceRequest) return;
            setWorkspaceSettled(false);
            if (
              workspaceMismatchId(err) &&
              identityReconciliations < WORKSPACE_IDENTITY_RECONCILE_LIMIT
            ) {
              identityReconciliations += 1;
              resolveWorkspace(attempt + 1);
              return;
            }
            if (workspaceLookupRetryable(err)) {
              setWorkspaceError(WORKSPACE_SYNC_RETRYING);
              retryTimer = setTimeout(
                () => resolveWorkspace(attempt + 1),
                Math.min(15_000, 1000 * 2 ** Math.min(attempt, 4)),
              );
              return;
            }
            setWorkspaceError(null);
            setWorkspaceFault(workspaceFaultMessage(err));
            reportProjectNoticeError(err, options.projectId);
          });
      };
      resolveWorkspace(0);
    });
  });

  const [listingConnection, setListingConnection] = createSignal<SourceTreeConnection>();
  const [listingRevision, setListingRevision] = createSignal(0);
  createEffect(() => {
    const c = displayedClient();
    const workspaceId = filesWorkspaceId();
    const address = filesSourceAddress();
    if (!c || !workspaceId) return;
    const connection = connectSourceTreeWorkspace({
      projectId: options.projectId,
      workspaceId,
      browse: (rootId, dir) => c.browseProjectSource(options.projectId, { rootId, dir }, address),
    });
    // Read the same projection as the tree without adding watched directories.
    setListingConnection(connection);
    const unsubscribe = connection.subscribe(() => setListingRevision((revision) => revision + 1));
    onCleanup(() => {
      unsubscribe();
      connection.disconnect();
      setListingConnection(undefined);
    });
  });
  const observeDirectory = (rootId: string, dir: string) => {
    const connection = listingConnection();
    let active = true;
    connection?.observe([`${rootId}\0${dir}`], true);
    void connection?.load(rootId, dir).catch(error => {
      if (active) reportProjectNoticeError(error, options.projectId);
    });
    return () => { active = false; connection?.observe([], false); };
  };
  const directoryEntries = (rootId: string, dir: string) => {
    listingRevision();
    if (!workspaceSettled()) return undefined;
    const snapshot = listingConnection()?.get(rootId, dir);
    return snapshot && !snapshot.stale ? snapshot.listing.entries : undefined;
  };
  /** Host-confirmed lifecycle results reach listings before their watcher event. */
  const confirmDirectoryChange = (change: SourceChange) => listingConnection()?.confirm(change);

  return {
    directoryEntries, confirmDirectoryChange, observeDirectory, filesWorkspaceId, filesSourceAddress, filesRoots, workspaceError, workspaceFault, workspaceSettled,
    treeSettled, setTreeSettled, refreshProjectWorkspace, refreshWatchCoverage, observationNoticeFor
  };
}
