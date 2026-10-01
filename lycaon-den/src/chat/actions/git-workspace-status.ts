import type { LycaonClient } from "../../api/client.ts";
import { isApiErrorCode } from "../../api/http.ts";
import type { GitFileEntry, GitStatusSummary } from "../../api/types.ts";

/** One repository's status summary joined with the changed files of the same revision. */
export type GitWorkspaceStatus = Omit<GitStatusSummary, "revision" | "refreshing"> & {
  files: GitFileEntry[];
};

const CHANGES_PAGE_LIMIT = 500;
const READ_ATTEMPTS = 3;

/**
 * Reads the status summary and the changed files of one published revision.
 * A published revision answers even while a newer one loads; revision zero
 * means the first snapshot is still loading.
 */
export async function loadGitWorkspaceStatus(
  client: Pick<LycaonClient, "getGitStatus" | "listGitChanges">,
  projectId: string,
  repoId: string,
  sessionId?: string,
): Promise<GitWorkspaceStatus> {
  let latest: GitStatusSummary | undefined;
  for (let attempt = 0; attempt < READ_ATTEMPTS; attempt++) {
    const summary = await client.getGitStatus(projectId, repoId, sessionId);
    latest = summary;
    if (!summary.available) return workspaceStatus(summary, []);
    if (summary.revision !== 0) {
      // A refresh landing between the two reads expires the requested generation.
      const files = await readChanges(client, projectId, repoId, summary.revision, sessionId).catch((error: unknown) => {
        if (isApiErrorCode(error, ["cursor_generation_expired"])) return undefined;
        throw error;
      });
      if (files) return workspaceStatus(summary, files);
    }
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  throw new Error(latest?.revision === 0
    ? "Git status is still loading. Try again."
    : "Git status changed while loading its files. Try again.");
}

async function readChanges(
  client: Pick<LycaonClient, "listGitChanges">,
  projectId: string,
  repoId: string,
  revision: number,
  sessionId?: string,
): Promise<GitFileEntry[] | undefined> {
  const files: GitFileEntry[] = [];
  let cursor: string | undefined;
  do {
    const page = await client.listGitChanges(projectId, repoId, {
      session_id: sessionId, revision, limit: CHANGES_PAGE_LIMIT,
      ...(cursor ? { cursor } : {}),
    });
    if (page.revision !== revision) return undefined;
    files.push(...page.files);
    cursor = page.next_cursor;
  } while (cursor);
  return files;
}

function workspaceStatus(summary: GitStatusSummary, files: GitFileEntry[]): GitWorkspaceStatus {
  const { revision: _revision, refreshing: _refreshing, ...status } = summary;
  return { ...status, files };
}
