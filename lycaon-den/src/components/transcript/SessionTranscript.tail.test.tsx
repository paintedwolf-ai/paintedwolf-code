import { resetSessionTranscriptChatTest } from "./session-transcript-chat-test-harness.ts";

import { TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT } from "../../chat/transcript/presentation/transcript-disclosure.tsx";

import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { render } from "@solidjs/testing-library";
import { SessionTranscript } from "./transcript-viewport-test-harness.tsx";
import type { TranscriptTailSlot } from "./SessionTranscript.tsx";
import type { Message } from "../../api/types.ts";

const message = (id: string, ord: number): Message => ({
  id,
  role: "user",
  origin: "user",
  authority: "user",
  trust_tier: "trusted",
  content: `body of ${id}`,
  created_at: `2026-01-01T00:00:0${ord}Z`,
  ord,
});

const slot = (
  id: string,
  present: () => boolean,
  label = id,
): TranscriptTailSlot => ({
  id,
  present,
  children: () => <p data-testid={`tail-${id}`}>{label}</p>,
});

describe("SessionTranscript tail", () => {
  afterEach(resetSessionTranscriptChatTest);

  it("remeasures changing tail content without treating it as a virtual row", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    try {
      const { container } = render(() => (
        <SessionTranscript layout="chat" sessionId="tail-measure"
          messages={[message("m1", 1)]} tail={[slot("pending-sends", () => true)]} />
      ));
      const row = container.querySelector<HTMLElement>("[data-transcript-tail=pending-sends]")!;
      expect(row.hasAttribute("data-index")).toBe(false);
      row.dispatchEvent(new Event(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, { bubbles: true }));
      row.querySelector("p")!.textContent = "Updated pending message";
      await new Promise((resolve) => setTimeout(resolve, 40));
      expect(warn.mock.calls.flat().map(String).join(" ")).not.toContain("Missing attribute name");
      expect(row.isConnected).toBe(true);
    } finally {
      warn.mockRestore();
    }
  });

  it("seats tail content in the row flow, after the virtual rows", () => {
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s-tail"
        messages={[message("m1", 1)]}
        tail={[slot("pending-sends", () => true)]}
      />
    ));

    const inner = container.querySelector(".den-chat-stream-inner");
    expect(inner).toBeTruthy();
    const rows = [...(inner?.querySelectorAll(".transcript-viewport-row") ?? [])];
    // The tail is the last row, not a sibling of the transcript.
    const last = rows[rows.length - 1];
    expect(last?.getAttribute("data-transcript-tail")).toBe("pending-sends");
    expect(last?.querySelector("[data-testid=tail-pending-sends]")).toBeTruthy();
  });

  it("takes the first row's seat when no rows precede it", () => {
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s-tail-first"
        messages={[]}
        tail={[slot("pending-sends", () => true)]}
      />
    ));

    const row = container.querySelector<HTMLElement>(
      "[data-transcript-tail=pending-sends]",
    );
    expect(row?.classList.contains("transcript-viewport-row")).toBe(true);
    // The seam drops out for the first row on screen — the seat a host echo
    // will take, so replacing the optimistic row moves nothing.
    expect(row?.getAttribute("data-seam")).toBeNull();
  });

  it("yields that seat to a real row as soon as one exists", () => {
    const [messages, setMessages] = createSignal<Message[]>([]);
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s-tail-yield"
        messages={messages()}
        tail={[slot("pending-sends", () => true)]}
      />
    ));

    expect(
      container
        .querySelector("[data-transcript-tail=pending-sends]")
        ?.getAttribute("data-seam"),
    ).toBeNull();

    setMessages([message("m1", 1)]);

    // The tail now opens its own unit below the rows.
    expect(
      container
        .querySelector("[data-transcript-tail=pending-sends]")
        ?.getAttribute("data-seam"),
    ).toBe("section");
    // The row's day label is the first row, so it takes the seat.
    expect(
      container
        .querySelector(".transcript-viewport-row[data-time-row]")
        ?.getAttribute("data-seam"),
    ).toBeNull();
  });

  it("renders nothing when there are no rows and no present tail", () => {
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s-tail-absent"
        messages={[]}
        tail={[slot("pending-sends", () => false)]}
      />
    ));

    expect(container.querySelector("[data-testid=message-stream]")).toBeNull();
  });

  it("keeps an absent slot out of the flow so it reserves no seam", () => {
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s-tail-mixed"
        messages={[message("m1", 1)]}
        tail={[
          slot("turn-outcome", () => false),
          slot("pending-sends", () => true),
        ]}
      />
    ));

    expect(container.querySelector("[data-transcript-tail=turn-outcome]")).toBeNull();
    expect(container.querySelector("[data-transcript-tail=pending-sends]")).toBeTruthy();
  });
});
