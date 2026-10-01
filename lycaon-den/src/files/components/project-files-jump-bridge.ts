/** Session-scoped jump history by project. */

import {
  fileBufferKey,
  parseFileBufferKey,
  retargetPathUnderPath,
} from "./project-files-model.ts";
import {
  createJumpHistory,
  jumpBack,
  jumpForward,
  pushJump,
  retargetJumpHistory,
  type JumpEntry,
  type JumpHistory,
} from "../history/jump-history.ts";

const historyByProject = new Map<string, JumpHistory>();

export function resetProjectJumpHistoryForTests(): void {
  historyByProject.clear();
}

function historyFor(projectId: string): JumpHistory {
  let hist = historyByProject.get(projectId);
  if (!hist) {
    hist = createJumpHistory();
    historyByProject.set(projectId, hist);
  }
  return hist;
}

export function pushProjectJump(
  projectId: string,
  entry: JumpEntry,
  now = Date.now(),
): void {
  historyByProject.set(
    projectId,
    pushJump(historyFor(projectId), entry, now),
  );
}

export function navigateProjectJumpBack(projectId: string): JumpEntry | null {
  const { hist, entry } = jumpBack(historyFor(projectId));
  historyByProject.set(projectId, hist);
  return entry;
}

export function navigateProjectJumpForward(projectId: string): JumpEntry | null {
  const { hist, entry } = jumpForward(historyFor(projectId));
  historyByProject.set(projectId, hist);
  return entry;
}

/** Retarget jumps after a path move. */
export function retargetProjectJumpHistory(
  projectId: string,
  rootId: string,
  fromPath: string,
  toPath: string,
): void {
  const mapEntry = (entry: JumpEntry): JumpEntry | null => {
    const parsed = parseFileBufferKey(entry.bufferKey);
    let nextKey = entry.bufferKey;
    if (parsed?.kind === "path" && parsed.rootId === rootId && !parsed.jobId) {
      const nextPath = retargetPathUnderPath(parsed.path, fromPath, toPath);
      if (nextPath != null) nextKey = fileBufferKey(rootId, nextPath);
    }
    if (entry.rootId !== rootId || entry.jobId) {
      return { ...entry, bufferKey: nextKey };
    }
    const nextPath = retargetPathUnderPath(entry.path, fromPath, toPath);
    return {
      ...entry,
      bufferKey: nextKey,
      ...(nextPath == null ? {} : { path: nextPath }),
    };
  };
  historyByProject.set(
    projectId,
    retargetJumpHistory(historyFor(projectId), mapEntry),
  );
}
