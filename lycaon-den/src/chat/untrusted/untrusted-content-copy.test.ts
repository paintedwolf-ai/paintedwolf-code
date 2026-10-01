import { describe, expect, it } from "vitest";
import {
  EXTERNAL_CONTENT_BADGE_TITLE,
  externalContentSearchQuery,
} from "./untrusted-content-copy.ts";

describe("externalContentSearchQuery", () => {
  it("scopes untrusted:true to the session", () => {
    expect(externalContentSearchQuery("sess-1")).toBe(
      "session:sess-1 untrusted:true",
    );
  });

  it("returns empty for blank session id", () => {
    expect(externalContentSearchQuery("  ")).toBe("");
  });
});

describe("EXTERNAL_CONTENT_BADGE_TITLE", () => {
  it("stays short and mentions click-to-search", () => {
    expect(EXTERNAL_CONTENT_BADGE_TITLE.length).toBeLessThan(80);
    expect(EXTERNAL_CONTENT_BADGE_TITLE).toMatch(/click to search/i);
  });
});
