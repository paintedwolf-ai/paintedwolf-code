// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { watchDocumentVisibilityResume } from "./visibility-resume.ts";

describe("watchDocumentVisibilityResume", () => {
  it("wakes reconnect when the document becomes visible", () => {
    const wakeReconnect = vi.fn();
    let visible = false;
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => (visible ? "visible" : "hidden"),
    });

    const stop = watchDocumentVisibilityResume({
      isReconnecting: () => true,
      wakeReconnect,
    });

    visible = true;
    document.dispatchEvent(new Event("visibilitychange"));
    expect(wakeReconnect).toHaveBeenCalledTimes(1);

    stop();
  });

  it("ignores visibility when SSE is not reconnecting", () => {
    const wakeReconnect = vi.fn();
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      value: "visible",
      writable: true,
    });

    watchDocumentVisibilityResume({
      isReconnecting: () => false,
      wakeReconnect,
    });

    document.dispatchEvent(new Event("visibilitychange"));
    expect(wakeReconnect).not.toHaveBeenCalled();
  });

  it("reconciles visible state even when SSE is connected", () => {
    const onVisible = vi.fn();
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      value: "visible",
      writable: true,
    });
    const stop = watchDocumentVisibilityResume({
      isReconnecting: () => false,
      wakeReconnect: vi.fn(),
      onVisible,
    });

    document.dispatchEvent(new Event("visibilitychange"));
    expect(onVisible).toHaveBeenCalledTimes(1);
    stop();
  });
});
