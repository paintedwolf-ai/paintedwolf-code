import { createSignal } from "solid-js";
import { render } from "solid-js/web";
import { waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Message } from "../../api/types.ts";
import { createTranscriptViewportController, TranscriptViewportProvider } from "../../chat/stream/transcript-viewport.tsx";
import { bindScrollportMotion, unbindScrollportMotion } from "../../platform/scrolling/scrollport-motion.ts";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import type { ResidentPresence } from "../../ui/resident-surfaces.ts";
import { SessionTranscript } from "./SessionTranscript.tsx";
import { resetSessionTranscriptChatTest } from "./session-transcript-chat-test-harness.ts";

describe("retained transcript attachment", () => {
  afterEach(async () => {
    vi.restoreAllMocks();
    await resetSessionTranscriptChatTest();
  });

  it("remeasures retained rows when they return with changed layout and no message update", async () => {
    let rowHeight = 100;
    vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
      return this.classList.contains("transcript-viewport-row") ? rowHeight : 0;
    });
    const host = document.createElement("div");
    host.className = "den-chat-stream";
    Object.defineProperties(host, {
      clientHeight: { configurable: true, value: 600 },
      scrollHeight: { configurable: true, value: 2000 },
    });
    document.body.appendChild(host);
    const resident = document.createElement("div");
    const [presence, setPresence] = createSignal<ResidentPresence>("idle");
    const viewport = createTranscriptViewportController({ sessionId: () => "retained-layout" });
    bindScrollportMotion(host, host, host);
    viewport.attachStream(host);
    const messages: Message[] = Array.from({ length: 12 }, (_, index) => ({
      id: `layout-${index}`, role: "user", origin: "user", authority: "user", trust_tier: "trusted",
      content: `Layout message ${index}`, ord: index + 1, created_at: "2026-09-12T00:00:00Z",
    }));
    const dispose = render(() => (
      <ResidentPresenceProvider presence={presence()}>
        <TranscriptViewportProvider value={viewport}>
          <SessionTranscript layout="chat" sessionId="retained-layout" messages={messages} />
        </TranscriptViewportProvider>
      </ResidentPresenceProvider>
    ), resident);
    try {
      host.appendChild(resident);
      setPresence("active");
      await new Promise(requestAnimationFrame);
      setPresence("idle");
      resident.remove();
      rowHeight = 40;
      await new Promise(requestAnimationFrame);
      host.appendChild(resident);
      setPresence("active");
      await waitFor(() => {
        const rows = [...resident.querySelectorAll<HTMLElement>(".transcript-viewport-row")];
        expect(rows.length).toBeGreaterThan(1);
        for (let index = 1; index < rows.length; index += 1) {
          // The measured box carries its own seam, so rows advance by the measured height.
          expect(
            Number(rows[index]!.dataset.virtualStart) - Number(rows[index - 1]!.dataset.virtualStart),
            rows[index - 1]!.dataset.msgId ?? rows[index - 1]!.dataset.timeRow,
          ).toBeCloseTo(40);
        }
      });
    } finally {
      dispose();
      viewport.attachStream(null);
      unbindScrollportMotion(host);
      host.remove();
    }
  });

  it("observes scrolling after a detached resident enters its host without a message update", async () => {
    const host = document.createElement("div");
    host.className = "den-chat-stream";
    Object.defineProperties(host, {
      clientHeight: { configurable: true, value: 400 },
      scrollHeight: { configurable: true, value: 16_000 },
    });
    document.body.appendChild(host);
    const resident = document.createElement("div");
    const [presence, setPresence] = createSignal<ResidentPresence>("idle");
    const viewport = createTranscriptViewportController({ sessionId: () => "retained-session" });
    bindScrollportMotion(host, host, host);
    viewport.attachStream(host);
    const messages: Message[] = Array.from({ length: 80 }, (_, index) => ({
      id: `row-${index}`, role: "user", origin: "user", authority: "user", trust_tier: "trusted",
      content: `Retained message ${index}`, ord: index + 1, created_at: "2026-09-12T00:00:00Z",
    }));
    const dispose = render(() => (
      <ResidentPresenceProvider presence={presence()}>
        <TranscriptViewportProvider value={viewport}>
          <SessionTranscript layout="chat" sessionId="retained-session" messages={messages} />
        </TranscriptViewportProvider>
      </ResidentPresenceProvider>
    ), resident);
    try {
      await Promise.resolve();
      host.appendChild(resident);
      setPresence("active");
      await Promise.resolve();
      await Promise.resolve();
      host.scrollTop = 15_600;
      host.dispatchEvent(new Event("scroll"));
      await new Promise(requestAnimationFrame);
      await waitFor(() => {
        expect(resident.querySelector('[data-msg-id="row-79"]')).not.toBeNull();
        expect(resident.querySelector('[data-msg-id="row-0"]')).toBeNull();
      });
      host.scrollTop = 0;
      host.dispatchEvent(new Event("scroll"));
      await new Promise(requestAnimationFrame);
      await waitFor(() => {
        expect(resident.querySelector('[data-msg-id="row-0"]')).not.toBeNull();
        expect(resident.querySelector('[data-msg-id="row-79"]')).toBeNull();
      });
    } finally {
      dispose();
      viewport.attachStream(null);
      unbindScrollportMotion(host);
      host.remove();
    }
  });
});
