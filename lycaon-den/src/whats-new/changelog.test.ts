import { describe, expect, it } from "vitest";
import { CHANGELOG_RAW, sectionFor } from "./changelog.ts";

const SAMPLE = `# Changelog

## [Unreleased]

### Added

- Not shipped yet.

## [0.2.0] — 2026-08-01

### Added

- Second release feature.

## [0.1.0] — 2026-07-15

### Added

- First release feature.
`;

describe("sectionFor", () => {
  it("prefix-matches date-suffixed headings", () => {
    expect(sectionFor(SAMPLE, "0.1.0")).toBe(
      "### Added\n\n- First release feature.",
    );
    expect(sectionFor(SAMPLE, "0.2.0")).toContain("Second release feature");
  });

  it("tolerates a leading v on the queried version", () => {
    expect(sectionFor(SAMPLE, "v0.1.0")).toBe(sectionFor(SAMPLE, "0.1.0"));
    expect(sectionFor(SAMPLE, "V0.2.0")).toContain("Second release");
  });

  it("returns empty when the version is absent", () => {
    expect(sectionFor(SAMPLE, "9.9.9")).toBe("");
    expect(sectionFor(SAMPLE, "0.1.0-dev")).toBe("");
  });

  it("returns the last section through EOF", () => {
    expect(sectionFor(SAMPLE, "0.1.0")).toMatch(/First release feature\.$/);
  });

  it("never returns Unreleased for a real version", () => {
    const body = sectionFor(SAMPLE, "0.2.0");
    expect(body).not.toMatch(/Not shipped yet/);
    expect(sectionFor(SAMPLE, "Unreleased")).toContain("Not shipped yet");
  });

  // The real CHANGELOG is embedded at build time, so What's New is only ever as
  // good as the file that shipped. Pin the shape rather than a version: the
  // heading style has to be one sectionFor can read, whatever version is current.
  it("embeds a root CHANGELOG sectionFor can read", () => {
    const current = /^## \[([^\]]+)\]/m.exec(CHANGELOG_RAW);
    expect(current).not.toBeNull();
    if (!current) return;
    expect(sectionFor(CHANGELOG_RAW, current[1]!)).not.toBe("");
  });
});
