import { describe, expect, it } from "vitest";
import { externalContentSearchQuery } from "./untrusted-content-copy.ts";

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
