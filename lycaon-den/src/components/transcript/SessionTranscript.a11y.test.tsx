import { describe, expect, it } from "vitest";
import { render } from "@solidjs/testing-library";
import { SessionTranscript } from "./transcript-viewport-test-harness.tsx";
import type { Message } from "../../api/types.ts";

describe("SessionTranscript accessibility semantics", () => {
  it("exposes labelled user and assistant articles", () => {
    const messages: Message[] = [
      {
        id: "u1",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "hello",
        created_at: "2026-01-01T00:00:00Z",
      },
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "hi there",
        created_at: "2026-01-01T00:00:01Z",
      },
    ];
    const { container } = render(() => (
      <SessionTranscript layout="chat" messages={messages} />
    ));

    const window = container.querySelector('[data-testid="message-stream"]');
    expect(window?.getAttribute("role")).toBeNull();
    expect(window?.getAttribute("aria-live")).toBeNull();

    const user = container.querySelector('[data-testid="transcript-article-user"]');
    expect(user?.tagName).toBe("ARTICLE");
    expect(user?.getAttribute("aria-label")).toBe("You");
    expect(user?.getAttribute("aria-label")).not.toContain("hello");

    const assistant = container.querySelector(
      '[data-testid="transcript-article-assistant"]',
    );
    expect(assistant?.tagName).toBe("ARTICLE");
    expect(assistant?.getAttribute("aria-label")).toBe("Assistant");
    expect(assistant?.getAttribute("aria-busy")).toBeNull();
  });

  it("sets aria-busy on streaming assistant article from wire status", () => {
    const messages: Message[] = [
      {
        id: "a-stream",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "partial",
        status: "streaming",
        created_at: "2026-01-01T00:00:00Z",
      },
    ];
    const { container } = render(() => (
      <SessionTranscript layout="chat" messages={messages} />
    ));
    const assistant = container.querySelector(
      '[data-testid="transcript-article-assistant"]',
    );
    expect(assistant?.getAttribute("aria-busy")).toBe("true");
  });
});
