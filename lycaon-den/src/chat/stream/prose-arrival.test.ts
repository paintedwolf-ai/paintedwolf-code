// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { bindScrollportMotion, unbindScrollportMotion } from "../../platform/scrolling/scrollport-motion.ts";
import { createTranscriptViewportController } from "./transcript-viewport.tsx";
import { canPresentProse, fadeProseArrival } from "./prose-arrival.ts";

const cleanups: Array<() => void> = [];
afterEach(() => {
  cleanups.splice(0).reverse().forEach((cleanup) => cleanup());
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  document.getSelection()?.removeAllRanges();
  document.body.replaceChildren();
});

/** A following transcript at its tail (600) as a new answer row lands at 900. */
function fixture(height = 650) {
  const stream = document.createElement("div");
  stream.className = "den-chat-stream";
  const row = document.createElement("div");
  row.className = "transcript-viewport-row";
  row.dataset.msgId = "answer";
  const prose = document.createElement("div");
  prose.className = "assistant-prose";
  prose.textContent = "The complete answer is readable immediately.";
  row.append(prose);
  stream.append(row);
  document.body.append(stream);
  let contentHeight = 900 + height;
  let scrollTop = 600;
  Object.defineProperties(stream, {
    clientHeight: { value: 300 },
    scrollHeight: { get: () => contentHeight, configurable: true },
    scrollTop: {
      get: () => scrollTop,
      set: (value: number) => {
        scrollTop = Math.max(0, Math.min(contentHeight - 300, value));
      },
    },
  });
  vi.spyOn(stream, "getBoundingClientRect").mockReturnValue({ top: 0, bottom: 300, height: 300 } as DOMRect);
  vi.spyOn(row, "getBoundingClientRect").mockImplementation(() => ({ top: 900 - scrollTop, bottom: 900 + height - scrollTop, height } as DOMRect));
  const animate = vi.fn();
  prose.animate = animate;
  vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: false } as MediaQueryList)));
  const motion = bindScrollportMotion(stream, stream, stream);
  cleanups.push(() => unbindScrollportMotion(stream));
  const sessionId = crypto.randomUUID();
  const controller = createTranscriptViewportController({ sessionId: () => sessionId });
  controller.attachStream(stream);
  controller.activateSession(sessionId);
  cleanups.push(() => controller.attachStream(null));
  return {
    stream,
    row,
    prose,
    animate,
    motion,
    controller,
    sessionId,
    grow: (by: number) => {
      contentHeight += by;
      motion.notifyLayoutMutated();
    },
  };
}

const delivery = { delivery: "prose", rowKey: "answer", firstContent: true } as const;
async function paint() {
  await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
}

describe("prose arrival", () => {
  it("keeps following a tall first answer through growth and visual arrival", async () => {
    const f = fixture();
    f.controller.contentChanged(delivery);
    f.motion.notifyLayoutMutated();
    expect(f.stream.scrollTop).toBe(1250);
    expect(f.controller.following()).toBe(true);

    await paint();
    expect(f.stream.scrollTop).toBe(1250);
    expect(f.animate).toHaveBeenCalledOnce();

    f.grow(400);
    f.controller.contentChanged({ ...delivery, firstContent: false });
    await paint();
    expect(f.stream.scrollTop).toBe(1650);
    expect(f.controller.following()).toBe(true);
    expect(f.animate).toHaveBeenCalledOnce();
  });

  it("does not write scroll from a delayed arrival frame without layout changes", async () => {
    const f = fixture();
    const commit = vi.spyOn(f.motion, "commit");
    f.controller.contentChanged(delivery);
    await paint();
    expect(commit).not.toHaveBeenCalled();
    expect(f.stream.scrollTop).toBe(600);
    expect(f.controller.following()).toBe(true);
  });

  it("follows a short answer and its streaming growth without replaying the fade", async () => {
    const f = fixture(100);
    f.controller.contentChanged(delivery);
    f.motion.notifyLayoutMutated();
    await paint();
    expect(f.stream.scrollTop).toBe(700);
    expect(f.controller.following()).toBe(true);

    f.grow(80);
    f.controller.contentChanged({ ...delivery, firstContent: false });
    expect(f.stream.scrollTop).toBe(780);
    expect(f.animate).toHaveBeenCalledOnce();
  });

  it("treats repeated first-frame chunks as one arrival", async () => {
    const f = fixture(100);
    f.controller.contentChanged(delivery);
    f.controller.contentChanged({ ...delivery, firstContent: false });
    f.controller.contentChanged(delivery);
    await paint();
    expect(f.animate).toHaveBeenCalledOnce();
  });

  it.each(["reading", "switch", "hidden", "detach"])("discards a pending arrival on %s", async (interruption) => {
    const f = fixture();
    f.controller.contentChanged(delivery);
    if (interruption === "reading") f.controller.stopFollowing();
    if (interruption === "switch") f.controller.activateSession("different", f.sessionId);
    if (interruption === "hidden") f.stream.setAttribute("data-resident", "idle");
    if (interruption === "detach") f.controller.attachStream(null);
    await paint();
    expect(f.stream.scrollTop).toBe(600);
    expect(f.animate).not.toHaveBeenCalled();
  });

  it("does not animate or move an answer that arrives while the reader is elsewhere", async () => {
    const f = fixture();
    f.controller.stopFollowing();
    f.controller.contentChanged(delivery);
    f.grow(0);
    await paint();
    expect(f.animate).not.toHaveBeenCalled();
    expect(f.stream.scrollTop).toBe(600);
  });

  it("does not start a virtual reveal when a tall answer arrives", async () => {
    const f = fixture();
    const scrollToOffset = vi.fn();
    cleanups.push(f.controller.attachVirtualWindow({
      items: () => [{ kind: "assistant", key: "answer", text: "Ready" }],
      readingPosition: () => null,
      offsetForPosition: () => null,
      scrollToIndex: vi.fn(),
      scrollToOffset,
      ensureAnchorLoaded: async () => {},
      ensureRowLoaded: async () => {},
      readingDay: () => null,
    measureOrigin: () => {},
    }));
    f.controller.contentChanged(delivery);
    await paint();
    expect(scrollToOffset).not.toHaveBeenCalled();
    expect(f.controller.following()).toBe(true);
  });

  it("honors reduced motion and document visibility", () => {
    const f = fixture();
    vi.mocked(window.matchMedia).mockReturnValue({ matches: true } as MediaQueryList);
    fadeProseArrival(f.row);
    expect(f.animate).not.toHaveBeenCalled();
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    expect(canPresentProse(f.stream)).toBe(false);
  });
});
