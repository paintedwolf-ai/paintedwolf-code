import { describe, expect, it, vi } from "vitest";
import {
  openUrlInBrowser,
} from "./open-browser.ts";

describe("open-browser", () => {
  it("invokes Tauri open_in_browser with preset", async () => {
    const invokeOpen = vi.fn().mockResolvedValue(undefined);
    await openUrlInBrowser(
      { url: "https://example.com", browser: "chrome" },
      { isTauri: true, invokeOpen },
    );
    expect(invokeOpen).toHaveBeenCalledWith({
      url: "https://example.com",
      preset: "chrome",
      customTemplate: undefined,
    });
  });

  it("uses window open on web", async () => {
    const openUrl = vi.fn();
    await openUrlInBrowser(
      { url: "https://example.com", browser: "firefox" },
      { isTauri: false, openUrl },
    );
    expect(openUrl).toHaveBeenCalledWith("https://example.com");
  });
});
