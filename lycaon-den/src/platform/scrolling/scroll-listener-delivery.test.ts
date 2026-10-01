// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { bindScrollportMotion, unbindScrollportMotion } from "./scrollport-motion.ts";
import { flushScrollportFrameForTests } from "./scrollport-frame.ts";
import { subscribeStreamScroll } from "../../chat/stream/stream-scroll.ts";

vi.mock("overlayscrollbars", () => ({ OverlayScrollbars: () => undefined }));

afterEach(() => vi.unstubAllGlobals());

for (const kind of ["commit", "stream"] as const) {
  describe(`${kind} scroll listeners`, () => {
    function fixture() {
      vi.stubGlobal("requestAnimationFrame", vi.fn(() => 1));
      vi.stubGlobal("cancelAnimationFrame", vi.fn());
      const host = document.createElement("div");
      Object.defineProperties(host, {
        clientHeight: { value: 100 },
        clientWidth: { value: 100 },
        scrollHeight: { value: 1000 },
        scrollWidth: { value: 100 },
      });
      const motion = bindScrollportMotion(host, host, host);
      const cleanups: Array<() => void> = [];
      let offset = 100;
      return {
        subscribe(handler: () => void) {
          const release = kind === "commit"
            ? motion.subscribeCommits(handler)
            : subscribeStreamScroll(host, handler);
          cleanups.push(release);
          return release;
        },
        deliver() {
          if (kind === "commit") motion.commit(offset += 100, "thumb_drag");
          else {
            host.dispatchEvent(new Event("scroll"));
            flushScrollportFrameForTests(host);
          }
        },
        dispose() {
          for (const release of cleanups) release();
          unbindScrollportMotion(host);
        },
      };
    }

    it("delivers a listener that resubscribes itself only once per dispatch", () => {
      const f = fixture();
      let calls = 0;
      let release = () => {};
      const handler = () => {
        if (++calls > 20) throw new Error("Listener dispatch did not yield");
        release();
        release = f.subscribe(handler);
      };
      release = f.subscribe(handler);
      // Preserve the dispatcher while the listener is replaced.
      f.subscribe(() => {});
      try {
        f.deliver();
        expect(calls).toBe(1);
        f.deliver();
        expect(calls).toBe(2);
      } finally {
        f.dispose();
      }
    });

    it("defers newly subscribed listeners and skips disposed listeners", () => {
      const f = fixture();
      const added = vi.fn();
      const removed = vi.fn();
      let release = () => {};
      f.subscribe(() => {
        release();
        f.subscribe(added);
      });
      release = f.subscribe(removed);
      try {
        f.deliver();
        expect(removed).not.toHaveBeenCalled();
        expect(added).not.toHaveBeenCalled();
        f.deliver();
        expect(added).toHaveBeenCalledOnce();
      } finally {
        f.dispose();
      }
    });

    it("defers a replacement of a callback still waiting in the current batch", () => {
      const f = fixture();
      const replaced = vi.fn();
      let release = () => {};
      let replace = true;
      f.subscribe(() => {
        if (!replace) return;
        replace = false;
        release();
        f.subscribe(replaced);
      });
      release = f.subscribe(replaced);
      try {
        f.deliver();
        expect(replaced).not.toHaveBeenCalled();
        f.deliver();
        expect(replaced).toHaveBeenCalledOnce();
      } finally {
        f.dispose();
      }
    });

    it("does not let stale cleanup remove a newer subscription", () => {
      const f = fixture();
      const handler = vi.fn();
      const release = f.subscribe(handler);
      release();
      f.subscribe(handler);
      try {
        release();
        f.deliver();
        expect(handler).toHaveBeenCalledOnce();
      } finally {
        f.dispose();
      }
    });
  });
}
