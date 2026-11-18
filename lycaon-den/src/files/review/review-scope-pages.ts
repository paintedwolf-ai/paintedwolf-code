import type { SourceWalkFile, SourceWalkResponse } from "../../api/types.ts";
import { commandWindowCoverageNotices, type CommandCoverageNotice } from "../source/source-command-window.ts";

type ReviewPages = {
  baseline: string;
  files: SourceWalkFile[];
  notices: CommandCoverageNotice[];
  commitAvailable: boolean;
  rootError: string | null;
};

export async function readReviewPages(
  readPage: (baseline: string, cursor?: string) => Promise<SourceWalkResponse>,
  requestedBaseline: string,
  current: () => boolean,
): Promise<ReviewPages | undefined> {
  let answer: string | undefined;
  let cursor: string | undefined;
  let rootSnapshot: string | undefined;
  let rootError: string | null = null;
  let commitAvailable = false;
  const files: SourceWalkFile[] = [];
  const coverage = new Map<string, CommandCoverageNotice>();

  do {
    // The first page resolves a chat-relative baseline for the entire range.
    const page = await readPage(answer ?? requestedBaseline, cursor);
    if (!current()) return undefined;
    answer ??= page.baseline;
    commitAvailable ||= page.commit_available;
    if (page.commit_roots) {
      const snapshot = JSON.stringify(page.commit_roots);
      if (rootSnapshot !== undefined && rootSnapshot !== snapshot) {
        throw new Error("Git changed while loading. Refresh the comparison.");
      }
      rootSnapshot = snapshot;
      const failures = page.commit_roots.filter((root) => !root.available).length;
      if (failures) rootError = `${failures} folder${failures === 1 ? "" : "s"} could not be compared with Git. This list is incomplete.`;
    }
    files.push(...page.files);
    for (const command of page.commands ?? []) {
      for (const notice of commandWindowCoverageNotices(command)) {
        const key = `${command.id}:${notice.key}`;
        coverage.set(key, { ...notice, key });
      }
    }
    cursor = page.next_cursor;
  } while (cursor);

  return { baseline: answer, files, notices: [...coverage.values()], commitAvailable, rootError };
}
