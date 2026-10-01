import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const dir = join(import.meta.dirname);

describe("review live rows import direction — no new wire", () => {
  it("consumes the existing source stream without HTTP or new topics", () => {
    const src = readFileSync(join(dir, "review-live.ts"), "utf8");
    for (const forbidden of [
      /from ["'].*api\/client/,
      /listProjectSource/,
      /fetch\(/,
      /"source_changed_feed"/,
      /EventTopic/,
    ]) {
      expect(src, `review-live.ts matched ${forbidden}`).not.toMatch(
        forbidden,
      );
    }
  });

  it("source event fan-in cannot open or move the Files editor", () => {
    const src = readFileSync(join(dir, "../source/source-events.ts"), "utf8");
    expect(src).toMatch(/noteReviewLanding/);
    for (const sink of [
      "openFilesBuffer",
      "setFilesStagePaneMode",
      "openReviewLens",
      "emphasizeAndScrollToLine",
      "revealLine",
      "loadBuffer",
      "enterFollow",
      "noteFollowActivity",
    ]) {
      expect(src, `fan-in must not reach ${sink}`).not.toContain(sink);
    }
  });
});
