// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { pinDrawerTranscriptSection } from "./drawer-transcript-scroll.ts";
import {
  bindScrollportMotion,
  unbindScrollportMotion,
} from "../platform/scrolling/scrollport-motion.ts";

describe("drawer-transcript-scroll", () => {
  it("pinDrawerTranscriptSection glides the host under stacked headers", async () => {
    const pane = document.createElement("div");
    const summary = document.createElement("div");
    summary.className = "den-worker-transcript-section-summary";
    Object.defineProperty(summary, "offsetHeight", { value: 32 });
    const section = document.createElement("div");
    section.setAttribute("data-section", "progress");
    const body = document.createElement("div");
    body.className = "den-worker-transcript-section-body";
    section.appendChild(body);
    pane.appendChild(summary);
    pane.appendChild(section);
    document.body.appendChild(pane);

    Object.defineProperty(pane, "scrollTop", { value: 0, writable: true });
    Object.defineProperty(pane, "scrollHeight", { value: 500 });
    Object.defineProperty(pane, "clientHeight", { value: 200 });
    bindScrollportMotion(pane, pane, pane);
    vi.spyOn(body, "getBoundingClientRect").mockReturnValue({
      top: 120,
      bottom: 200,
      left: 0,
      right: 0,
      width: 0,
      height: 80,
      x: 0,
      y: 120,
      toJSON: () => ({}),
    });
    vi.spyOn(pane, "getBoundingClientRect").mockReturnValue({
      top: 0,
      bottom: 400,
      left: 0,
      right: 0,
      width: 0,
      height: 400,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    });

    await expect(pinDrawerTranscriptSection({
      pane,
      sectionKey: "progress",
      sectionIndex: 1,
    })).resolves.toBe(true);
    expect(pane.scrollTop).toBe(56);
    unbindScrollportMotion(pane);
    pane.remove();
  });
});
