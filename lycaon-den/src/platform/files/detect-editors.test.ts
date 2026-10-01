import { describe, expect, it, vi } from "vitest";
import { detectEditors } from "./detect-editors.ts";

describe("detect-editors", () => {
  it("returns null in non-Tauri (web) contexts — the probe cannot run there", async () => {
    const got = await detectEditors({ isTauri: false });
    expect(got).toBeNull();
  });

  it("returns the Rust command result in Tauri contexts", async () => {
    const invokeDetect = vi.fn().mockResolvedValue([
      {
        id: "cursor",
        displayName: "Cursor",
        installPath: "/Applications/Cursor.app",
      },
    ]);
    const got = await detectEditors({ isTauri: true, invokeDetect });
    expect(got).toEqual([
      {
        id: "cursor",
        displayName: "Cursor",
        installPath: "/Applications/Cursor.app",
      },
    ]);
  });

  it("propagates invoke failures instead of reporting an empty probe", async () => {
    // Probe failure remains distinct from an empty result.
    const invokeDetect = vi.fn().mockRejectedValue(new Error("boom"));
    await expect(detectEditors({ isTauri: true, invokeDetect })).rejects.toThrow("boom");
  });
});
