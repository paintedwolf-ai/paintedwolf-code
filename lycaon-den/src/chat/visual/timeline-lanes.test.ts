import { describe, expect, it } from "vitest";
import { testTimelineManifest } from "./timeline-archive.fixture.ts";
import { timelineFacts, timelineMarkers, timelineMomentText } from "./timeline-lanes.ts";

describe("timeline lanes", () => {
  it("marks actions and the page events a reader looks for, in time order", () => {
    const markers = timelineMarkers(testTimelineManifest());
    expect(markers.map((m) => [m.lane, m.atMs, m.tone, m.label])).toEqual([
      ["action", 100, "neutral", "click #go"],
      ["page", 130, "muted", "Layout shift 0.010 after input"],
      ["page", 300, "danger", "GET /api/items 500"],
      ["page", 320, "danger", "status 500"],
      ["page", 900, "warning", "Layout shift 0.180"],
      ["page", 900, "warning", "#content moved 120 px down"],
      ["page", 910, "muted", "Long task 72 ms"],
    ]);
    expect(markers[0]?.endMs).toBe(140);
  });

  it("leaves successful requests and console output off the lanes", () => {
    const manifest = testTimelineManifest();
    manifest.events = [
      { at_ms: 10, kind: "request", detail: { method: "GET", url: "/ok", status: 200 } },
      { at_ms: 20, kind: "console", detail: { line: "ready" } },
    ];
    manifest.summary.watch = [];
    expect(timelineMarkers(manifest).filter((m) => m.lane === "page")).toEqual([]);
  });

  it("states the summary, most telling first", () => {
    const facts = timelineFacts(testTimelineManifest().summary);
    expect(facts.map((f) => [f.text, f.tone])).toEqual([
      ["Stable from 0.90s", "neutral"],
      ["Unexpected layout shift 0.180 (1)", "warning"],
      ["Shift after input 0.010", "muted"],
      ["#content moved up to 120 px", "warning"],
      ["1 failed request", "danger"],
      ["1 error", "danger"],
      ["1 long task, longest 72 ms", "muted"],
    ]);
    expect(facts[1]?.atMs).toBe(900);
  });

  it("speaks a moment with the action it follows", () => {
    const manifest = testTimelineManifest();
    expect(timelineMomentText(manifest, 50)).toBe("0.05s");
    expect(timelineMomentText(manifest, 900)).toBe("0.90s, after click #go");
  });
});
