import { afterEach, describe, expect, it, vi } from "vitest";
import { announceThroughHost } from "./host-announcement.ts";

const invokeMock = vi.fn();
const runtime = vi.hoisted(() => ({
  tauri: true,
  platform: "linux" as "linux" | "macos" | "windows" | null,
}));

vi.mock("@tauri-apps/api/core", () => ({
  invoke: (...args: unknown[]) => invokeMock(...args),
}));

vi.mock("../runtime.ts", () => ({
  isTauriRuntime: () => runtime.tauri,
  tauriPlatform: () => runtime.platform,
}));

afterEach(() => {
  invokeMock.mockReset();
  runtime.tauri = true;
  runtime.platform = "linux";
});

describe("announceThroughHost", () => {
  it("asks the Linux host to speak the text", async () => {
    invokeMock.mockResolvedValue(undefined);
    await announceThroughHost("Assistant: done");
    expect(invokeMock).toHaveBeenCalledWith("den_announce", { text: "Assistant: done" });
  });

  it("leaves macOS and other platforms to the live region", async () => {
    runtime.platform = "macos";
    await announceThroughHost("Assistant: done");
    runtime.platform = "windows";
    await announceThroughHost("Assistant: done");
    expect(invokeMock).not.toHaveBeenCalled();
  });

  it("does nothing outside the desktop runtime or for blank text", async () => {
    runtime.tauri = false;
    await announceThroughHost("Assistant: done");
    runtime.tauri = true;
    await announceThroughHost("  ");
    expect(invokeMock).not.toHaveBeenCalled();
  });

  it("swallows a host failure", async () => {
    invokeMock.mockRejectedValue(new Error("no host"));
    await expect(announceThroughHost("Assistant: done")).resolves.toBeUndefined();
  });
});
