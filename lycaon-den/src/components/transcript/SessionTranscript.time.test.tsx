import { createSignal } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render } from "@solidjs/testing-library";
import type { Message, TurnClock } from "../../api/types.ts";
import {
  captureUnreadBoundary,
  resetUnreadBoundariesForTests,
} from "../../attention/unread-boundary.ts";
import { saveMessageTimes } from "../../settings/chat/chat-prefs.ts";
import { SessionTranscript } from "./transcript-viewport-test-harness.tsx";
import { resetSessionTranscriptChatTest } from "./session-transcript-chat-test-harness.ts";

const NOW = Date.parse("2026-09-12T18:00:00Z");
const ago = (minutes: number) => new Date(NOW - minutes * 60_000).toISOString();

const prompt = (id: string, ord: number, minutesAgo: number): Message => ({
  id,
  role: "user",
  origin: "user",
  authority: "user",
  trust_tier: "trusted",
  content: `body of ${id}`,
  created_at: ago(minutesAgo),
  ord,
});

const settled = (opening: string, minutesAgo: number, patch: Partial<TurnClock> = {}): TurnClock => ({
  session_id: "s-time",
  opening_message_id: opening,
  active_ms: 300_000,
  work_ms: 300_000,
  running: false,
  settled_at: ago(minutesAgo),
  ...patch,
});

const text = (el: Element | null | undefined) => el?.textContent?.replace(/\s+/g, " ").trim();

describe("SessionTranscript time rows", () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["Date", "setTimeout", "clearTimeout"] });
    vi.setSystemTime(NOW);
  });

  afterEach(async () => {
    vi.useRealTimers();
    resetUnreadBoundariesForTests();
    await saveMessageTimes("hover");
    await resetSessionTranscriptChatTest();
  });

  it("labels the day and closes each settled turn with its finish and work", () => {
    const messages = [prompt("u1", 1, 30), prompt("u2", 2, 20)];
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s-time"
        messages={messages}
        turnClocks={{
          u1: settled("u1", 25, { active_ms: 398_000 }),
          u2: settled("u2", 18, { work_ms: 800, active_ms: 800 }),
        }}
      />
    ));

    const rows = [...container.querySelectorAll<HTMLElement>(".transcript-viewport-row")].map(
      (row) => row.dataset.msgId ?? row.dataset.timeRow,
    );
    expect(rows).toEqual(["time:day:u1", "u1", "turn-tail:u1", "u2", "turn-tail:u2"]);
    expect(text(container.querySelector('[data-testid="transcript-day"] time'))).toMatch(/^Today, /);

    const [first, second] = container.querySelectorAll('[data-testid="turn-tail"]');
    expect(text(first?.querySelector("time"))).toBe("Finished 25m ago");
    const work = first?.querySelector('[data-testid="turn-tail-work"]');
    expect(text(work)).toBe("Worked 5m 0s");
    expect(work?.getAttribute("data-tip")).toMatch(/^Started .+\. Time spent waiting on you isn't counted\.$/);
    // Work under a second is not shown.
    expect(text(second)).toBe("Finished 18m ago");
    expect(second?.querySelector('[data-testid="turn-tail-work"]')).toBeNull();
    // The tail anchors the turn seam, so it stays with the turn it closes and
    // the next prompt takes the turn rung below it.
    const seams = [...container.querySelectorAll<HTMLElement>(".transcript-viewport-row")].map(
      (row) => row.dataset.seam,
    );
    expect(seams).toEqual([undefined, "row", "row", "turn", "row"]);
  });

  it("moves relative copy with the minute clock", async () => {
    const messages = [prompt("u1", 1, 30)];
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s-time"
        messages={messages}
        turnClocks={{ u1: settled("u1", 18) }}
      />
    ));
    const finish = () => text(container.querySelector('[data-testid="turn-tail"] time'));
    expect(finish()).toBe("Finished 18m ago");
    // The clock ticks just past each minute boundary.
    await vi.advanceTimersByTimeAsync(60_100);
    expect(finish()).toBe("Finished 19m ago");
  });

  it("fills the running turn's reserved tail without inserting or moving rows", () => {
    const [clocks, setClocks] = createSignal<Record<string, TurnClock>>({
      u1: { ...settled("u1", 0), running: true, running_at: ago(1), settled_at: undefined },
    });
    const [active, setActive] = createSignal(true);
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s-time"
        messages={[prompt("u1", 1, 1)]}
        turnClocks={clocks()}
        visibleTurnActive={active()}
        pendingSends={[{
          kind: "prompt", operationId: "next", text: "Next prompt",
          state: "sending", createdAt: NOW,
        }]}
      />
    ));
    const rows = () => [...container.querySelectorAll(".transcript-viewport-row")];
    const before = rows();
    const tail = container.querySelector('[data-testid="turn-tail"]')!;
    expect(tail).not.toBeNull();
    expect(tail.getAttribute("aria-hidden")).toBe("true");
    expect(text(tail)).toBe("");
    setClocks({ u1: settled("u1", 0) });
    // The pending opener already bounds the previous turn.
    expect(text(tail)).toContain("Finished just now");
    setActive(false);
    expect(container.querySelector('[data-testid="turn-tail"]')).toBe(tail);
    expect(tail.hasAttribute("aria-hidden")).toBe(false);
    expect(rows()).toHaveLength(before.length);
    rows().forEach((row, index) => expect(row).toBe(before[index]));
    expect(text(container.querySelector('[data-testid="turn-tail"] time'))).toBe("Finished just now");
    setActive(true);
    // A newer pending opener keeps the previous turn settled.
    expect(text(tail)).toContain("Finished just now");
    rows().forEach((row, index) => expect(row).toBe(before[index]));
  });

  it("puts a message's time in its hover toolbar, or under the bubble when the person asks", () => {
    const recovery = { held: () => false, onEdit: () => {}, onRewind: () => {}, onCopy: () => {} };
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s-time"
        messages={[prompt("u1", 1, 12)]}
        recovery={recovery}
        turnClocks={{ u1: settled("u1", 11) }}
      />
    ));
    const time = () => container.querySelector('[data-testid="message-time"]');
    expect(text(time())).toBe("12m ago");
    expect(time()?.closest('[data-testid="message-actions"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="turn-tail"]')).not.toBeNull();

    // The preference applies at once; persisting it waits on a debounce.
    void saveMessageTimes("always");
    expect(time()?.closest('[data-testid="message-actions"]')).toBeNull();
    expect(time()?.classList.contains("den-msg-time-below")).toBe(true);
    expect(time()?.previousElementSibling?.classList.contains("bubble--user")).toBe(true);
  });

  it("keeps every turn's tail readable whatever the message-times preference says", () => {
    for (const preference of ["hover", "always"] as const) {
      void saveMessageTimes(preference);
      const { container, unmount } = render(() => (
        <SessionTranscript
          layout="chat"
          sessionId="s-time"
          messages={[prompt("u1", 1, 30), prompt("u2", 2, 20)]}
          turnClocks={{ u1: settled("u1", 25), u2: settled("u2", 18) }}
        />
      ));
      const tails = [...container.querySelectorAll<HTMLElement>('[data-testid="turn-tail"]')];
      expect(tails, preference).toHaveLength(2);
      // Nothing hides the anchor a turn seam holds.
      expect(tails.every((tail) => tail.style.opacity === ""), preference).toBe(true);
      unmount();
    }
  });

  it("draws the new line above the first reply after the stamp from the previous look", () => {
    captureUnreadBoundary("s-time", ago(15), NOW);
    const reply: Message = {
      id: "a1", role: "assistant", origin: "model", authority: "none", trust_tier: "trusted",
      content: "done", created_at: ago(10), ord: 2,
    };
    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-time" messages={[prompt("u1", 1, 20), reply]} />
    ));
    const rows = [...container.querySelectorAll<HTMLElement>(".transcript-viewport-row")].map(
      (row) => row.dataset.msgId ?? row.dataset.timeRow,
    );
    expect(rows).toEqual(["time:day:u1", "u1", "unread", "a1", "turn-tail:u1"]);
    const unread = container.querySelector('[data-testid="transcript-unread"] [data-tip]');
    expect(text(unread)).toMatch(/^New, you last looked at /);
    expect(unread?.getAttribute("data-tip")).toMatch(/^You last looked at /);
  });

  it("draws the new line above an unread prompt that arrived after the previous look", () => {
    captureUnreadBoundary("s-time", ago(15), NOW);
    const reply: Message = {
      id: "a1", role: "assistant", origin: "model", authority: "none", trust_tier: "trusted",
      content: "done", created_at: ago(5), ord: 2,
    };
    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-time" messages={[prompt("u1", 1, 10), reply]} />
    ));
    const rows = [...container.querySelectorAll<HTMLElement>(".transcript-viewport-row")].map(
      (row) => row.dataset.msgId ?? row.dataset.timeRow,
    );
    expect(rows).toEqual(["time:day:u1", "unread", "u1", "a1", "turn-tail:u1"]);
    const unread = container.querySelector('[data-testid="transcript-unread"] [data-tip]');
    expect(text(unread)).toMatch(/^New, you last looked at /);
  });

  it("omits the new line and leading day line when hasMoreBefore is true and unread boundary is before loaded window", () => {
    captureUnreadBoundary("s-time", ago(30), NOW);
    const reply: Message = {
      id: "a1", role: "assistant", origin: "model", authority: "none", trust_tier: "trusted",
      content: "done", created_at: ago(5), ord: 2,
    };
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s-time"
        hasMoreBefore={true}
        messages={[prompt("u1", 1, 10), reply]}
      />
    ));
    const rows = [...container.querySelectorAll<HTMLElement>(".transcript-viewport-row")].map(
      (row) => row.dataset.msgId ?? row.dataset.timeRow,
    );
    expect(rows).toEqual(["u1", "a1", "turn-tail:u1"]);
    expect(container.querySelector('[data-testid="transcript-unread"]')).toBeNull();
    expect(container.querySelector('[data-testid="transcript-day"]')).toBeNull();
  });

  it("keeps time markers, unread markers, and turn tails inside spans when transcriptSpans are present", () => {
    captureUnreadBoundary("s-time", ago(15), NOW);
    const reply: Message = {
      id: "a1", role: "assistant", origin: "model", authority: "none", trust_tier: "trusted",
      content: "done", created_at: ago(10), ord: 2,
    };
    const spans = [
      {
        kind: "run" as const,
        runId: "run-ambient",
        startMessageId: "u1",
        ambientSpan: true,
        items: [
          { kind: "user" as const, key: "u1", text: "body of u1" },
          { kind: "assistant" as const, key: "a1", text: "done" },
        ],
      },
    ];
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s-time"
        transcriptSpans={spans}
        messages={[prompt("u1", 1, 20), reply]}
        turnClocks={{ u1: settled("u1", 10) }}
      />
    ));
    const rows = [...container.querySelectorAll<HTMLElement>(".transcript-viewport-row")].map(
      (row) => row.dataset.msgId ?? row.dataset.timeRow,
    );
    expect(rows).toEqual(["time:day:u1", "u1", "unread", "a1", "turn-tail:u1"]);
    expect(container.querySelector('[data-testid="transcript-unread"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="turn-tail"]')).not.toBeNull();
  });
});

