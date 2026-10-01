import { describe, expect, it } from "vitest";
import { shouldShowNoFolderBanner } from "./nofolder-banner.ts";

describe("shouldShowNoFolderBanner", () => {
  const ready = {
    isDraft: false,
    rootCount: 0,
    dismissed: false,
    chatReady: true,
  };

  it("shows for a non-draft chat-ready project with 0 roots", () => {
    expect(shouldShowNoFolderBanner(ready)).toBe(true);
  });

  it("suppresses drafts (draft promote / Save to folder controls the CTA)", () => {
    expect(shouldShowNoFolderBanner({ ...ready, isDraft: true })).toBe(false);
  });

  it("hides when any folder is attached", () => {
    expect(shouldShowNoFolderBanner({ ...ready, rootCount: 1 })).toBe(false);
    expect(shouldShowNoFolderBanner({ ...ready, rootCount: 2 })).toBe(false);
  });

  it("hides after session-local dismiss for this project", () => {
    expect(shouldShowNoFolderBanner({ ...ready, dismissed: true })).toBe(false);
  });

  it("waits until the focused chat is ready", () => {
    expect(shouldShowNoFolderBanner({ ...ready, chatReady: false })).toBe(false);
  });
});
