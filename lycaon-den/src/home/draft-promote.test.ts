import { describe, expect, it } from "vitest";
import {
  draftPromoteFirstTurnComplete,
  rememberDraftProjectExchange,
  shouldShowDraftPromoteBanner,
} from "./draft-promote.ts";

describe("draftPromoteFirstTurnComplete", () => {
  it("waits for a settled user+assistant exchange", () => {
    expect(
      draftPromoteFirstTurnComplete({
        messages: [{ role: "user" }],
        live: false,
      }),
    ).toBe(false);
    expect(
      draftPromoteFirstTurnComplete({
        messages: [{ role: "user" }, { role: "assistant" }],
        live: true,
      }),
    ).toBe(false);
    expect(
      draftPromoteFirstTurnComplete({
        messages: [{ role: "user" }, { role: "assistant" }],
        live: false,
      }),
    ).toBe(true);
  });
});

describe("rememberDraftProjectExchange", () => {
  it("records a project once and reuses the set when unchanged", () => {
    const first = rememberDraftProjectExchange(new Set(), "p1");
    expect(first.has("p1")).toBe(true);
    expect(rememberDraftProjectExchange(first, "p1")).toBe(first);
  });
});

describe("shouldShowDraftPromoteBanner", () => {
  const ready = {
    isDraft: true,
    projectHasExchange: true,
    dismissed: false,
    promotePending: false,
    saveActionVisible: false,
  };

  it("never prompts for a non-draft project", () => {
    expect(shouldShowDraftPromoteBanner({ ...ready, isDraft: false })).toBe(false);
  });

  it("offers one save action as navigation collapses and expands", () => {
    expect(shouldShowDraftPromoteBanner({ ...ready, saveActionVisible: true })).toBe(false);
    expect(shouldShowDraftPromoteBanner({ ...ready, saveActionVisible: false })).toBe(true);
  });

  it("shows on any session once the project has had an exchange", () => {
    expect(shouldShowDraftPromoteBanner(ready)).toBe(true);
  });

  it("hides after project-level dismiss", () => {
    expect(shouldShowDraftPromoteBanner({ ...ready, dismissed: true })).toBe(false);
  });

  it("waits until the project has an exchange", () => {
    expect(
      shouldShowDraftPromoteBanner({ ...ready, projectHasExchange: false }),
    ).toBe(false);
  });

  it("stays quiet while a folder pick is queued", () => {
    expect(shouldShowDraftPromoteBanner({ ...ready, promotePending: true })).toBe(
      false,
    );
  });
});
