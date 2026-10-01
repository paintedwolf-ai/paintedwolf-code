import type { LycaonClient } from "../../api/client.ts";
import type { SourceRangeTarget } from "../../api/http-capabilities/source-history.ts";
import type {
  SourceComparison,
  SourceEncoding,
  SourceTip,
} from "../../api/types.ts";
import {
  applyFileUndo,
  fileMutationError,
  type FileMutationOutcome,
  type FileUndo,
} from "../commands/file-mutations.ts";
import { applyHunkReject, type DiffLineHunk } from "../review/line-diff.ts";
import { fileBasename } from "../review/review-model.ts";
import {
  comparisonEndpoints,
  type ComparisonEndpoints,
} from "../source/source-comparison.ts";
import {
  sourceContentIsReadable,
  sourceContentNotice,
} from "../source/source-content-availability.ts";

export function historyUnavailableCopy(diff?: SourceComparison | null): string {
  const unavailable = [diff?.before, diff?.after].find(
    (side) => side && !sourceContentIsReadable(side.availability),
  );
  if (unavailable) {
    const notice = sourceContentNotice({ availability: unavailable.availability, reason: unavailable.reason, sizeBytes: unavailable.size_bytes });
    if (notice) return notice.message;
  }
  return "This comparison has metadata, but its text content is unavailable.";
}

export function isTombstoneDiff(
  endpoints: ComparisonEndpoints,
  tip?: SourceTip | null,
): boolean {
  if (tip?.state === "absent") return true;
  return endpoints.after.state === "absent";
}

export function isCreateDiff(endpoints: ComparisonEndpoints): boolean {
  return endpoints.before.state === "absent";
}

type FileRevertTarget = {
  comparisonTarget?: SourceRangeTarget;
  fileId: string;
  rootId: string;
  path: string;
  baseline: string;
  tip?: SourceTip | null;
  sessionId?: string;
};

export async function planFileRevert(
  client: LycaonClient,
  projectId: string,
  target: FileRevertTarget,
): Promise<
  | { ok: true; member: FileUndo; undoContent: string; label: string }
  | { ok: false; conflict: true }
  | { ok: false; conflict: false; error: string }
> {
  const name = fileBasename(target.path);
  let diff: SourceComparison;
  try {
    diff = await client.getProjectSourceComparison(
      projectId,
      target.comparisonTarget ?? {
        fileId: target.fileId,
        baseline: target.baseline,
      },
      { sessionId: target.sessionId },
    );
  } catch (err) {
    return fileMutationError(err, "Could not load the comparison.");
  }
  const endpoints = comparisonEndpoints(diff);
  if (!endpoints) {
    return {
      ok: false,
      conflict: false,
      error: "No changes to this file in this view.",
    };
  }
  if (endpoints.before.state === "absent" && endpoints.after.state === "absent") {
    return { ok: false, conflict: false, error: "The working file already matches this baseline." };
  }
  if (
    !sourceContentIsReadable(endpoints.before.availability) ||
    !sourceContentIsReadable(endpoints.after.availability)
  ) {
    return { ok: false, conflict: false, error: historyUnavailableCopy(diff) };
  }
  if (target.tip && (target.tip.state !== endpoints.after.state ||
      (target.tip.sha256 && endpoints.after.sha256 && target.tip.sha256 !== endpoints.after.sha256))) {
    return { ok: false, conflict: true };
  }
  if (isTombstoneDiff(endpoints, target.tip)) {
    return {
      ok: true,
      member: {
        kind: "create",
        rootId: target.rootId,
        path: target.path,
        content: endpoints.before.content,
        encoding: "utf-8",
      },
      undoContent: "",
      label: `Reverted ${name}`,
    };
  }
  if (isCreateDiff(endpoints)) {
    return {
      ok: true,
      member: { kind: "delete", rootId: target.rootId, path: target.path },
      undoContent: endpoints.after.content,
      label: `Reverted ${name}`,
    };
  }
  let live;
  try {
    live = await client.getProjectSource(projectId, target.path, {
      rootId: target.rootId || undefined,
      sessionId: target.sessionId,
    });
  } catch (err) {
    return fileMutationError(err, "Could not read the file.");
  }
  const baseSha = live.sha256?.trim() || "";
  if (endpoints.after.sha256 && baseSha !== endpoints.after.sha256) return { ok: false, conflict: true };
  if (!baseSha || !live.encoding) {
    return { ok: false, conflict: false, error: "File is no longer editable." };
  }
  return {
    ok: true,
    member: {
      kind: "put",
      rootId: target.rootId,
      path: target.path,
      content: endpoints.before.content,
      encoding: live.encoding,
      baseSha256: baseSha,
    },
    undoContent: live.content ?? endpoints.after.content,
    label: `Reverted ${name}`,
  };
}

export async function revertFileToBaseline(
  client: LycaonClient,
  projectId: string,
  target: FileRevertTarget,
): Promise<FileMutationOutcome> {
  const plan = await planFileRevert(client, projectId, target);
  if (!plan.ok) return plan;
  if (plan.member.kind === "delete") {
    return applyFileUndo(client, projectId, plan.member, target.sessionId, {
      undoCreate: {
        kind: "create",
        rootId: plan.member.rootId,
        path: plan.member.path,
        content: plan.undoContent,
        encoding: "utf-8",
      },
      label: plan.label,
    });
  }
  if (plan.member.kind === "put") {
    return applyFileUndo(client, projectId, plan.member, target.sessionId, {
      undoContent: plan.undoContent,
      label: plan.label,
    });
  }
  return applyFileUndo(client, projectId, plan.member, target.sessionId, {
    label: plan.label,
  });
}

type HunkRejectTarget = {
  rootId: string;
  path: string;
  hunk: DiffLineHunk;
  sessionId?: string;
  before: string;
  after: string;
  encoding?: SourceEncoding;
  baseSha256?: string;
  tip?: SourceTip | null;
  beforeAbsent?: boolean;
};

export async function rejectHunkInFile(
  client: LycaonClient,
  projectId: string,
  target: HunkRejectTarget,
): Promise<FileMutationOutcome> {
  const name = fileBasename(target.path);
  if (target.tip?.state === "absent") {
    return applyFileUndo(
      client,
      projectId,
      {
        kind: "create",
        rootId: target.rootId,
        path: target.path,
        content: applyHunkReject(target.after, target.before, target.hunk) ||
          target.before,
        encoding: target.encoding ?? "utf-8",
      },
      target.sessionId,
      { label: `Rejected hunk in ${name}` },
    );
  }
  let encoding = target.encoding;
  let baseSha = target.baseSha256?.trim() || "";
  let disk = target.after;
  if (!encoding || !baseSha) {
    try {
      const live = await client.getProjectSource(projectId, target.path, {
        rootId: target.rootId || undefined,
        sessionId: target.sessionId,
      });
      encoding = live.encoding;
      baseSha = live.sha256?.trim() || "";
      disk = live.content ?? disk;
    } catch (err) {
      return fileMutationError(err, "Could not read the file.");
    }
  }
  if (!encoding || !baseSha) {
    return { ok: false, conflict: false, error: "File is no longer editable." };
  }
  const next = applyHunkReject(disk, target.before, target.hunk);
  if (next === "" && target.beforeAbsent) {
    return applyFileUndo(
      client,
      projectId,
      { kind: "delete", rootId: target.rootId, path: target.path },
      target.sessionId,
      {
        undoCreate: {
          kind: "create",
          rootId: target.rootId,
          path: target.path,
          content: disk,
          encoding,
        },
        label: `Rejected hunk in ${name}`,
      },
    );
  }
  return applyFileUndo(
    client,
    projectId,
    {
      kind: "put",
      rootId: target.rootId,
      path: target.path,
      content: next,
      encoding,
      baseSha256: baseSha,
    },
    target.sessionId,
    { undoContent: disk, label: `Rejected hunk in ${name}` },
  );
}
