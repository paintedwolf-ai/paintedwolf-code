import { describe, expect, it } from "vitest";
import { trailingTabState } from "../tabs/files-tab-trailing.ts";

describe("tree trailing precedence — presence over in-scope", () => {
  it("renders presence when both presence and in-scope are true", () => {
    expect(
      trailingTabState({
        presence: true,
        dirty: false,
        changeKind: "changed",
      }),
    ).toBe("presence");
  });

  it("renders the scope mark when only in-scope is true", () => {
    expect(
      trailingTabState({
        presence: false,
        dirty: false,
        changeKind: "added",
      }),
    ).toBe("added");
  });

  it("renders nothing when neither is present", () => {
    expect(
      trailingTabState({
        presence: false,
        dirty: false,
        changeKind: null,
      }),
    ).toBeNull();
  });
});
