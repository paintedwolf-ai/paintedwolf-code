import type { MessageOrigin } from "../../../api/types.ts";

type ActionTranscriptMessage = {
  role: string;
  content: string;
  ord?: number;
  origin?: MessageOrigin;
};

export function nextUserActionOrd(
  messages: ReadonlyArray<ActionTranscriptMessage>,
  afterOrd: number,
): number | undefined {
  return messages.find((message) =>
    message.role === "user" && message.origin === "user" && (message.ord ?? 0) > afterOrd,
  )?.ord;
}

export function assistantProseAfterOrd(
  messages: ReadonlyArray<ActionTranscriptMessage>,
  afterOrd: number,
): string | null {
  const beforeOrd = nextUserActionOrd(messages, afterOrd) ?? Infinity;
  for (let i = messages.length - 1; i >= 0; i--) {
    const message = messages[i]!;
    if (
      message.role === "assistant" &&
      (message.ord ?? 0) > afterOrd &&
      (message.ord ?? 0) < beforeOrd &&
      message.content.trim()
    ) {
      return message.content.trim();
    }
  }
  return null;
}
