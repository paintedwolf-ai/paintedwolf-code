// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import { createTranscriptViewportController } from "../../stream/transcript-viewport.tsx";
import { registerTranscriptViewport } from "../../stream/transcript-viewport.tsx";
import { revealChicklet } from "./transcript-reveal.ts";
import { transcriptDisclosureKey } from "./transcript-disclosure-key.ts";

const CARD = transcriptDisclosureKey.checkpoint("card-1");

afterEach(() => {
  document.body.replaceChildren();
});

function fixture(sessionId: string, matchText?: string) {
  const stream = document.createElement("div");
  stream.className = "den-chat-stream";
  stream.dataset.sessionId = sessionId;
  const body = document.createElement("div");
  body.className = "den-chat-stream-body";
  const row = document.createElement("div");
  row.dataset.msgId = "m1";
  row.dataset.disclosureKey = CARD;
  row.className = "transcript-viewport-row";
  const card = document.createElement("article");
  card.textContent = matchText ?? "body";
  row.append(card);
  body.append(row);
  stream.append(body);
  document.body.append(stream);

  const controller = createTranscriptViewportController({ sessionId: () => sessionId });
  controller.attachStream(stream);
  const releaseController = registerTranscriptViewport(controller);
  const scrollToIndex = vi.fn();
  const releaseView = controller.attachVirtualWindow({
    items: () => [{ kind: "user", key: "m1", text: "body" }],
    readingPosition: () => null,
    offsetForPosition: () => null,
    scrollToIndex,
    scrollToOffset: vi.fn(),
    ensureAnchorLoaded: async () => {},
    ensureRowLoaded: async () => {},
    readingDay: () => null,
    measureOrigin: () => {},
  });
  vi.spyOn(controller, "ensureVisible").mockImplementation(() => {});
  return {
    stream,
    row,
    controller,
    scrollToIndex,
    releaseController,
    releaseView,
  };
}

describe("transcript reveal", () => {
  it("routes windowing, temporary expansion, alignment, and focus through one controller", async () => {
    const test = fixture("s1");
    try {
      await expect(
        revealChicklet(test.stream, {
          sessionId: "s1",
          anchor: { chicklet: "message", anchorId: "m1" },
        }),
      ).resolves.toBe(true);
      expect(test.scrollToIndex).toHaveBeenCalledWith(0, { align: "start" });
      expect(test.controller.disclosures.isOpen(CARD)).toBe(true);
      expect(test.controller.ensureVisible).toHaveBeenCalledWith(
        test.row,
        expect.objectContaining({ align: "start" }),
      );
      expect(document.activeElement).toBe(test.row);
    } finally {
      test.releaseView();
      test.releaseController();
    }
  });

  it("waits for a worker transcript to mount before revealing its row", async () => {
    const revealing = revealChicklet(() => document.querySelector(".den-chat-stream"), {
      sessionId: "late-worker", anchor: { chicklet: "message", anchorId: "m1" },
    }, { waitForMount: true, timeoutMs: 1000 });
    const test = fixture("late-worker");
    try {
      expect(await revealing).toBe(true);
      expect(document.activeElement).toBe(test.row);
    } finally {
      test.releaseView();
      test.releaseController();
    }
  });

  it("paints a text match without changing selection state", async () => {
    const test = fixture("s2", "the retry budget is exhausted");
    try {
      await revealChicklet(test.stream, {
        sessionId: "s2",
        anchor: {
          chicklet: "message",
          anchorId: "m1",
          matchText: "retry budget",
        },
      });
      expect(test.row.querySelector("[data-den-find-mark]")).not.toBeNull();
    } finally {
      test.releaseView();
      test.releaseController();
    }
  });

  it("rejects empty and cross-session anchors", async () => {
    const test = fixture("s3");
    try {
      await expect(
        revealChicklet(test.stream, {
          sessionId: "missing",
          anchor: { chicklet: "message", anchorId: "m1" },
        }),
      ).resolves.toBe(false);
      await expect(
        revealChicklet(test.stream, {
          sessionId: "s3",
          anchor: { chicklet: "message", anchorId: "" },
        }),
      ).resolves.toBe(false);
    } finally {
      test.releaseView();
      test.releaseController();
    }
  });

  it("drops a mounted reveal target after its viewport changes sessions", async () => {
    const [sessionId, setSessionId] = createSignal("s4");
    const stream = document.createElement("div");
    stream.className = "den-chat-stream";
    stream.dataset.sessionId = "s4";
    document.body.append(stream);
    const controller = createTranscriptViewportController({ sessionId });
    controller.attachStream(stream);
    const releaseController = registerTranscriptViewport(controller);
    const releaseView = controller.attachVirtualWindow({
      items: () => [{ kind: "user", key: "m1", text: "body" }],
      readingPosition: () => null,
      offsetForPosition: () => null,
      scrollToIndex: vi.fn(),
      scrollToOffset: vi.fn(),
      ensureAnchorLoaded: async () => {},
      ensureRowLoaded: async () => {},
      readingDay: () => null,
    measureOrigin: () => {},
    });

    try {
      const revealing = revealChicklet(stream, {
        sessionId: "s4",
        anchor: { chicklet: "message", anchorId: "m1" },
      });
      await Promise.resolve();
      setSessionId("s5");

      const row = document.createElement("div");
      row.dataset.msgId = "m1";
      row.className = "transcript-viewport-row";
      stream.append(row);

      await expect(revealing).resolves.toBe(false);
      expect(document.activeElement).not.toBe(row);
    } finally {
      releaseView();
      releaseController();
    }
  });
});
