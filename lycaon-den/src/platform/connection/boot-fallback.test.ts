// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { dismissBootFallback } from "./boot-fallback.ts";

function animationEnd(name: string): AnimationEvent {
  return Object.assign(new Event("animationend"), { animationName: name }) as AnimationEvent;
}

describe("dismissBootFallback", () => {
  afterEach(() => {
    document.getElementById("den-boot-fallback")?.remove();
    document.body.classList.remove("den-app-ready");
    vi.restoreAllMocks();
  });

  it("removes the splash immediately when reduced motion is preferred", async () => {
    window.matchMedia = vi.fn().mockReturnValue({
      matches: true,
      media: "(prefers-reduced-motion: reduce)",
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    });

    const el = document.createElement("div");
    el.id = "den-boot-fallback";
    document.body.appendChild(el);

    await dismissBootFallback();

    expect(document.getElementById("den-boot-fallback")).toBeNull();
    expect(document.body.classList.contains("den-app-ready")).toBe(true);
  });

  it("marks the app ready when the splash element is missing", async () => {
    await dismissBootFallback();
    expect(document.body.classList.contains("den-app-ready")).toBe(true);
  });

  it("removes the splash without waiting for animation in a peer window", async () => {
    window.matchMedia = vi.fn().mockReturnValue({
      matches: false,
      media: "(prefers-reduced-motion: reduce)",
    });
    const el = document.createElement("div");
    el.id = "den-boot-fallback";
    document.body.appendChild(el);

    await dismissBootFallback({ immediate: true });

    expect(document.getElementById("den-boot-fallback")).toBeNull();
    expect(document.body.classList.contains("den-app-ready")).toBe(true);
  });

  it("waits for fade-in before marking fade-out", async () => {
    window.matchMedia = vi.fn().mockReturnValue({
      matches: false,
      media: "(prefers-reduced-motion: reduce)",
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    });

    const el = document.createElement("div");
    el.id = "den-boot-fallback";
    document.body.appendChild(el);

    const pending = dismissBootFallback();
    expect(el.classList.contains("den-boot-fallback--out")).toBe(false);
    expect(document.body.classList.contains("den-app-ready")).toBe(false);

    el.dispatchEvent(animationEnd("den-boot-fade-in"));
    await Promise.resolve();

    expect(el.classList.contains("den-boot-fallback--out")).toBe(true);
    expect(document.body.classList.contains("den-app-ready")).toBe(true);
    expect(el.style.pointerEvents).toBe("none");

    el.dispatchEvent(animationEnd("den-boot-fade-out"));
    await pending;

    expect(document.getElementById("den-boot-fallback")).toBeNull();
    expect(document.body.classList.contains("den-app-ready")).toBe(true);
  });

  it("skips the fade-in wait when the splash is already visible", async () => {
    window.matchMedia = vi.fn().mockReturnValue({
      matches: false,
      media: "(prefers-reduced-motion: reduce)",
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    });

    const el = document.createElement("div");
    el.id = "den-boot-fallback";
    el.style.opacity = "1";
    document.body.appendChild(el);

    const pending = dismissBootFallback();
    await Promise.resolve();

    expect(el.classList.contains("den-boot-fallback--out")).toBe(true);
    expect(document.body.classList.contains("den-app-ready")).toBe(true);

    el.dispatchEvent(animationEnd("den-boot-fade-out"));
    await pending;
  });
});
