import type { SearchHit } from "../api/types.ts";
import type {
  RevealChickletKind,
  TranscriptRevealAnchor,
} from "../chat/transcript/presentation/transcript-reveal-target.ts";

export type SearchNavReveal = TranscriptRevealAnchor & {
  worker?: { workerId: string; childSessionId: string };
};

export type SearchNavTarget = {
  projectId: string;
  sessionId?: string;
  openWorklog?: boolean;
  reveal?: SearchNavReveal;
};

function revealChickletKindForHit(kind: string): RevealChickletKind | null {
  switch (kind) {
    case "tool":
    case "web":
    case "network":
      return "tool";
    case "evidence":
    case "claim":
      return "citation";
    // Message hits reveal their source message.
    case "message":
      return "message";
    default:
      return null;
  }
}

/** Maps a hit to its navigation target. */
export function searchHitToNavTarget(
  hit: SearchHit,
  /** Message highlight text. */
  matchText?: string,
): SearchNavTarget {
  const kind = hit.hit_kind.toLowerCase();
  const openWorklog = kind === "claim";
  const workerId = hit.worker_id?.trim();
  const parentSessionId = hit.parent_session_id?.trim();
  const childSessionId = hit.session_id?.trim();
  const worker =
    workerId && parentSessionId && childSessionId
      ? { workerId, childSessionId }
      : undefined;
  const chatSessionId = worker ? parentSessionId : childSessionId;
  const chicklet = revealChickletKindForHit(kind);
  const reveal =
    chicklet && hit.source_ref
      ? {
          anchorId: hit.source_ref,
          chicklet,
          matchText,
          worker,
        }
      : undefined;
  return {
    projectId: hit.project_id,
    sessionId: chatSessionId,
    openWorklog,
    reveal,
  };
}
