// @vitest-environment jsdom

import { fileEditPreviewFixture } from "../../test/file-edit-fixture.ts";
import { flushScrollportFrameForTests } from "../../platform/scrolling/scrollport-frame.ts";
import { describe, expect, it, vi } from "vitest";

import { TranscriptItem } from "../transcript/projection/transcript-item-model.ts";
import { transcriptItemContainsMessage } from "../transcript/projection/transcript-item-anchors.ts";
import { createTranscriptViewportController } from "./transcript-viewport.tsx";
import { registerTranscriptViewport } from "./transcript-viewport.tsx";
import { transcriptViewportForSession } from "./transcript-viewport.tsx";

import { installViewportFixtureCleanup, userItem, toolItem, virtualWindow, streamFixture, followingFixture } from "./transcript-viewport-test-fixture.ts";
installViewportFixtureCleanup();
describe("transcript viewport reveal", () => {
  function revealFixture() {
    const f = streamFixture({ content: 1_000, scrollTop: 200 });
    const row = document.createElement("div");
    row.className = "transcript-viewport-row";
    row.dataset.msgId = "row-a";
    f.stream.append(row);
    vi.spyOn(row, "getBoundingClientRect").mockImplementation(
      () => ({ top: 20 - (f.stream.scrollTop - 200), bottom: 60 - (f.stream.scrollTop - 200) }) as DOMRect,
    );
    return { ...f, row };
  }

  it("aligns a mounted subcommand through the virtualizer without recentering its group", async () => {
    const { stream, row } = revealFixture();
    const controller = createTranscriptViewportController({ sessionId: () => "s1" });
    controller.attachStream(stream);
    const scrollToIndex = vi.fn();
    const scrollToOffset = vi.fn();
    controller.attachVirtualWindow(virtualWindow({
      items: () => [toolItem("row-a", "chosen")],
      scrollToIndex,
      scrollToOffset,
    }));
    await expect(controller.revealAnchor(
      { chicklet: "tool", anchorId: "chosen" },
      { align: "start", element: () => row },
    )).resolves.toBe(true);
    expect(scrollToIndex).not.toHaveBeenCalled();
    expect(scrollToOffset).toHaveBeenCalledWith(220, { glide: false });
    expect(controller.following()).toBe(false);
  });

  it("does not scroll an obsolete walk step after its history load finishes", async () => {
    const controller = createTranscriptViewportController({ sessionId: () => "s1" });
    let finishLoad!: () => void;
    const loaded = new Promise<void>((resolve) => { finishLoad = resolve; });
    const scrollToIndex = vi.fn();
    controller.attachVirtualWindow(virtualWindow({
      items: () => [userItem("old"), userItem("new")],
      scrollToIndex,
      ensureAnchorLoaded: (anchor) => anchor.anchorId === "old" ? loaded : Promise.resolve(),
    }));
    const abort = new AbortController();
    const oldReveal = controller.revealAnchor(
      { chicklet: "message", anchorId: "old" },
      { signal: abort.signal },
    );
    abort.abort();
    await controller.revealAnchor({ chicklet: "message", anchorId: "new" });
    finishLoad();
    await expect(oldReveal).resolves.toBe(false);
    expect(scrollToIndex.mock.calls).toEqual([[1, { align: "start" }]]);
  });

  it("finds later writes inside a folded diff", async () => {
    const controller = createTranscriptViewportController({ sessionId: () => "s1" });
    const scrollToIndex = vi.fn();
    controller.attachVirtualWindow(virtualWindow({
      items: () => [userItem("prompt"), {
        kind: "file_edit", key: "fold", anchorMessageId: "first-write",
        folds: [{
          key: "first", path: "a.ts", net: fileEditPreviewFixture({ path: "a.ts", before: "", after: "two" }),
          steps: [{
            key: "second", tool: "write", toolCallId: "call-second", messageId: "second-write",
            snapshot: fileEditPreviewFixture({ path: "a.ts", before: "one", after: "two" }),
          }],
        }],
      }],
      scrollToIndex,
    }));
    await expect(controller.revealAnchor({ chicklet: "tool", anchorId: "call-second" })).resolves.toBe(true);
    await expect(controller.revealAnchor({ chicklet: "message", anchorId: "second-write" })).resolves.toBe(true);
    expect(scrollToIndex.mock.calls).toEqual([[1, { align: "start" }], [1, { align: "start" }]]);
  });

  it("drops an asynchronous reveal when the session changes", async () => {
    const controller = createTranscriptViewportController({ sessionId: () => "s1" });
    controller.activateSession("s1");
    let finishLoad: (() => void) | undefined;
    const loaded = new Promise<void>((resolve) => {
      finishLoad = resolve;
    });
    const scrollToIndex = vi.fn();
    controller.attachVirtualWindow(virtualWindow({
      // Matching row IDs isolate cancellation by session identity.
      items: () => [userItem("target")],
      scrollToIndex,
      ensureAnchorLoaded: () => loaded,
    }));

    const revealing = controller.revealAnchor({
      chicklet: "message",
      anchorId: "target",
    });
    controller.activateSession("s2", "s1");
    finishLoad?.();

    await expect(revealing).resolves.toBe(false);
    expect(scrollToIndex).not.toHaveBeenCalled();
  });

  it("loads and scrolls the containing virtual row", async () => {
    const controller = createTranscriptViewportController({ sessionId: () => "s1" });
    const releaseController = registerTranscriptViewport(controller);
    const scrollToIndex = vi.fn();
    const ensureAnchorLoaded = vi.fn(async () => {});
    const releaseView = controller.attachVirtualWindow(virtualWindow({
      items: () => [userItem("a"), toolItem("b", "call-9"), userItem("c")],
      scrollToIndex,
      ensureAnchorLoaded,
    }));
    try {
      await expect(
        controller.revealAnchor({
          chicklet: "tool",
          anchorId: "call-9",
        }),
      ).resolves.toBe(true);
      expect(ensureAnchorLoaded).toHaveBeenCalledOnce();
      expect(scrollToIndex).toHaveBeenCalledWith(1, { align: "start" });
    } finally {
      releaseView();
      releaseController();
    }
  });

  it("reveals a row that streams in after the reveal starts", async () => {
    const controller = createTranscriptViewportController({ sessionId: () => "s1" });
    const scrollToIndex = vi.fn();
    let items = [userItem("a")];
    controller.attachVirtualWindow(virtualWindow({ items: () => items, scrollToIndex }));
    const revealing = controller.revealAnchor({ chicklet: "tool", anchorId: "call-late" });
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    items = [userItem("a"), toolItem("b", "call-late")];
    await expect(revealing).resolves.toBe(true);
    expect(scrollToIndex).toHaveBeenCalledWith(1, { align: "start" });
  });

  it("does not reveal through another session's viewport", () => {
    const controller = createTranscriptViewportController({ sessionId: () => "s1" });
    const releaseController = registerTranscriptViewport(controller);
    try {
      expect(transcriptViewportForSession("s2")).toBeNull();
    } finally {
      releaseController();
    }
  });

  it("accepts exactly one virtual window", () => {
    const controller = createTranscriptViewportController({ sessionId: () => "s1" });
    const window = virtualWindow({ items: () => [userItem("a")] });
    const detach = controller.attachVirtualWindow(window);

    expect(() =>
      controller.attachVirtualWindow({
        ...window,
        items: () => [userItem("b")],
      }),
    ).toThrow("only one virtual window");

    detach();
    expect(() => controller.attachVirtualWindow(window)).not.toThrow();
  });

  it("measures only the latest pending reveal and cancels detached work", () => {
    const f = streamFixture({ content: 2_000, scrollTop: 200 });
    const first = document.createElement("div");
    const second = document.createElement("div");
    f.stream.append(first, second);
    const firstRead = vi.spyOn(first, "getBoundingClientRect").mockReturnValue({ top: 500, bottom: 520, height: 20 } as DOMRect);
    const secondRead = vi.spyOn(second, "getBoundingClientRect").mockReturnValue({ top: 700, bottom: 720, height: 20 } as DOMRect);
    const controller = createTranscriptViewportController({ sessionId: () => "s-coalesced" });
    controller.attachStream(f.stream);
    controller.ensureVisible(first, { align: "start", smooth: false });
    controller.ensureVisible(second, { align: "start", smooth: false });
    expect(firstRead).not.toHaveBeenCalled();
    expect(secondRead).not.toHaveBeenCalled();
    flushScrollportFrameForTests(f.stream);
    expect(firstRead).not.toHaveBeenCalled();
    expect(secondRead).toHaveBeenCalledOnce();
    const settled = f.stream.scrollTop;
    controller.ensureVisible(first, { align: "start", smooth: false });
    controller.attachStream(null);
    flushScrollportFrameForTests(f.stream);
    expect(firstRead).not.toHaveBeenCalled();
    expect(f.stream.scrollTop).toBe(settled);
  });

  it("aligns a reveal below the chat header's notifications and tab panel", () => {
    const stage = document.createElement("div");
    stage.className = "den-shell-stage--chat";
    const header = document.createElement("header");
    header.className = "den-shell-header-chat";
    const tabPanel = document.createElement("div");
    tabPanel.className = "tabs__panel-clip";
    header.append(tabPanel);
    const f = streamFixture({ content: 1_000, scrollTop: 200 });
    const row = document.createElement("div");
    f.stream.append(row);
    stage.append(header, f.stream);
    document.body.append(stage);

    vi.mocked(f.stream.getBoundingClientRect).mockReturnValue({
      top: 100,
      bottom: 400,
      left: 0,
      right: 600,
    } as DOMRect);
    vi.spyOn(header, "getBoundingClientRect").mockReturnValue({
      top: 0,
      bottom: 140,
      left: 0,
      right: 600,
    } as DOMRect);
    vi.spyOn(tabPanel, "getBoundingClientRect").mockReturnValue({
      top: 140,
      bottom: 180,
      left: 0,
      right: 600,
    } as DOMRect);
    vi.spyOn(row, "getBoundingClientRect").mockImplementation(
      () => ({ top: 120 - (f.stream.scrollTop - 200), bottom: 160 - (f.stream.scrollTop - 200) }) as DOMRect,
    );
    const controller = createTranscriptViewportController({
      sessionId: () => "s-overlay-reveal",
    });
    controller.attachStream(f.stream);

    controller.ensureVisible(row, { align: "start", smooth: false });
    flushScrollportFrameForTests(f.stream);

    // The header covers 40px of the scrollport and the tab panel another 40px.
    expect(f.stream.scrollTop).toBe(140);
  });
});

describe("virtual window commands", () => {
  it("keeps the tail pin authoritative over virtual content shifts while following", () => {
    const f = followingFixture("s-command-follow", { content: 1_000, scrollTop: 100 });
    f.controller.commitReveal(175, false);
    f.controller.shiftVirtualContent(125, f.stream.scrollTop);
    expect(f.controller.following()).toBe(true);
    expect(f.stream.scrollTop).toBe(700);
  });

  it("commits reveals and content shifts through the scrollport", () => {
    const f = streamFixture({ content: 1_000, scrollTop: 100 });
    const controller = createTranscriptViewportController({ sessionId: () => "s-commands" });
    controller.attachStream(f.stream);
    controller.stopFollowing();

    controller.commitReveal(175, false);
    expect(f.stream.scrollTop).toBe(175);

    expect(controller.shiftVirtualContent(125, f.stream.scrollTop)).toBe(125);
    expect(f.stream.scrollTop).toBe(300);
  });
});

describe("transcript stable identity", () => {
  it("matches the result message behind a tool row", () => {
    const item = toolItem("assistant-1:call-9", "call-9", "tool-result-1");
    expect(transcriptItemContainsMessage(item, "tool-result-1")).toBe(true);
    expect(transcriptItemContainsMessage(item, "assistant-1:call-9")).toBe(true);
    expect(transcriptItemContainsMessage(item, "missing")).toBe(false);
  });

  it("finds a result message inside an activity span", () => {
    const child = toolItem("assistant-1:call-9", "call-9", "tool-result-1");
    const group: TranscriptItem = {
      kind: "activity_span",
      key: "group-1",
      label: "terminal",
      entries: [{ kind: "tool", part: child.part }],
    };
    expect(transcriptItemContainsMessage(group, "tool-result-1")).toBe(true);
  });
});
