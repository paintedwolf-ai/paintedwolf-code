import { render } from "@solidjs/testing-library";
import { afterEach, expect, it, vi } from "vitest";
import { StreamScrollJump } from "./stream-scroll-jump.tsx";
import { createTranscriptViewportController, TranscriptViewportProvider } from "./transcript-viewport.tsx";
import { bindScrollportMotion, unbindScrollportMotion } from "../../platform/scrolling/scrollport-motion.ts";
import { streamScrollEdges } from "./stream-scroll.ts";

afterEach(() => { vi.unstubAllGlobals(); document.body.replaceChildren(); });

function fixture() {
  const resizes = new Set<() => void>();
  vi.stubGlobal("ResizeObserver", class {
    private readonly notify: () => void;
    constructor(callback: ResizeObserverCallback) {
      this.notify = () => callback([], this as unknown as ResizeObserver);
    }
    observe() { resizes.add(this.notify); }
    unobserve() {}
    disconnect() { resizes.delete(this.notify); }
  });
  const host = document.createElement("div");
  host.className = "den-chat-stream";
  const body = document.createElement("div");
  body.className = "den-chat-stream-body";
  host.append(body);
  document.body.append(host);
  let height = 300;
  Object.defineProperties(host, {
    clientHeight: { value: 300 },
    scrollHeight: { get: () => height },
    scrollTop: { value: 0, writable: true },
  });
  bindScrollportMotion(host, host, host);
  streamScrollEdges(host);
  const viewport = createTranscriptViewportController({ sessionId: () => "jump" });
  viewport.attachStream(host);
  const view = render(() => <TranscriptViewportProvider value={viewport}><StreamScrollJump /></TranscriptViewportProvider>);
  return {
    viewport,
    jump: view.getByTestId("stream-scroll-jump"),
    grow(next: number) {
      height = next;
      for (const notify of resizes) notify();
    },
    dispose() {
      view.unmount();
      viewport.attachStream(null);
      unbindScrollportMotion(host);
    },
  };
}

it("stays hidden while the latest message is followed", async () => {
  const f = fixture();
  try {
    f.grow(1_200);
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    expect(f.jump.getAttribute("aria-hidden")).toBe("true");
  } finally {
    f.dispose();
  }
});

it("offers the jump when content lands below a reader who stopped following", async () => {
  const f = fixture();
  try {
    f.viewport.stopFollowing();
    expect(f.jump.getAttribute("aria-hidden")).toBe("true");

    f.grow(1_200);
    await vi.waitFor(() => expect(f.jump.getAttribute("aria-hidden")).toBe("false"));
    expect(f.jump.tabIndex).toBe(0);

    f.viewport.jumpToTail(false);
    await vi.waitFor(() => expect(f.jump.getAttribute("aria-hidden")).toBe("true"));
  } finally {
    f.dispose();
  }
});
