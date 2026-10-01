/** Reverts an exact replacement effect only while its after-image is current. */

import type { LycaonClient } from "../api/client.ts";
import {
  applyFilePutBatch,
  fileMutationError,
  type FileBatchReport,
  type FilePut,
} from "../files/commands/file-mutations.ts";
import { comparisonEndpoints } from "../files/source/source-comparison.ts";

export type ReplaceRevertTarget = {
  root_id: string;
  path: string;
};

export async function revertReplaceBatch(
  client: LycaonClient,
  projectId: string,
  batchId: string,
  applied: readonly ReplaceRevertTarget[],
): Promise<FileBatchReport> {
  const members: FilePut[] = [];
  const skipped: FileBatchReport["skipped"] = [];
  for (const target of applied) {
    try {
      const live = await client.getProjectSource(projectId, target.path, {
        rootId: target.root_id || undefined,
      });
      if (!live.file_id || !live.sha256 || !live.encoding || live.binary || live.over_limit) {
        skipped.push({ path: target.path, reason: "not editable" });
        continue;
      }
      const effectId = await findBatchEffect(client, projectId, live.file_id, batchId);
      if (!effectId) {
        skipped.push({ path: target.path, reason: "history unavailable" });
        continue;
      }
      const comparison = await client.getProjectSourceComparison(projectId, { effectId });
      const endpoints = comparisonEndpoints(comparison);
      if (!endpoints || endpoints.before.availability !== "available" || !endpoints.after.sha256) {
        skipped.push({ path: target.path, reason: "history unavailable" });
        continue;
      }
      if (live.sha256.trim() !== endpoints.after.sha256.trim()) {
        skipped.push({ path: target.path, reason: "changed since replacement" });
        continue;
      }
      members.push({
        rootId: target.root_id,
        path: target.path,
        content: endpoints.before.content,
        encoding: live.encoding,
        baseSha256: endpoints.after.sha256.trim(),
        undoContent: live.content ?? "",
      });
    } catch (err) {
      const failure = fileMutationError(err, "Revert failed");
      skipped.push({
        path: target.path,
        reason: failure.conflict ? "changed since replacement" : failure.error,
      });
    }
  }
  const report = await applyFilePutBatch(client, projectId, members);
  return { ok: report.ok, skipped: [...skipped, ...report.skipped], undos: report.undos };
}

async function findBatchEffect(
  client: LycaonClient,
  projectId: string,
  fileId: string,
  batchId: string,
): Promise<string | undefined> {
  let cursor: string | undefined;
  for (;;) {
    const page = await client.listProjectSourceVersions(
      projectId, { fileId }, { lane: "retained", limit: 100, cursor },
    );
    const version = page.versions.find((entry) => entry.batch_id === batchId);
    if (version) return version.effect_id;
    const next = page.next_cursor;
    if (!next) return undefined;
    cursor = next;
  }
}
