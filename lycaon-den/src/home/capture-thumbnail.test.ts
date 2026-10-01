// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import { recordProjectThumbnail } from "./thumbnail-store.ts";
import {
  captureProjectThumbnail,
  renderProjectThumbnail,
} from "./capture-thumbnail.ts";

vi.mock("../chat/stream/den-main-thread-perf.ts", () => ({
  perfMark: vi.fn(),
}));

vi.mock("../settings/appearance/appearance-prefs.ts", () => ({
  activeThemePaintKey: () => "theme-key",
}));

vi.mock("./thumbnail-store.ts", () => ({
  recordProjectThumbnail: vi.fn(),
}));

const palette = {
  background: "#111",
  panel: "#222",
  line: "#333",
  text: "#eee",
  muted: "#999",
  accent: "#58f",
};

beforeEach(() => {
  vi.mocked(recordProjectThumbnail).mockClear();
});

describe("renderProjectThumbnail", () => {
  it("renders a bounded semantic SVG from project state", () => {
    const domReads = vi.spyOn(window, "getComputedStyle");
    const dataUrl = renderProjectThumbnail(
      {
        projectName: "Painted & Wolf",
        sessionTitle: "Fix <performance>",
        messageCount: 42,
        stage: "Conversation",
      },
      palette,
    );

    const svg = decodeURIComponent(dataUrl.split(",", 2)[1] ?? "");
    expect(dataUrl).toMatch(/^data:image\/svg\+xml/);
    expect(svg).toContain("Painted &amp; Wolf");
    expect(svg).toContain("Fix &lt;performance&gt;");
    expect(svg).toContain("42 messages");
    const document = new DOMParser().parseFromString(svg, "image/svg+xml");
    const textClip = document.querySelector("clipPath#project-thumb-text rect");
    expect(textClip?.getAttribute("x")).toBe("108");
    expect(textClip?.getAttribute("width")).toBe("188");
    expect(document.querySelectorAll("text")).toHaveLength(3);
    for (const text of document.querySelectorAll("text")) {
      expect(text.parentElement?.getAttribute("clip-path")).toBe(
        "url(#project-thumb-text)",
      );
    }
    expect(document.querySelector("text:last-of-type")?.getAttribute("y")).toBe(
      "86",
    );
    expect(svg.length).toBeLessThan(3_500);
    expect(domReads).not.toHaveBeenCalled();
  });
});

describe("captureProjectThumbnail", () => {
  it("stores the semantic thumbnail under the active paint key", () => {
    captureProjectThumbnail("p1", {
      projectName: "Painted Wolf",
      sessionTitle: "Performance",
      messageCount: 7,
      stage: "Conversation",
    });

    expect(recordProjectThumbnail).toHaveBeenCalledWith(
      "p1",
      expect.stringMatching(/^data:image\/svg\+xml/),
      "theme-key",
    );
  });
});
