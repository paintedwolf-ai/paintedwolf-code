// @vitest-environment jsdom
import { transcriptDisclosureKey } from "../transcript/presentation/transcript-disclosure-key.ts";
import { fileEditPreviewFixture } from "../../test/file-edit-fixture.ts";
import { flushScrollportFrameForTests, scheduleScrollportFrame } from "../../platform/scrolling/scrollport-frame.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  bindScrollportMotion,
  scrollportMotionForHost,
  unbindScrollportMotion,
} from "../../platform/scrolling/scrollport-motion.ts";
import type { TranscriptItem } from "../transcript/projection/transcript-item-model.ts";
import { transcriptItemContainsMessage } from "../transcript/projection/transcript-item-anchors.ts";
import {
  createTranscriptViewportController,
  registerTranscriptViewport,
  transcriptViewportForSession,
  type TranscriptViewportController,
} from "./transcript-viewport.tsx";
import { cancelStreamSpringScroll } from "./stream-scroll-spring.ts";
import {
  persistTranscriptViewportSnapshot,
  transcriptViewportSnapshot,
} from "./transcript-viewport-state.ts";
import { createAppStore } from "../../store/app-state.ts";
import { resetAppStateSnapshotForTests } from "../../store/app-state-snapshot.ts";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";
import { mockScrollerMotion } from "../../test/scroll-mock.ts";
import {
  beginShellLayoutBusy,
  endShellLayoutBusy,
  flushShellLayoutSettleForTests,
  resetShellLayoutBusyForTests,
} from "../../shell/shell-layout-busy.ts";

type VirtualWindow = Parameters<TranscriptViewportController["attachVirtualWindow"]>[0];

const motionHosts = new Set<HTMLElement>();

function userItem(key: string): TranscriptItem {
  return { kind: "user", key, text: key };
}

function toolItem(
  key: string,
  toolCallId: string,
  messageId = key,
): Extract<TranscriptItem, { kind: "tool" }> {
  return {
    kind: "tool",
    key,
    part: {
      id: key,
      toolCallId,
      assistantMessageId: key.split(":")[0] ?? key,
      messageId,
      tool: "command",
      kind: "command",
      status: "completed",
    },
  };
}

function virtualWindow(
  view: Partial<VirtualWindow> & Pick<VirtualWindow, "items">,
): VirtualWindow {
  return {
    readingPosition: () => null,
    offsetForPosition: () => null,
    scrollToIndex: vi.fn(),
    scrollToOffset: vi.fn(),
    ensureAnchorLoaded: async () => {},
    ensureRowLoaded: async () => {},
    readingDay: () => null,
    measureOrigin: () => {},
    ...view,
  };
}

beforeEach(() => {
  vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: false }) as MediaQueryList));
  vi.stubGlobal("ResizeObserver", class {
    observe() {}
    unobserve() {}
    disconnect() {}
  });
});

afterEach(() => {
  for (const host of motionHosts) unbindScrollportMotion(host);
  motionHosts.clear();
  resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  document.body.replaceChildren();
});

/** A scrollport clamped to its content, 300px tall. */
function streamFixture(opts?: { content?: number; scrollTop?: number }) {
  const stream = document.createElement("div");
  stream.className = "den-chat-stream";
  document.body.append(stream);
  let content = opts?.content ?? 2_000;
  let clientHeight = 300;
  let top = opts?.scrollTop ?? 1_700;
  const clamp = () => {
    top = Math.max(0, Math.min(content - clientHeight, top));
  };
  Object.defineProperties(stream, {
    clientHeight: { get: () => clientHeight },
    scrollHeight: { get: () => content },
    scrollTop: {
      get: () => top,
      set: (value: number) => {
        top = value;
        clamp();
      },
    },
  });
  mockScrollerMotion(stream);
  vi.spyOn(stream, "getBoundingClientRect").mockImplementation(() => ({
    top: 0,
    bottom: clientHeight,
    left: 0,
    right: 600,
    height: clientHeight,
  }) as DOMRect);
  bindScrollportMotion(stream, stream, stream);
  motionHosts.add(stream);
  const motion = scrollportMotionForHost(stream)!;
  return {
    stream,
    motion,
    /** Content grows or shrinks and the transcript reports it before paint. */
    resize(nextContent: number, nextClientHeight = clientHeight) {
      content = nextContent;
      clientHeight = nextClientHeight;
      clamp();
      motion.notifyLayoutMutated();
    },
    /** Content changes without a layout report yet. */
    setContent(nextContent: number) {
      content = nextContent;
      clamp();
    },
    scrollTo(value: number) {
      stream.scrollTop = value;
      stream.dispatchEvent(new Event("scroll"));
      flushScrollportFrameForTests(stream);
    },
  };
}

function followingFixture(sessionId: string, opts?: { content?: number; scrollTop?: number }) {
  const fixture = streamFixture(opts);
  const controller = createTranscriptViewportController({ sessionId: () => sessionId });
  controller.attachStream(fixture.stream);
  controller.activateSession(sessionId);
  return { ...fixture, controller };
}

/** Rows of 100px keyed r0…rN; `resident` decides which rows are loaded. */
function rowsWindow(
  stream: HTMLElement,
  resident: (rowKey: string) => boolean,
  view: Partial<VirtualWindow> = {},
): VirtualWindow {
  return virtualWindow({
    items: () => Array.from({ length: 20 }, (_, index) => userItem(`r${index}`)),
    readingPosition: () => ({
      rowKey: `r${Math.floor(stream.scrollTop / 100)}`,
      rowOffsetPx: stream.scrollTop % 100,
    }),
    offsetForPosition: (position) =>
      resident(position.rowKey)
        ? Number(position.rowKey.slice(1)) * 100 + position.rowOffsetPx
        : null,
    ...view,
  });
}

function runtime(sessionId: string) {
  return {
    client: () => undefined,
    appStore: createAppStore(),
    sessionId: () => sessionId,
    tabOpen: () => false,
    panelRetracted: () => false,
    panelHeightPx: () => 0,
    onPanelRetractedChange: () => {},
  };
}

async function frame(): Promise<void> {
  await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
}

describe("following the latest message", () => {
  it("End resumes following and reaches an expanded tail before paint", () => {
    const f = followingFixture("s-expand-end", { content: 300, scrollTop: 0 });
    const stop = f.controller.bindRuntime(runtime("s-expand-end"));
    const finish = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("activity"), "open");
    f.resize(900);
    finish();
    const event = new KeyboardEvent("keydown", { key: "End", bubbles: true, cancelable: true });
    f.stream.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
    expect(f.controller.following()).toBe(true);
    expect(f.stream.scrollTop).toBe(600);
    stop();
  });

  it("holds a short transcript through expansion, later geometry, and arrivals", () => {
    const f = followingFixture("s-expand", { content: 300, scrollTop: 0 });
    const finish = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("activity"), "open");
    for (const height of [300, 400, 900, 1_400]) {
      f.resize(height);
      expect(f.stream.scrollTop).toBe(0);
    }
    finish();
    flushScrollportFrameForTests(f.stream);
    expect(f.controller.following()).toBe(false);
    f.controller.contentChanged({ delivery: "structural" });
    f.resize(1_600);
    f.controller.contentChanged({ delivery: "prose", rowKey: "answer", firstContent: true });
    f.resize(1_800);
    expect(f.stream.scrollTop).toBe(0);
    f.controller.jumpToTail(false);
    f.resize(2_000);
    expect(f.stream.scrollTop).toBe(1_700);
  });

  it("keeps following when expanded content still fits", () => {
    const f = followingFixture("s-expand-fits", { content: 200, scrollTop: 0 });
    const finish = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("activity"), "open");
    f.resize(280);
    finish();
    flushScrollportFrameForTests(f.stream);
    expect(f.controller.following()).toBe(true);
    f.resize(500);
    expect(f.stream.scrollTop).toBe(200);
  });

  it("waits for the final row measurement before restoring following", () => {
    const f = followingFixture("s-expand-measure", { content: 300, scrollTop: 0 });
    const finish = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("activity"), "open");
    scheduleScrollportFrame(f.stream, "measure", () => f.resize(900));
    finish();
    expect(f.controller.following()).toBe(false);
    flushScrollportFrameForTests(f.stream);
    expect(f.controller.following()).toBe(false);
    expect(f.stream.scrollTop).toBe(0);
  });

  it("waits for child expansion and does not treat a nearly visible tail as visible", () => {
    const f = followingFixture("s-expand-child", { content: 300, scrollTop: 0 });
    const finishParent = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("activity"), "open");
    const finishChild = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("tool"), "open");
    finishParent();
    expect(f.controller.following()).toBe(false);
    f.resize(340);
    finishChild();
    flushScrollportFrameForTests(f.stream);
    expect(f.controller.following()).toBe(false);
    expect(f.stream.scrollTop).toBe(0);
  });

  it("preserves an existing reading position through expansion", () => {
    const f = followingFixture("s-expand-reading", { content: 1_000, scrollTop: 250 });
    f.controller.stopFollowing();
    const finish = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("tool"), "open");
    f.resize(1_300);
    finish();
    flushScrollportFrameForTests(f.stream);
    expect(f.stream.scrollTop).toBe(250);
    expect(f.controller.following()).toBe(false);
  });

  it("selection during an expansion prevents following from being restored", () => {
    const f = followingFixture("s-expand-selection", { content: 300, scrollTop: 0 });
    const stop = f.controller.bindRuntime(runtime("s-expand-selection"));
    const text = document.createTextNode("Expanded content to select");
    f.stream.append(text);
    const finish = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("activity"), "open");
    const range = document.createRange();
    range.selectNodeContents(text);
    document.getSelection()?.removeAllRanges();
    document.getSelection()?.addRange(range);
    document.dispatchEvent(new Event("selectionchange"));
    finish();
    flushScrollportFrameForTests(f.stream);
    expect(f.controller.following()).toBe(false);
    document.getSelection()?.removeAllRanges();
    stop();
  });

  it.each(["reading", "jump", "session", "detach"])("a settled expansion cannot override later %s intent", (action) => {
    const f = followingFixture(`s-expand-${action}`, { content: 300, scrollTop: 0 });
    const finish = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("activity"), "open");
    finish();
    if (action === "reading") f.controller.stopFollowing();
    if (action === "jump") f.controller.jumpToTail(false);
    if (action === "session") {
      f.controller.activateSession("other-session");
      f.controller.stopFollowing();
    }
    if (action === "detach") f.controller.attachStream(null);
    flushScrollportFrameForTests(f.stream);
    expect(f.controller.following()).toBe(action === "jump");
  });

  it("resumes following and repins to tail when closing a disclosure opened at the tail", () => {
    const f = followingFixture("s-motion-tail", { content: 2_000, scrollTop: 1_700 });
    expect(f.controller.following()).toBe(true);

    const finishOpen = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("card-1"), "open");
    f.resize(3_200);
    finishOpen();
    flushScrollportFrameForTests(f.stream);
    expect(f.controller.following()).toBe(false);
    expect(f.stream.scrollTop).toBe(1_700);

    const finishClose = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("card-1"), "close");
    f.resize(2_000);
    finishClose();
    flushScrollportFrameForTests(f.stream);
    expect(f.controller.following()).toBe(true);
    expect(f.stream.scrollTop).toBe(1_700);
  });

  // An accordion closes the open row and opens the pressed one in one motion, in either call order.
  it.each([
    ["close then open", ["close", "open"]],
    ["open then close", ["open", "close"]],
  ] as const)("treats a joined motion with an expansion as an expansion (%s)", (_, order) => {
    const f = followingFixture("s-motion-accordion", { content: 2_000, scrollTop: 1_700 });
    expect(f.controller.following()).toBe(true);

    const finishes = order.map((direction) =>
      f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool(direction === "open" ? "row-b" : "row-a"), direction));
    f.resize(2_300);
    for (const finish of finishes) finish();
    flushScrollportFrameForTests(f.stream);

    expect(f.controller.following()).toBe(false);
    expect(f.stream.scrollTop).toBe(1_700);
  });

  it("does not resume following when closing a disclosure in historical messages", () => {
    const f = followingFixture("s-motion-history", { content: 4_000, scrollTop: 1_000 });
    f.controller.stopFollowing();

    const finishOpen = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("hist-card"), "open");
    f.resize(4_500);
    finishOpen();
    flushScrollportFrameForTests(f.stream);
    expect(f.controller.following()).toBe(false);

    const finishClose = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("hist-card"), "close");
    f.resize(4_000);
    finishClose();
    flushScrollportFrameForTests(f.stream);
    expect(f.controller.following()).toBe(false);
    expect(f.stream.scrollTop).toBe(1_000);
  });

  it("does not resume following on close if the user scrolled away during motion", () => {
    const f = followingFixture("s-motion-scrolled", { content: 2_000, scrollTop: 1_700 });
    const stop = f.controller.bindRuntime(runtime("s-motion-scrolled"));

    const finishOpen = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("card-1"), "open");
    f.resize(3_200);
    f.stream.dispatchEvent(
      new WheelEvent("wheel", { deltaY: -100, bubbles: true, cancelable: true }),
    );
    f.scrollTo(500);
    finishOpen();
    flushScrollportFrameForTests(f.stream);

    const finishClose = f.controller.beginDisclosureMotion(transcriptDisclosureKey.tool("card-1"), "close");
    f.resize(2_000);
    finishClose();
    flushScrollportFrameForTests(f.stream);

    expect(f.controller.following()).toBe(false);
    expect(f.stream.scrollTop).toBe(500);
    stop();
  });

  it("keeps the latest message in view as content grows, before paint", () => {
    const f = followingFixture("s-grow");
    f.resize(2_400);
    expect(f.stream.scrollTop).toBe(2_100);
    f.resize(1_900);
    expect(f.stream.scrollTop).toBe(1_600);
  });

  it("keeps the latest line in view above a growing composer", () => {
    const f = followingFixture("s-composer");
    f.resize(2_000, 240);
    expect(f.stream.scrollTop).toBe(1_760);
  });

  it("keeps following when a new card arrives", () => {
    const f = followingFixture("s-card");
    f.controller.contentChanged({ delivery: "structural" });
    expect(f.controller.following()).toBe(true);
    f.resize(2_400);
    expect(f.stream.scrollTop).toBe(2_100);
  });

  it("keeps the reader's place when a card arrives while they read", () => {
    const f = followingFixture("s-card-reading");
    f.controller.stopFollowing();
    f.controller.contentChanged({ delivery: "structural" });
    expect(f.controller.following()).toBe(false);
    f.resize(2_400);
    expect(f.stream.scrollTop).toBe(1_700);
  });

  it("keeps the reader's place after they act on the transcript", () => {
    const f = followingFixture("s-act");
    f.controller.stopFollowing();
    f.resize(2_400);
    expect(f.stream.scrollTop).toBe(1_700);
  });

  it("resumes following on a user send and lands on the latest message", () => {
    const f = followingFixture("s-send");
    f.controller.stopFollowing();
    f.scrollTo(500);
    f.setContent(2_300);

    f.controller.jumpToTail(false);

    expect(f.controller.following()).toBe(true);
    expect(f.stream.scrollTop).toBe(2_000);
  });

  it("a confirmed send arriving after reader input never resumes following", () => {
    const f = followingFixture("s-confirm");
    f.controller.jumpToTail(false);
    f.controller.stopFollowing();
    f.scrollTo(500);
    f.setContent(2_300);
    f.controller.contentChanged({ delivery: "structural" });
    expect(f.controller.following()).toBe(false);
    expect(f.stream.scrollTop).toBe(500);
    f.resize(2_500);
    expect(f.stream.scrollTop).toBe(500);
  });

  it("repins through tab chrome changes", () => {
    const f = followingFixture("s-chrome");
    f.setContent(2_200);
    f.controller.chromeChanged({ tabOpen: true, panelRetracted: false, panelHeightPx: 0 });
    expect(f.stream.scrollTop).toBe(1_900);
  });

  it("glides to the latest message on jump and then keeps it in view", async () => {
    const f = followingFixture("s-jump");
    f.controller.stopFollowing();
    f.scrollTo(200);

    f.controller.jumpToTail(true);
    expect(f.controller.following()).toBe(true);
    await vi.waitFor(() => expect(f.stream.scrollTop).toBe(1_700));
    await frame();

    f.resize(2_400);
    expect(f.stream.scrollTop).toBe(2_100);
  });

  it("keeps the reader where an interrupted jump stopped", async () => {
    const f = followingFixture("s-jump-interrupted");
    f.controller.stopFollowing();
    f.scrollTo(200);

    f.controller.jumpToTail(true);
    cancelStreamSpringScroll(f.stream);
    await Promise.resolve();
    await Promise.resolve();

    expect(f.controller.following()).toBe(false);
    expect(f.stream.scrollTop).toBe(200);
  });

  it("lands a jump on the latest message even when a card arrives mid-glide", async () => {
    const f = followingFixture("s-jump-card");
    f.controller.stopFollowing();
    f.scrollTo(200);

    f.controller.jumpToTail(true);
    f.controller.contentChanged({ delivery: "structural" });

    expect(f.controller.following()).toBe(true);
    await vi.waitFor(() => expect(f.stream.scrollTop).toBe(1_700));
  });

  it("lands a send from the tail at once and keeps pinning what follows", () => {
    const f = followingFixture("s-jump-on-tail");
    expect(f.stream.scrollTop).toBe(1_700);

    // The optimistic row is already in the DOM, farther below than a glance.
    f.setContent(2_150);
    f.controller.jumpToTail(true);
    expect(f.stream.scrollTop).toBe(1_850);

    // Its measurement, then the composer's activity lane, reach the tail before paint.
    f.resize(2_180);
    expect(f.stream.scrollTop).toBe(1_880);
    f.resize(2_180, 278);
    expect(f.stream.scrollTop).toBe(1_902);
  });

  it("jumps at once under reduced motion", () => {
    vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: true }) as MediaQueryList));
    const f = followingFixture("s-jump-reduced");
    f.controller.stopFollowing();
    f.scrollTo(200);

    f.controller.jumpToTail(true);

    expect(f.stream.scrollTop).toBe(1_700);
  });
});

describe("reading position", () => {
  it("restores a resident viewport after its inactive DOM loses the offset", () => {
    const f = followingFixture("s-resident");
    f.controller.attachVirtualWindow(rowsWindow(f.stream, () => true));
    const stop = f.controller.bindRuntime(runtime("s-resident"));
    f.controller.stopFollowing();
    f.scrollTo(1_234);
    stop();
    f.scrollTo(0);
    f.controller.rowsChanged();

    const stopReopened = f.controller.bindRuntime(runtime("s-resident"));

    expect(f.stream.scrollTop).toBe(1_234);
    stopReopened();
    expect(transcriptViewportSnapshot("s-resident")?.position).toEqual({ rowKey: "r12", rowOffsetPx: 34 });
  });

  it("retains the saved row until the scroll extent is published", () => {
    persistTranscriptViewportSnapshot("s-extent", {
      position: { rowKey: "r9", rowOffsetPx: 10 }, openKeys: [],
    });
    const f = streamFixture({ content: 300, scrollTop: 0 });
    const controller = createTranscriptViewportController({ sessionId: () => "s-extent" });
    controller.attachStream(f.stream);
    controller.attachVirtualWindow(rowsWindow(f.stream, () => true));
    controller.activateSession("s-extent");
    expect(f.stream.scrollTop).toBe(0);

    f.resize(2_000);
    controller.rowsChanged();

    expect(f.stream.scrollTop).toBe(910);
    expect(controller.following()).toBe(false);
  });

  it("saves where the reader is and restores it when the session reopens", () => {
    const first = followingFixture("s-read");
    first.controller.attachVirtualWindow(rowsWindow(first.stream, () => true));
    const stop = first.controller.bindRuntime(runtime("s-read"));
    first.controller.stopFollowing();
    first.scrollTo(1_234);
    stop();

    expect(transcriptViewportSnapshot("s-read")?.position).toEqual({
      rowKey: "r12",
      rowOffsetPx: 34,
    });

    const reopened = streamFixture({ scrollTop: 0 });
    const controller = createTranscriptViewportController({ sessionId: () => "s-read" });
    controller.attachStream(reopened.stream);
    controller.attachVirtualWindow(rowsWindow(reopened.stream, () => true));

    expect(controller.activateSession("s-read")).toBe(true);
    expect(controller.following()).toBe(false);
    expect(reopened.stream.scrollTop).toBe(1_234);
  });

  it("stores no position while following", () => {
    const f = followingFixture("s-follow-save");
    f.controller.attachVirtualWindow(rowsWindow(f.stream, () => true));
    f.controller.saveSession();
    expect(transcriptViewportSnapshot("s-follow-save")).toMatchObject({ openKeys: [] });
    expect(transcriptViewportSnapshot("s-follow-save")?.position).toBeUndefined();
  });

  it("does not release following or record position 0 when shell layout is unstable or stream height is 0", () => {
    const f = followingFixture("s-unstable");
    f.controller.attachVirtualWindow(rowsWindow(f.stream, () => true));
    const stop = f.controller.bindRuntime(runtime("s-unstable"));
    beginShellLayoutBusy();
    try {
      f.scrollTo(0);
      f.controller.saveSession();
      expect(f.controller.following()).toBe(true);
      expect(transcriptViewportSnapshot("s-unstable")?.position).toBeUndefined();
    } finally {
      endShellLayoutBusy();
      resetShellLayoutBusyForTests();
      stop();
    }
  });

  it("a reveal during unstable layout stops following at once and samples the place on settle", async () => {
    const f = followingFixture("s-reveal-unstable");
    f.controller.attachVirtualWindow(rowsWindow(f.stream, () => true));
    const stop = f.controller.bindRuntime(runtime("s-reveal-unstable"));
    beginShellLayoutBusy();
    try {
      f.controller.ensureVisible(document.createElement("div"));
      expect(f.controller.following()).toBe(false);
      f.stream.scrollTop = 1_234;
    } finally {
      endShellLayoutBusy();
    }
    await flushShellLayoutSettleForTests();
    expect(f.controller.following()).toBe(false);
    expect(f.stream.scrollTop).toBe(1_234);
    f.controller.saveSession();
    expect(transcriptViewportSnapshot("s-reveal-unstable")?.position).toEqual({
      rowKey: "r12",
      rowOffsetPx: 34,
    });
    resetShellLayoutBusyForTests();
    stop();
  });

  it("repins tail on shell layout settle when following", async () => {
    const f = followingFixture("s-settle");
    f.controller.attachVirtualWindow(rowsWindow(f.stream, () => true));
    const stop = f.controller.bindRuntime(runtime("s-settle"));
    beginShellLayoutBusy();
    f.scrollTo(100);
    endShellLayoutBusy();
    await flushShellLayoutSettleForTests();
    expect(f.controller.following()).toBe(true);
    expect(f.stream.scrollTop).toBe(1_700);
    stop();
  });

  it("reaches the tail that grew under a modal once the modal closes", async () => {
    const geometryReports: ResizeObserverCallback[] = [];
    vi.stubGlobal("ResizeObserver", class {
      constructor(callback: ResizeObserverCallback) {
        geometryReports.push(callback);
      }
      observe() {}
      unobserve() {}
      disconnect() {}
    });
    const f = followingFixture("s-modal");
    const stage = document.createElement("div");
    document.body.append(stage);
    stage.append(f.stream);
    const stop = f.controller.bindRuntime(runtime("s-modal"));
    // A modal makes the stage inert; the transcript keeps growing behind it.
    stage.setAttribute("inert", "");
    f.resize(2_400);
    for (const report of geometryReports) report([], {} as ResizeObserver);
    expect(f.stream.scrollTop).toBe(1_700);

    stage.removeAttribute("inert");
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(f.controller.following()).toBe(true);
    expect(f.stream.scrollTop).toBe(2_100);
    stop();
  });

  it("does not resurrect a previous reading position when shell layout settles", async () => {
    const f = followingFixture("s-no-resurrect", { content: 2_000, scrollTop: 0 });
    f.controller.attachVirtualWindow(rowsWindow(f.stream, () => true));
    const stop = f.controller.bindRuntime(runtime("s-no-resurrect"));

    f.controller.stopFollowing();
    f.scrollTo(800);
    expect(f.stream.scrollTop).toBe(800);
    f.scrollTo(1_500);

    await flushShellLayoutSettleForTests();
    expect(f.stream.scrollTop).toBe(1_500);
    stop();
  });

  it("restores once the saved row arrives with the transcript", () => {
    persistTranscriptViewportSnapshot("s-hydrate", {
      position: { rowKey: "r9", rowOffsetPx: 10 },
      openKeys: [],
    });
    const f = streamFixture({ scrollTop: 0 });
    let loaded = false;
    const controller = createTranscriptViewportController({ sessionId: () => "s-hydrate" });
    controller.attachStream(f.stream);
    controller.attachVirtualWindow(rowsWindow(f.stream, () => loaded));

    expect(controller.activateSession("s-hydrate")).toBe(true);
    expect(f.stream.scrollTop).toBe(0);

    loaded = true;
    controller.rowsChanged();
    expect(f.stream.scrollTop).toBe(910);
  });

  it("loads older history for a saved row, then restores it", async () => {
    persistTranscriptViewportSnapshot("s-history", {
      position: { rowKey: "r3", rowOffsetPx: 0 },
      openKeys: [],
    });
    const f = streamFixture({ scrollTop: 0 });
    let loaded = false;
    const ensureRowLoaded = vi.fn(async () => {
      loaded = true;
    });
    const controller = createTranscriptViewportController({ sessionId: () => "s-history" });
    controller.attachStream(f.stream);
    controller.attachVirtualWindow(
      rowsWindow(f.stream, () => loaded, { ensureRowLoaded }),
    );
    controller.activateSession("s-history");
    expect(ensureRowLoaded).not.toHaveBeenCalled();

    const stop = controller.bindRuntime(runtime("s-history"));

    expect(ensureRowLoaded).toHaveBeenCalledWith("r3");
    await vi.waitFor(() => expect(f.stream.scrollTop).toBe(300));
    stop();
  });

  it("opens at the latest message when the saved row is gone", async () => {
    persistTranscriptViewportSnapshot("s-gone", {
      position: { rowKey: "r3", rowOffsetPx: 0 },
      openKeys: [],
    });
    const f = streamFixture({ scrollTop: 0 });
    const controller = createTranscriptViewportController({ sessionId: () => "s-gone" });
    controller.attachStream(f.stream);
    controller.attachVirtualWindow(rowsWindow(f.stream, () => false));
    controller.activateSession("s-gone");

    const stop = controller.bindRuntime(runtime("s-gone"));

    await vi.waitFor(() => expect(controller.following()).toBe(true));
    expect(f.stream.scrollTop).toBe(1_700);
    stop();
  });

  it("drops a pending restore once the reader starts scrolling", async () => {
    persistTranscriptViewportSnapshot("s-reader-first", {
      position: { rowKey: "r3", rowOffsetPx: 0 },
      openKeys: [],
    });
    const f = streamFixture({ scrollTop: 0 });
    let loaded = false;
    let finishLoad!: () => void;
    const controller = createTranscriptViewportController({ sessionId: () => "s-reader-first" });
    controller.attachStream(f.stream);
    controller.attachVirtualWindow(rowsWindow(f.stream, () => loaded, {
      ensureRowLoaded: () => new Promise<void>((resolve) => {
        finishLoad = () => {
          loaded = true;
          resolve();
        };
      }),
    }));
    controller.activateSession("s-reader-first");
    const stop = controller.bindRuntime(runtime("s-reader-first"));

    f.stream.dispatchEvent(new WheelEvent("wheel", { bubbles: true, deltaY: 40 }));
    finishLoad();
    await Promise.resolve();
    await Promise.resolve();

    expect(f.stream.scrollTop).toBe(0);
    expect(controller.following()).toBe(false);
    stop();
  });

  it("does not carry a saved position into another session", () => {
    const f = followingFixture("s-old");
    f.controller.attachVirtualWindow(rowsWindow(f.stream, () => true));
    f.controller.stopFollowing();
    f.stream.scrollTop = 500;

    expect(f.controller.activateSession("s-new", "s-old")).toBe(false);

    expect(f.controller.following()).toBe(true);
    expect(transcriptViewportSnapshot("s-old")?.position).toEqual({
      rowKey: "r17",
      rowOffsetPx: 0,
    });
    expect(transcriptViewportSnapshot("s-new")).toBeUndefined();
  });
});

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
