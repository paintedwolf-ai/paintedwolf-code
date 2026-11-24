import type { LycaonClient } from "../../api/client.ts";
import { loadComparisonSnapshot, type ComparisonSnapshot } from "../../api/source-reader.ts";
import type {
  FileEditReference,
  GitDiffStatItem,
  GitFileEntry,
  NavigationReference,
  NavigationTarget,
  ProjectRoot,
  SourceChangeOp,
  SourceComparisonSelector,
} from "../../api/types.ts";
import type { FileEditFold } from "../../chat/file-edit/file-edit-fold.ts";
import type { LocalPathTarget } from "../../platform/navigation/open-local-path.ts";
import { openFilesSurface } from "../../platform/navigation/open-files-surface.ts";
import { openSourceLocation, type OpenSourceLocationResult } from "../../platform/navigation/open-source.ts";
import { absolutePathForBuffer } from "../components/project-files-model.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { isComposedBufferKind } from "../documents/project-files-buffer-kind.ts";
import { fileVersionFromComparison, type FileVersionView } from "../history/file-version.ts";
import { resolveCheckoutRootPath, worktreeIdFromBranchId } from "./files-checkout-scope.ts";
import { outsideHostPath } from "./outside-namespace.ts";

type DiffSideKind = "before" | "after";
type GitDiffStageKind = "working" | "staged" | "unstaged";

type VerifiedLiveFileOpenTarget = {
  kind: "live";
  projectId: string;
  repoId?: string;
  worktreeId?: string;
  rootId?: string;
  path: string;
  fileId?: string;
  workerId?: string;
  entryKind?: "file" | "folder" | "unknown";
  line?: number;
  column?: number;
  endLine?: number;
  selectCheckout?: boolean;
};

type HistoricalRevisionBinding =
  | {
      kind: "git-blobs";
      rootId: string;
      beforeBlobOid: string;
      afterBlobOid: string;
      displayPath: string;
      beforePath?: string;
    }
  | {
      kind: "git-stage";
      rootId: string;
      worktreeId?: string;
      stage: GitDiffStageKind;
      path: string;
      beforePath?: string;
      beforeBlobOid?: string;
      expectedHead?: string;
      expectedAfterSha256?: string;
    }
  | {
      kind: "effect";
      effectId: string;
    }
  | {
      kind: "version";
      versionId: string;
    }
  | {
      kind: "chat-edit";
      sessionId: string;
      first: FileEditReference;
      last: FileEditReference;
      beforeSha256: string;
      afterSha256: string;
    }
  | {
      kind: "unavailable";
      reason: string;
    };

type VerifiedHistoricalDiffOpenTarget = {
  kind: "historical-diff";
  projectId: string;
  repoId?: string;
  worktreeId?: string;
  rootId: string;
  path: string;
  beforePath?: string;
  side?: DiffSideKind;
  revision?: string;
  op?: SourceChangeOp;
  title?: string;
  detail?: string;
  binding: HistoricalRevisionBinding;
};

type AmbiguousLegacyFileOpenTarget = {
  kind: "ambiguous";
  projectId: string;
  path: string;
  candidates: readonly NavigationTarget[];
};

type VerifiedFileOpenTarget =
  | VerifiedLiveFileOpenTarget
  | VerifiedHistoricalDiffOpenTarget
  | AmbiguousLegacyFileOpenTarget;

export function fileOpenTarget(
  buffer: Pick<FileBuffer, "kind" | "rootId" | "path" | "jobId" | "sourcePresent"> &
    Partial<Pick<FileBuffer, "worktreeId" | "branchId" | "checkoutUnavailable">>,
  roots: readonly Pick<ProjectRoot, "id" | "path">[],
  line?: number,
  outsideRootId?: string,
  projectId?: string,
): LocalPathTarget | null {
  if (isComposedBufferKind(buffer.kind)) return null;
  if (outsideRootId && buffer.rootId === outsideRootId) {
    return {
      absolutePath: outsideHostPath(buffer.path),
      projectRoots: roots.map((candidate) => candidate.path),
      entryKind: "file",
      line,
      unavailable: buffer.sourcePresent ? undefined : "This file is not present on disk.",
    };
  }
  const root = roots.find((candidate) => candidate.id === buffer.rootId);
  const worktreeId = buffer.worktreeId?.trim() || worktreeIdFromBranchId(buffer.branchId);
  const resolvedRootPath = root && projectId && worktreeId
    ? resolveCheckoutRootPath(projectId, buffer.rootId, worktreeId, root.path) ?? root.path
    : root?.path;
  const baseRoots = roots.map((candidate) => candidate.path);
  const projectRoots = resolvedRootPath && !baseRoots.includes(resolvedRootPath)
    ? [...baseRoots, resolvedRootPath]
    : baseRoots;
  return {
    absolutePath: resolvedRootPath ? absolutePathForBuffer(resolvedRootPath, buffer.path) : buffer.path,
    projectRoots,
    entryKind: "file",
    line,
    unavailable: buffer.jobId
      ? "This action requires the worker workspace location."
      : buffer.checkoutUnavailable
        ? "This checkout is unavailable on disk."
        : !root
          ? "The containing folder is unavailable."
          : !buffer.sourcePresent
            ? "This file is not present on disk."
            : undefined,
  };
}

function gitChangeNavigationTarget(args: {
  projectId: string;
  repoId?: string;
  worktreeId?: string;
  file: Pick<GitFileEntry, "path" | "root_id" | "root_relative_path"> & Partial<GitFileEntry>;
}): NavigationTarget | null {
  const projectId = args.projectId.trim();
  const rootId = args.file.root_id?.trim() ?? "";
  const path = (args.file.root_relative_path || args.file.path).trim();
  if (!projectId || !rootId || !path) return null;
  return {
    project_id: projectId,
    root_id: rootId,
    path,
    entry_kind: "file",
    ...(args.repoId?.trim() ? { repo_id: args.repoId.trim() } : {}),
    ...(args.worktreeId?.trim() ? { worktree_id: args.worktreeId.trim() } : {}),
  };
}

export function gitChangeOpenTarget(args: {
  projectId: string;
  repoId?: string;
  worktreeId?: string;
  file: Pick<GitFileEntry, "path" | "root_id" | "root_relative_path"> & Partial<GitFileEntry>;
  selectCheckout?: boolean;
}): VerifiedLiveFileOpenTarget | null {
  const target = gitChangeNavigationTarget(args);
  if (!target) return null;
  return {
    kind: "live",
    projectId: target.project_id,
    rootId: target.root_id,
    path: target.path,
    entryKind: "file",
    ...(target.repo_id ? { repoId: target.repo_id } : {}),
    ...(target.worktree_id ? { worktreeId: target.worktree_id } : {}),
    ...(args.selectCheckout ?? Boolean(target.worktree_id) ? { selectCheckout: true } : {}),
  };
}

export function gitDiffSideOpenTarget(args: {
  projectId: string;
  repoId?: string;
  worktreeId?: string;
  stage: GitDiffStageKind;
  side: DiffSideKind;
  item: GitDiffStatItem;
  headSha?: string;
  mode?: "historical" | "live";
}): VerifiedFileOpenTarget | null {
  const projectId = args.projectId.trim();
  if (!projectId) return null;
  const afterRootId = args.item.root_id?.trim() ?? "";
  const afterPath = (args.item.root_relative_path || args.item.path).trim();
  const beforeRootId = (args.item.old_root_id?.trim() || afterRootId);
  const beforePath = (args.item.old_root_relative_path || args.item.old_path || afterPath).trim();
  const rootId = args.side === "before" ? beforeRootId : afterRootId;
  const displayPath = args.side === "before" ? beforePath : afterPath;
  if (!rootId || !displayPath) return null;

  const repoId = args.repoId?.trim() || undefined;
  const worktreeId = args.worktreeId?.trim() || undefined;

  if (args.mode === "live") {
    return {
      kind: "live",
      projectId,
      rootId: afterRootId || rootId,
      path: afterPath || displayPath,
      entryKind: "file",
      ...(repoId ? { repoId } : {}),
      ...(worktreeId ? { worktreeId, selectCheckout: true } : {}),
    };
  }

  const beforeOid = args.item.before_blob_oid?.trim() ?? "";
  const afterOid = args.item.after_blob_oid?.trim() ?? "";
  const headSha = args.headSha?.trim() || undefined;
  const renamed = beforePath !== afterPath ? beforePath : undefined;

  if (args.stage === "staged") {
    const revision = args.side === "before" ? beforeOid || headSha : afterOid || headSha;
    return {
      kind: "historical-diff",
      projectId,
      rootId,
      path: displayPath,
      ...(renamed ? { beforePath: renamed } : {}),
      side: args.side,
      ...(repoId ? { repoId } : {}),
      ...(worktreeId ? { worktreeId } : {}),
      ...(revision ? { revision } : {}),
      binding: {
        kind: "git-blobs",
        rootId,
        beforeBlobOid: beforeOid,
        afterBlobOid: afterOid,
        displayPath,
        ...(renamed ? { beforePath: renamed } : {}),
      },
    };
  }

  if (args.side === "before" && beforeOid) {
    return {
      kind: "historical-diff",
      projectId,
      rootId,
      path: displayPath,
      ...(renamed ? { beforePath: renamed } : {}),
      side: "before",
      revision: beforeOid,
      ...(repoId ? { repoId } : {}),
      ...(worktreeId ? { worktreeId } : {}),
      binding: {
        kind: "git-blobs",
        rootId,
        beforeBlobOid: args.item.after_sha256 === undefined && afterOid ? beforeOid : beforeOid,
        afterBlobOid: afterOid || beforeOid,
        displayPath,
        ...(renamed ? { beforePath: renamed } : {}),
      },
    };
  }

  if (afterOid) {
    return {
      kind: "historical-diff",
      projectId,
      rootId,
      path: displayPath,
      ...(renamed ? { beforePath: renamed } : {}),
      side: args.side,
      revision: afterOid,
      ...(repoId ? { repoId } : {}),
      ...(worktreeId ? { worktreeId } : {}),
      binding: {
        kind: "git-blobs",
        rootId,
        beforeBlobOid: beforeOid,
        afterBlobOid: afterOid,
        displayPath,
        ...(renamed ? { beforePath: renamed } : {}),
      },
    };
  }

  if (args.item.after_sha256 === undefined) {
    return {
      kind: "historical-diff",
      projectId,
      rootId,
      path: displayPath,
      side: args.side,
      ...(repoId ? { repoId } : {}),
      ...(worktreeId ? { worktreeId } : {}),
      binding: {
        kind: "unavailable",
        reason: "Historical working-tree bytes were not captured for this diff.",
      },
    };
  }

  return {
    kind: "historical-diff",
    projectId,
    rootId,
    path: afterPath || displayPath,
    ...(renamed ? { beforePath: renamed } : {}),
    side: args.side,
    ...(headSha ? { revision: headSha } : {}),
    ...(repoId ? { repoId } : {}),
    ...(worktreeId ? { worktreeId } : {}),
    binding: {
      kind: "git-stage",
      rootId: afterRootId || rootId,
      ...(worktreeId ? { worktreeId } : {}),
      stage: args.stage,
      path: afterPath || displayPath,
      ...(renamed ? { beforePath: renamed } : {}),
      ...(beforeOid ? { beforeBlobOid: beforeOid } : {}),
      ...(headSha ? { expectedHead: headSha } : {}),
      expectedAfterSha256: args.item.after_sha256,
    },
  };
}

export function activityFileOpenTarget(args: {
  projectId: string;
  mode: "live" | "historical-diff";
  path?: string;
  rootId?: string;
  line?: number;
  endLine?: number;
  locations?: readonly NavigationTarget[];
  rootRefs?: readonly { id: string; label?: string }[];
  sessionId?: string;
  fold?: FileEditFold;
  revisionIndex?: number | null;
  effectId?: string;
  versionId?: string;
}): VerifiedFileOpenTarget | null {
  const projectId = args.projectId.trim();
  if (!projectId) return null;
  const locations = (args.locations ?? []).filter((loc) => loc.project_id === projectId);

  if (args.mode === "historical-diff") {
    const step = args.fold
      ? args.revisionIndex == null
        ? args.fold.steps.at(-1)
        : args.fold.steps[args.revisionIndex]
      : undefined;
    const firstStep = args.fold
      ? args.revisionIndex == null
        ? args.fold.steps[0]
        : step
      : undefined;
    const target = step?.navigationTarget ?? (locations.length === 1 ? locations[0] : undefined);
    const rootId = (step?.snapshot.root_id || target?.root_id || args.rootId || "").trim();
    const path = (step?.snapshot.path || target?.path || args.path || "").trim();
    if (!path) return null;

    if (args.sessionId && firstStep && step) {
      return {
        kind: "historical-diff",
        projectId,
        rootId,
        path,
        ...(target?.repo_id ? { repoId: target.repo_id } : {}),
        ...(target?.worktree_id ? { worktreeId: target.worktree_id } : {}),
        binding: {
          kind: "chat-edit",
          sessionId: args.sessionId,
          first: firstStep.snapshot.reference,
          last: step.snapshot.reference,
          beforeSha256: firstStep.snapshot.before_sha256,
          afterSha256: step.snapshot.after_sha256,
        },
      };
    }
    if (args.effectId?.trim()) {
      return {
        kind: "historical-diff",
        projectId,
        rootId,
        path,
        ...(target?.repo_id ? { repoId: target.repo_id } : {}),
        ...(target?.worktree_id ? { worktreeId: target.worktree_id } : {}),
        revision: args.effectId.trim(),
        binding: { kind: "effect", effectId: args.effectId.trim() },
      };
    }
    if (args.versionId?.trim()) {
      return {
        kind: "historical-diff",
        projectId,
        rootId,
        path,
        ...(target?.repo_id ? { repoId: target.repo_id } : {}),
        ...(target?.worktree_id ? { worktreeId: target.worktree_id } : {}),
        revision: args.versionId.trim(),
        binding: { kind: "version", versionId: args.versionId.trim() },
      };
    }
    return {
      kind: "historical-diff",
      projectId,
      rootId,
      path,
      binding: {
        kind: "unavailable",
        reason: locations.length > 1
          ? "This historical event matches multiple locations and has no retained revision."
          : "No retained revision was recorded for this historical activity.",
      },
    };
  }

  if (locations.length === 1) {
    const loc = locations[0]!;
    return {
      kind: "live",
      projectId,
      rootId: loc.root_id,
      path: loc.path,
      fileId: loc.file_id,
      workerId: loc.worker_id,
      entryKind: loc.entry_kind,
      line: args.line,
      endLine: args.endLine,
      ...(loc.repo_id ? { repoId: loc.repo_id } : {}),
      ...(loc.worktree_id ? { worktreeId: loc.worktree_id, selectCheckout: true } : {}),
    };
  }
  if (locations.length > 1) {
    const requestedPath = args.path?.trim() ?? "";
    const norm = requestedPath.replace(/\\/g, "/");
    const exact = requestedPath
      ? locations.filter((loc) => {
          if (loc.path === norm || norm.endsWith(`/${loc.path}`)) return true;
          const rootLabel = args.rootRefs?.find((r) => r.id === loc.root_id)?.label;
          return Boolean(rootLabel && norm === `@${rootLabel}/${loc.path}`);
        })
      : locations;
    if (exact.length === 1) {
      const loc = exact[0]!;
      return {
        kind: "live",
        projectId,
        rootId: loc.root_id,
        path: loc.path,
        fileId: loc.file_id,
        workerId: loc.worker_id,
        entryKind: loc.entry_kind,
        line: args.line,
        endLine: args.endLine,
        ...(loc.repo_id ? { repoId: loc.repo_id } : {}),
        ...(loc.worktree_id ? { worktreeId: loc.worktree_id, selectCheckout: true } : {}),
      };
    }
    return {
      kind: "ambiguous",
      projectId,
      path: requestedPath || locations[0]!.path,
      candidates: exact.length > 1 ? exact : locations,
    };
  }

  const rawPath = (args.path ?? "").trim();
  if (!rawPath) return null;
  return {
    kind: "live",
    projectId,
    ...(args.rootId?.trim() ? { rootId: args.rootId.trim() } : {}),
    path: rawPath,
    line: args.line,
    endLine: args.endLine,
  };
}

/** Resolves bare relative paths on legacy events across attached roots and registered checkouts. */
export async function resolveLegacyLiveCandidates(args: {
  client: Pick<LycaonClient, "browseProjectSource"> & Partial<Pick<LycaonClient, "listGitRepos" | "listGitWorktrees">>;
  projectId: string;
  roots: readonly Pick<ProjectRoot, "id" | "path">[];
  path: string;
  sessionId?: string;
}): Promise<NavigationReference> {
  const projectId = args.projectId.trim();
  const relPath = args.path.trim().replace(/^\/+/, "");
  const slash = relPath.lastIndexOf("/");
  const dir = slash < 0 ? "." : relPath.slice(0, slash);
  const leaf = slash < 0 ? relPath : relPath.slice(slash + 1);
  const candidates: NavigationTarget[] = [];
  const seen = new Set<string>();

  const pushCandidate = (target: NavigationTarget) => {
    const key = `${target.root_id}\0${target.worktree_id ?? ""}\0${target.path}`;
    if (seen.has(key)) return;
    seen.add(key);
    candidates.push(target);
  };

  const baseRef = {
    id: "legacy",
    syntax: "link",
    mention: relPath || ".",
    explicit: false,
    project_id: projectId,
    path: relPath,
  } as const;

  if (!("browseProjectSource" in args.client) || typeof args.client.browseProjectSource !== "function") {
    return {
      ...baseRef,
      status: "missing",
    };
  }
  let repoWorktrees: { repoId: string; worktreeId: string; isProjectCheckout: boolean }[] = [];
  if ("listGitRepos" in args.client && typeof args.client.listGitRepos === "function" &&
      "listGitWorktrees" in args.client && typeof args.client.listGitWorktrees === "function") {
    try {
      const repos = await args.client.listGitRepos(projectId, args.sessionId);
      for (const repo of repos.repos) {
        if (!repo.available || !repo.repo_id) continue;
        const view = await args.client.listGitWorktrees(projectId, repo.repo_id, args.sessionId);
        for (const wt of view.worktrees) {
          if (!wt.available) continue;
          repoWorktrees.push({
            repoId: repo.repo_id,
            worktreeId: wt.worktree_id,
            isProjectCheckout: wt.is_project_checkout === true,
          });
        }
      }
    } catch {
      repoWorktrees = [];
    }
  }

  if (repoWorktrees.length > 0) {
    for (const root of args.roots) {
      for (const wt of repoWorktrees) {
        try {
          const listing = await args.client.browseProjectSource(
            projectId,
            { rootId: root.id, dir },
            args.sessionId,
            wt.worktreeId,
          );
          const match = listing.entries.find((entry) => entry.name === leaf);
          if (match) {
            pushCandidate({
              project_id: projectId,
              root_id: root.id,
              repo_id: wt.repoId,
              worktree_id: wt.worktreeId,
              path: relPath,
              entry_kind: match.is_dir ? "folder" : "file",
            });
          }
        } catch {
          // Unmapped root for this checkout.
        }
      }
    }
  }

  if (candidates.length === 0) {
    for (const root of args.roots) {
      try {
        const listing = await args.client.browseProjectSource(projectId, { rootId: root.id, dir }, args.sessionId);
        const match = listing.entries.find((entry) => entry.name === leaf);
        if (match) {
          pushCandidate({
            project_id: projectId,
            root_id: root.id,
            path: relPath,
            entry_kind: match.is_dir ? "folder" : "file",
          });
        }
      } catch {
        // Root unavailable.
      }
    }
  }

  if (candidates.length === 1) {
    const only = candidates[0]!;
    return {
      ...baseRef,
      status: "resolved",
      root_id: only.root_id,
      path: only.path,
      entry_kind: only.entry_kind,
      ...(only.repo_id ? { repo_id: only.repo_id } : {}),
      ...(only.worktree_id ? { worktree_id: only.worktree_id } : {}),
      candidates,
    };
  }
  if (candidates.length > 1) {
    return {
      ...baseRef,
      status: "ambiguous",
      candidates,
    };
  }
  return {
    ...baseRef,
    status: "missing",
  };
}

function selectorForHistoricalBinding(binding: HistoricalRevisionBinding): {
  selector?: SourceComparisonSelector;
  sessionId?: string;
} {
  switch (binding.kind) {
    case "git-blobs":
      return {
        selector: {
          kind: "blob",
          root_id: binding.rootId,
          before_blob_oid: binding.beforeBlobOid,
          blob_oid: binding.afterBlobOid,
          display_path: binding.displayPath,
        },
      };
    case "git-stage":
      return {
        selector: {
          kind: "commit",
          root_id: binding.rootId,
          path: binding.path,
          stage: binding.stage,
          ...(binding.worktreeId ? { worktree_id: binding.worktreeId } : {}),
          ...(binding.beforePath ? { before_path: binding.beforePath } : {}),
          ...(binding.beforeBlobOid ? { before_blob_oid: binding.beforeBlobOid } : {}),
          ...(binding.expectedHead !== undefined ? { expected_head: binding.expectedHead } : {}),
          ...(binding.expectedAfterSha256 !== undefined ? { expected_after_sha256: binding.expectedAfterSha256 } : {}),
        },
      };
    case "effect":
      return { selector: { kind: "effect", effect_id: binding.effectId } };
    case "version":
      return { selector: { kind: "version", version_id: binding.versionId } };
    case "chat-edit":
      return {
        sessionId: binding.sessionId,
        selector: {
          kind: "chat",
          first: binding.first,
          last: binding.last,
          expected_before_sha256: binding.beforeSha256,
          expected_after_sha256: binding.afterSha256,
        },
      };
    case "unavailable":
      return {};
  }
}

function unavailableHistoricalVersionView(
  target: Pick<VerifiedHistoricalDiffOpenTarget, "rootId" | "path" | "op" | "revision">,
  reason: string,
): FileVersionView {
  return {
    source: { kind: "unavailable" },
    versionId: target.revision ?? "",
    fileId: "",
    rootId: target.rootId,
    path: target.path,
    op: target.op,
    ts: "",
    beforeAvailability: "unavailable",
    beforeReason: reason,
    sha256: null,
    sizeBytes: 0,
    availability: "unavailable",
    reason,
    initialComparison: "before",
  };
}

type OpenVerifiedFileTargetResult =
  | { status: "opened-live"; result: OpenSourceLocationResult }
  | { status: "opened-historical"; version: FileVersionView; snapshot?: ComparisonSnapshot }
  | { status: "ambiguous"; candidates: readonly NavigationTarget[] };

export async function openVerifiedFileTarget(
  target: VerifiedFileOpenTarget,
  options: {
    client?: LycaonClient | null;
    sessionId?: string;
  } = {},
): Promise<OpenVerifiedFileTargetResult> {
  if (target.kind === "ambiguous") {
    return { status: "ambiguous", candidates: target.candidates };
  }
  if (target.kind === "live") {
    const result = await openSourceLocation(
      {
        projectId: target.projectId,
        rootId: target.rootId,
        path: target.path,
        fileId: target.fileId,
        jobId: target.workerId,
        repoId: target.repoId,
        worktreeId: target.worktreeId,
        selectCheckout: target.selectCheckout ?? Boolean(target.worktreeId),
        entryKind: target.entryKind,
        line: target.line,
        column: target.column,
        endLine: target.endLine,
        intent: "permanent",
      },
      options.client ? { client: options.client } : undefined,
    );
    return { status: "opened-live", result };
  }

  const { selector, sessionId } = selectorForHistoricalBinding(target.binding);
  if (!selector || !options.client) {
    const reason = target.binding.kind === "unavailable"
      ? target.binding.reason
      : "The host is unavailable to read this historical revision.";
    const version = unavailableHistoricalVersionView(target, reason);
    openFilesSurface({
      kind: "historical-diff",
      projectId: target.projectId,
      rootId: target.rootId,
      path: target.path,
      previewId: `${target.rootId}:${target.path}:${target.revision ?? "unavailable"}`,
      title: target.title,
      detail: target.detail ?? reason,
      version,
    });
    return { status: "opened-historical", version };
  }

  try {
    const snapshot = await loadComparisonSnapshot(
      options.client,
      target.projectId,
      selector,
      sessionId ?? options.sessionId,
    );
    const version = fileVersionFromComparison(
      {
        versionId: target.revision ?? "",
        fileId: "",
        rootId: target.rootId,
        path: target.path,
        op: snapshot.op ?? target.op ?? "write",
        ts: "",
        initialComparison: "before",
      },
      snapshot,
    ) ?? unavailableHistoricalVersionView(target, "This historical revision is no longer available.");
    openFilesSurface({
      kind: "historical-diff",
      projectId: target.projectId,
      rootId: target.rootId,
      path: target.path,
      previewId: `${target.rootId}:${target.path}:${target.revision ?? snapshot.reference?.view.id ?? "diff"}`,
      title: target.title,
      detail: target.detail,
      version,
    });
    return { status: "opened-historical", version, snapshot };
  } catch (error) {
    const reason = error instanceof Error ? error.message : "This historical revision is unavailable.";
    const version = unavailableHistoricalVersionView(target, reason);
    openFilesSurface({
      kind: "historical-diff",
      projectId: target.projectId,
      rootId: target.rootId,
      path: target.path,
      previewId: `${target.rootId}:${target.path}:${target.revision ?? "unavailable"}`,
      title: target.title,
      detail: target.detail ?? reason,
      version,
    });
    return { status: "opened-historical", version };
  }
}
