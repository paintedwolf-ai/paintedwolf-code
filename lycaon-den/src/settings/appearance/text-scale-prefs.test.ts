import { describe, expect, it } from "vitest";
import { LINUX_TEXT_SCALING_CATEGORY } from "../../platform/desktop/accessibility-text-size.ts";
import { textSizeHint } from "./text-scale-prefs.ts";

describe("textSizeHint", () => {
  it("names the macOS accessibility text size it composes with", () => {
    expect(textSizeHint("macos", "UICTContentSizeCategoryL")).toContain(
      "Mac's accessibility text size",
    );
  });

  it("tells a Linux reader the desktop factor is already applied by the window", () => {
    expect(textSizeHint("linux", LINUX_TEXT_SCALING_CATEGORY)).toContain(
      "which the window already applies",
    );
  });

  it("admits when a Linux desktop exposes no text scaling preference", () => {
    expect(textSizeHint("linux", "default")).toContain("reports no text scaling preference");
  });

  it("claims no host composition elsewhere", () => {
    expect(textSizeHint("windows", "default")).toBe("Adjusts the app's reading size.");
    expect(textSizeHint(null, "default")).toBe("Adjusts the app's reading size.");
  });
});
