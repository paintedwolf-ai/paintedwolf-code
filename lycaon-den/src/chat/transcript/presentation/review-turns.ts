import type { Message } from "../../../api/types.ts";
import type { DisplayTranscriptItem } from "../projection/transcript-item-model.ts";

/**
 * The opening user message of each settled turn, keyed by the turn's terminal assistant row.
 *
 * One pass over the display rows serves every mounted row; a row asks for its own key.
 * A turn opens at a user row that is not a queue continuation and closes at the next one.
 * Its terminal assistant row is reviewable once the host has stopped streaming it, unless the
 * turn is still the visible live turn, where only a committed report stays reviewable.
 */
export function reviewTurnsByAssistant(
  items: readonly DisplayTranscriptItem[],
  messageById: ReadonlyMap<string, Message>,
  visibleTurnActive: boolean,
): ReadonlyMap<string, string> {
  const turns = new Map<string, string>();
  let opening: string | null = null;
  let terminal: string | null = null;

  const closeTurn = (hasLaterUser: boolean) => {
    if (!terminal || !opening) return;
    const wire = messageById.get(terminal);
    if (wire?.kind === "agent_note" || wire?.status === "streaming") return;
    // A committed report remains reviewable when later session activity starts.
    if (!hasLaterUser && visibleTurnActive && wire?.kind !== "completion_report") return;
    turns.set(terminal, opening);
  };

  for (const item of items) {
    if (item.kind === "user") {
      if (messageById.get(item.key)?.kind === "user_continuation") continue;
      closeTurn(true);
      opening = item.key;
      terminal = null;
    } else if (item.kind === "assistant") {
      terminal = item.key;
    }
  }
  closeTurn(false);
  return turns;
}
