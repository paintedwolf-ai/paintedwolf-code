import { describe, expect, it } from "vitest";
import {
  scopeChangeKindFromFlags,
  scopeChangeMarkSpec,
} from "./files-scope-change-mark.ts";

describe("scopeChangeKindFromFlags", () => {
  it("returns null when the path is out of scope", () => {
    expect(
      scopeChangeKindFromFlags({
        inScope: false,
        added: false,
        deleted: false,
      }),
    ).toBeNull();
  });

  it("names creates, edits, and deletes", () => {
    expect(
      scopeChangeKindFromFlags({
        inScope: true,
        added: true,
        deleted: false,
      }),
    ).toBe("added");
    expect(
      scopeChangeKindFromFlags({
        inScope: true,
        added: false,
        deleted: false,
      }),
    ).toBe("changed");
    expect(
      scopeChangeKindFromFlags({
        inScope: true,
        added: true,
        deleted: true,
      }),
    ).toBe("deleted");
  });

  it("treats an added path as added even when not flagged inScope", () => {
    expect(
      scopeChangeKindFromFlags({
        inScope: false,
        added: true,
        deleted: false,
      }),
    ).toBe("added");
  });

  it("treats a deleted path as deleted even when not flagged inScope", () => {
    expect(
      scopeChangeKindFromFlags({
        inScope: false,
        added: false,
        deleted: true,
      }),
    ).toBe("deleted");
  });
});

describe("scopeChangeMarkSpec", () => {
  it("gives each kind a distinct glyph and spoken label", () => {
    const added = scopeChangeMarkSpec({ kind: "added", subject: "view" });
    const changed = scopeChangeMarkSpec({ kind: "changed", subject: "view" });
    const deleted = scopeChangeMarkSpec({ kind: "deleted", subject: "view" });

    expect(added.glyph).toBe("+");
    expect(changed.glyph).toBe("•");
    expect(deleted.glyph).toBe("−");

    expect(new Set([added.glyph, changed.glyph, deleted.glyph]).size).toBe(3);
    expect(added.label).toMatch(/Added/i);
    expect(changed.label).toMatch(/Changed/i);
    expect(deleted.label).toMatch(/Deleted/i);
  });

  it("names a shown version's change separately from the scope in view", () => {
    const view = scopeChangeMarkSpec({ kind: "deleted", subject: "view" });
    const version = scopeChangeMarkSpec({ kind: "deleted", subject: "version" });
    expect(version.glyph).toBe(view.glyph);
    expect(version.label).not.toBe(view.label);
  });
});
