import { describe, expect, it } from "vitest";
import { prefsSnapshot } from "./web-research-enabled-prefs.ts";

describe("web-research-enabled-prefs", () => {
  it("prefsSnapshot sorts provider ids and carries warming toggles", () => {
    expect(
      prefsSnapshot(true, false, true, new Set(["brave", "direct"])),
    ).toEqual({
      warming: true,
      guess_domains: false,
      search_enabled: true,
      enabled_providers: ["brave", "direct"],
    });
    expect(prefsSnapshot(false, false, false, new Set(["direct"]))).toEqual({
      warming: false,
      guess_domains: false,
      search_enabled: false,
      enabled_providers: ["direct"],
    });
  });
});
