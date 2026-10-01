import type { Setter } from "solid-js";
import type { DirState } from "./files-tree-context.ts";
import type { FilesTreeScrollRestoration } from "./files-tree-scrolling.ts";
import { restoredFilesTreeDisclosures } from "./files-tree-intent-state.ts";
import type { SourceTreeCommand } from "../../api/types.ts";
import { retainFilesTreeSession, type FilesTreeSession } from "./files-tree-paged-session.ts";
import { FilesTreePathAbsentError, type TreePresentation } from "./files-tree-navigation.ts";
import { reportSurfaceFailure } from "../../notices/surface-failure.ts";
import { createDeferred, createEffect, createMemo, createSignal, onCleanup, untrack } from "solid-js";
import { scrollportMotionForHost } from "../../platform/scrolling/scrollport-motion.ts";
import { FILES_TREE_ROW_HEIGHT_PX } from "./project-files-tree-flat.ts";
import { getFilesTreeScrollTop } from "./files-tree-view-state.ts";
import { workspaceMismatchId } from "../source/source-workspace-identity.ts";
import { type FilesTreeProps, type ActiveFile } from "./files-tree-context.ts";

function activeFileRevealKey(file: ActiveFile): string | null {
  return file ? `${file.rootId}\0${file.path}\0${file.revision ?? 0}` : null;
}


type SourceProps = Pick<FilesTreeProps, "projectId" | "workspaceId" | "client" | "sessionId" |
  "filterQuery" | "reviewScope" | "activeFile" | "navigationRevision" | "autoReveal" |
  "onWorkspaceMismatch" | "onInitialLoadSettled" | "onSelectEntry">;

const TREE_FAILURE = {
  code: "files_tree_unavailable",
  title: "Files unavailable",
  suggestedAction: "Reopen the Files view to try again.",
};

export function createFilesTreeSource(props: SourceProps, deps: {
  navElement: () => HTMLElement | undefined;
  surfaceLive: () => boolean;
  restoration: FilesTreeScrollRestoration;
  virtualViewportHeight: () => number;
  virtualScrollTop: () => number;
  setVirtualScrollTop: Setter<number>;
  setDirStates: Setter<Record<string, DirState>>;
  presentedTree: () => { workspace: string; presentation: TreePresentation | undefined };
  navigationAim: () => unknown;
  hasNewReaderNavigation: () => boolean;
  cancelFocusNavigation: () => void;
  revealTreePath: (rootId: string, path: string, isDir: boolean, align: "nearest" | "start", signal: AbortSignal, follow?: boolean) => Promise<boolean | undefined>;
}) {
  let revealPathGeneration = 0;
  let seededWorkspaceKey = "";
  const [sourceView, setSourceView] = createSignal<FilesTreeSession>();
  const [viewVersion, setViewVersion] = createSignal(0);
  const state = createMemo(() => { viewVersion(); return sourceView()?.state(); });
  let automaticReveal: AbortController | undefined;
  let explicitReveal: AbortController | undefined;
  const currentFileAim = createMemo(() => activeFileRevealKey(props.activeFile));
  let suppressedAutomaticAim: { workspace: string; revision?: number; target: string | null } | undefined;
  onCleanup(() => explicitReveal?.abort());
  const cancelTreeReveal = () => {
    automaticReveal?.abort(); explicitReveal?.abort();
    suppressedAutomaticAim = untrack(() => ({
      workspace: props.workspaceId,
      revision: props.navigationRevision,
      target: currentFileAim(),
    }));
    const host = deps.navElement()?.closest<HTMLElement>(".den-files-tree-scroll-frame");
    if (host) scrollportMotionForHost(host)?.cancelReveal();
    deps.cancelFocusNavigation();
  };
  /** Tree failures reach the person through the notification stack. */
  const reportTreeError = (error: unknown) => {
    if ((error as { name?: string }).name === "AbortError") return;
    const replacement = workspaceMismatchId(error);
    if (replacement) props.onWorkspaceMismatch?.(replacement);
    else reportSurfaceFailure(TREE_FAILURE, error, props.projectId);
  };
  const runCommand = async (command: SourceTreeCommand) => {
    cancelTreeReveal();
    const view = sourceView();
    if (!view) return;
    await view.command(command, deps.presentedTree().presentation?.id);
  };
  /** A failed view or session names itself once per failure; the notice merges repeats. */
  const observeFailure = () => {
    let reported: unknown;
    createEffect(() => {
      viewVersion();
      const current = state();
      const failed = current?.state === "failed" ? current : undefined;
      const error = failed ? undefined : sourceView()?.error();
      const key = failed ?? error;
      if (!key) { reported = undefined; return; }
      if (key === reported) return;
      reported = key;
      if (failed) {
        reportSurfaceFailure(TREE_FAILURE, failed.failure?.message ?? "The file tree could not be prepared.", props.projectId);
      } else untrack(() => reportTreeError(error));
    });
  };

  function observeSession() {
    createEffect(() => {
      const project = props.projectId;
      const workspace = props.workspaceId;
      const client = props.client;
      const session = props.sessionId;
      if (!project || !workspace || !client) { setSourceView(undefined); return; }
      const held = untrack(() => retainFilesTreeSession(client, project, workspace, session, {
        disclosures: restoredFilesTreeDisclosures(workspace), filter: props.filterQuery, review: props.reviewScope,
      }));
      if (seededWorkspaceKey !== workspace) {
        seededWorkspaceKey = workspace; deps.setDirStates({});
        deps.restoration.pendingScroll = getFilesTreeScrollTop(workspace);
        deps.setVirtualScrollTop(deps.restoration.pendingScroll ?? 0);
        deps.restoration.activeReveal = activeFileRevealKey(untrack(() => props.activeFile));
      }
      setSourceView(held.view);
      const unsubscribe = held.view.subscribe(() => setViewVersion(version => version + 1));
      onCleanup(() => { unsubscribe(); held.release(); });
    });
    createEffect(() => {
      const view = sourceView();
      if (!view || !deps.surfaceLive()) return;
      const detach = view.attach();
      const abort = new AbortController();
      const workspace = props.workspaceId;
      onCleanup(() => { abort.abort(); detach(); });
      // A failed first read still settles: the tree renders its failure.
      void view.ready(abort.signal).then(current => {
        abort.signal.throwIfAborted();
        const height = untrack(deps.virtualViewportHeight);
        const top = Math.min(deps.restoration.pendingScroll ?? untrack(deps.virtualScrollTop),
          Math.max(0, current.extent.rows * FILES_TREE_ROW_HEIGHT_PX - height));
        if (deps.restoration.pendingScroll != null) deps.setVirtualScrollTop(top);
        const start = Math.floor(top / FILES_TREE_ROW_HEIGHT_PX);
        return view.range(start, start + Math.ceil(height / FILES_TREE_ROW_HEIGHT_PX), abort.signal);
      }).catch(reportTreeError).finally(() => {
        if (!abort.signal.aborted) props.onInitialLoadSettled?.(workspace);
      }).catch(() => undefined);
    });
    // Workspace refreshes can finish after the first frame arrives.
    createEffect(() => {
      viewVersion();
      const current = state();
      const presentation = sourceView()?.presentation();
      const hasFrame = presentation?.state?.id === current?.id && !!presentation?.frames.length;
      const empty = current?.state === "ready" && current.extent.rows === 0;
      if (!deps.surfaceLive() || current?.state === "preparing" || current?.state === "failed" || hasFrame || empty) {
        props.onInitialLoadSettled?.(props.workspaceId);
      }
    });
    createEffect(() => {
      const view = sourceView();
      const scope = props.reviewScope;
      if (!view) return;
      untrack(() => {
        void view.open().then(current => {
          if (JSON.stringify(current.intent.review) !== JSON.stringify(scope)) {
            cancelTreeReveal();
            return view.command({ kind: "review", scope });
          }
        }).catch(reportTreeError);
      });
    });
  }

  function observeReveal() {
    onCleanup(() => { revealPathGeneration++; });
    createEffect(() => {
      void deps.navigationAim();
      if (deps.hasNewReaderNavigation()) {
        explicitReveal?.abort();
        deps.restoration.pendingScroll = undefined;
        deps.restoration.activeReveal = null;
        revealPathGeneration++;
        const host = deps.navElement()?.closest<HTMLElement>(".den-files-tree-scroll-frame");
        if (host) scrollportMotionForHost(host)?.cancelReveal();
      }
    });
    const sourceViewIdentity = createMemo(() => state()?.id);
    let preparedTreePath: { view: FilesTreeSession; key: string } | undefined;
    createEffect(() => {
      const target = props.autoReveal === false ? null : props.activeFile;
      const view = sourceView();
      const viewId = sourceViewIdentity();
      const live = deps.surfaceLive();
      const abort = new AbortController();
      automaticReveal = abort;
      onCleanup(() => abort.abort());
      const key = target ? JSON.stringify([props.workspaceId, viewId, target.rootId, target.path, target.revision, props.navigationRevision]) : "";
      if (!live) explicitReveal?.abort();
      if (!target || !view || !viewId || !live) return;
      // Editor presentation can deliver a target after newer tree input took control.
      if (suppressedAutomaticAim?.workspace === props.workspaceId &&
        (suppressedAutomaticAim.revision != null
          ? suppressedAutomaticAim.revision === props.navigationRevision
          : suppressedAutomaticAim.target === activeFileRevealKey(target))) return;
      if (target.revision != null && props.navigationRevision != null && target.revision !== props.navigationRevision) return;
      if (preparedTreePath?.view === view && preparedTreePath.key === key) return;
      explicitReveal?.abort();
      untrack(() => {
        const follow = deps.restoration.pendingScroll == null;
        void deps.revealTreePath(target.rootId, target.path, false, "nearest", abort.signal,
          follow).then(completed => {
          if (completed && !abort.signal.aborted) preparedTreePath = { view, key };
        }).catch(error => {
          // Following the reader's file is best effort when the tree does not list it.
          if (abort.signal.aborted || error instanceof FilesTreePathAbsentError) return;
          console.error("Files tree reveal failed", { projectId: props.projectId, target, activity: view.activity() }, error);
          reportTreeError(error);
        });
      });
    });
  }

  function observeFilter() {
    const deferredFilterQuery = createDeferred(() => props.filterQuery ?? "", { timeoutMs: 120 });
    createEffect(() => {
      const view = sourceView(); const query = deferredFilterQuery();
      if (!view) return;
      untrack(() => { void view.open().then(current => {
        if ((current.intent.filter ?? "") !== query) {
          cancelTreeReveal();
          return view.command({ kind: "filter", query });
        }
      }).catch(reportTreeError); });
    });
      return deferredFilterQuery;
  }
  const scrollToPath = (rootId: string, path: string, isDir: boolean) => {
    deps.restoration.pendingScroll = undefined;
    deps.restoration.activeReveal = null;
    const generation = ++revealPathGeneration;
    const host = deps.navElement()?.closest<HTMLElement>(".den-files-tree-scroll-frame");
    if (host) scrollportMotionForHost(host)?.cancelReveal();
    cancelTreeReveal();
    const abort = new AbortController(); explicitReveal = abort;
    void deps.revealTreePath(rootId, path, isDir, "start", abort.signal).then(completed => {
      if (!completed || abort.signal.aborted || generation !== revealPathGeneration) return;
      props.onSelectEntry({ rootId, path, kind: isDir ? "folder" : "file" });
      const selector = isDir ? ".den-files-tree__label--dir" : ".den-files-tree__label--file";
      const button = [...deps.navElement()?.querySelectorAll<HTMLButtonElement>(selector) ?? []]
        .find(button => button.dataset.root === rootId && button.dataset.path === path);
      button?.focus({ preventScroll: true });
    }).catch(error => {
      if (abort.signal.aborted || generation !== revealPathGeneration) return;
      reportTreeError(error);
    });
  };
  return { sourceView, viewVersion, state,
    cancelTreeReveal, reportTreeError, runCommand,
    observeFailure, observeSession, observeReveal, observeFilter, scrollToPath,
    advanceNavigation: () => ++revealPathGeneration,
    navigationGeneration: () => revealPathGeneration,
  };
}
