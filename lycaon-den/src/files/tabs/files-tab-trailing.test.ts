import { describe, expect, it } from "vitest";
import {
  trailingTabState,
  trailingTabStateLabels,
} from "./files-tab-trailing.ts";

describe("trailingTabState", () => {
  it("precedence is presence > dirty > scope change kind", () => {
    expect(
      trailingTabState({
        presence: true,
        dirty: true,
        changeKind: "changed",
      }),
    ).toBe("presence");
    expect(
      trailingTabState({
        presence: false,
        dirty: true,
        changeKind: "added",
      }),
    ).toBe("dirty");
    expect(
      trailingTabState({
        presence: false,
        dirty: false,
        changeKind: "changed",
      }),
    ).toBe("changed");
    expect(
      trailingTabState({
        presence: false,
        dirty: false,
        changeKind: "added",
      }),
    ).toBe("added");
    expect(
      trailingTabState({
        presence: false,
        dirty: false,
        changeKind: "deleted",
      }),
    ).toBe("deleted");
    expect(
      trailingTabState({
        presence: false,
        dirty: false,
        changeKind: null,
      }),
    ).toBeNull();
  });

  it("labels name every active fact including suppressed ones", () => {
    expect(
      trailingTabStateLabels({
        agentLabels: ["Fix login: waiting for your approval to edit", "Fix login: read this turn"],
        dirty: true,
        change: { kind: "added", subject: "view" },
      }),
    ).toEqual([
      "Fix login: waiting for your approval to edit",
      "Fix login: read this turn",
      "Unsaved changes",
      "Added in the current view",
    ]);
    expect(
      trailingTabStateLabels({
        agentLabels: [],
        dirty: false,
        change: { kind: "deleted", subject: "version" },
      }),
    ).toEqual(["Deleted in this version"]);
  });
});
