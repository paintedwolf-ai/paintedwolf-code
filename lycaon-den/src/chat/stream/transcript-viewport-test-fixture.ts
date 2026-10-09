// @vitest-environment jsdom

import { flushScrollportFrameForTests } from "../../platform/scrolling/scrollport-frame.ts";
import { afterEach, beforeEach, vi } from "vitest";
import { bindScrollportMotion, scrollportMotionForHost, unbindScrollportMotion } from "../../platform/scrolling/scrollport-motion.ts";
import { TranscriptItem } from "../transcript/projection/transcript-item-model.ts";

import { createTranscriptViewportController } from "./transcript-viewport.tsx";

import { type TranscriptViewportController } from "./transcript-viewport-types.ts";

import { createAppStore } from "../../store/app-state.ts";
import { resetAppStateSnapshotForTests } from "../../store/app-state-snapshot.ts";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";
import { mockScrollerMotion } from "../../test/scroll-mock.ts";

type VirtualWindow = Parameters<TranscriptViewportController["attachVirtualWindow"]>[0];

const motionHosts = new Set<HTMLElement>();

export function userItem(key: string): TranscriptItem {
  return { kind: "user", key, text: key };
}

export function toolItem(
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

export function virtualWindow(
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

export function installViewportFixtureCleanup() {
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

}

/** A scrollport clamped to its content, 300px tall. */
export function streamFixture(opts?: { content?: number; scrollTop?: number }) {
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

export function followingFixture(sessionId: string, opts?: { content?: number; scrollTop?: number }) {
  const fixture = streamFixture(opts);
  const controller = createTranscriptViewportController({ sessionId: () => sessionId });
  controller.attachStream(fixture.stream);
  controller.activateSession(sessionId);
  return { ...fixture, controller };
}

/** Rows of 100px keyed r0…rN; `resident` decides which rows are loaded. */
export function rowsWindow(
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

export function runtime(sessionId: string) {
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

export async function frame(): Promise<void> {
  await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
}
