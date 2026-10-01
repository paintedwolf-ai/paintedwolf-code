/**
 * Blueprint deletion — one pipeline behind the row menu and the bulk toolbar.
 * A batch is N host calls, so every path settles into its own outcome.
 */

import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError } from "../../api/http.ts";
import { confirmDestructive } from "../../platform/interaction/confirm-dialog.ts";

export type BlueprintDeleteClient = Pick<LycaonClient, "deleteBlueprint">;

/** A row to delete: the host addresses it by id, the view by path. */
export type BlueprintDeleteTarget = { id: string; path: string };

export type BlueprintDeleteOutcome =
  | { path: string; ok: true }
  | { path: string; ok: false; reason: string };

export type BlueprintDeleteReport = {
  /** One entry per requested path, in request order. */
  outcomes: BlueprintDeleteOutcome[];
  deleted: string[];
  failed: { path: string; reason: string }[];
};

/** An absent blueprint satisfies deletion. */
function alreadyGone(err: unknown): boolean {
  return err instanceof LycaonApiError && err.code === "blueprint_not_found";
}

function failureReason(err: unknown): string {
  if (err instanceof LycaonApiError) {
    const message = err.message.trim();
    if (message) return message;
    const title = err.title?.trim();
    if (title) return title;
    return `The app answered ${err.status}.`;
  }
  if (err instanceof Error && err.message.trim()) return err.message.trim();
  return "The delete did not go through.";
}

/** Targets settle independently, including failures. */
export async function deleteBlueprints(
  client: BlueprintDeleteClient,
  projectId: string,
  targets: readonly BlueprintDeleteTarget[],
  onSettled?: (outcome: BlueprintDeleteOutcome) => void,
): Promise<BlueprintDeleteReport> {
  const outcomes: BlueprintDeleteOutcome[] = [];
  for (const { id, path } of targets) {
    let outcome: BlueprintDeleteOutcome;
    try {
      await client.deleteBlueprint(projectId, id);
      outcome = { path, ok: true };
    } catch (err) {
      outcome = alreadyGone(err)
        ? { path, ok: true }
        : { path, ok: false, reason: failureReason(err) };
    }
    outcomes.push(outcome);
    onSettled?.(outcome);
  }
  const failed: { path: string; reason: string }[] = [];
  const deleted: string[] = [];
  for (const outcome of outcomes) {
    if (outcome.ok) deleted.push(outcome.path);
    else failed.push({ path: outcome.path, reason: outcome.reason });
  }
  return { outcomes, deleted, failed };
}

/** A single failure includes its reason; multiple failures retain row details. */
export function blueprintDeleteSummary(
  report: BlueprintDeleteReport,
  labelFor: (path: string) => string,
): string | null {
  const failed = report.failed;
  const first = failed[0];
  if (!first) return null;
  if (report.deleted.length === 0) {
    return failed.length === 1
      ? `Could not delete “${labelFor(first.path)}” — ${first.reason}`
      : `Could not delete any of the ${failed.length} selected blueprints — each row says why.`;
  }
  const progress = `Deleted ${report.deleted.length} of ${report.outcomes.length}.`;
  return failed.length === 1
    ? `${progress} “${labelFor(first.path)}” could not be deleted — ${first.reason}`
    : `${progress} ${failed.length} could not be deleted — each row says why.`;
}

export function confirmDeleteBlueprints(
  count: number,
  title?: string,
): Promise<boolean> {
  const message =
    count === 1
      ? title
        ? `Delete “${title}”? This removes the blueprint file from the project and cannot be undone.`
        : "Delete this blueprint? This removes the file from the project and cannot be undone."
      : `Delete ${count} blueprints? This removes the files from the project and cannot be undone.`;
  return confirmDestructive({
    message,
    title: count === 1 ? "Delete blueprint" : "Delete blueprints",
    okLabel: count === 1 ? "Delete" : `Delete ${count}`,
  });
}
