import { describe, expect, it, vi } from "vitest";

const handle = { label: "main" };
vi.mock("@tauri-apps/api/window", () => ({ getCurrentWindow: () => handle }));

describe("currentWindow", () => {
  it("resolves this window's native handle", async () => {
    const { currentWindow } = await import("./current-window.ts");
    expect(await currentWindow()).toBe(handle);
  });
});
