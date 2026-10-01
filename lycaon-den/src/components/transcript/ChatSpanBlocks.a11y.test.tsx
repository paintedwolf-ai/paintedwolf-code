import { createSignal } from "solid-js";
import { render } from "@solidjs/testing-library";
import { describe, expect, it } from "vitest";
import type { Message } from "../../api/types.ts";
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
});
