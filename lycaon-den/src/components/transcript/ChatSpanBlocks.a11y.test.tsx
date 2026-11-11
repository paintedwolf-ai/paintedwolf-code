import { createSignal } from "solid-js";
import { render } from "@solidjs/testing-library";
import { afterEach, describe, expect, it } from "vitest";

import type { Message } from "../../api/types.ts";
import {
  clearTranscriptAnnouncementMemory,
  noteTranscriptAnnouncementBaseline,
} from "../../chat/transcript/presentation/transcript-live-announcement.ts";
import type { ChatSpanBlock } from "../../chat/workflow/workflow-spans.ts";
import type { RawTranscriptItem } from "../../chat/transcript/projection/transcript-item-model.ts";
import { ChatSpanBlocks } from "./transcript-viewport-test-harness.tsx";

function user(id: string, content: string): Message {
  return {
    id,
    role: "user",
    origin: "user",
    authority: "user",
    trust_tier: "trusted",
    content,
    created_at: "t",
  };
}

function assistant(
  id: string,
  content: string,
  status: Message["status"],
): Message {
  return {
    id,
    role: "assistant",
    origin: "model",
    authority: "none",
    trust_tier: "trusted",
    content,
    status,
    created_at: "t",
  };
}

function item(message: Message): RawTranscriptItem {
  return message.role === "assistant"
    ? { kind: "assistant", key: message.id, text: message.content }
    : { kind: "user", key: message.id, text: message.content };
}

function blocks(messages: Message[]): ChatSpanBlock[] {
  return [
    {
      kind: "run",
      runId: "ambient",
      ambientSpan: true,
      items: messages.map(item),
    },
  ];
}

describe("ChatSpanBlocks live log", () => {
  afterEach(() => {
    clearTranscriptAnnouncementMemory();
  });

  it("keeps virtual windows non-live and announces a completed turn once", async () => {
    const [messages, setMessages] = createSignal<Message[]>([user("u1", "Hi")]);
    const { getByTestId } = render(() => (
      <ChatSpanBlocks
        blocks={blocks(messages())}
        messages={messages()}
        sessionId="s1"
        workers={[]}
        visibleTurnActive={false}
      />
    ));

    const log = getByTestId("conversation-log");
    expect(log.getAttribute("role")).toBe("log");
    expect(log.getAttribute("aria-live")).toBe("polite");
    expect(log.getAttribute("aria-relevant")).toBe("additions");
    expect(log.getAttribute("aria-atomic")).toBe("false");
    expect(log.getAttribute("aria-label")).toBe("Conversation");
    expect(
      log
        .querySelector(".den-chat-transcript-visual")
        ?.getAttribute("aria-live"),
    ).toBe("off");
    expect(getByTestId("message-stream").getAttribute("role")).toBeNull();

    setMessages([user("u1", "Hi"), assistant("a1", "Part", "streaming")]);
    await Promise.resolve();
    expect(getByTestId("transcript-live-announcement").textContent).toBe("");

    setMessages([
      user("u1", "Hi"),
      assistant("a1", "Complete answer", "complete"),
    ]);
    await Promise.resolve();
    expect(getByTestId("transcript-live-announcement").textContent).toBe(
      "Assistant: Complete answer",
    );
  });

  it("keeps an inactive surface's log silent", () => {
    const [active, setActive] = createSignal(false);
    const { getByTestId } = render(() => (
      <ChatSpanBlocks
        blocks={blocks([user("u1", "Hi")])}
        messages={[user("u1", "Hi")]}
        sessionId="s1"
        workers={[]}
        visibleTurnActive={false}
        surfaceActive={active}
      />
    ));
    const log = getByTestId("conversation-log");
    expect(log.getAttribute("aria-live")).toBe("off");
    setActive(true);
    expect(log.getAttribute("aria-live")).toBe("polite");
  });

  it("keeps the live region silent when a restored session reconciles an existing assistant reply", async () => {
    const initialHistory = [user("u1", "Prompt before reload")];
    noteTranscriptAnnouncementBaseline("s1", initialHistory);
    const [messages, setMessages] = createSignal<Message[]>(initialHistory);
    const { getByTestId } = render(() => (
      <ChatSpanBlocks
        blocks={blocks(messages())}
        messages={messages()}
        sessionId="s1"
        workers={[]}
        visibleTurnActive={false}
      />
    ));

    expect(getByTestId("transcript-live-announcement").textContent).toBe("");

    const reconciled = [
      user("u1", "Prompt before reload"),
      assistant("a1", "Restored reply", "complete"),
    ];
    noteTranscriptAnnouncementBaseline("s1", reconciled);
    setMessages(reconciled);
    await Promise.resolve();
    expect(getByTestId("transcript-live-announcement").textContent).toBe("");

    setMessages([
      ...reconciled,
      assistant("a2", "New live reply", "complete"),
    ]);
    await Promise.resolve();
    expect(getByTestId("transcript-live-announcement").textContent).toBe(
      "Assistant: New live reply",
    );
  });
});
