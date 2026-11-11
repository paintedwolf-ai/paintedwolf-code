import type { Message } from "../../../api/types.ts";
import { isCoordinatorDraftWire } from "../projection/draft-model.ts";
import { isDenTranscriptMessage } from "../projection/message-transcript.ts";

export type TranscriptAnnouncementState = {
  sessionId: string;
  hadMessages: boolean;
  tailId?: string;
  /** Whether each assistant row, visible or not, was still being written. */
  assistantPending: ReadonlyMap<string, boolean>;
  baselineRevision?: number;
};

export type TranscriptAnnouncementStep = {
  state: TranscriptAnnouncementState;
  announcement?: string;
};

type TranscriptAnnouncementBaseline = {
  revision: number;
  state: TranscriptAnnouncementState;
};

const baselineBySession = new Map<string, TranscriptAnnouncementBaseline>();
let nextBaselineRevision = 0;

/**
 * A reply is still being written while it streams or while the host holds it
 * as an uncommitted draft, which arrives hidden and becomes visible on commit.
 */
function isPendingAssistant(message: Message): boolean {
  return message.status === "streaming" || isCoordinatorDraftWire(message);
}

/** Whether a message is a visible, settled assistant reply. */
export function isCommittedAssistantReply(message: Message): boolean {
  return (
    message.role === "assistant" &&
    isDenTranscriptMessage(message) &&
    !isPendingAssistant(message) &&
    Boolean(message.content.trim())
  );
}

function captureState(
  sessionId: string,
  messages: readonly Message[],
  baselineRevision?: number,
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
    baselineRevision,
  };
}

/** Record hydrated or restored transcript rows as already-seen history. */
export function noteTranscriptAnnouncementBaseline(
  sessionId: string,
  messages: readonly Message[],
): void {
  const sid = sessionId.trim();
  if (!sid) return;
  const revision = ++nextBaselineRevision;
  baselineBySession.set(sid, {
    revision,
    state: captureState(sid, messages, revision),
  });
}

export function clearTranscriptAnnouncementMemory(sessionId?: string): void {
  const sid = sessionId?.trim();
  if (!sid) {
    baselineBySession.clear();
    return;
  }
  baselineBySession.delete(sid);
}

/** A newly written phase note announces its summary. */
function workflowExplainAnnouncement(message: Message): string | undefined {
  const summary = message.workflow_explain?.summary.trim();
  return summary && isDenTranscriptMessage(message) ? summary : undefined;
}

/** Announces the latest visible assistant completion or newly written phase note. */
// A screen reader should say a link's label, not its destination.
function spokenText(content: string): string {
  return content
    .trim()
    .replace(/\[((?:\\.|[^\]\\])*)\]\((?:<[^>]*>|[^)\s]*)\)/g, (_link, label: string) =>
      label.replace(/\\(.)/g, "$1"),
    );
}

export function advanceTranscriptAnnouncement(
  previous: TranscriptAnnouncementState | undefined,
  sessionId: string,
  messages: readonly Message[],
): TranscriptAnnouncementStep {
  const sid = sessionId.trim();
  const baseline = baselineBySession.get(sid);
  const prior =
    previous?.sessionId === sessionId &&
    baseline &&
    baseline.revision !== previous.baselineRevision
      ? { ...baseline.state, sessionId }
      : previous;

  // A reload empties the rows before refilling them; the history it brings back is not new.
  if (prior?.sessionId === sessionId && prior.hadMessages && messages.length === 0) {
    return { state: prior };
  }
  const state = captureState(sessionId, messages, baseline?.revision);
  if (!prior || prior.sessionId !== sessionId) return { state };

  let appendedFrom = -1;
  if (!prior.hadMessages) {
    appendedFrom = 0;
  } else if (prior.tailId) {
    const tailIndex = messages.findIndex(
      (message) => message.id === prior.tailId,
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
    if (!isCommittedAssistantReply(message)) {
      continue;
    }
    const settled = prior.assistantPending.get(message.id) === true;
    if (settled || appended) announcement = `Assistant: ${spokenText(message.content)}`;
  }

  return { state, announcement };
}
