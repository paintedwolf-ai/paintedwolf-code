// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  applyWalkMark,
  clearWalkMarks,
  installWalkTranscriptMark,
  walkTranscriptStreamForSession,
} from "./walk-transcript-mark.ts";
import {
  createTranscriptViewportController,
  registerTranscriptViewport,
} from "../../stream/transcript-viewport.tsx";
import {
  enterWalk,
  leaveWalk,
  resetWalkForTests,
  stepWalk,
} from "../../../files/walk/walk-store.ts";
import {
  walkClientFixture,
  walkEffectFixture,
  walkResponseFixture,
} from "../../../files/walk/walk-fixtures.ts";

const MARK = "den-walk-step-mark";
const CHAT_CSS = readFileSync(
  join(import.meta.dirname, "../../../chat-domain.css"),
  "utf8",
);
const WALK_MARK_SELECTOR = [
  ".den-chat-stream",
  "  .transcript-viewport-row",
  "  :is(",
  "    [data-tool-call-id],",
  "    .den-activity-span,",
  "    .den-diff-group",
  "  ).den-walk-step-mark {",
].join("\n");

function stream(ids: string[], sessionId?: string): HTMLElement {
  const root = document.createElement("div");
  root.className = "den-chat-stream";
  if (sessionId) root.dataset.sessionId = sessionId;
  for (const id of ids) {
    const el = document.createElement("div");
    el.setAttribute("data-tool-call-id", id);
    root.appendChild(el);
  }
  document.body.appendChild(root);
  return root;
}

beforeEach(() => {
  document.body.innerHTML = "";
  resetWalkForTests();
});

describe("applyWalkMark", () => {
  it("keeps the walk treatment above tool-card styling", () => {
    const start = CHAT_CSS.indexOf(WALK_MARK_SELECTOR);
    expect(start).toBeGreaterThan(-1);
    const body = CHAT_CSS.slice(start, CHAT_CSS.indexOf("}", start));
    expect(body).toContain("box-shadow: 0 0 0 2px var(--den-accent-signal)");
    expect(body).toContain("var(--den-accent-signal) 14%");
  });

  it("rings exactly one chicklet", () => {
    const root = stream(["t1", "t2", "t3"]);
    expect(applyWalkMark(root, "t2")).toBe(true);
    expect(root.querySelectorAll(`.${MARK}`)).toHaveLength(1);
    expect(
      root.querySelector(`[data-tool-call-id="t2"]`)?.classList.contains(MARK),
    ).toBe(true);
  });

  it("moves the ring rather than accumulating them", () => {
    const root = stream(["t1", "t2"]);
    applyWalkMark(root, "t1");
    applyWalkMark(root, "t2");
    expect(root.querySelectorAll(`.${MARK}`)).toHaveLength(1);
    expect(
      root.querySelector(`[data-tool-call-id="t2"]`)?.classList.contains(MARK),
    ).toBe(true);
  });

  it("rings the visible activity item when its connected tool is grouped", () => {
    const root = stream([], "session-1");
    const span = document.createElement("details");
    span.className = "den-activity-span";
    span.innerHTML = [
      "<summary>Read ×2</summary>",
      '<div><div class="den-tool-part" data-tool-call-id="t1"></div></div>',
    ].join("");
    root.appendChild(span);

    expect(applyWalkMark(root, "t1")).toBe(true);
    expect(span.classList.contains(MARK)).toBe(true);
    expect(
      span.querySelector("[data-tool-call-id=t1]")?.classList.contains(MARK),
    ).toBe(false);
  });

  it("moves the visible chat ring forward and backward with the Walk playhead", async () => {
    const root = stream(["call-a", "call-b"], "s1");
    const client = walkClientFixture(() =>
      walkResponseFixture([
        walkEffectFixture("a", 1, 1),
        walkEffectFixture("b", 1, 2),
      ]),
    );
    const stop = installWalkTranscriptMark();
    try {
      await enterWalk("p1", client, "s1");
      await vi.waitFor(() =>
        expect(
          root.querySelector("[data-tool-call-id=call-a]")?.classList.contains(MARK),
        ).toBe(true),
      );

      stepWalk("p1", 1);
      await vi.waitFor(() =>
        expect(
          root.querySelector("[data-tool-call-id=call-b]")?.classList.contains(MARK),
        ).toBe(true),
      );

      stepWalk("p1", -1);
      await vi.waitFor(() =>
        expect(
          root.querySelector("[data-tool-call-id=call-a]")?.classList.contains(MARK),
        ).toBe(true),
      );
      expect(root.querySelectorAll(`.${MARK}`)).toHaveLength(1);
    } finally {
      stop();
    }
  });

  it("tracks expansion and collapse without changing the selected command or reopening cards", async () => {
    const root = stream([], "s1");
    root.innerHTML = '<details class="den-activity-span"><summary>Commands</summary><div class="den-tool-part" data-tool-call-id="call-a"></div><div class="den-tool-part" data-tool-call-id="call-b"></div></details>';
    const activity = root.querySelector<HTMLDetailsElement>("details")!;
    const command = root.querySelector<HTMLElement>("[data-tool-call-id=call-a]")!;
    const controller = createTranscriptViewportController({ sessionId: () => "s1" });
    controller.attachStream(root);
    const reveal = vi.spyOn(controller, "revealAnchor").mockResolvedValue(true);
    const release = registerTranscriptViewport(controller);
    const stop = installWalkTranscriptMark();
    try {
      await enterWalk("p1", walkClientFixture(() => walkResponseFixture([walkEffectFixture("a", 1, 1)])), "s1");
      expect(activity.classList.contains(MARK)).toBe(true);
      expect(activity.open).toBe(false);
      expect(reveal).toHaveBeenCalledOnce();
      activity.dataset.animating = "true";
      activity.open = true;
      await Promise.resolve();
      expect(activity.classList.contains(MARK)).toBe(true);
      expect(reveal).toHaveBeenCalledOnce();
      delete activity.dataset.animating;
      await vi.waitFor(() => expect(command.classList.contains(MARK)).toBe(true));
      expect(reveal).toHaveBeenCalledTimes(2);
      expect(reveal.mock.calls[1]![1]!.element!()).toBe(command);
      expect(reveal.mock.calls[0]![1]!.signal!.aborted).toBe(true);

      activity.dataset.animating = "true";
      activity.dataset.closing = "";
      await Promise.resolve();
      expect(activity.classList.contains(MARK)).toBe(true);
      expect(reveal).toHaveBeenCalledTimes(2);
      activity.open = false;
      delete activity.dataset.animating;
      delete activity.dataset.closing;
      await vi.waitFor(() => expect(reveal).toHaveBeenCalledTimes(3));
      expect(root.querySelectorAll(`.${MARK}`)).toHaveLength(1);
      expect(reveal.mock.calls[2]![1]!.element!()).toBe(activity.querySelector("summary"));
      leaveWalk("p1");
      activity.open = true;
      await Promise.resolve();
      expect(reveal.mock.calls[2]![1]!.signal!.aborted).toBe(true);
      expect(root.querySelectorAll(`.${MARK}`)).toHaveLength(0);
      expect(reveal).toHaveBeenCalledTimes(3);
    } finally {
      stop();
      release();
    }
  });

  it("clears every ring when the walk ends", () => {
    const root = stream(["t1"]);
    applyWalkMark(root, "t1");
    clearWalkMarks();
    expect(root.querySelectorAll(`.${MARK}`)).toHaveLength(0);
  });

  it("mounts an unmounted row and resolves the selected command without navigating again on remount", async () => {
    const root = stream([], "s1");
    const controller = createTranscriptViewportController({ sessionId: () => "s1" });
    controller.attachStream(root);
    const revealAnchor = vi.spyOn(controller, "revealAnchor");
    const scrollToIndex = vi.fn(() => {
      const card = document.createElement("div");
      card.dataset.toolCallId = "call-a";
      root.append(card);
    });
    controller.attachVirtualWindow({
      items: () => [{
        kind: "tool", key: "a", part: {
          id: "a", toolCallId: "call-a", assistantMessageId: "assistant", messageId: "result",
          tool: "write", kind: "write", status: "completed",
        },
      }],
      ensureAnchorLoaded: async () => {},
      ensureRowLoaded: async () => {},
      readingDay: () => null,
    measureOrigin: () => {},
      readingPosition: () => null,
      offsetForPosition: () => null,
      scrollToIndex,
      scrollToOffset: vi.fn(),
    });
    const release = registerTranscriptViewport(controller);
    const stop = installWalkTranscriptMark();
    try {
      await enterWalk("p1", walkClientFixture(() => walkResponseFixture([walkEffectFixture("a", 1, 1)])), "s1");
      await vi.waitFor(() => expect(root.querySelectorAll(`.${MARK}`)).toHaveLength(1));
      expect(scrollToIndex.mock.calls).toEqual([[0, { align: "center" }]]);
      await vi.waitFor(() => expect(revealAnchor).toHaveBeenCalledOnce());
      const replacement = document.createElement("div");
      replacement.dataset.toolCallId = "call-a";
      root.replaceChildren(replacement);
      await vi.waitFor(() => expect(replacement.classList.contains(MARK)).toBe(true));
      expect(scrollToIndex).toHaveBeenCalledOnce();
      expect(revealAnchor).toHaveBeenCalledOnce();
    } finally {
      stop();
      release();
    }
  });

  it("reports a miss for a step this transcript does not hold", () => {
    const root = stream(["t1"]);
    expect(applyWalkMark(root, "nope")).toBe(false);
    expect(root.querySelectorAll(`.${MARK}`)).toHaveLength(0);
  });

  it("survives a missing transcript entirely", () => {
    expect(applyWalkMark(null, "t1")).toBe(false);
  });

  it("does not trip over an id with CSS-significant characters", () => {
    const root = stream(['call"1']);
    expect(applyWalkMark(root, 'call"1')).toBe(true);
  });

  it("marks a mounted card without competing with virtual navigation", () => {
    const root = stream(["t1"], "session-1");
    const target = root.querySelector<HTMLElement>("[data-tool-call-id=t1]")!;
    root.getBoundingClientRect = () =>
      ({ top: 0, bottom: 100, height: 100 }) as DOMRect;
    target.getBoundingClientRect = () =>
      ({ top: 20, bottom: 40, height: 20 }) as DOMRect;

    const controller = createTranscriptViewportController({
      sessionId: () => "session-1",
    });
    controller.attachStream(root);
    const ensureVisible = vi
      .spyOn(controller, "ensureVisible")
      .mockImplementation(() => {});
    const release = registerTranscriptViewport(controller);
    try {
      expect(applyWalkMark(root, "t1")).toBe(true);
      expect(ensureVisible).not.toHaveBeenCalled();
    } finally {
      release();
    }
  });

  it("resolves the transcript by session during a stage crossfade", () => {
    const leaving = stream(["old"], "session-old");
    const incoming = stream(["new"], "session-new");

    expect(walkTranscriptStreamForSession("session-new")).toBe(incoming);
    expect(walkTranscriptStreamForSession("session-old")).toBe(leaving);
    expect(walkTranscriptStreamForSession("missing")).toBeNull();
    expect(walkTranscriptStreamForSession("   ")).toBeNull();
  });
});
