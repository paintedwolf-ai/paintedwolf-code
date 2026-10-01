import { describe, expect, it } from "vitest";
import { queryScanScoped } from "./search-export.ts";

describe("search-export", () => {
  it("detects scan-scoped queries", () => {
    expect(queryScanScoped("kind:scan severity")).toBe(true);
    expect(queryScanScoped("shape:artifact token")).toBe(true);
    expect(queryScanScoped("needle")).toBe(false);
    expect(queryScanScoped("kind:web fetch")).toBe(false);
  });
});
