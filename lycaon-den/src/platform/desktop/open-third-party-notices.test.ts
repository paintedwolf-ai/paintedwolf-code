import { beforeEach, describe, expect, it, vi } from "vitest";
import { openThirdPartyNotices } from "./open-third-party-notices.ts";

const mocks = vi.hoisted(() => ({ invoke: vi.fn(), native: vi.fn() }));
vi.mock("@tauri-apps/api/core", () => ({ invoke: mocks.invoke }));
vi.mock("../runtime.ts", () => ({ isTauriRuntime: mocks.native }));

describe("openThirdPartyNotices", () => {
  beforeEach(() => vi.resetAllMocks());
  it("explains the desktop requirement without invoking a missing bridge", async () => {
    mocks.native.mockReturnValue(false);
    await expect(openThirdPartyNotices()).rejects.toThrow("available in the desktop app");
    expect(mocks.invoke).not.toHaveBeenCalled();
  });
  it("opens the native notices when the bridge is available", async () => {
    mocks.native.mockReturnValue(true);
    await openThirdPartyNotices();
    expect(mocks.invoke).toHaveBeenCalledWith("open_third_party_notices");
  });
  it("preserves a native operation failure", async () => {
    mocks.native.mockReturnValue(true);
    mocks.invoke.mockRejectedValue(new Error("File unavailable"));
    await expect(openThirdPartyNotices()).rejects.toThrow("File unavailable");
  });
});
