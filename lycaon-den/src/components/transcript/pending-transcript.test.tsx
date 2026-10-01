import { readSourceText } from "../../test/stylesheet-source.ts";
import { join } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { render } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { SessionTranscript } from "./transcript-viewport-test-harness.tsx";
import type { ChatSpanBlock } from "../../chat/workflow/workflow-spans.ts";
import type { Message } from "../../api/types.ts";
import type { PendingSend } from "../../chat/send/pending-sends.ts";

const preference = vi.hoisted(() => ({ value: "hover" }));
vi.mock("../../settings/chat/chat-prefs.ts", () => ({ messageTimesPref: () => preference.value }));
afterEach(() => { preference.value = "hover"; });

const entry = (operationId: string, overrides?: Partial<PendingSend>): PendingSend => ({
  kind: "prompt",
  operationId,
  text: "hello there",
  state: "sending",
  createdAt: Date.now(),
  ...overrides,
});

describe("Pending transcript rows", () => {
  it("keeps queued submissions out of the transcript before and after acceptance", () => {
    const [entries, setEntries] = createSignal([entry("queued", { kind: "queued_prompt" })]);
    const view = render(() => <SessionTranscript messages={[]} pendingSends={entries()} />);
    expect(view.queryByTestId("pending-send-bubble")).toBeNull();
    expect(view.queryByTestId("transcript-day")).toBeNull();
    setEntries([entry("queued", { kind: "queued_prompt", state: "accepted" })]);
    expect(view.queryByTestId("pending-send-bubble")).toBeNull();
    expect(view.queryByTestId("transcript-day")).toBeNull();
  });

  it("renders nothing without entries", () => {
    const { queryByTestId } = render(() => <SessionTranscript messages={[]} pendingSends={[]} />);
    expect(queryByTestId("message-stream")).toBeNull();
  });

  it("renders one user-styled bubble per entry, oldest first", () => {
    const { getAllByTestId } = render(() => (
      <SessionTranscript messages={[]} pendingSends={[entry("op-1"), entry("op-2", { text: "and this" })]} />
    ));
    const bubbles = getAllByTestId("pending-send-bubble");
    expect(bubbles).toHaveLength(2);
    expect(bubbles[0]?.getAttribute("data-operation-id")).toBe("op-1");
    expect(bubbles[0]?.classList.contains("bubble--user")).toBe(true);
    // An optimistic prompt uses the same styling as a direct host echo.
    expect(bubbles[0]?.classList.contains("bubble--reserved")).toBe(false);
    expect(bubbles[1]?.textContent).toContain("and this");
  });

  it("outlines only the queue head Send reserved, with nothing above the text", () => {
    const { getByTestId } = render(() => (
      <SessionTranscript messages={[]}
        pendingSends={[entry("item-1", { kind: "queue_send", text: "say ALPHA" })]}
      />
    ));
    const bubble = getByTestId("pending-send-bubble");
    expect(bubble.getAttribute("data-pending-kind")).toBe("queue_send");
    expect(bubble.classList.contains("bubble--reserved")).toBe(true);
    expect(bubble.textContent).toBe("say ALPHA");
    expect(bubble.querySelector("p")).toBeNull();
  });

  it("shows attachment labels as chips", () => {
    const { getByTestId } = render(() => (
      <SessionTranscript messages={[]}
        pendingSends={[entry("op-1", { attachmentLabels: ["notes.txt", "img.png"] })]}
      />
    ));
    const chips = getByTestId("pending-send-attachment-chips");
    expect(chips.textContent).toContain("notes.txt");
    expect(chips.textContent).toContain("img.png");
  });

  it("drops a bubble reactively when its entry resolves", () => {
    const [entries, setEntries] = createSignal<PendingSend[]>([entry("op-1")]);
    const { queryAllByTestId } = render(() => (
      <SessionTranscript messages={[]} pendingSends={entries()} />
    ));
    expect(queryAllByTestId("pending-send-bubble")).toHaveLength(1);
    setEntries([]);
    expect(queryAllByTestId("pending-send-bubble")).toHaveLength(0);
  });

  it.each(["hover", "always"])("keeps the row, bubble and date marker through confirmation with %s times", (times) => {
    preference.value = times;
    const pending = entry("op-1");
    const [messages, setMessages] = createSignal<Message[]>([]);
    const [entries, setEntries] = createSignal([pending]);
    const spans = (): ChatSpanBlock[] => messages().length === 0 ? [] : [{
      kind: "run", runId: "ambient", ambientSpan: true,
      items: messages().map((message) => ({ kind: "user", key: message.id, text: message.content })),
    }];
    const view = render(() => <SessionTranscript messages={messages()} transcriptSpans={spans()} pendingSends={entries()} />);
    const bubble = view.getByTestId("pending-send-bubble");
    const row = bubble.closest("[data-msg-id]");
    const marker = view.getByTestId("transcript-day");
    expect(view.queryAllByTestId("message-time")).toHaveLength(times === "always" ? 1 : 0);
    setMessages([{
      id: pending.operationId, content: pending.text, role: "user", origin: "user",
      authority: "user", trust_tier: "trusted", ord: 1,
      created_at: new Date(pending.createdAt).toISOString(),
    }]);
    expect(view.getByTestId("transcript-article-user")).toBe(bubble);
    expect(bubble.closest("[data-msg-id]")).toBe(row);
    expect(view.getByTestId("transcript-day")).toBe(marker);
    expect(view.queryAllByTestId("pending-send-bubble")).toHaveLength(0);
    setEntries([]);
    expect(view.getByTestId("transcript-article-user")).toBe(bubble);
    expect(view.getAllByTestId("transcript-day")).toHaveLength(1);
    expect(view.queryAllByTestId("message-time")).toHaveLength(times === "always" ? 1 : 0);
  });

  it("is seated by the row seam, the only seam in the stream", () => {
    const chat = readSourceText(
      join(import.meta.dirname, "../../chat-utilities.css"),
      "utf8",
    );
    // A second seam would double the row gap.
    expect(chat).not.toMatch(/@utility den-chat-stream-body\s*\{[^}]*gap:/);
    expect(chat).not.toMatch(/\.den-chat-stream-body\s*>\s*\*\s*\+\s*\*/);
    // Rows carry no margin at all: the seam is padding inside the row beneath it.
    expect(chat).not.toMatch(
      /\.den-chat-stream-inner \.transcript-viewport-row\s*\{[^}]*margin/,
    );
    for (const rung of ["row", "section", "turn"]) {
      expect(chat, rung).toContain(
        `.den-seam[data-seam="${rung}"] {\n  padding-top: var(--transcript-${rung}-gap);`,
      );
    }
  });
});
