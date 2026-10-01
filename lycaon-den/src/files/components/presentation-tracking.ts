import type { SourceWalkFile } from "../../api/types.ts";
import type { LycaonClient } from "../../api/client.ts";

export type PresentationTarget = {
  fileId: string;
  effectId: string;
  ordinal: number;
};

function normalizeTarget(
  target: PresentationTarget | null,
): PresentationTarget | null {
  if (!target) return null;
  const fileId = target.fileId.trim();
  const effectId = target.effectId.trim();
  if (!fileId || !effectId || !Number.isSafeInteger(target.ordinal) || target.ordinal <= 0) return null;
  return {
    fileId,
    effectId,
    ordinal: target.ordinal,
  };
}

/** Presentation requires the displayed file identity and content hash. */
export function filePresentationTarget(
  file: SourceWalkFile | null,
  displayedFileId: string | null,
  displayedSha256: string | null,
): PresentationTarget | null {
  if (!file || file.file_id !== displayedFileId || file.tip.state !== "content" ||
    !displayedSha256 || file.tip.sha256 !== displayedSha256) return null;
  return normalizeTarget({
    fileId: file.file_id,
    effectId: file.presentation_effect_id ?? "",
    ordinal: file.presentation_ordinal ?? 0,
  });
}

type Hold = {
  tokens: Set<string>;
  latest: PresentationTarget;
  complete: (target: PresentationTarget) => void;
};

const tokenBinding = new Map<string, string>();
const holds = new Map<string, Hold>();

function holdKey(projectId: string, fileId: string): string {
  return `${projectId}\0${fileId}`;
}

export function trackFilePresentation(
  projectId: string,
  token: string,
  target: PresentationTarget | null,
  onComplete: (target: PresentationTarget) => void,
): void {
  const tok = token.trim();
  if (!tok) return;

  const normalized = normalizeTarget(target);
  const previousKey = tokenBinding.get(tok);
  const nextKey = normalized
    ? holdKey(projectId, normalized.fileId)
    : null;

  if (previousKey && previousKey === nextKey && normalized) {
    const hold = holds.get(previousKey);
    if (hold && normalized.ordinal > hold.latest.ordinal) {
      hold.latest = normalized;
      hold.complete = onComplete;
    }
    return;
  }

  if (previousKey) {
    releaseToken(tok, previousKey);
  }

  if (!nextKey || !normalized) return;

  let hold = holds.get(nextKey);
  if (!hold) {
    hold = { tokens: new Set(), latest: normalized, complete: onComplete };
    holds.set(nextKey, hold);
  }
  hold.tokens.add(tok);
  if (normalized.ordinal > hold.latest.ordinal) {
    hold.latest = normalized;
    hold.complete = onComplete;
  }
  tokenBinding.set(tok, nextKey);
}

function releaseToken(token: string, key: string): void {
  tokenBinding.delete(token);
  const hold = holds.get(key);
  if (!hold) return;
  hold.tokens.delete(token);
  if (hold.tokens.size > 0) return;
  holds.delete(key);
  hold.complete(hold.latest);
}

export async function completeFilePresentation(
  client: LycaonClient,
  projectId: string,
  target: PresentationTarget,
): Promise<void> {
  const fileId = target.fileId.trim();
  const effectId = target.effectId.trim();
  const ordinal = target.ordinal;
  if (!normalizeTarget(target)) return;
  await client.completeProjectSourcePresentation(projectId, {
    file_id: fileId,
    effect_id: effectId,
    ordinal,
  });
}
