import type { Message } from "../../../api/types.ts";
import { isCoordinatorDraftWire } from "../projection/draft-model.ts";
import { isDenTranscriptMessage } from "../projection/message-transcript.ts";

export type TranscriptAnnouncementState = {
  sessionId: string;
  hadMessages: boolean;
  tailId?: string;
  /** Whether each assistant row, visible or not, was still being written. */
  assistantPending: ReadonlyMap<string, boolean>;
};

export type TranscriptAnnouncementStep = {
  state: TranscriptAnnouncementState;
  announcement?: string;
};

/**
 * A reply is still being written while it streams or while the host holds it
 * as an uncommitted draft, which arrives hidden and becomes visible on commit.
 */
function isPendingAssistant(message: Message): boolean {
  return message.status === "streaming" || isCoordinatorDraftWire(message);
}

function captureState(
  sessionId: string,
  messages: readonly Message[],
): TranscriptAnnouncementState {
  const assistantPending = new Map<string, boolean>();
  for (const message of messages) {
    if (message.role === "assistant") {
      assistantPending.set(message.id, isPendingAssistant(message));
    }
  }
  return {
    sessionId,
    hadMessages: messages.length > 0,
    tailId: messages[messages.length - 1]?.id,
    assistantPending,
  };
}

/** A newly written phase note announces its summary. */
function workflowExplainAnnouncement(message: Message): string | undefined {
  const summary = message.workflow_explain?.summary.trim();
  return summary && isDenTranscriptMessage(message) ? summary : undefined;
}

/** Announces the latest visible assistant completion or newly written phase note. */
export function advanceTranscriptAnnouncement(
  previous: TranscriptAnnouncementState | undefined,
  sessionId: string,
  messages: readonly Message[],
): TranscriptAnnouncementStep {
  // A reload empties the rows before refilling them; the history it brings back is not new.
  if (previous?.sessionId === sessionId && previous.hadMessages && messages.length === 0) {
    return { state: previous };
  }
  const state = captureState(sessionId, messages);
  if (!previous || previous.sessionId !== sessionId) return { state };

  let appendedFrom = -1;
  if (!previous.hadMessages) {
    appendedFrom = 0;
  } else if (previous.tailId) {
    const tailIndex = messages.findIndex(
      (message) => message.id === previous.tailId,
    );
    if (tailIndex >= 0) appendedFrom = tailIndex + 1;
  }

  let announcement: string | undefined;
  for (let index = 0; index < messages.length; index += 1) {
    const message = messages[index];
    if (!message) continue;
    const appended = appendedFrom >= 0 && index >= appendedFrom;
    const note = appended ? workflowExplainAnnouncement(message) : undefined;
    if (note) {
      announcement = note;
      continue;
    }
    if (
      message.role !== "assistant" ||
      !isDenTranscriptMessage(message) ||
      isPendingAssistant(message) ||
      !message.content.trim()
    ) {
      continue;
    }
    const settled = previous.assistantPending.get(message.id) === true;
    if (settled || appended) announcement = `Assistant: ${message.content.trim()}`;
  }

  return { state, announcement };
}
