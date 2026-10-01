import type { ComparisonSnapshot } from "../../api/source-reader.ts";
import type {
  SecretScreen,
  SourceChangeOp,
  SourceDeletedFile,

  SourceFileCommit,
  SourceFileVersion,
  SourceGitChange,
  SourceGitHistoryState,
  SourceTip,
  SourceWalkEffect,
} from "../../api/types.ts";
import type { DenEditorVersionComparison } from "../../../shared/app-state-types.ts";
import { normalizeEolForEditor } from "../../components/source/editor/eol.ts";
import { sourceContributorLabel } from "../../components/source/annotations/source-contributor-label.ts";
import { commandWindowLine } from "../source/source-command-window.ts";
import { gitChangeRefs, gitChangeTitle } from "../source/source-git-change.ts";
import { comparisonEndpoints } from "../source/source-comparison.ts";
import { sourceContentIsReadable, type ContentSide } from "../source/source-content-availability.ts";

type FileVersionAvailability = NonNullable<
  ComparisonSnapshot["after"]
>["availability"];

export type FileVersionView = {
  source: { kind: "reader"; reader: import("../../api/source-reader.ts").ComparisonReference | import("../../api/types.ts").SourceComparisonSelector } | { kind: "text"; before: string; after: string } | { kind: "unavailable" };
  versionId: string;
  reviewedThroughOrdinal?: number;
  fileId: string;
  rootId: string;
  path: string;
  /** Absent when no recorded effect produced the state. */
  op: SourceChangeOp | undefined;
  /** The git movement that produced this state; absent for non-git states. */
  gitChange?: SourceGitChange | null;
  /** The commit this preview shows; a commit state has no versionId. */
  commit?: SourceFileCommit | null;
  cause?: string;
  ts: string;
  beforeAvailability: FileVersionAvailability;
  beforeReason?: string | null;
  beforeSizeBytes?: number | null;
  sha256: string | null;
  sizeBytes: number;
  availability: FileVersionAvailability;
  reason?: string | null;
  /** Offsets match content after newline normalization. */
  secretScreen?: SecretScreen;
  /** Walk starts on the producing change; history starts on restore impact. */
  initialComparison?: DenEditorVersionComparison;
};

/** The state a version presents: the side after its change. */
export function versionAfterSide(version: FileVersionView): ContentSide {
  return { availability: version.availability, reason: version.reason, sizeBytes: version.sizeBytes };
}

/** The state a version replaced. */
export function versionBeforeSide(version: FileVersionView): ContentSide {
  return { availability: version.beforeAvailability, reason: version.beforeReason, sizeBytes: version.beforeSizeBytes };
}

export type FileVersionHistory = {
  status: "idle" | "loading" | "ready" | "error";
  gitStatus: "idle" | "loading" | "ready" | "error";
  current: SourceTip | null;
  versions: readonly SourceFileVersion[];
  /** One page of the file's git lineage, joined by the host. */
  commits: readonly SourceFileCommit[];
  /** Ref movements that introduced these commits. */
  arrivals: readonly SourceGitChange[];
  /** Latest Git-history result for this file. */
  gitHistoryState: SourceGitHistoryState;
  trackedSince: string | null;
  /** Continues the retained lane; null once it is exhausted. */
  nextVersionsCursor: string | null;
  /** Continues the Git lane; null once it is exhausted. */
  nextGitCursor: string | null;
  loadingMore: boolean;
};

export const emptyFileVersionHistory = (): FileVersionHistory => ({
  status: "idle",
  gitStatus: "idle",
  current: null,
  versions: [],
  commits: [],
  arrivals: [],
  gitHistoryState: "not_requested",
  trackedSince: null,
  nextVersionsCursor: null,
  nextGitCursor: null,
  loadingMore: false,
});

export function fileVersionFromComparison(
  selection: {
    versionId: string;
    fileId: string;
    rootId: string;
    path: string;
    op: SourceChangeOp | undefined;
    cause?: string;
    gitChange?: SourceGitChange | null;
    ts: string;
    initialComparison?: DenEditorVersionComparison;
  },
  diff: ComparisonSnapshot,
): FileVersionView | null {
  const endpoints = comparisonEndpoints(diff);
  // A lens with no effect for this file has no bytes to show.
  if (!endpoints) return null;
  const { before, after } = endpoints;
  return {
    source: diff.reference ? { kind: "reader", reader: diff.reference } : { kind: "unavailable" },
    versionId: selection.versionId.trim(),
    fileId: selection.fileId,
    rootId: selection.rootId,
    path: selection.path,
    op: selection.op == null ? undefined : before.state === "absent" ? "create" : after.state === "absent" ? "delete" : selection.op,
    cause: selection.cause,
    gitChange: selection.gitChange ?? null,
    ts: selection.ts,
    beforeAvailability: before.availability,
    beforeReason: before.reason ?? null,
    beforeSizeBytes: before.size_bytes ?? null,
    sha256: after.sha256?.trim() || null,
    sizeBytes: after.size_bytes,
    availability: after.availability,
    reason: after.reason ?? null,
    ...(after.secret_screen
      ? { secretScreen: after.secret_screen }
      : {}),
    ...(selection.initialComparison
      ? { initialComparison: selection.initialComparison }
      : {}),
  };
}

export function walkVersionFromComparison(
  effect: SourceWalkEffect,
  diff: ComparisonSnapshot,
  gitChange: SourceGitChange | null = null,
): FileVersionView | null {
  return fileVersionFromComparison(
    {
      versionId: effect.after_version_id,
      fileId: effect.file_id,
      rootId: effect.root_id,
      path: effect.path,
      op: effect.op,
      cause: effect.cause,
      gitChange,
      ts: effect.observed_at,
      initialComparison: "before",
    },
    diff,
  );
}

/** Labels the actor, Git movement, or command behind a state. */
export function fileVersionActor(version: SourceFileVersion): string {
  const change = version.git_change;
  if (change) return gitChangeRefs(change) || "Git";
  const command = version.command;
  if (command) return `Command · ${commandWindowLine(command)}`;
  if (version.contributors?.length) {
    return [...new Set(version.contributors.map(sourceContributorLabel))].join("; ");
  }
  const explicit = version.actor_label?.trim();
  if (explicit) return explicit;
  switch (version.origin) {
    case "agent":
      return "AI";
    case "user":
      // Caller attribution requires a recorded contributor.
      return "A person";
    case "external":
      return "External change";
    case undefined:
      return "";
  }
}

/** What happened to reach this state; undefined means no recorded effect. */
export function fileVersionAction(op: SourceChangeOp | undefined): string {
  switch (op) {
    case "create":
      return "Created";
    case "delete":
      return "Deleted";
    case "rename":
      return "Renamed";
    case "write":
      return "Edited";
    case undefined:
      return "Earlier state";
  }
}

export function fileVersionTitle(version: SourceFileVersion): string {
  const change = version.git_change;
  if (change) return gitChangeTitle(change);
  if (isFileVersionRestoreCause(version.cause)) return "Restored";
  return fileVersionAction(version.op);
}

export function fileVersionViewTitle(view: FileVersionView): string {
  if (view.reviewedThroughOrdinal) return "Reviewed changes";
  if (view.commit) return "Committed";
  if (view.gitChange) return gitChangeTitle(view.gitChange);
  if (isFileVersionRestoreCause(view.cause)) return "Restored";
  return fileVersionAction(view.op);
}

export function isFileVersionRestoreCause(cause: string | undefined): boolean {
  return cause === "version_restore" || cause === "git_commit_restore";
}

/** Labels retained worker states by their source branch. */
export function fileVersionBranchLabel(
  version: SourceFileVersion,
): string | null {
  return version.workspace_kind === "worker" ? "Worker branch" : null;
}

/** Names a state whose bytes never reached the working file. */
export function fileVersionLandingLabel(
  version: SourceFileVersion,
): string | null {
  return version.landing === "editor_document" ? "Never saved to disk" : null;
}

export function fileVersionIsReadable(version: FileVersionView): boolean {
  return version.source.kind !== "unavailable" && sourceContentIsReadable(version.availability);
}

/** Projects a proposed snapshot into the version the editor presents. */
export function fileVersionFromSnapshot(selection: {
  rootId: string;
  path: string;
  /** Null identifies an absent file; an empty string is an empty file. */
  before: string | null;
  after: string | null;
}): FileVersionView {
  return {
    versionId: "",
    fileId: "",
    rootId: selection.rootId,
    path: selection.path,
    op: selection.before === null ? "create" : selection.after === null ? "delete" : "write",
    ts: "",
    source: { kind: "text", after: normalizeEolForEditor(selection.after ?? "").text, before: normalizeEolForEditor(selection.before ?? "").text },
    beforeAvailability: selection.before === null ? "absent" : "available",
    sha256: null,
    sizeBytes: new TextEncoder().encode(selection.after ?? "").length,
    availability: selection.after === null ? "absent" : "available",
    initialComparison: "current",
  };
}

/** The deletion view shows retained bytes without making them a working draft. */
export function fileVersionBeforeDeletion(fileId: string, rootId: string, path: string, deleted: SourceDeletedFile): FileVersionView {
  const side = deleted.previous;
  return {
    versionId: side.version_id ?? "", fileId, rootId, path, op: "delete",
    ts: deleted.deleted_at, source: deleted.source ? { kind: "reader", reader: deleted.source } : { kind: "unavailable" }, beforeAvailability: side.availability,
    beforeReason: side.reason ?? null, beforeSizeBytes: side.size_bytes, sha256: side.sha256 ?? null,
    sizeBytes: side.size_bytes, availability: "absent",
    initialComparison: "before",
    ...(side.secret_screen ? { secretScreen: side.secret_screen } : {}),
  };
}
