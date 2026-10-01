// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";

let resolveFlush: (() => void) | undefined;
const flushComposerDocumentsToDisk = vi.hoisted(() =>
  vi.fn(
    () =>
      new Promise<void>((resolve) => {
        resolveFlush = resolve;
      }),
  ),
);

vi.mock("../../chat/composer/composer-document-store.ts", () => ({ flushComposerDocumentsToDisk }));

import { watchWindowExitForAppState } from "./app-state-window.ts";

describe("watchWindowExitForAppState", () => {
  it("keeps the window-exit flush pending until durability completes", async () => {
    const dispose = watchWindowExitForAppState();
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "hidden",
    });

    document.dispatchEvent(new Event("visibilitychange"));

    expect(flushComposerDocumentsToDisk).toHaveBeenCalledOnce();
    expect(resolveFlush).toBeTypeOf("function");
    resolveFlush?.();
    await Promise.resolve();
    dispose();
  });
});
