// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { resetTextScaleForTests } from "./accessibility-text-size.ts";

const invokeMock = vi.fn();
const listenMock = vi.fn();
const isTauriRuntimeMock = vi.fn(() => false);

vi.mock("@tauri-apps/api/core", () => ({
  invoke: (...args: unknown[]) => invokeMock(...args),
}));

vi.mock("@tauri-apps/api/event", () => ({
  listen: (...args: unknown[]) => listenMock(...args),
}));

vi.mock("../runtime.ts", () => ({
  isTauriRuntime: () => isTauriRuntimeMock(),
}));

describe("applyTextScale", () => {
  afterEach(() => {
    // Keep the product and operating-system inputs isolated between tests.
    resetTextScaleForTests();
    document.documentElement.style.removeProperty("--den-text-scale");
  });

  it("sets --den-text-scale on documentElement", async () => {
    const { applyTextScale } = await import("./accessibility-text-size.ts");
    applyTextScale(19 / 17);
    expect(document.documentElement.style.getPropertyValue("--den-text-scale")).toBe(
      String(19 / 17),
    );
  });

  it("applies payload.textScale", async () => {
    const { applyTextSizePayload } = await import("./accessibility-text-size.ts");
    applyTextSizePayload({
      category: "UICTContentSizeCategoryXL",
      textScale: 19 / 17,
      revision: 1,
    });
    expect(document.documentElement.style.getPropertyValue("--den-text-scale")).toBe(
      String(19 / 17),
    );
  });

  it("composes the product scale with the operating-system scale", async () => {
    const { applyProductTextScale, applyTextScale, effectiveTextScale } = await import(
      "./accessibility-text-size.ts"
    );
    applyTextScale(19 / 17);
    expect(effectiveTextScale()).toBe(19 / 17);
    applyProductTextScale(1.15);
    expect(effectiveTextScale()).toBe((19 / 17) * 1.15);
    expect(document.documentElement.style.getPropertyValue("--den-text-scale")).toBe(
      String((19 / 17) * 1.15),
    );
    applyProductTextScale(1);
    expect(effectiveTextScale()).toBe(19 / 17);
  });
});

describe("setupAccessibilityTextSize", () => {
  afterEach(() => {
    document.documentElement.style.removeProperty("--den-text-scale");
    invokeMock.mockReset();
    listenMock.mockReset();
    isTauriRuntimeMock.mockReset();
    isTauriRuntimeMock.mockReturnValue(false);
    vi.resetModules();
  });

  it("no-ops outside Tauri runtime", async () => {
    isTauriRuntimeMock.mockReturnValue(false);
    const { setupAccessibilityTextSize } = await import("./accessibility-text-size.ts");
    await setupAccessibilityTextSize();
    expect(invokeMock).not.toHaveBeenCalled();
    expect(document.documentElement.style.getPropertyValue("--den-text-scale")).toBe("");
  });

  it("invokes preferred size and listens for live changes", async () => {
    isTauriRuntimeMock.mockReturnValue(true);
    invokeMock.mockResolvedValueOnce({
      category: "UICTContentSizeCategoryXL",
      textScale: 19 / 17,
      revision: 1,
    });
    listenMock.mockImplementation(
      async (_event: string, handler: (e: { payload: unknown }) => void) => {
        handler({
          payload: {
            category: "UICTContentSizeCategoryXXL",
            textScale: 21 / 17,
            revision: 2,
          },
        });
        return () => {};
      },
    );

    const { setupAccessibilityTextSize, TEXT_SIZE_CHANGED_EVENT } = await import(
      "./accessibility-text-size.ts"
    );
    await setupAccessibilityTextSize();

    expect(listenMock).toHaveBeenCalledWith(TEXT_SIZE_CHANGED_EVENT, expect.any(Function));
    expect(invokeMock).toHaveBeenCalledWith("accessibility_preferred_text_size");
    expect(document.documentElement.style.getPropertyValue("--den-text-scale")).toBe(
      String(21 / 17),
    );
  });

  it("keeps a live update when an older initial snapshot arrives afterward", async () => {
    isTauriRuntimeMock.mockReturnValue(true);
    invokeMock.mockResolvedValueOnce({
      category: "UICTContentSizeCategoryXL",
      textScale: 19 / 17,
      revision: 1,
    });
    listenMock.mockImplementation(
      async (_event: string, handler: (e: { payload: unknown }) => void) => {
        handler({
          payload: {
            category: "UICTContentSizeCategoryXXL",
            textScale: 21 / 17,
            revision: 2,
          },
        });
        return () => {};
      },
    );

    const { setupAccessibilityTextSize } = await import("./accessibility-text-size.ts");
    await setupAccessibilityTextSize();

    expect(document.documentElement.style.getPropertyValue("--den-text-scale")).toBe(
      String(21 / 17),
    );
  });
});
