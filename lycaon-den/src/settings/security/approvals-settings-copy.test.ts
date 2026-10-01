import { describe, expect, it } from "vitest";
import {
  APPROVAL_POSTURE_CARDS,
  APPROVALS_SETTINGS_COPY,
} from "./approvals-settings-copy.ts";
import { SAVED_APPROVALS_COPY } from "./saved-approvals-copy.ts";

describe("approvals-settings-copy", () => {
  it("baseline distinguishes the sandbox from requests that reach past it", () => {
    expect(APPROVALS_SETTINGS_COPY.baselineLines[0]).toMatch(/macOS/);
    expect(APPROVALS_SETTINGS_COPY.baselineLines[0]).toMatch(/temp/);
    expect(APPROVALS_SETTINGS_COPY.baselineLines[0]).toMatch(/cache/);
    expect(APPROVALS_SETTINGS_COPY.baselineLines[1]).toMatch(/request/i);
    expect(APPROVALS_SETTINGS_COPY.baselineLines[1]).toMatch(/outside the sandbox/i);
  });

  it("posture cards cover Light / Balanced / Strict with taglines only", () => {
    expect(APPROVAL_POSTURE_CARDS.map((c) => c.id)).toEqual([
      "light",
      "balanced",
      "strict",
    ]);
    for (const card of APPROVAL_POSTURE_CARDS) {
      expect(card.label.length).toBeGreaterThan(0);
      expect(card.tagline.length).toBeGreaterThan(0);
      expect(card).not.toHaveProperty("preview");
      expect(card).not.toHaveProperty("defaultBadge");
    }
    // External-content reads do not raise approval cards.
    expect(APPROVAL_POSTURE_CARDS[1]?.tagline).not.toMatch(/external content/i);
    expect(APPROVAL_POSTURE_CARDS[1]?.tagline).toMatch(/detection/i);
    expect(APPROVAL_POSTURE_CARDS[1]?.tagline).toMatch(/paths outside your attached folders/i);
    expect(APPROVAL_POSTURE_CARDS[2]?.tagline).toMatch(/each new .*host/i);
    expect(APPROVAL_POSTURE_CARDS[2]?.tagline).not.toMatch(/outside your attached folders/i);
  });
});

describe("saved-approvals-copy", () => {
  it("promises one full list with revoke-anywhere semantics", () => {
    expect(SAVED_APPROVALS_COPY.explain).toMatch(/in one place/);
    expect(SAVED_APPROVALS_COPY.explain).toMatch(/Chat approvals last until you delete the chat/);
    expect(SAVED_APPROVALS_COPY.explain).toMatch(/Revoke any row/);
    expect(SAVED_APPROVALS_COPY.chickletTip).toMatch(/either place/);
  });
});
