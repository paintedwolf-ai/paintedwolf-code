// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { observeCrumbClip } from "./files-editor-crumb-overflow.ts";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("observeCrumbClip", () => {
  it("reports clip from IntersectionObserver without reading scrollWidth", () => {
    const observed: Element[] = [];
    let callback: IntersectionObserverCallback | undefined;
    class FakeIO {
      constructor(cb: IntersectionObserverCallback) {
        callback = cb;
      }
      observe(el: Element) {
        observed.push(el);
      }
      disconnect() {}
      unobserve() {}
      takeRecords(): IntersectionObserverEntry[] {
        return [];
      }
    }
    vi.stubGlobal("IntersectionObserver", FakeIO);

    const crumb = document.createElement("nav");
    const sentinel = document.createElement("span");
    Object.defineProperty(crumb, "scrollWidth", {
      get() {
        throw new Error("scrollWidth must not be read when IO is available");
      },
    });
    const onClip = vi.fn();
    const stop = observeCrumbClip(crumb, sentinel, onClip);

    expect(observed).toEqual([sentinel]);
    callback?.(
      [{ intersectionRatio: 0.4 } as IntersectionObserverEntry],
      {} as IntersectionObserver,
    );
    expect(onClip).toHaveBeenCalledWith(true);
    callback?.(
      [{ intersectionRatio: 1 } as IntersectionObserverEntry],
      {} as IntersectionObserver,
    );
    expect(onClip).toHaveBeenCalledWith(false);
    stop();
  });

  it("falls back to a box compare when IntersectionObserver is missing", () => {
    vi.stubGlobal("IntersectionObserver", undefined);
    const crumb = document.createElement("nav");
    const sentinel = document.createElement("span");
    Object.defineProperty(crumb, "scrollWidth", { value: 240 });
    Object.defineProperty(crumb, "clientWidth", { value: 120 });
    const onClip = vi.fn();
    const stop = observeCrumbClip(crumb, sentinel, onClip);
    expect(onClip).toHaveBeenCalledWith(true);
    stop();
  });
});
