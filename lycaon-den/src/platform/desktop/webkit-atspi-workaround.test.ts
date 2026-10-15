// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  WEBKIT_ATSPI_WORKAROUND_ATTRIBUTE,
  applyWebEngineFact,
  setupWebKitAtspiWorkaround,
  webkitAtspiWorkaroundActive,
} from "./webkit-atspi-workaround.ts";

const invokeMock = vi.fn();
const isTauriRuntimeMock = vi.fn(() => false);

vi.mock("@tauri-apps/api/core", () => ({
  invoke: (...args: unknown[]) => invokeMock(...args),
}));

vi.mock("../runtime.ts", () => ({
  isTauriRuntime: () => isTauriRuntimeMock(),
}));

const stamped = () => document.documentElement.hasAttribute(WEBKIT_ATSPI_WORKAROUND_ATTRIBUTE);

afterEach(() => {
  applyWebEngineFact(null);
  invokeMock.mockReset();
  isTauriRuntimeMock.mockReturnValue(false);
});

describe("WebKitGTK AT-SPI workaround", () => {
  it("applies on every WebKitGTK release the shell reports", () => {
    for (const version of ["2.50.4", "2.52.6", "2.54.1", null]) {
      applyWebEngineFact({ engine: "webkitgtk", version });
      expect(webkitAtspiWorkaroundActive()).toBe(true);
      expect(stamped()).toBe(true);
    }
  });

  it("leaves every other engine on its own rendering", () => {
    applyWebEngineFact({ engine: "wkwebview", version: "21" });
    expect(webkitAtspiWorkaroundActive()).toBe(false);
    expect(stamped()).toBe(false);
    applyWebEngineFact(null);
    expect(webkitAtspiWorkaroundActive()).toBe(false);
  });

  it("reads the engine fact from the shell", async () => {
    isTauriRuntimeMock.mockReturnValue(true);
    invokeMock.mockResolvedValueOnce({ engine: "webkitgtk", version: "2.50.4" });
    await setupWebKitAtspiWorkaround();
    expect(invokeMock).toHaveBeenCalledWith("den_web_engine");
    expect(webkitAtspiWorkaroundActive()).toBe(true);
  });

  it("keeps the default rendering without a shell or a fact", async () => {
    await setupWebKitAtspiWorkaround();
    expect(invokeMock).not.toHaveBeenCalled();
    isTauriRuntimeMock.mockReturnValue(true);
    invokeMock.mockRejectedValueOnce(new Error("no command"));
    await setupWebKitAtspiWorkaround();
    expect(webkitAtspiWorkaroundActive()).toBe(false);
  });
});
