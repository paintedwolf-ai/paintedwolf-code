import { describe, expect, it } from "vitest";
import { buildTestTimelineZip, testTimelineManifest } from "./timeline-archive.fixture.ts";
import { frameIndexAt, isTimelineMime, revokeTimeline, timelineClock, unpackTimelineBytes } from "./timeline-archive.ts";

describe("timeline archive", () => {
  it("decodes frames in manifest order with their times", async () => {
    const recording = await unpackTimelineBytes(buildTestTimelineZip());
    expect(recording.frames.map((frame) => frame.atMs)).toEqual([0, 120, 900, 1400]);
    expect(recording.manifest.actions[0]?.label).toBe("click #go");
    revokeTimeline(recording);
  });

  it("refuses an archive from another format version", async () => {
    const manifest = { ...testTimelineManifest(), version: 2 };
    await expect(unpackTimelineBytes(buildTestTimelineZip(manifest))).rejects.toThrow("version 2");
  });

  it("shows the last frame painted at or before a moment", () => {
    const frames = [0, 120, 900, 1400].map((atMs, index) => ({ index, atMs, width: 1, height: 1, byteLength: 1, src: "" }));
    expect(frameIndexAt(frames, 0)).toBe(0);
    expect(frameIndexAt(frames, 899)).toBe(1);
    expect(frameIndexAt(frames, 900)).toBe(2);
    expect(frameIndexAt(frames, 5000)).toBe(3);
  });

  it("names its mime and labels moments as the poster does", () => {
    expect(isTimelineMime(" application/vnd.lycaon.timeline+zip ")).toBe(true);
    expect(isTimelineMime("application/vnd.lycaon.filmstrip+zip")).toBe(false);
    expect(timelineClock(850)).toBe("0.85s");
  });
});
