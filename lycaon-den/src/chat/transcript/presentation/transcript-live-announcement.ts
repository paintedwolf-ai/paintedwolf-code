import type { Message } from "../../../api/types.ts";
import { isDenTranscriptMessage } from "../projection/message-transcript.ts";

export type TranscriptAnnouncementState = {
  sessionId: string;
  hadMessages: boolean;
  tailId?: string;
  assistantStreaming: ReadonlyMap<string, boolean>;
};

export type TranscriptAnnouncementStep = {
  state: TranscriptAnnouncementState;
  announcement?: string;
};

function captureState(
  sessionId: string,
  messages: readonly Message[],
): TranscriptAnnouncementState {
  const assistantStreaming = new Map<string, boolean>();
  for (const message of messages) {
    if (message.role !== "assistant" || !isDenTranscriptMessage(message)) {
      continue;
    }
    assistantStreaming.set(message.id, message.status === "streaming");
  }
  return {
    sessionId,
    hadMessages: messages.length > 0,
    tailId: messages[messages.length - 1]?.id,
    assistantStreaming,
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
      message.status === "streaming" ||
      !message.content.trim()
    ) {
      continue;
    }
    const settled = previous.assistantStreaming.get(message.id) === true;
    if (settled || appended) announcement = `Assistant: ${message.content.trim()}`;
  }

  return { state, announcement };
}
